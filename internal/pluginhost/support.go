package pluginhost

// SupportPluginHeaderValue 报告当前二进制文件是否在启用 CGO 的情况下构建。
func SupportPluginHeaderValue() string {
	return supportPluginValue
}
