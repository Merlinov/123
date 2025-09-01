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

// DataSource описывает один источник данных
type DataSource struct {
	Name         string `json:"Name"`         // TG4, TG5, etc
	DataFileName string `json:"DataFileName"` // Tg4.dat, Tg5.dat
	ParserType   string `json:"ParserType"`   // tg4, tg5
	LogFileName  string `json:"LogFileName"`  // write_attempts_tg4.log
	Enabled      bool   `json:"Enabled"`      // включен ли источник
	Quality      int    `json:"Quality"`      // качество данных
}

// Config описывает структуру конфигурационного файла
type Config struct {
	LogMode     string       `json:"LogMode"`     // "all" или "errors"
	ConnString  string       `json:"ConnString"`  // строка подключения к MS SQL
	DataSources []DataSource `json:"DataSources"` // массив источников данных
}

// SourceRunner управляет одним источником данных
type SourceRunner struct {
	Source   DataSource
	Logger   *log.Logger
	Database *sql.DB
	StopChan chan struct{}
	Running  bool
	Mutex    sync.Mutex
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

	return &SourceRunner{
		Source:   source,
		Logger:   logger,
		Database: database,
		StopChan: make(chan struct{}),
		Running:  false,
	}, nil
}

// Start запускает обработку данных в отдельной горутине
func (sr *SourceRunner) Start(logMode string) {
	sr.Mutex.Lock()
	defer sr.Mutex.Unlock()

	if sr.Running {
		return // уже запущен
	}

	sr.Running = true
	sr.StopChan = make(chan struct{})

	go func() {
		for {
			select {
			case <-sr.StopChan:
				sr.Mutex.Lock()
				sr.Running = false
				sr.Mutex.Unlock()
				return
			default:
				err := sr.processData(logMode)
				if err != nil {
					sr.Logger.Printf("Ошибка обработки данных %s: %v", sr.Source.Name, err)
				}

				// Ждем 15 секунд или сигнал остановки
				select {
				case <-sr.StopChan:
					sr.Mutex.Lock()
					sr.Running = false
					sr.Mutex.Unlock()
					return
				case <-time.After(15 * time.Second):
				}
			}
		}
	}()
}

// Stop останавливает обработку данных
func (sr *SourceRunner) Stop() {
	sr.Mutex.Lock()
	defer sr.Mutex.Unlock()

	if sr.Running {
		close(sr.StopChan)
		sr.Running = false
	}
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
		return sr.processTG5Data(logMode)
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

// processTG5Data обрабатывает данные от TG5 (заглушка для будущей реализации)
func (sr *SourceRunner) processTG5Data(logMode string) error {
	// Когда будет готова модель Data_TG5, раскомментируй:
	// data, err := datafile.ReadData[model.Data_TG5](sr.Source.DataFileName)
	// if err != nil {
	//     return err
	// }
	//
	// // Извлечение временных меток специфично для TG5
	// // timeStampSystem := time.Now()
	// // timeStampFile := extractTG5Timestamp(data)
	//
	// // return db.SaveCurrentValues(sr.Database, data, timeStampSystem, timeStampFile, sr.Source.Quality, sr.Logger, logMode)

	// Пока что возвращаем ошибку
	return fmt.Errorf("парсер TG5 еще не реализован")
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
