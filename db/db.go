package db

import (
	"database/sql"
	"fmt"
	"log"
	"reflect"
	"time"

	"tg45/logs"
	"tg45/model"
)

func SaveCurrentValues(
	db *sql.DB,
	data model.Data_TG4,
	timeStampSystem, timeStampFile time.Time,
	quality int,
	logger *log.Logger,
	logMode string,
) error {

	v := reflect.ValueOf(data)
	t := reflect.TypeOf(data)

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		value := v.Field(i)

		if value.Kind() == reflect.Float32 {
			idTag := field.Tag.Get("idTag")
			if idTag == "" {
				continue
			}
			val := value.Float()

			res, err := db.Exec(
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
				_, err := db.Exec(
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

			_, err = db.Exec(
				"INSERT INTO Data (IdTag, Value, TimeStamp, TimeStampSystem, Quality) VALUES (@p1, @p2, @p3, @p4, @p5)",
				idTag, val, timeStampFile, timeStampSystem, quality,
			)
			if err != nil {
				logs.LogWriteAttempt(logger, idTag, "DATA_INSERT_FAIL", err, logMode)
			} else {
				logs.LogWriteAttempt(logger, idTag, "DATA_INSERT_SUCCESS", nil, logMode)
			}
		}
	}

	return nil
}
