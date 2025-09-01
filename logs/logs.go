package logs

import (
	"log"
	"time"
)

func LogWriteAttempt(logger *log.Logger, idTag string, status string, err error, logMode string) {
	now := time.Now().Format(time.RFC3339)
	isError := err != nil || status == "UPDATE_FAIL" || status == "INSERT_FAIL" || status == "ROWSAFFECTED_FAIL"

	if logMode == "all" || (logMode == "errors" && isError) {
		if err != nil {
			logger.Printf("Time: %s | IdTag: %s | Status: %s | Error: %v", now, idTag, status, err)
		} else {
			logger.Printf("Time: %s | IdTag: %s | Status: %s", now, idTag, status)
		}
	}
}
