package main

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
	"github.com/sasha-s/go-deadlock"
	"github.com/skratchdot/open-golang/open"
	"gopkg.in/natefinch/lumberjack.v2"

	"tg45/runner"
	"tg45/source"

	_ "github.com/denisenkom/go-mssqldb"
)

const (
	AppTitle      = "Файлообмен ТЭЦ"
	ConfigPath    = "config.json"
	IconPath      = "icon.png"
	WindowWidth   = 900
	WindowHeight  = 700
	WatchInterval = 5 * time.Second
	StatusStopped = "Остановлен"
	StatusRunning = "Запущен"
	ButtonStart   = "Запустить"
	ButtonStop    = "Остановить"
)

type AppManager struct {
	configPath    string
	cfg           runner.Config
	sourceManager *source.Manager
	content       *fyne.Container
	myWindow      fyne.Window
	configValid   bool
	mutex         sync.RWMutex // ← ДОБАВЛЯЕМ mutex
	// UI элементы
	errorLabel       *widget.Label
	reloadButton     *widget.Button
	editConfigButton *widget.Button
	// поля для мониторинга
	healthMonitorRunning bool
	healthTicker         *time.Ticker
	ctx                  context.Context
	cancelFunc           context.CancelFunc
	startTime            time.Time
	healthMetrics        *HealthMetrics
	appLogger            *log.Logger
}

func main() {
	// Проверяем единственность экземпляра (почему то 2 висит)
	singleInstance := NewSingleInstance("TG45DataCollector")
	locked, err := singleInstance.Lock()
	if err != nil || !locked {
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
	} // 🔧 ДОБАВЬ GRACEFUL CLEANUP
	defer func() {
		if err := singleInstance.Release(); err != nil {
			log.Printf("Ошибка при освобождении блокировки: %v", err)
		}
	}()

	// 🔧 SIGNAL HANDLING для graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		log.Println("Получен сигнал завершения...")
		os.Exit(0) // defer выполнится автоматически
	}()пон

	myApp := app.New()
	myWindow := myApp.NewWindow(AppTitle)
	myWindow.Resize(fyne.NewSize(WindowWidth, WindowHeight))

	if icon, err := fyne.LoadResourceFromPath(IconPath); err == nil {
		myWindow.SetIcon(icon)
	}
	ctx, cancel := context.WithCancel(context.Background())

	appManager := &AppManager{
		configPath:    ConfigPath,
		myWindow:      myWindow,
		configValid:   false,
		ctx:           ctx,
		cancelFunc:    cancel,
		startTime:     time.Now(),
		sourceManager: source.NewManager(),
	}
	// Запуск приложения
	appManager.run()
}

func (am *AppManager) initAppLogger() {
	var maxSize int
	switch am.cfg.LogMode {
	case "all":
		maxSize = 50 // больше логов = больше файлы
	case "errors":
		maxSize = 10 // меньше логов = меньше файлы
	default:
		maxSize = 20
	}

	am.appLogger = log.New(&lumberjack.Logger{
		Filename:   "app.log",
		MaxSize:    maxSize,
		MaxBackups: 3,
		MaxAge:     30,
		Compress:   true,
	}, "[APP] ", log.LstdFlags)

	am.appLogger.Printf("Logger инициализирован с LogMode: %s (MaxSize: %dMB)", am.cfg.LogMode, maxSize)
}

// дефолтный logger если конфиг не загружен
func (am *AppManager) initDefaultLogger() {
	am.appLogger = log.New(&lumberjack.Logger{
		Filename:   "app.log",
		MaxSize:    20,
		MaxBackups: 3,
		MaxAge:     30,
		Compress:   true,
	}, "[APP] ", log.LstdFlags)

	am.appLogger.Println("Logger инициализирован с дефолтными настройками (конфиг не загружен)")
}

// Главный метод запуска приложения
func (am *AppManager) run() {
	// Инициализация HealthMetrics
	am.healthMetrics = &HealthMetrics{
		StartTime: time.Now(),
		mutex:     deadlock.RWMutex{},
	}

	am.initializeUI()
	am.reloadConfig()

	if am.configValid {
		am.initAppLogger()
	} else {
		am.initDefaultLogger()
	}

	go am.configWatcher()

	// Системный трей (сворачивается в трей, завершается только там)
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
			fyne.NewMenuItem("Полный выход", func() {
				am.shutdown()
			}),
		)
		desk.SetSystemTrayMenu(menu)
	}

	am.myWindow.SetCloseIntercept(func() {
		am.myWindow.Hide()
	})
	//можно еще на ctrl-Q завершить, для удобства
	ctrlQ := &desktop.CustomShortcut{KeyName: fyne.KeyQ, Modifier: fyne.KeyModifierControl}
	am.myWindow.Canvas().AddShortcut(ctrlQ, func(shortcut fyne.Shortcut) {
		am.shutdown()
	})

	// 🎯 Автозапуск ПЕРЕД ShowAndRun
	if am.configValid && am.cfg.StartAllOnLaunch {
		go func() {
			select {
			case <-am.ctx.Done():
				return // Graceful shutdown
			case <-time.After(1 * time.Second):
				am.delayedAutostart()
			}
		}()
	}

	am.myWindow.ShowAndRun() // ← ЕДИНСТВЕННЫЙ ВЫЗОВ
}

