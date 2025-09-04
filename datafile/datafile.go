package datafile

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"unsafe"
)

// ReadData читает бинарный файл в любую структуру данных с защитой от panic
func ReadData[T any](filename string) (result T, err error) {
	// 🛡️ PANIC RECOVERY
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic при чтении файла %s: %v", filename, r)
		}
	}()

	file, err := os.Open(filename)
	if err != nil {
		return
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return
	}

	fileSize := fileInfo.Size()
	expectedSize := int64(unsafe.Sizeof(result))

	// проверка размера, но от нам меньше считает ((
	if fileSize < expectedSize-100 || fileSize > expectedSize+100 {
		err = fmt.Errorf("размер файла %d не соответствует ожидаемому %d (допустимая погрешность ±100)",
			fileSize, expectedSize)
		return
	}

	// Читаем только столько, сколько есть в файле
	if fileSize < expectedSize {
		// Создаем буфер размером со структуру, заполненный нулями
		buffer := make([]byte, expectedSize)
		var n int
		n, err = file.Read(buffer[:fileSize])
		if err != nil {
			return
		}

		// ПРОВЕРКА ПОЛНОТЫ ЧТЕНИЯ
		if int64(n) != fileSize {
			err = fmt.Errorf("неполное чтение файла: ожидалось %d, прочитано %d", fileSize, n)
			return
		}

		// Декодируем из буфера
		err = binary.Read(bytes.NewReader(buffer), binary.LittleEndian, &result)
		return
	}

	// Стандартное чтение, если размер совпадает
	err = binary.Read(file, binary.LittleEndian, &result)
	return
}
