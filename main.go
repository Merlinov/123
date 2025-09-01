package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
	"github.com/skratchdot/open-golang/open"

	"tg45/runner"

	_ "github.com/denisenkom/go-mssqldb"
)

// Константы вместо хардкода
const (
	AppTitle      = "Файлообмен ТЭЦ"
	ConfigPath    = "config.json"
	IconPath      = "icon.png"
	WindowWidth   = 900
	WindowHeight  = 700
	WatchInterval = 5 * time.Second
)

type SourceControl struct {
	Source      runner.DataSource
	Runner      *runner.SourceRunner
	StatusLabel *widget.Label
	RunStopBtn  *widget.Button
	ViewLogBtn  *widget.Button
}

type AppManager struct {
	configPath     string
	cfg            runner.Config
	sourceControls []*SourceControl
	content        *fyne.Container
	myWindow       fyne.Window
	configValid    bool

	// UI элементы
	errorLabel       *widget.Label
	reloadButton     *widget.Button
	editConfigButton *widget.Button
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow(AppTitle)
	myWindow.Resize(fyne.NewSize(WindowWidth, WindowHeight))

	if icon, err := fyne.LoadResourceFromPath(IconPath); err == nil {
		myWindow.SetIcon(icon)
	}

	appManager := &AppManager{
		configPath:  ConfigPath,
		myWindow:    myWindow,
		configValid: false,
	}

	// Запуск приложения
	appManager.run()
}

// Главный метод запуска приложения
func (am *AppManager) run() {
	am.initializeUI()
	am.reloadConfig()
	go am.configWatcher()

	am.myWindow.SetCloseIntercept(func() {
		am.stopAllSources()
		am.myWindow.Close()
	})

	am.myWindow.ShowAndRun()
}

// ========== ВСЕ ОСТАЛЬНЫЕ МЕТОДЫ ИЗ ТВОЕГО ОРИГИНАЛЬНОГО КОДА ==========

// initializeUI создает базовый интерфейс, который всегда доступен
func (am *AppManager) initializeUI() {
	am.content = container.NewVBox()

	// Заголовок
	title := widget.NewLabel(AppTitle)
	title.TextStyle = fyne.TextStyle{Bold: true}
	am.content.Add(title)
	am.content.Add(widget.NewSeparator())

	// Статус конфигурации
	am.errorLabel = widget.NewLabel("Загрузка конфигурации...")
	am.errorLabel.TextStyle = fyne.TextStyle{Italic: true}
	am.content.Add(am.errorLabel)

	// Кнопки управления конфигурацией - ВСЕГДА доступны
	am.reloadButton = widget.NewButton("Перезагрузить конфиг", func() {
		am.reloadConfig()
	})

	am.editConfigButton = widget.NewButton("Изменить конфиг", func() {
		am.showConfigEditor()
	})

	configButtons := container.NewHBox(am.reloadButton, am.editConfigButton)
	am.content.Add(configButtons)
	am.content.Add(widget.NewSeparator())

	am.myWindow.SetContent(container.NewVScroll(am.content))
}

// reloadConfig перезагружает конфигурацию и обновляет UI
func (am *AppManager) reloadConfig() {
	// Останавливаем все текущие источники
	am.stopAllSources()

	// Очищаем старые контролы источников
	am.clearSourcesFromUI()

	// Проверяем существование файла
	if _, err := os.Stat(am.configPath); errors.Is(err, os.ErrNotExist) {
		am.configValid = false
		am.errorLabel.SetText("❌ Файл config.json не найден")
		am.showConfigExample()
		return
	}

	// Пытаемся загрузить конфиг
	cfg, err := runner.LoadConfig(am.configPath)
	if err != nil {
		am.configValid = false
		am.errorLabel.SetText(fmt.Sprintf("❌ Ошибка чтения конфига: %v", err))
		return
	}

	// Валидируем конфиг
	if err := am.validateConfig(cfg); err != nil {
		am.configValid = false
		am.errorLabel.SetText(fmt.Sprintf("❌ Неверный конфиг: %v", err))
		return
	}

	// Конфиг валидный - обновляем состояние
	am.cfg = cfg
	am.configValid = true
	am.errorLabel.SetText("✅ Конфигурация загружена успешно")

	// Создаем новые контролы источников
	am.createSourceControls()
	am.updateSourcesUI()
}

// createSourceControls создает контролы для источников данных
func (am *AppManager) createSourceControls() {
	am.sourceControls = []*SourceControl{}

	for _, source := range am.cfg.DataSources {
		if !source.Enabled {
			continue
		}

		sourceRunner, err := runner.NewSourceRunner(source, am.cfg.ConnString)
		if err != nil {
			am.errorLabel.SetText(fmt.Sprintf("❌ Ошибка создания runner для %s: %v", source.Name, err))
			continue
		}

		control := &SourceControl{
			Source:      source,
			Runner:      sourceRunner,
			StatusLabel: widget.NewLabel(fmt.Sprintf("%s: Остановлен", source.Name)),
		}

		// Создаем кнопки с правильными замыканиями
		am.createSourceButton(control, source)

		am.sourceControls = append(am.sourceControls, control)
	}
}