// метод для отложенного автозапуска
func (am *AppManager) delayedAutostart() {
	// Ждем чтобы UI успел полностью отрисоваться
	time.Sleep(1 * time.Second)

	am.logToApp("Автозапуск включен в конфиге - запускаем все источники...")
	if err := am.sourceManager.StartAll(); err != nil {
		am.logCriticalEvent(fmt.Sprintf("Ошибка автозапуска при запуске: %v", err))
	} else {
		am.logToApp("Автозапуск завершен успешно")
	}

	// Обновляем UI с правильными цветами кнопок
	fyne.Do(func() {
		am.clearSourcesFromUI()
		am.updateSourcesUI()
	})
}

func (am *AppManager) logToApp(message string) {
	if am.appLogger != nil {
		am.appLogger.Println(message)
	} else {
		log.Println("[FALLBACK]", message)
	}
}

// Новые методы для управления
func (am *AppManager) showStatusDialog() {
	status := "Статус источников:\n\n"

	runners := am.sourceManager.GetRunners()
	for _, runner := range runners {
		runStatus := "Остановлен"
		if runner.IsRunning() {
			runStatus = "Запущен"
		}
		status += fmt.Sprintf("• %s: %s\n", runner.Source.Name, runStatus)
	}

	dialog.ShowInformation("Статус системы", status, am.myWindow)
}

func (am *AppManager) getSafeLogMode() string {
	am.mutex.RLock()
	logMode := am.cfg.LogMode
	am.mutex.RUnlock()
	return logMode
}

// В shutdown:
func (am *AppManager) shutdown() {
	am.logToApp("Начинаем shutdown...")
	am.stopHealthMonitoring()

	// 🔧 ДОБАВЬ ОЧИСТКУ HealthMetrics
	if am.healthMetrics != nil {
		am.healthMetrics.mutex.Lock()
		am.healthMetrics = nil
		am.healthMetrics.mutex.Unlock()
	}

	if am.cancelFunc != nil {
		am.cancelFunc()
	}

	time.Sleep(1 * time.Second)
	am.closeOldSources()

	am.logToApp("Shutdown завершен")
	fyne.CurrentApp().Quit()
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
	am.mutex.Lock()
	defer am.mutex.Unlock()

	// Сохраняем текущие состояния
	var runningStates map[string]bool
	if am.sourceManager != nil {
		runningStates = am.sourceManager.GetStatus()
	} else {
		runningStates = make(map[string]bool)
	}

	// Останавливаем мониторинг
	am.stopHealthMonitoring()

	// Очищаем UI
	am.clearSourcesFromUI()

	// Проверяем существование файла
	if _, err := os.Stat(am.configPath); errors.Is(err, os.ErrNotExist) {
		am.configValid = false
		am.errorLabel.SetText("❌ Файл config.json не найден")
		am.showConfigExample()
		return
	}

	// Загружаем конфиг
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

	// Конфиг валидный
	am.cfg = cfg
	am.configValid = true
	am.errorLabel.SetText("✅ Конфигурация загружена успешно")

	// Создаем контролы через менеджер
	if err := am.sourceManager.CreateControls(cfg); err != nil {
		am.errorLabel.SetText(fmt.Sprintf("❌ Ошибка создания контролов: %v", err))
		return
	}

	// Обновляем UI
	am.updateSourcesUI()
	am.startHealthMonitoring()

	// Восстанавливаем состояния
	runners := am.sourceManager.GetRunners()
	for _, runner := range runners {
		if wasRunning, exists := runningStates[runner.Source.Name]; exists && wasRunning {
			runner.Start(am.cfg.LogMode)
		}
	}

	// // Автозапуск при первом запуске
	// if len(runningStates) == 0 && am.configValid && am.cfg.StartAllOnLaunch {
	// 	//am.appLogger.Println("Автозапуск включен в конфиге - запускаем все источники...")
	// 	am.sourceManager.StartAll()
	// 	//am.appLogger.Println("Автозапуск завершен")
	// }
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
			am.appLogger.Println("HealthMonitoring: горутина завершена")
		}()

		for {
			select {
			case <-am.ctx.Done(): // ДОБАВЬ проверку контекста
				am.appLogger.Println("HealthMonitoring: получен сигнал остановки")
				return
			case <-am.healthTicker.C:
				am.checkSourcesHealth()
			}
		}
	}()
}

