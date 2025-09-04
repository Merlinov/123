package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"tg45/datafile"
	"tg45/db"
	"tg45/model"
	"tg45/utils"
)

/*
"23s"        // 23 секунды
"16m"        // 16 минут
"1h01m01s"   // 1 час 1 минута 1 секунда
"90m"        // 90 минут (= 1.5 часа)
"3600s"      // 3600 секунд (= 1 час)
"1h30m"      // 1 час 30 минут
"2h15m30s"   // 2 часа 15 минут 30 секунд
"500ms"      // 500 миллисекунд
"1.5h"       // 1.5 часа с десятичной дробью
"45m30s"     // 45 минут 30 секунд
*/

// DataSource описывает один источник данных
type DataSource struct {
	Name           string `json:"Name"`           // TG4, TG5, etc
	DataFileName   string `json:"DataFileName"`   // Tg4.dat, Tg5.dat
	ParserType     string `json:"ParserType"`     // tg4, tg5
	LogFileName    string `json:"LogFileName"`    // write_attempts_tg4.log
	Enabled        bool   `json:"Enabled"`        // включен ли источник
	Quality        int    `json:"Quality"`        // качество данных
	UpdateInterval string `json:"UpdateInterval"` //интервал парсинга бинарника
}

// Config описывает структуру конфигурационного файла
type Config struct {
	LogMode          string       `json:"LogMode"`          // "all" или "errors"
	ConnString       string       `json:"ConnString"`       // строка подключения к MS SQL
	DataSources      []DataSource `json:"DataSources"`      // массив источников данных
	StartAllOnLaunch bool         `json:"StartAllOnLaunch"` // автозапуск
}

// SourceRunner управляет одним источником данных
type SourceRunner struct {
	Source         DataSource
	Logger         *log.Logger
	Database       *sql.DB
	StopChan       chan struct{}
	Running        bool
	Mutex          sync.Mutex
	wg             sync.WaitGroup // ← для правильного управления горутинами
	updateInterval time.Duration  // ← интервал парсинга

	restartCount int       // счетчик перезапусков
	lastError    error     // последняя ошибка
	lastRestart  time.Time // время последнего перезапуска
	// Метрики для мониторинга
	totalProcessed    int64
	totalErrors       int64
	lastSuccessTime   time.Time
	avgProcessingTime time.Duration
	rateLimiter       *rate.Limiter
	logFile           io.Closer
	circuitBreaker    *CircuitBreaker
}

func (sr *SourceRunner) GetMetrics() map[string]interface{} {
	sr.Mutex.Lock()
	defer sr.Mutex.Unlock()

	// 🔧 СОЗДАЙ DEFENSIVE COPIES
	metrics := map[string]interface{}{
		"processed":    sr.totalProcessed,
		"errors":       sr.totalErrors,
		"restarts":     sr.restartCount,
		"last_success": sr.lastSuccessTime, // time.Time копируется по значению
		"avg_time":     sr.avgProcessingTime,
		"running":      sr.Running,
	}

	// 🔧 БЕЗОПАСНАЯ КОПИЯ ERROR
	if sr.lastError != nil {
		metrics["last_error"] = sr.lastError.Error() // Копируем string, а не error
	} else {
		metrics["last_error"] = nil
	}

	return metrics
}

func NewCircuitBreaker(threshold int, timeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		threshold: threshold,
		timeout:   timeout,
	}
}

func (cb *CircuitBreaker) CallWithContext(ctx context.Context, fn func() error) error {
	cb.mutex.Lock()

	if cb.failures >= cb.threshold {
		if time.Since(cb.lastFailTime) < cb.timeout {
			cb.mutex.Unlock()
			return fmt.Errorf("circuit breaker is open")
		}
		cb.failures = 0 // Reset после timeout
	}
	cb.mutex.Unlock()

	errCh := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("panic: %v", r)
			}
		}()
		errCh <- fn()
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errCh:
		if err != nil {
			cb.mutex.Lock()
			cb.failures++
			cb.lastFailTime = time.Now()
			cb.mutex.Unlock()
			return err
		}

		cb.mutex.Lock()
		cb.failures = 0 // Успех - сброс
		cb.mutex.Unlock()
		return nil
	}
}

