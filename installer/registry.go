package installer

import (
	"os"

	"golang.org/x/sys/windows/registry"
)

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
