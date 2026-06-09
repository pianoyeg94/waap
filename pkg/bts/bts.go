package bts

import "unsafe"

func BytesToStringZeroCopy(bytes []byte) string {
	return unsafe.String(unsafe.SliceData(bytes), len(bytes))
}
