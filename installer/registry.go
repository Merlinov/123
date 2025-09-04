package installer

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

// Добавь в installer/registry.go
func AddToStartup() error {
	key, err := registry.OpenKey(registry.CURRENT_USER,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()

	exePath, _ := os.Executable()
	return key.SetStringValue("TG45DataCollector", exePath)
}