func (am *AppManager) checkSourcesHealth() {
	//добавь проверку context
	select {
	case <-am.ctx.Done():
		return
	default:
		// Продолжаем
	}
	runners := am.sourceManager.GetRunners()
	if len(runners) == 0 {
		return
	}

	for _, runner := range runners {
		if runner == nil {
			continue
		}

		currentlyRunning := runner.IsRunning()

		if _, err := os.Stat(runner.Source.DataFileName); os.IsNotExist(err) {
			am.logCriticalEvent(fmt.Sprintf("Файл данных недоступен: %s", runner.Source.DataFileName))
			if currentlyRunning {
				runner.Stop()
				// ПРОВЕРКА CONTEXT в fyne.Do а то кнопочки приуныли
				fyne.Do(func() {
					select {
					case <-am.ctx.Done():
						return // Не обновляем UI после shutdown
					default:
						am.updateAllButtonColors()
					}
				})
			}
			continue
		}

		if !currentlyRunning {
			am.logCriticalEvent(fmt.Sprintf("Источник %s неожиданно остановился", runner.Source.Name))
		}
	}
}

func (am *AppManager) stopHealthMonitoring() {
	if am.healthTicker != nil {
		am.healthTicker.Stop()
		am.healthMonitorRunning = false
		am.healthTicker = nil
	}
}

func (am *AppManager) closeOldSources() {
	if am.sourceManager != nil {
		am.sourceManager.Close()
	}
}

// createStyledButton создает кнопку с цветным фоном
// func createStyledButton(text string, bgColor color.Color, onTapped func()) fyne.CanvasObject {
// 	button := widget.NewButton(text, onTapped)

// 	// Создаем цветной прямоугольник как фон
// 	bgRect := canvas.NewRectangle(bgColor)

// 	// Помещаем кнопку поверх цветного фона
// 	return container.NewStack(bgRect, button)
// }

