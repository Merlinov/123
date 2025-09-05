package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"time"

	"tg45/logs"
)

// FieldData для сортировки полей по IdTag (предотвращение deadlock)
type FieldData struct {
	IdTag string
	Value float64
	Field reflect.StructField
}

// SaveCurrentValuesContext - с UPSERT, deadlock retry и сортировкой для избежания дубликатов
func SaveCurrentValuesContext[T any](
	ctx context.Context,
	db *sql.DB,
	data T,
	timeStampSystem, timeStampFile time.Time,
	quality int,
	logger *log.Logger,
	logMode string,
) (err error) {
	// 🔧 DEADLOCK RETRY LOGIC
	maxRetries := 3
	for attempt := 0; attempt <= maxRetries; attempt++ {
		err = saveWithTransaction(ctx, db, data, timeStampSystem, timeStampFile, quality, logger, logMode)

		if err == nil {
			return nil // Успех!
		}

		// 🔧 ПРОВЕРЯЕМ НА DEADLOCK ERROR
		if isDeadlockError(err) && attempt < maxRetries {
			backoffDelay := time.Duration(50*(attempt+1)) * time.Millisecond // 50ms, 100ms, 150ms
			if logMode == "all" {
				logger.Printf("DEADLOCK обнаружен (попытка %d/%d), ждем %v: %v",
					attempt+1, maxRetries+1, backoffDelay, err)
			}

			// 🔧 ЛОГИРУЕМ DEADLOCK ДЛЯ СТАТИСТИКИ
			logs.LogWriteAttempt(logger, "SYSTEM", "DEADLOCK_RETRY", err, logMode)

			time.Sleep(backoffDelay)
			continue
		}

		// Не deadlock или исчерпали попытки
		break
	}

	return err
}

// isDeadlockError проверяет, является ли ошибка deadlock
func isDeadlockError(err error) bool {
	if err == nil {
		return false
	}
	errStr := strings.ToLower(err.Error())
	return strings.Contains(errStr, "was deadlocked") ||
		strings.Contains(errStr, "deadlock victim") ||
		strings.Contains(errStr, "transaction (process id")
}

// saveWithTransaction - основная логика транзакции с сортировкой для предотвращения deadlock
func saveWithTransaction[T any](
	ctx context.Context,
	db *sql.DB,
	data T,
	timeStampSystem, timeStampFile time.Time,
	quality int,
	logger *log.Logger,
	logMode string,
) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic в saveWithTransaction: %v", r)
		}
	}()

	v := reflect.ValueOf(data)
	t := reflect.TypeOf(data)

	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return fmt.Errorf("получен nil pointer")
		}
		v = v.Elem()
		t = t.Elem()
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ошибка создания транзакции: %w", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Выбираем таблицу для архивных данных
	var dataTable string
	typeName := t.Name()
	switch typeName {
	case "Data_TG4":
		dataTable = "ByMinutes"
	case "Data_TG5_Minute":
		dataTable = "ByMinutes"
	case "Data_TG5_Hour":
		dataTable = "ByHours"
	default:
		dataTable = "ByMinutes"
	}

	// 🔧 СОБИРАЕМ ВСЕ ПОЛЯ И СОРТИРУЕМ ПО IdTag ДЛЯ ПРЕДОТВРАЩЕНИЯ DEADLOCK
	var fields []FieldData
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		value := v.Field(i)

		if value.Kind() == reflect.Float32 {
			idTag := field.Tag.Get("idTag")
			if idTag == "" {
				continue
			}

			fields = append(fields, FieldData{
				IdTag: idTag,
				Value: value.Float(),
				Field: field,
			})
		}
	}

	// 🔧 СОРТИРУЕМ ПО IdTag ДЛЯ ПРЕДОТВРАЩЕНИЯ DEADLOCK
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].IdTag < fields[j].IdTag
	})

	// 🔧 ОБРАБАТЫВАЕМ В УПОРЯДОЧЕННОМ ВИДЕ
	successCount := 0
	for _, fieldData := range fields {
		// 🔧 UPSERT для CurrentValue с более специфичными блокировками
		currentValueQuery := `
MERGE CurrentValue WITH (UPDLOCK, SERIALIZABLE) AS target
USING (VALUES (@p1)) AS source (IdTag)
ON target.IdTag = source.IdTag
WHEN MATCHED THEN 
    UPDATE SET Value = @p2, Quality = @p3, timeStampSystem = @p4, TimeStamp = @p5
WHEN NOT MATCHED THEN 
    INSERT (IdTag, Value, Quality, timeStampSystem, TimeStamp)
    VALUES (@p1, @p2, @p3, @p4, @p5);`

		_, err := tx.ExecContext(ctx, currentValueQuery,
			fieldData.IdTag, fieldData.Value, quality, timeStampSystem, timeStampFile)
		if err != nil {
			logs.LogWriteAttempt(logger, fieldData.IdTag, "CURRENTVALUE_UPSERT_FAIL", err, logMode)
			return fmt.Errorf("ошибка UPSERT CurrentValue IdTag=%s: %w", fieldData.IdTag, err)
		}
		logs.LogWriteAttempt(logger, fieldData.IdTag, "CURRENTVALUE_UPSERT_SUCCESS", nil, logMode)

		// 🔧 UPSERT для архивной таблицы с блокировками
		archiveQuery := fmt.Sprintf(`
MERGE %s WITH (UPDLOCK, SERIALIZABLE) AS target
USING (VALUES (@p1, @p2)) AS source (IdTag, TimeStamp)
ON target.IdTag = source.IdTag AND target.TimeStamp = source.TimeStamp
WHEN MATCHED THEN 
    UPDATE SET Value = @p3, Quality = @p4, timeStampSystem = @p5
WHEN NOT MATCHED THEN 
    INSERT (IdTag, TimeStamp, Value, Quality, timeStampSystem)
    VALUES (@p1, @p2, @p3, @p4, @p5);`, dataTable)

		// 🔧 ДОБАВЬ ДЕТАЛЬНОЕ ЛОГИРОВАНИЕ
		if logMode == "all" {
			logger.Printf("[DEBUG] Выполняем MERGE в %s для IdTag=%s, TimeStamp=%v",
				dataTable, fieldData.IdTag, timeStampFile)
		}

		_, err = tx.ExecContext(ctx, archiveQuery,
			fieldData.IdTag, timeStampFile, fieldData.Value, quality, timeStampSystem)
		if err != nil {
			// 🔧 ДОБАВЬ БОЛЕЕ ДЕТАЛЬНОЕ ЛОГИРОВАНИЕ ОШИБКИ
			logger.Printf("[ERROR] MERGE в %s failed для IdTag=%s: %v", dataTable, fieldData.IdTag, err)
			logs.LogWriteAttempt(logger, fieldData.IdTag, fmt.Sprintf("%s_UPSERT_FAIL", dataTable), err, logMode)
			// НЕ роняем транзакцию для архивной таблицы
		} else {
			if logMode == "all" {
				logger.Printf("[DEBUG] MERGE в %s успешно для IdTag=%s", dataTable, fieldData.IdTag)
			}
			logs.LogWriteAttempt(logger, fieldData.IdTag, fmt.Sprintf("%s_UPSERT_SUCCESS", dataTable), nil, logMode)
		}

		successCount++
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ошибка коммита транзакции: %w", err)
	}

	if logMode == "all" {
		logger.Printf("UPSERT завершен: %d значений в %s", successCount, dataTable)
	}

	return nil
}

