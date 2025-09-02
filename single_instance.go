package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

type SingleInstance struct {
	fileLock *flock.Flock
}

func NewSingleInstance(appName string) *SingleInstance {
	// Создаем lock файл во временной директории
	lockFile := filepath.Join(os.TempDir(), appName+".lock")
	return &SingleInstance{
		fileLock: flock.New(lockFile),
	}
}

func (si *SingleInstance) Lock() (bool, error) {
	locked, err := si.fileLock.TryLock()
	if err != nil {
		return false, fmt.Errorf("ошибка блокировки: %w", err)
	}

	if !locked {
		return false, fmt.Errorf("приложение уже запущено")
	}

	return true, nil
}

func (si *SingleInstance) Release() {
	if si.fileLock != nil {
		si.fileLock.Unlock()
	}
}
