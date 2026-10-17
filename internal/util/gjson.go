package util

import (
	"unsafe"

	"github.com/tidwall/gjson"
)

// GetGJSONBytesNoCopy 返回可能直接引用 data 的 GJSON 结果。
// 调用方在使用期间不得保留该结果，也不得修改 data。
func GetGJSONBytesNoCopy(data []byte, path string) gjson.Result {
	if len(data) == 0 {
		return gjson.Result{}
	}
	return gjson.Get(unsafe.String(unsafe.SliceData(data), len(data)), path)
}

// ParseGJSONBytesNoCopy 将 data 解析为直接引用 data 的 GJSON 结果。
// gjson.ParseBytes 会复制整个文档，这对多兆字节的载荷而言
// 代价过高。调用方不得保留该结果，也不得在使用期间
// 修改 data。
func ParseGJSONBytesNoCopy(data []byte) gjson.Result {
	if len(data) == 0 {
		return gjson.Result{}
	}
	return gjson.Parse(unsafe.String(unsafe.SliceData(data), len(data)))
}
