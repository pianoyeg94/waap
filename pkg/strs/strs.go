package strs

import "unsafe"

func StringToBytesZeroCopy(str string) []byte {
	strHeader := (*struct {
		Data unsafe.Pointer
		Len  int
	})(unsafe.Pointer(&str))
	sliceHeader := struct {
		Data unsafe.Pointer
		Len  int
		Cap  int
	}{
		Data: strHeader.Data,
		Len:  strHeader.Len,
		Cap:  strHeader.Len,
	}
	return *(*[]byte)(unsafe.Pointer(&sliceHeader))
}

func StringSetToSlice(set map[string]struct{}) []string {
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	return result
}
