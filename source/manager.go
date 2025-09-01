// source/manager.go
package source

import "tg45/runner"

type Manager struct {
	//controls []*Control
	config runner.Config
}

func (m *Manager) CreateControls(cfg runner.Config) error
func (m *Manager) StartAll() error
func (m *Manager) StopAll() error
