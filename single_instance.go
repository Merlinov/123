package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

type SingleInstance struct {
	fileLock *flock.Flock
	lockPath string
}

func NewSingleInstance(appName string) *SingleInstance {
	lockFile := filepath.Join(os.TempDir(), appName+".lock")
	return &SingleInstance{
		fileLock: flock.New(lockFile),
		lockPath: lockFile,
	}
}

func (si *SingleInstance) Lock() (bool, error) {
	locked, err := si.fileLock.TryLock()
	if err != nil {
		return false, fmt.Errorf("ошибка блокировки %s: %w", si.lockPath, err)
	}

	if !locked {
		return false, fmt.Errorf("приложение уже запущено (lock файл: %s)", si.lockPath)
	}

	return true, nil
}

func (si *SingleInstance) Release() error {
	if si.fileLock != nil && si.fileLock.Locked() {
		if err := si.fileLock.Unlock(); err != nil {
			return fmt.Errorf("ошибка освобождения блокировки %s: %w", si.lockPath, err)
		}
	}
	return nil
}