// Улучшенная функция для создания кнопок
func (am *AppManager) createSourceButton(control *SourceControl, source runner.DataSource) {
	control.RunStopBtn = widget.NewButton("Start", nil)
	control.ViewLogBtn = widget.NewButton("Лог", nil)

	// Правильные замыкания
	control.RunStopBtn.OnTapped = func() {
		am.toggleSource(control)
	}

	control.ViewLogBtn.OnTapped = func() {
		if err := open.Run(source.LogFileName); err != nil {
			dialog.ShowError(err, am.myWindow)
		}
	}
}

// updateSourcesUI обновляет UI с источниками данных
// updateSourcesUI обновляет UI с источниками данных
func (am *AppManager) updateSourcesUI() {
	if !am.configValid {
		return
	}

	if len(am.sourceControls) == 0 {
		noSourcesLabel := widget.NewLabel("Нет активных источников данных.\nПроверьте настройку 'Enabled' в config.json")
		am.content.Add(noSourcesLabel)
	} else {
		// Создаем сетку с 4 колонками (Источник, Статус, Управление, Логи)
		gridContainer := container.New(layout.NewGridLayout(4))

		// Добавляем заголовки
		gridContainer.Add(widget.NewLabelWithStyle("Источник", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		gridContainer.Add(widget.NewLabelWithStyle("Статус", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		gridContainer.Add(widget.NewLabelWithStyle("Управление", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}))
		gridContainer.Add(widget.NewLabelWithStyle("Логи", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}))

		// Добавляем данные по источникам
		for _, control := range am.sourceControls {
			// Убираем ручное изменение размеров - Grid сам все выровняет
			gridContainer.Add(widget.NewLabel(control.Source.Name))
			gridContainer.Add(control.StatusLabel)
			gridContainer.Add(control.RunStopBtn)
			gridContainer.Add(control.ViewLogBtn)
		}

		am.content.Add(gridContainer)

		// Кнопки общего управления
		am.content.Add(widget.NewSeparator())

		startAllButton := widget.NewButton("Запустить все", func() {
			am.startAllSources()
		})

		stopAllButton := widget.NewButton("Остановить все", func() {
			am.stopAllSources()
		})

		generalControls := container.NewHBox(startAllButton, stopAllButton)
		am.content.Add(generalControls)

		// Информация о конфигурации
		am.content.Add(widget.NewSeparator())
		infoLabel := widget.NewLabel(fmt.Sprintf("Режим логирования: %s | База данных: %s",
			am.cfg.LogMode, am.extractServerFromConnString(am.cfg.ConnString)))
		infoLabel.TextStyle = fyne.TextStyle{Italic: true}
		am.content.Add(infoLabel)
	}

	// Обновляем отображение
	am.content.Refresh()
}

// clearSourcesFromUI убирает старые элементы источников из UI
func (am *AppManager) clearSourcesFromUI() {
	newContent := container.NewVBox()

	// Копируем первые 5 элементов (заголовок, разделитель, статус, кнопки, разделитель)
	if len(am.content.Objects) >= 5 {
		for i := 0; i < 5; i++ {
			newContent.Add(am.content.Objects[i])
		}
	}

	am.content.Objects = newContent.Objects
	am.content.Refresh()
}

// configWatcher следит за изменениями файла конфигурации
func (am *AppManager) configWatcher() {
	var lastModTime time.Time
	ticker := time.NewTicker(WatchInterval)
	defer ticker.Stop()

	for range ticker.C {
		if stat, err := os.Stat(am.configPath); err == nil {
			if !lastModTime.Equal(stat.ModTime()) {
				lastModTime = stat.ModTime()
				time.Sleep(100 * time.Millisecond)
				go func() {
					time.Sleep(1 * time.Second)
					am.reloadConfig()
				}()
			}
		}
	}
}

// toggleSource переключает состояние источника
func (am *AppManager) toggleSource(control *SourceControl) {
	if control.Runner.IsRunning() {
		control.Runner.Stop()
		control.StatusLabel.SetText(fmt.Sprintf("%s: Остановлен", control.Source.Name))
		control.RunStopBtn.SetText("Start")
	} else {
		control.Runner.Start(am.cfg.LogMode)
		control.StatusLabel.SetText(fmt.Sprintf("%s: Запущен", control.Source.Name))
		control.RunStopBtn.SetText("Stop")
	}
}

// startAllSources запускает все источники
func (am *AppManager) startAllSources() {
	for _, control := range am.sourceControls {
		if !control.Runner.IsRunning() {
			control.Runner.Start(am.cfg.LogMode)
			control.StatusLabel.SetText(fmt.Sprintf("%s: Запущен", control.Source.Name))
			control.RunStopBtn.SetText("Stop")
		}
	}
}

// stopAllSources останавливает все источники
func (am *AppManager) stopAllSources() {
	for _, control := range am.sourceControls {
		if control.Runner.IsRunning() {
			control.Runner.Stop()
			control.StatusLabel.SetText(fmt.Sprintf("%s: Остановлен", control.Source.Name))
			control.RunStopBtn.SetText("Start")
		}
	}
}

// validateConfig проверяет корректность конфигурации
func (am *AppManager) validateConfig(cfg runner.Config) error {
	var missingFields []string
	if strings.TrimSpace(cfg.LogMode) == "" {
		missingFields = append(missingFields, "LogMode")
	}
	if strings.TrimSpace(cfg.ConnString) == "" {
		missingFields = append(missingFields, "ConnString")
	}
	if len(cfg.DataSources) == 0 {
		missingFields = append(missingFields, "DataSources")
	}

	if len(missingFields) > 0 {
		return fmt.Errorf("отсутствуют обязательные поля: %s", strings.Join(missingFields, ", "))
	}
	for _, source := range cfg.DataSources {
		if source.Enabled {
			// Проверяем директорию файла данных
			dataDir := filepath.Dir(source.DataFileName)
			if _, err := os.Stat(dataDir); os.IsNotExist(err) {
				return fmt.Errorf("директория данных не существует для %s: %s", source.Name, dataDir)
			}

			// Проверяем/создаем директорию для логов
			logDir := filepath.Dir(source.LogFileName)
			if err := os.MkdirAll(logDir, 0755); err != nil {
				return fmt.Errorf("не удалось создать директорию логов для %s: %v", source.Name, err)
			}
		}
	}
	return nil
}

// extractServerFromConnString извлекает имя сервера из строки подключения
func (am *AppManager) extractServerFromConnString(connString string) string {
	parts := strings.Split(connString, ";")
	for _, part := range parts {
		if strings.HasPrefix(strings.ToLower(part), "server=") {
			return strings.TrimPrefix(part, "server=")
		}
	}
	return "неизвестно"
}

// showConfigExample показывает пример конфигурации
func (am *AppManager) showConfigExample() {
	const exampleConfig = `{
  "_comment": "Конфигурация для множественных источников данных ТЭЦ",
  "LogMode": "errors",
  "ConnString": "server=10.211.55.7,1433;database=tec1new;user id=sa;password=VeryStr0ngP@ssw0rd;encrypt=disable",
  "DataSources": [
    {
      "Name": "TG4",
      "DataFileName": "C:\\TECData\\\\Station1\\\\Tg4.dat",
      "ParserType": "tg4",
      "LogFileName": "C:\\\\Logs\\\\TEC\\\\write_attempts_tg4.log",
      "Enabled": true,
      "Quality": 262336
    },
    {
      "Name": "TG5",
      "DataFileName": "D:\\\\Equipment\\\\TG5\\\\data\\\\Tg5.dat",
      "ParserType": "tg5",
      "LogFileName": "C:\\\\Logs\\\\TG5\\\\write_attempts_tg5.log",
      "Enabled": true,
      "Quality": 262337
    }
  ]
}`

	dialog.ShowInformation("Пример конфигурации",
		fmt.Sprintf("Создайте файл %s с такой структурой:\n\n%s", am.configPath, exampleConfig),
		am.myWindow)
}

// showConfigEditor показывает редактор конфигурации
func (am *AppManager) showConfigEditor() {
	var data []byte

	if _, err := os.Stat(am.configPath); errors.Is(err, os.ErrNotExist) {
		// Файла нет - создаем с примером
		data = []byte(`{
  "_comment": "Конфигурация для множественных источников данных ТЭЦ",
  "LogMode": "errors",
  "ConnString": "server=10.211.55.7,1433;database=tec1new;user id=sa;password=VeryStr0ngP@ssw0rd;encrypt=disable",
  "DataSources": [
    {
      "Name": "TG4",
      "DataFileName": "Tg4.dat",
      "ParserType": "tg4",
      "LogFileName": "write_attempts_tg4.log",
      "Enabled": true
    }
  ]
}`)
	} else {
		data, _ = os.ReadFile(am.configPath)
	}

	entry := widget.NewMultiLineEntry()
	entry.SetText(string(data))

	configWin := fyne.CurrentApp().NewWindow("Редактировать config.json")
	configWin.Resize(fyne.NewSize(800, 600))

	saveBtn := widget.NewButton("Сохранить", func() {
		err := os.WriteFile(am.configPath, []byte(entry.Text), 0o644)
		if err != nil {
			dialog.ShowError(err, configWin)
		} else {
			configWin.Close()
			dialog.ShowInformation("Сохранено", "Конфигурация сохранена и будет автоматически перезагружена через несколько секунд.", am.myWindow)
		}
	})

	cancelBtn := widget.NewButton("Отмена", func() {
		configWin.Close()
	})

	buttons := container.NewHBox(saveBtn, cancelBtn)
	configWin.SetContent(container.NewBorder(nil, buttons, nil, nil, container.NewVScroll(entry)))
	configWin.Show()
}