// NewSourceRunner создает новый runner для источника
func NewSourceRunner(source DataSource, connString string) (*SourceRunner, error) {
	// Инициализируем логгер для источника
	logger, logFile, err := utils.InitLogger(source.LogFileName)
	if err != nil {
		return nil, err
	}

	database, err := sql.Open("sqlserver", connString)
	if err != nil {
		logFile.Close() // Закрой только при ошибке
		return nil, err
	}

	database.SetMaxOpenConns(10)
	database.SetMaxIdleConns(2)
	database.SetConnMaxLifetime(5 * time.Minute)
	database.SetConnMaxIdleTime(2 * time.Minute)

	if err := database.Ping(); err != nil {
		database.Close()
		logFile.Close()
		return nil, fmt.Errorf("не удается подключиться к базе данных: %w", err)
	}

	// Парсим интервал обновления
	var updateInterval time.Duration
	if source.UpdateInterval != "" {
		parsed, err := time.ParseDuration(source.UpdateInterval)
		if err != nil {
			logger.Printf("Ошибка парсинга UpdateInterval '%s': %v. Используем 15s по умолчанию", source.UpdateInterval, err)
			updateInterval = 15 * time.Second
		} else {
			updateInterval = parsed
		}
	} else {
		updateInterval = 15 * time.Second
	}

	// 🔧 ДОБАВЬ ВАЛИДАЦИЮ
	if updateInterval < 1*time.Second {
		logger.Printf("[%s] UpdateInterval слишком мал (%v), установлен минимум 1s", source.Name, updateInterval)
		updateInterval = 1 * time.Second
	}

	if updateInterval > 1*time.Hour {
		logger.Printf("[%s] UpdateInterval слишком велик (%v), установлен максимум 1h", source.Name, updateInterval)
		updateInterval = 1 * time.Hour
	}

	logger.Printf("[%s] Установлен интервал обновления: %v", source.Name, updateInterval)
	return &SourceRunner{
		Source:         source,
		Logger:         logger,
		Database:       database,
		logFile:        logFile, // 🔧 Сохрани для закрытия в Close()
		StopChan:       make(chan struct{}),
		Running:        false,
		updateInterval: updateInterval,
		restartCount:   0,
		lastError:      nil,
		lastRestart:    time.Time{},
		rateLimiter:    rate.NewLimiter(rate.Limit(1), 5),
		circuitBreaker: NewCircuitBreaker(5, 1*time.Minute),
	}, nil
}

type CircuitBreaker struct {
	failures     int
	threshold    int
	timeout      time.Duration
	lastFailTime time.Time
	mutex        sync.Mutex
}

func (cb *CircuitBreaker) Call(fn func() error) error {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	if cb.failures >= cb.threshold {
		if time.Since(cb.lastFailTime) < cb.timeout {
			return fmt.Errorf("circuit breaker is open")
		}
		cb.failures = 0 // reset
	}

	err := fn()
	if err != nil {
		cb.failures++
		cb.lastFailTime = time.Now()
		return err
	}

	cb.failures = 0
	return nil
}

func (sr *SourceRunner) Start(logMode string) {
	sr.Mutex.Lock()
	defer sr.Mutex.Unlock()

	if sr.Running {
		if logMode == "all" {
			sr.Logger.Printf("[%s] Уже запущен", sr.Source.Name)
		}
		return
	}

	sr.Logger.Printf("[%s] Старт процесса", sr.Source.Name)
	sr.Running = true
	sr.StopChan = make(chan struct{})

	sr.wg.Add(1)
	go func() {
		defer sr.wg.Done()
		// ДОБАВЛЯЕМ ЗАЩИТУ ОТ PANIC
		defer func() {
			if r := recover(); r != nil {
				sr.Logger.Printf("[%s] КРИТИЧЕСКАЯ ОШИБКА - горутина упала: %v", sr.Source.Name, r)
				sr.Mutex.Lock()
				sr.lastError = fmt.Errorf("panic: %v", r)
				sr.restartCount++
				sr.lastRestart = time.Now()
				sr.Running = false
				sr.Mutex.Unlock()

				// Автоматический перезапуск через 30 секунд (если не превышен лимит)
				if sr.restartCount <= 5 { // максимум 5 перезапусков
					go func() {
						time.Sleep(30 * time.Second)
						sr.Logger.Printf("[%s] Попытка автоматического перезапуска (%d/5)",
							sr.Source.Name, sr.restartCount)
						sr.Start(logMode)
					}()
				}
			} else {
				sr.Mutex.Lock()
				sr.Running = false
				sr.Mutex.Unlock()
			}

			if logMode == "all" {
				sr.Logger.Printf("[%s] Горутина завершилась", sr.Source.Name)
			}
		}()

		sr.runMainLoop(logMode)
	}()

	if logMode == "all" {
		sr.Logger.Printf("[%s] Start completed", sr.Source.Name)
	}
}

