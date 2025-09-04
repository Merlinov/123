// source/manager.go
package source

import (
	"fmt"
	"sync"

	"tg45/runner"
)

type Manager struct {
	runners   []*runner.SourceRunner
	config    runner.Config
	mutex     sync.RWMutex
	isRunning bool
}

func NewManager() *Manager {
	return &Manager{
		runners:   make([]*runner.SourceRunner, 0),
		isRunning: false,
	}
}

func (m *Manager) CreateControls(cfg runner.Config) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Останавливаем старые runners
	m.stopAllUnsafe()

	// Закрываем старые соединения с обработкой ошибок
	var closeErrors []error
	for _, runner := range m.runners {
		if err := runner.Close(); err != nil {
			closeErrors = append(closeErrors, err)
			// Продолжаем закрывать остальные
		}
	}

	// Очищаем массив и обновляем конфиг
	m.runners = make([]*runner.SourceRunner, 0)
	m.config = cfg

	// Создаем новые runners
	for _, source := range cfg.DataSources {
		if !source.Enabled {
			continue
		}

		sourceRunner, err := runner.NewSourceRunner(source, cfg.ConnString)
		if err != nil {
			return fmt.Errorf("ошибка создания runner для %s: %w", source.Name, err)
		}

		m.runners = append(m.runners, sourceRunner)
	}

	return nil
}

func (m *Manager) StartAll() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.isRunning {
		return nil
	}

	for _, runner := range m.runners {
		runner.Start(m.config.LogMode)
	}

	m.isRunning = true
	return nil
}

func (m *Manager) StopAll() error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	return m.stopAllUnsafe()
}

func (m *Manager) stopAllUnsafe() error {
	if !m.isRunning {
		return nil
	}

	for _, runner := range m.runners {
		runner.Stop()
	}

	m.isRunning = false
	return nil
}

func (m *Manager) GetRunners() []*runner.SourceRunner {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	result := make([]*runner.SourceRunner, len(m.runners))
	copy(result, m.runners)
	return result
}

func (m *Manager) GetStatus() map[string]bool {
	m.mutex.RLock()
	runners := make([]*runner.SourceRunner, len(m.runners))
	copy(runners, m.runners)
	m.mutex.RUnlock()

	status := make(map[string]bool)
	for _, runner := range runners {
		status[runner.Source.Name] = runner.IsRunning()
	}
	return status
}

func (m *Manager) Close() error {
	m.StopAll()

	for _, runner := range m.runners {
		if err := runner.Close(); err != nil {
			return err
		}
	}

	return nil
}
