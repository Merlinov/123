package main

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
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
	StatusStopped = "Остановлен"
	StatusRunning = "Запущен"

	// Тексты кнопок
	ButtonStart = "Запустить"
	ButtonStop  = "Остановить"
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
	//  поля для мониторинга
	healthMonitorRunning bool
	healthTicker         *time.Ticker
	ctx                  context.Context
	cancelFunc           context.CancelFunc
}

func main() {
	// Проверяем единственность экземпляра СРАЗУ
	singleInstance := NewSingleInstance("TG45DataCollector")
	locked, err := singleInstance.Lock()
	if err != nil || !locked {
		// Показываем пользователю сообщение об ошибке
		tempApp := app.New()
		tempWindow := tempApp.NewWindow("Ошибка")
		tempWindow.Resize(fyne.NewSize(400, 200))

		var errorMsg string
		if err != nil {
			errorMsg = fmt.Sprintf("Ошибка запуска: %v", err)
		} else {
			errorMsg = "Приложение уже запущено!\n\nПроверьте системный трей или диспетчер задач."
		}

		dialog.ShowError(errors.New(errorMsg), tempWindow)
		tempWindow.ShowAndRun()
		os.Exit(1)
	}

	// Освобождаем лок при завершении приложения
	defer singleInstance.Release()

	myApp := app.New()
	myWindow := myApp.NewWindow(AppTitle)
	myWindow.Resize(fyne.NewSize(WindowWidth, WindowHeight))

	if icon, err := fyne.LoadResourceFromPath(IconPath); err == nil {
		myWindow.SetIcon(icon)
	}
	ctx, cancel := context.WithCancel(context.Background())

	appManager := &AppManager{
		configPath:  ConfigPath,
		myWindow:    myWindow,
		configValid: false,
		ctx:         ctx,
		cancelFunc:  cancel,
	}

	// Запуск приложения
	appManager.run()
}

// Главный метод запуска приложения
func (am *AppManager) run() {
	am.initializeUI()
	am.reloadConfig()
	go am.configWatcher()

	// ДОБАВЛЯЕМ поддержку системного трея
	if desk, ok := fyne.CurrentApp().(desktop.App); ok {
		menu := fyne.NewMenu("TG45 ТЭЦ",
			fyne.NewMenuItem("Показать", func() {
				am.myWindow.Show()
				am.myWindow.RequestFocus()
			}),
			fyne.NewMenuItem("Статус", func() {
				am.showStatusDialog()
			}),
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Скрыть в трей", func() {
				am.myWindow.Hide()
			}),
			fyne.NewMenuItem("Полный выход", func() { // НОВЫЙ пункт
				am.shutdown()
			}),
		)
		desk.SetSystemTrayMenu(menu)
	}

	// Изменяем поведение закрытия - сворачиваем в трей вместо выхода
	am.myWindow.SetCloseIntercept(func() {
		am.myWindow.Hide()
	})

	// ДОБАВЬ возможность выхода по Ctrl+Q
	ctrlQ := &desktop.CustomShortcut{KeyName: fyne.KeyQ, Modifier: fyne.KeyModifierControl}
	am.myWindow.Canvas().AddShortcut(ctrlQ, func(shortcut fyne.Shortcut) {
		am.shutdown()
	})

	am.myWindow.ShowAndRun()
}

// Новые методы для управления
func (am *AppManager) showStatusDialog() {
	status := "Статус источников:\n\n"
	for _, control := range am.sourceControls {
		runStatus := "Остановлен"
		if control.Runner.IsRunning() {
			runStatus = "Запущен"
		}
		status += fmt.Sprintf("• %s: %s\n", control.Source.Name, runStatus)
	}

	dialog.ShowInformation("Статус системы", status, am.myWindow)
}

func (am *AppManager) shutdown() {

	fmt.Println("Начинаем shutdown...")

	am.stopHealthMonitoring()

	if am.cancelFunc != nil {
		am.cancelFunc()
	}

	time.Sleep(1 * time.Second)
	am.closeOldSources()

	fyne.CurrentApp().Quit()
	fmt.Println("Shutdown завершен")
}