// Выносим основной цикл в отдельный метод
func (sr *SourceRunner) runMainLoop(logMode string) {
	if logMode == "all" {
		sr.Logger.Printf("[%s] Горутина запустилась", sr.Source.Name)
	}

	for {
		select {
		case <-sr.StopChan:
			if logMode == "all" {
				sr.Logger.Printf("[%s] Процесс остановлен", sr.Source.Name)
			}
			return
		default:
			// ИСПОЛЬЗУЕМ RETRY LOGIC
			err := sr.processDataWithRetry(logMode)
			if err != nil {
				sr.Logger.Printf("[%s] Ошибка обработки данных: %v", sr.Source.Name, err)
				sr.Mutex.Lock()
				sr.lastError = err
				sr.Mutex.Unlock()
			} else {
				// Сбрасываем счетчик при успешной обработке
				sr.Mutex.Lock()
				sr.restartCount = 0
				sr.lastError = nil
				sr.Mutex.Unlock()
			}

			select {
			case <-sr.StopChan:
				return
			case <-time.After(sr.updateInterval):
				// продолжаем
			}
		}
	}
}

// НОВЫЙ метод с retry logic
func (sr *SourceRunner) processDataWithRetry(logMode string) error {
	if !sr.rateLimiter.Allow() {
		return fmt.Errorf("rate limit exceeded")
	}

	maxRetries := 3
	baseDelay := 2 * time.Second

	// 🔧 СОЗДАЙ CONTEXT для всей операции retry
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	for attempt := 0; attempt <= maxRetries; attempt++ {
		err := sr.processDataWithContext(ctx, logMode)
		if err == nil {
			return nil // Успех!
		}

		if attempt == maxRetries {
			return fmt.Errorf("превышено количество попыток (%d): %w", maxRetries, err)
		}

		delay := baseDelay * time.Duration(1<<attempt) // 2s, 4s, 8s
		if logMode == "all" || attempt == maxRetries-1 {
			sr.Logger.Printf("[%s] Попытка %d/%d не удалась: %v. Повтор через %v",
				sr.Source.Name, attempt+1, maxRetries, err, delay)
		}

		select {
		case <-sr.StopChan:
			return fmt.Errorf("остановлен во время retry")
		case <-ctx.Done(): // 🔧 ДОБАВЬ ПРОВЕРКУ CONTEXT
			return ctx.Err()
		case <-time.After(delay):
			// continue retry
		}
	}
	return nil
}

func (sr *SourceRunner) processDataWithContext(ctx context.Context, logMode string) error {
	switch sr.Source.ParserType {
	case "tg4":
		return sr.processTG4Data(ctx, logMode)
	case "tg5":
		return sr.processTG5Data(ctx, logMode)
	case "tg5_hour":
		return sr.processTG5HourData(ctx, logMode)
	default:
		return fmt.Errorf("неизвестный тип парсера: %s", sr.Source.ParserType)
	}
}

// Stop останавливает обработку данных
func (sr *SourceRunner) Stop() {
	sr.Mutex.Lock()
	if !sr.Running {
		sr.Mutex.Unlock()
		return
	}

	sr.Logger.Printf("[%s] Остановка процесса", sr.Source.Name)
	close(sr.StopChan) // ← закрываем канал
	sr.Mutex.Unlock()  // ← ВАЖНО: освобождаем мьютекс ДО Wait!

	sr.wg.Wait() // ← ждем завершения горутины
	sr.Logger.Printf("[%s] Процесс остановлен", sr.Source.Name)
}

