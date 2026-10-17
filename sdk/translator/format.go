package translator

// Format 标识代理内部使用的请求/响应模式。
type Format string

// FromString 将任意标识符转换为转换器格式。
func FromString(v string) Format {
	return Format(v)
}

// String 返回原始的模式标识符。
func (f Format) String() string {
	return string(f)
}
