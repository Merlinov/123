package utils

import (
	"io"
	"log"

	"gopkg.in/natefinch/lumberjack.v2"
)

// InitLogger инициализирует логгер, который пишет в указанный файл
// func InitLogger(logFile string) (*log.Logger, io.Closer, error) {
// 	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
// 	if err != nil {
// 		return nil, nil, err
// 	}
// 	return log.New(f, "", log.LstdFlags), f, nil
// }

func InitLogger(filename string) (*log.Logger, io.Closer, error) {
	l := &lumberjack.Logger{
		Filename:   filename,
		MaxSize:    150,
		MaxBackups: 7,
		MaxAge:     30,
		Compress:   true,
	}
	logger := log.New(l, "", log.LstdFlags|log.Lmicroseconds)
	return logger, l, nil // lumberjack.Logger реализует io.Closer
}