// updateSourcesUI обновляет UI с источниками данных
func (am *AppManager) updateSourcesUI() {
	if !am.configValid {
		return
	}

	runners := am.sourceManager.GetRunners()

	if len(runners) == 0 {
		noSourcesLabel := widget.NewLabel("Нет активных источников данных.\nПроверьте настройку 'Enabled' в config.json")
		am.content.Add(noSourcesLabel)
	} else {
		gridContainer := container.New(layout.NewGridLayout(4))

		// Заголовки
		gridContainer.Add(widget.NewLabelWithStyle("Источник", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		gridContainer.Add(widget.NewLabelWithStyle("Статус", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		gridContainer.Add(widget.NewLabelWithStyle("Управление", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}))
		gridContainer.Add(widget.NewLabelWithStyle("Логи", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}))

		// Данные по источникам
		for _, runner := range runners {
			currentRunner := runner
			gridContainer.Add(widget.NewLabel(runner.Source.Name))

			// Статус
			statusText := fmt.Sprintf("%s: %s", runner.Source.Name, StatusStopped)
			if runner.IsRunning() {
				statusText = fmt.Sprintf("%s: %s", runner.Source.Name, StatusRunning)
			}
			statusLabel := widget.NewLabel(statusText)
			gridContainer.Add(statusLabel)

			// Кнопка управления
			var styledButton fyne.CanvasObject
			if runner.IsRunning() {
				styledButton = createCustomColorButton(ButtonStop,
					&color.NRGBA{R: 46, G: 125, B: 50, A: 255},
					&color.NRGBA{R: 255, G: 255, B: 255, A: 255},
					func() {
						am.toggleRunner(currentRunner)
					})
			} else {
				styledButton = createCustomColorButton(ButtonStart,
					&color.NRGBA{R: 233, G: 30, B: 99, A: 255},
					&color.NRGBA{R: 255, G: 255, B: 255, A: 255},
					func() {
						am.toggleRunner(currentRunner)
					})
			}
			gridContainer.Add(styledButton)
			// Кнопка логов
			logBtn := widget.NewButton("Лог", func() {
				if err := open.Run(runner.Source.LogFileName); err != nil {
					dialog.ShowError(err, am.myWindow)
				}
			})
			gridContainer.Add(logBtn)
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

func (am *AppManager) toggleRunner(runner *runner.SourceRunner) {
	am.mutex.RLock()
	logMode := am.cfg.LogMode
	am.mutex.RUnlock()

	// Синхронизируем целиком операцию toggle
	if runner.IsRunning() {
		runner.Stop()
		// Обновляем UI сразу после остановки
		fyne.Do(func() {
			am.updateAllButtonColors()
		})
	} else {
		runner.Start(logMode)
		// Обновляем UI сразу после запуска
		fyne.Do(func() {
			am.updateAllButtonColors()
		})
	}
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
			am.appLogger.Println("ConfigWatcher: получен сигнал остановки")
			return
		case <-ticker.C:
			if stat, err := os.Stat(am.configPath); err == nil {
				currentModTime := stat.ModTime()
				if !lastModTime.IsZero() && !lastModTime.Equal(currentModTime) {
					am.appLogger.Printf("Config file changed: %v -> %v\n", lastModTime, currentModTime)
					lastModTime = currentModTime
					time.Sleep(100 * time.Millisecond)

					// Проверяем контекст перед перезагрузкой
					select {
					case <-am.ctx.Done():
						return
					default:
						go func() {
							select {
							case <-am.ctx.Done():
								return
							case <-time.After(1 * time.Second):
								// Проверяем еще раз перед вызовом
								select {
								case <-am.ctx.Done():
									return
								default:
									am.reloadConfig()
								}
							}
						}()
					}
				} else if lastModTime.IsZero() {
					lastModTime = currentModTime
				}
			}
		}
	}
}

// startAllSources запускает все источники
func (am *AppManager) startAllSources() {
	if err := am.sourceManager.StartAll(); err != nil {
		am.logCriticalEvent(fmt.Sprintf("Ошибка запуска всех источников: %v", err))
	}
}

// метрики и health endpoint
type HealthMetrics struct {
	StartTime        time.Time
	TotalRestarts    int64
	LastError        string
	SuccessfulWrites int64
	FailedWrites     int64
	LastSuccessWrite time.Time
	mutex            deadlock.RWMutex
}

// критические алерты
func (am *AppManager) logCriticalEvent(message string) {
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	criticalLog := fmt.Sprintf("[CRITICAL] %s: %s\n", timestamp, message)

	f, err := os.OpenFile("critical.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Ошибка записи в critical.log: %v", err)
		fmt.Print(criticalLog) // Fallback в консоль
		return
	}
	defer f.Close()

	if _, err := f.WriteString(criticalLog); err != nil {
		log.Printf("Ошибка записи критического события: %v", err)
	}

	fmt.Print(criticalLog)
}

// stopAllSources останавливает все источники
func (am *AppManager) stopAllSources() {
	if err := am.sourceManager.StopAll(); err != nil {
		am.logCriticalEvent(fmt.Sprintf("Ошибка остановки всех источников: %v", err))
	}
}

// updateAllButtonColors обновляет цвета всех кнопок после массовых операций
func (am *AppManager) updateAllButtonColors() {
	// Безопасное обновление UI
	fyne.Do(func() {
		am.clearSourcesFromUI()
		am.updateSourcesUI()
	})
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
  "LogMode": "errors",
  "ConnString": "server=10.211.55.7,1433;database=tec1new;user id=sa;password=VeryStr0ngP@ssw0rd;encrypt=disable",
  "StartAllOnLaunch": true,
  "DataSources": [
    {
      "Name": "TG4",
      "DataFileName": "C:/TECData/Station1/Tg4.dat",
      "ParserType": "tg4",
      "LogFileName": "C:/Logs/TEC/write_attempts_tg4.log",
      "Enabled": true,
      "Quality": 262336
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
  "LogMode": "errors",
  "ConnString": "server=10.211.55.7,1433;database=tec1new;user id=sa;password=VeryStr0ngP@ssw0rd;encrypt=disable",
  "StartAllOnLaunch": true,
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
		// 🔧 ДОБАВЬ ПРОВЕРКУ ОШИБКИ
		var err error
		data, err = os.ReadFile(am.configPath)
		if err != nil {
			am.logCriticalEvent(fmt.Sprintf("Ошибка чтения конфига для редактирования: %v", err))
			return
		}
	}

	entry := widget.NewMultiLineEntry()
	entry.SetText(string(data))

	configWin := fyne.CurrentApp().NewWindow("Редактировать config.json")
	configWin.Resize(fyne.NewSize(800, 600))

	//  АВТОМАТИЧЕСКОЕ ЗАКРЫТИЕ ОКНА ПРИ SHUTDOWN
	go func() {
		<-am.ctx.Done()   // Ожидаем сигнал shutdown
		configWin.Close() // Автоматически закрываем окно
	}()

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
