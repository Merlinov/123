package db

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"reflect"
	"time"

	"tg45/logs"
)

// SaveCurrentValuesContext - ЕДИНСТВЕННАЯ функция с правильным context handling
func SaveCurrentValuesContext[T any](
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
			err = fmt.Errorf("panic в SaveCurrentValuesContext: %v", r)
		}
	}()

	// 🔧 ИСПРАВЛЕН reflection для указателей
	v := reflect.ValueOf(data)
	t := reflect.TypeOf(data)

	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return fmt.Errorf("получен nil pointer")
		}
		v = v.Elem()
		t = t.Elem() // 🔧 ВАЖНО: тип тоже нужно "развернуть"
	}

	// 🔧 ДОБАВИМ ТРАНЗАКЦИЮ для atomic операций
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("ошибка создания транзакции: %w", err)
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	successCount := 0
	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		value := v.Field(i)

		if value.Kind() == reflect.Float32 {
			idTag := field.Tag.Get("idTag")
			if idTag == "" {
				continue
			}
			val := value.Float()

			// 🔧 ИСПОЛЬЗУЕМ ПЕРЕДАННЫЙ CONTEXT, а не создаем новый в цикле
			res, err := tx.ExecContext(ctx,
				"UPDATE CurrentValue SET Value=@p1, Quality=@p5, timeStampSystem=@p2, TimeStamp=@p3 WHERE IdTag=@p4",
				val, timeStampSystem, timeStampFile, idTag, quality,
			)
			if err != nil {
				logs.LogWriteAttempt(logger, idTag, "UPDATE_FAIL", err, logMode)
				return fmt.Errorf("ошибка UPDATE IdTag=%s: %w", idTag, err)
			}

			rows, err := res.RowsAffected()
			if err != nil {
				logs.LogWriteAttempt(logger, idTag, "ROWSAFFECTED_FAIL", err, logMode)
				return fmt.Errorf("ошибка получения RowsAffected для IdTag=%s: %w", idTag, err)
			}

			if rows == 0 {
				// 🔧 КОНСИСТЕНТНОЕ использование context во всех операциях
				_, err := tx.ExecContext(ctx,
					"INSERT INTO CurrentValue (IdTag, Value, timeStampSystem, TimeStamp, Quality) VALUES (@p1, @p2, @p3, @p4, @p5)",
					idTag, val, timeStampSystem, timeStampFile, quality,
				)
				if err != nil {
					logs.LogWriteAttempt(logger, idTag, "INSERT_FAIL", err, logMode)
					return fmt.Errorf("ошибка INSERT IdTag=%s: %w", idTag, err)
				}
				logs.LogWriteAttempt(logger, idTag, "INSERT_SUCCESS", nil, logMode)
			} else {
				logs.LogWriteAttempt(logger, idTag, "UPDATE_SUCCESS", nil, logMode)
			}

			// 🔧 Data таблица тоже в транзакции
			_, err = tx.ExecContext(ctx,
				"INSERT INTO Data (IdTag, Value, TimeStamp, TimeStampSystem, Quality) VALUES (@p1, @p2, @p3, @p4, @p5)",
				idTag, val, timeStampFile, timeStampSystem, quality,
			)
			if err != nil {
				logs.LogWriteAttempt(logger, idTag, "DATA_INSERT_FAIL", err, logMode)
				// 🔧 НЕ возвращаем ошибку для Data таблицы - она не критична
			} else {
				logs.LogWriteAttempt(logger, idTag, "DATA_INSERT_SUCCESS", nil, logMode)
			}

			successCount++
		}
	}

	// 🔧 КОММИТИМ ТРАНЗАКЦИЮ
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("ошибка коммита транзакции: %w", err)
	}

	if logMode == "all" {
		logger.Printf("Успешно обработано %d значений", successCount)
	}

	return nil
}


