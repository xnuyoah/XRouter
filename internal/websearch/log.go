package websearch

import (
	log "github.com/sirupsen/logrus"
)

// 统一的日志字段：所有搜索渠道日志都带 component=websearch，
// 便于在全局日志里按组件筛选。
func wsLogger() *log.Entry {
	return log.WithField("component", "websearch")
}

func logDebugf(format string, args ...any) {
	wsLogger().Debugf(format, args...)
}

func logInfof(format string, args ...any) {
	wsLogger().Infof(format, args...)
}

func logWarnf(format string, args ...any) {
	wsLogger().Warnf(format, args...)
}

func logErrorf(format string, args ...any) {
	wsLogger().Errorf(format, args...)
}
