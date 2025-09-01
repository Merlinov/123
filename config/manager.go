// config/manager.go
package config

import "tg45/runner"

type Manager struct {
	path    string
	current runner.Config
	valid   bool
	// watcher *Watcher
}

func (m *Manager) Load() error
func (m *Manager) Validate() error
func (m *Manager) Watch(callback func()) error
