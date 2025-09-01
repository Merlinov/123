package datafile

import (
	"encoding/binary"
	"os"
)

// ReadData читает бинарный файл в любую структуру данных
func ReadData[T any](filename string) (T, error) {
	var data T
	f, err := os.Open(filename)
	if err != nil {
		return data, err
	}
	defer f.Close()
	err = binary.Read(f, binary.LittleEndian, &data)
	return data, err
}
