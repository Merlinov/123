package runner

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"
	"time"

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
	LogMode     string       `json:"LogMode"`     // "all" или "errors"
	ConnString  string       `json:"ConnString"`  // строка подключения к MS SQL
	DataSources []DataSource `json:"DataSources"` // массив источников данных
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
}

// NewSourceRunner создает новый runner для источника
func NewSourceRunner(source DataSource, connString string) (*SourceRunner, error) {
	// Инициализируем логгер для источника
	logger, err := utils.InitLogger(source.LogFileName)
	if err != nil {
		return nil, err
	}

	// Подключаемся к базе данных
	database, err := sql.Open("sqlserver", connString)
	if err != nil {
		return nil, err
	}

	// Парсим интервал обновления из конфигурации
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
		// Если поле не указано, используем 15 секунд по умолчанию
		updateInterval = 15 * time.Second
	}

	return &SourceRunner{
		Source:         source,
		Logger:         logger,
		Database:       database,
		StopChan:       make(chan struct{}),
		Running:        false,
		updateInterval: updateInterval,
	}, nil
}

// Start запускает обработку данных в отдельной горутине
func (sr *SourceRunner) Start(logMode string) {
	sr.Mutex.Lock()
	defer sr.Mutex.Unlock()

	if sr.Running {
		if logMode == "all" {
			sr.Logger.Printf("[%s] Уже запущен", sr.Source.Name)
		}
		return // уже запущен
	}

	sr.Logger.Printf("[%s] Старт процесса", sr.Source.Name)
	sr.Running = true
	sr.StopChan = make(chan struct{})

	sr.wg.Add(1)
	go func() {
		defer sr.wg.Done()
		defer func() {
			sr.Mutex.Lock()
			sr.Running = false
			sr.Mutex.Unlock()
			if logMode == "all" {
				sr.Logger.Printf("[%s] Горутина завершилась", sr.Source.Name)
			}
		}()

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
				err := sr.processData(logMode)
				if err != nil {
					sr.Logger.Printf("[%s] Ошибка обработки данных: %v", sr.Source.Name, err)
					// НЕ завершаем горутину при ошибке - просто логируем
				}

				// Ждем 15 секунд или сигнал остановки
				var lastTick time.Time
				select {
				case <-sr.StopChan:
					if logMode == "all" {
						sr.Logger.Printf("[%s] Получен сигнал остановки", sr.Source.Name)
					}
					return
				case t := <-time.After(sr.updateInterval): // ← используем настраиваемый интервал
					lastTick = t
					if logMode == "all" {
						sr.Logger.Printf("[%s] Следующая итерация в %v (интервал: %v)", sr.Source.Name, lastTick, sr.updateInterval)
					}
				}
			}
		}
	}()

	if logMode == "all" {
		sr.Logger.Printf("[%s] Start completed", sr.Source.Name)
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
	switch sr.Source.ParserType {
	case "tg4":
		return sr.processTG4Data(logMode)
	case "tg5":
		return sr.processTG5Data(logMode) // минутные TG5
	case "tg5_hour":
		return sr.processTG5HourData(logMode) // часовые TG5
	default:
		return fmt.Errorf("неизвестный тип парсера: %s", sr.Source.ParserType)
	}
}

// processTG4Data обрабатывает данные от TG4 с использованием generic ReadData
func (sr *SourceRunner) processTG4Data(logMode string) error {
	// Используем generic-функцию для чтения данных TG4
	data, err := datafile.ReadData[model.Data_TG4](sr.Source.DataFileName)
	if err != nil {
		return err
	}

	// Формируем метки времени из структуры TG4
	timeStampSystem := time.Now()
	timeStampFile := time.Date(
		int(data.Fltv180Offs360),             // год
		time.Month(int(data.Fltv181Offs364)), // месяц
		int(data.Fltv182Offs368),             // день
		int(data.Fltv177Offs348),             // час
		int(data.Fltv178Offs352),             // минута
		int(data.Fltv179Offs356),             // секунда
		0, time.UTC,
	)

	// Сохраняем данные с использованием существующей функции
	return db.SaveCurrentValues(sr.Database, data, timeStampSystem, timeStampFile, sr.Source.Quality, sr.Logger, logMode)
}

func (sr *SourceRunner) processTG5Data(logMode string) error {
	// Убираем заглушку и делаем реальную обработку
	data, err := datafile.ReadData[model.Data_TG5_Minute](sr.Source.DataFileName)
	if err != nil {
		return err
	}

	// Формируем метки времени из структуры TG5
	timeStampSystem := time.Now()
	timeStampFile := time.Date(
		int(data.Fltv105Offs447),             // год (idTag:281)
		time.Month(int(data.Fltv106Offs451)), // месяц (idTag:282)
		int(data.Fltv107Offs455),             // день (idTag:283)
		int(data.Fltv102Offs434),             // час (idTag:278)
		int(data.Fltv103Offs438),             // минута (idTag:279)
		int(data.Fltv104Offs442),             // секунда (idTag:280)
		0, time.UTC,
	)

	// Используем универсальную функцию
	return db.SaveCurrentValues(sr.Database, data, timeStampSystem, timeStampFile, sr.Source.Quality, sr.Logger, logMode)
}

// Новый метод для часовых данных TG5
func (sr *SourceRunner) processTG5HourData(logMode string) error {
	data, err := datafile.ReadData[model.Data_TG5_Hour](sr.Source.DataFileName)
	if err != nil {
		return err
	}

	// Извлекаем время из структуры часовых данных
	timeStampSystem := time.Now()
	timeStampFile := time.Date(
		int(data.Fltv105Offs447),             // год
		time.Month(int(data.Fltv106Offs451)), // месяц
		int(data.Fltv107Offs455),             // день
		int(data.Fltv102Offs434),             // час
		int(data.Fltv103Offs438),             // минута
		int(data.Fltv104Offs442),             // секунда
		0, time.UTC,
	)

	return db.SaveCurrentValues(sr.Database, data, timeStampSystem, timeStampFile, sr.Source.Quality, sr.Logger, logMode)
}

// Close закрывает соединения
func (sr *SourceRunner) Close() error {
	sr.Stop()
	return sr.Database.Close()
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