func (am *AppManager) updateSourceStatus(control *SourceControl, isRunning bool) {
	sourceName := control.Source.Name
	if isRunning {
		control.StatusLabel.SetText(fmt.Sprintf("%s: %s", sourceName, StatusRunning))
		control.RunStopBtn.SetText(ButtonStop)
	} else {
		control.StatusLabel.SetText(fmt.Sprintf("%s: %s", sourceName, StatusStopped))
		control.RunStopBtn.SetText(ButtonStart)
	}

	control.RunStopBtn.Refresh()
	control.StatusLabel.Refresh()
}

// Функция для создания кнопки с кастомным цветом
func createCustomColorButton(text string, bgColor color.Color, textColor color.Color, onTapped func()) fyne.CanvasObject {
	button := widget.NewButton(text, onTapped)

	customTheme := &customButtonTheme{
		buttonColor: bgColor,
		textColor:   textColor,
	}

	return container.NewThemeOverride(button, customTheme)
}

// customButtonTheme - кастомная тема для кнопки
// Кастомная тема для цветных кнопок
// ИСПРАВЛЕННАЯ customButtonTheme
type customButtonTheme struct {
	buttonColor color.Color
	textColor   color.Color
}

func (t *customButtonTheme) Color(name fyne.ThemeColorName, variant fyne.ThemeVariant) color.Color {
	switch name {
	// ПРАВИЛЬНЫЕ названия констант в Fyne:
	case theme.ColorNameButton:
		return t.buttonColor
	case theme.ColorNameForeground:
		return t.textColor
	default:
		return theme.DefaultTheme().Color(name, variant)
	}
}

func (t *customButtonTheme) Font(style fyne.TextStyle) fyne.Resource {
	return theme.DefaultTheme().Font(style)
}

func (t *customButtonTheme) Icon(name fyne.ThemeIconName) fyne.Resource {
	return theme.DefaultTheme().Icon(name)
}

func (t *customButtonTheme) Size(name fyne.ThemeSizeName) float32 {
	return theme.DefaultTheme().Size(name)
}

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
	// Сохраняем состояние запущенных источников
	runningStates := make(map[string]bool)
	for _, control := range am.sourceControls {
		if control.Runner != nil {
			runningStates[control.Source.Name] = control.Runner.IsRunning()
		}
	}
	// Останавливаем мониторинг если был запущен
	am.stopHealthMonitoring()
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
	// ДОБАВЛЯЕМ запуск мониторинга здоровья
	am.startHealthMonitoring()
	// Восстанавливаем состояние после создания контролов
	for _, control := range am.sourceControls {
		if wasRunning, exists := runningStates[control.Source.Name]; exists && wasRunning {
			control.Runner.Start(am.cfg.LogMode)
			am.updateSourceStatus(control, true)
		}
	}
}

func (am *AppManager) startHealthMonitoring() {
	if !am.configValid || am.healthMonitorRunning {
		return
	}

	am.healthMonitorRunning = true
	am.healthTicker = time.NewTicker(30 * time.Second)

	go func() {
		defer func() {
			am.healthMonitorRunning = false
			if am.healthTicker != nil {
				am.healthTicker.Stop()
			}
			fmt.Println("HealthMonitoring: горутина завершена")
		}()

		for {
			select {
			case <-am.ctx.Done(): // ДОБАВЬ проверку контекста
				fmt.Println("HealthMonitoring: получен сигнал остановки")
				return
			case <-am.healthTicker.C:
				am.checkSourcesHealth()
			}
		}
	}()
}

