package misc

import (
	"strings"

	log "github.com/sirupsen/logrus"
)

// 用于可视化分组相关日志行的分隔符。
var credentialSeparator = strings.Repeat("-", 67)

// LogCredentialSeparator 添加可视化分隔符以分组认证/密钥处理日志。
func LogCredentialSeparator() {
	log.Debug(credentialSeparator)
}