// IsRunning возвращает статус работы
func (sr *SourceRunner) IsRunning() bool {
	sr.Mutex.Lock()
	defer sr.Mutex.Unlock()
	return sr.Running
}

// processData обрабатывает данные из файла по типу парсера
func (sr *SourceRunner) processData(logMode string) error {
	// Создаем context для DB операций
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	switch sr.Source.ParserType {
	case "tg4":
		return sr.processTG4Data(ctx, logMode)
	case "tg5":
		return sr.processTG5Data(ctx, logMode)
	case "tg5_hour":
		return sr.processTG5HourData(ctx, logMode)
	default:
		return fmt.Errorf("неизвестный тип парсера: %s", sr.Source.ParserType)
	}
}

// processTG4Data обрабатывает данные от TG4 с использованием generic ReadData
func (sr *SourceRunner) processTG4Data(ctx context.Context, logMode string) error {
	data, err := datafile.ReadData[model.Data_TG4](sr.Source.DataFileName)
	if err != nil {
		return err
	}

	timeStampSystem := time.Now()
	timeStampFile := time.Date(
		int(data.Fltv180Offs360),
		time.Month(int(data.Fltv181Offs364)),
		int(data.Fltv182Offs368),
		int(data.Fltv177Offs348),
		int(data.Fltv178Offs352),
		int(data.Fltv179Offs356),
		0, time.UTC,
	)

	// 🔧 ИСПОЛЬЗУЕМ CONTEXT версию
	return db.SaveCurrentValuesContext(ctx, sr.Database, data, timeStampSystem, timeStampFile, sr.Source.Quality, sr.Logger, logMode)
}

// АНАЛОГИЧНО для processTG5Data
func (sr *SourceRunner) processTG5Data(ctx context.Context, logMode string) error {
	data, err := datafile.ReadData[model.Data_TG5_Minute](sr.Source.DataFileName)
	if err != nil {
		return err
	}

	timeStampSystem := time.Now()
	timeStampFile := time.Date(
		int(data.Fltv105Offs447),
		time.Month(int(data.Fltv106Offs451)),
		int(data.Fltv107Offs455),
		int(data.Fltv102Offs434),
		int(data.Fltv103Offs438),
		int(data.Fltv104Offs442),
		0, time.UTC,
	)

	return db.SaveCurrentValuesContext(ctx, sr.Database, data, timeStampSystem, timeStampFile, sr.Source.Quality, sr.Logger, logMode)
}

// Новый метод для часовых данных TG5
// АНАЛОГИЧНО для processTG5HourData
func (sr *SourceRunner) processTG5HourData(ctx context.Context, logMode string) error {
	data, err := datafile.ReadData[model.Data_TG5_Hour](sr.Source.DataFileName)
	if err != nil {
		return err
	}

	timeStampSystem := time.Now()
	timeStampFile := time.Date(
		int(data.Fltv105Offs447),
		time.Month(int(data.Fltv106Offs451)),
		int(data.Fltv107Offs455),
		int(data.Fltv102Offs434),
		int(data.Fltv103Offs438),
		int(data.Fltv104Offs442),
		0, time.UTC,
	)

	return db.SaveCurrentValuesContext(ctx, sr.Database, data, timeStampSystem, timeStampFile, sr.Source.Quality, sr.Logger, logMode)
}

// Close закрывает соединения
func (sr *SourceRunner) Close() error {
	sr.Stop()

	var errs []error

	if sr.logFile != nil {
		if err := sr.logFile.Close(); err != nil {
			errs = append(errs, fmt.Errorf("log file close: %w", err))
		}
	}

	if err := sr.Database.Close(); err != nil {
		errs = append(errs, fmt.Errorf("database close: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("close errors: %v", errs)
	}
	return nil
}

// LoadConfig читает и парсит JSON конфигурационный файл по указанному пути
func LoadConfig(filename string) (Config, error) {
	var cfg Config
	f, err := os.Open(filename)
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	decoder := json.NewDecoder(f)
	err = decoder.Decode(&cfg)
	return cfg, err
}
