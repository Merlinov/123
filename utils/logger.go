package utils

import (
	"log"
	"os"
)

// InitLogger инициализирует логгер, который пишет в указанный файл
func InitLogger(logFile string) (*log.Logger, error) {
	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	return log.New(f, "", log.LstdFlags), nil
}