func (am *AppManager) checkSourcesHealth() {
	if am.sourceControls == nil {
		return
	}

	for _, control := range am.sourceControls {
		if control.Runner == nil {
			continue
		}

		wasRunning := control.RunStopBtn.Text == ButtonStop // проверяем по "Stop"
		isRunning := control.Runner.IsRunning()

		if wasRunning != isRunning {
			am.updateSourceStatus(control, isRunning)
		}

		if !isRunning && wasRunning {
			control.StatusLabel.SetText(fmt.Sprintf("%s: Ошибка - автоперезапуск", control.Source.Name))
		}
	}
}

func (am *AppManager) stopHealthMonitoring() {
	if am.healthTicker != nil {
		am.healthTicker.Stop()
		am.healthMonitorRunning = false
	}
}

// createSourceControls создает контролы для источников данных
func (am *AppManager) createSourceControls() {
	// СНАЧАЛА закрываем старые ресурсы если они есть
	am.closeOldSources()

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

		am.createSourceButton(control, source)
		am.sourceControls = append(am.sourceControls, control)
	}
}

func (am *AppManager) closeOldSources() {
	if am.sourceControls == nil {
		return
	}

	for _, control := range am.sourceControls {
		if control.Runner != nil {
			// Останавливаем, если запущен
			if control.Runner.IsRunning() {
				control.Runner.Stop()
			}
			// Закрываем ресурсы (соединения с БД, файлы логов)
			if err := control.Runner.Close(); err != nil {
				// Логируем через errorLabel в UI, а не fmt.Printf
				am.errorLabel.SetText(fmt.Sprintf("⚠️ Ошибка закрытия ресурсов для %s: %v", control.Source.Name, err))
			}
		}
	}

	am.sourceControls = nil
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

// createStyledButton создает кнопку с цветным фоном
func createStyledButton(text string, bgColor color.Color, onTapped func()) fyne.CanvasObject {
	button := widget.NewButton(text, onTapped)

	// Создаем цветной прямоугольник как фон
	bgRect := canvas.NewRectangle(bgColor)

	// Помещаем кнопку поверх цветного фона
	return container.NewStack(bgRect, button)
}

// updateSourcesUI обновляет UI с источниками данных
func (am *AppManager) updateSourcesUI() {
	if !am.configValid {
		return
	}

	if len(am.sourceControls) == 0 {
		noSourcesLabel := widget.NewLabel("Нет активных источников данных.\nПроверьте настройку 'Enabled' в config.json")
		am.content.Add(noSourcesLabel)
	} else {
		gridContainer := container.New(layout.NewGridLayout(4))

		// Заголовки
		gridContainer.Add(widget.NewLabelWithStyle("Источник", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		gridContainer.Add(widget.NewLabelWithStyle("Статус", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		gridContainer.Add(widget.NewLabelWithStyle("Управление", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}))
		gridContainer.Add(widget.NewLabelWithStyle("Логи", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}))

		// Данные по источникам с КАСТОМНЫМИ ЦВЕТАМИ
		for _, control := range am.sourceControls {
			gridContainer.Add(widget.NewLabel(control.Source.Name))
			gridContainer.Add(control.StatusLabel)

			// КНОПКА С КАСТОМНЫМИ ЦВЕТАМИ в зависимости от состояния
			var styledButton fyne.CanvasObject
			if control.Runner != nil && control.Runner.IsRunning() {
				// ОРАНЖЕВАЯ кнопка Stop для запущенного источника
				styledButton = createCustomColorButton(ButtonStop,
					&color.NRGBA{R: 46, G: 125, B: 50, A: 255}, //фон

					&color.NRGBA{R: 255, G: 255, B: 255, A: 255}, //текст
					control.RunStopBtn.OnTapped)
			} else {
				// ФИОЛЕТОВАЯ кнопка Start для остановленного источника
				styledButton = createCustomColorButton(ButtonStart,
					&color.NRGBA{R: 233, G: 30, B: 99, A: 255},
					&color.NRGBA{R: 255, G: 255, B: 255, A: 255},
					control.RunStopBtn.OnTapped)
			}

			gridContainer.Add(styledButton)
			gridContainer.Add(control.ViewLogBtn)
		}

		am.content.Add(gridContainer)

		am.content.Add(widget.NewSeparator())

		startAllButton := widget.NewButton("Запустить все", func() {
			am.startAllSources()
			// ДОБАВЬ обновление цветов после массовых операций
			am.updateAllButtonColors()
		})

		stopAllButton := widget.NewButton("Остановить все", func() {
			am.stopAllSources()
			// ДОБАВЬ обновление цветов после массовых операций
			am.updateAllButtonColors()
		})

		generalControls := container.NewHBox(startAllButton, stopAllButton)
		am.content.Add(generalControls)

		am.content.Add(widget.NewSeparator())
		infoLabel := widget.NewLabel(fmt.Sprintf("Режим логирования: %s | База данных: %s",
			am.cfg.LogMode, am.extractServerFromConnString(am.cfg.ConnString)))
		infoLabel.TextStyle = fyne.TextStyle{Italic: true}
		am.content.Add(infoLabel)
	}

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
	if stat, err := os.Stat(am.configPath); err == nil {
		lastModTime = stat.ModTime()
	}

	ticker := time.NewTicker(WatchInterval)
	defer ticker.Stop()

	for {
		select {
		case <-am.ctx.Done(): // ДОБАВЬ проверку контекста
			fmt.Println("ConfigWatcher: получен сигнал остановки")
			return
		case <-ticker.C:
			if stat, err := os.Stat(am.configPath); err == nil {
				currentModTime := stat.ModTime()
				if !lastModTime.IsZero() && !lastModTime.Equal(currentModTime) {
					fmt.Printf("Config file changed: %v -> %v\n", lastModTime, currentModTime)
					lastModTime = currentModTime
					time.Sleep(100 * time.Millisecond)

					// Проверяем контекст перед перезагрузкой
					select {
					case <-am.ctx.Done():
						return
					default:
						go func() {
							time.Sleep(1 * time.Second)
							am.reloadConfig()
						}()
					}
				} else if lastModTime.IsZero() {
					lastModTime = currentModTime
				}
			}
		}
	}
}

// toggleSource переключает состояние источника
func (am *AppManager) toggleSource(control *SourceControl) {
	if control.Runner.IsRunning() {
		control.Runner.Stop()
		am.updateSourceStatus(control, false)
	} else {
		control.Runner.Start(am.cfg.LogMode)
		am.updateSourceStatus(control, true)
	}

	// ДОБАВЬ ПРИНУДИТЕЛЬНОЕ ОБНОВЛЕНИЕ ИНТЕРФЕЙСА!
	am.updateAllButtonColors()
}

// startAllSources запускает все источники
func (am *AppManager) startAllSources() {
	if am.sourceControls == nil {
		return
	}

	for _, control := range am.sourceControls {
		if control.Runner != nil && !control.Runner.IsRunning() {
			control.Runner.Start(am.cfg.LogMode)
			control.StatusLabel.SetText(fmt.Sprintf("%s: %s", control.Source.Name, StatusRunning)) // "Запущен"
			control.RunStopBtn.SetText(ButtonStop)                                                 // "Stop"
		}
	}
}

// stopAllSources останавливает все источники
func (am *AppManager) stopAllSources() {
	if am.sourceControls == nil {
		return
	}

	for _, control := range am.sourceControls {
		if control.Runner != nil && control.Runner.IsRunning() {
			control.Runner.Stop()
			control.StatusLabel.SetText(fmt.Sprintf("%s: %s", control.Source.Name, StatusStopped)) // "Остановлен"
			control.RunStopBtn.SetText(ButtonStart)                                                // "Start"
		}
	}
}

// updateAllButtonColors обновляет цвета всех кнопок после массовых операций
func (am *AppManager) updateAllButtonColors() {
	// Перерисовываем весь интерфейс с обновленными цветами кнопок
	am.clearSourcesFromUI()
	am.updateSourcesUI()
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
