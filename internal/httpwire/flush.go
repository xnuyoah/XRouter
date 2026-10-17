package httpwire

import (
	"net/http"
	"reflect"
)

// FlushResponse 优先使用可感知错误的冲刷，并保留旧的中间件行为。
// 仅绕过 Gin 的无返回值原始 Flush，使传输层错误仍可被观测。
func FlushResponse(w http.ResponseWriter) error {
	for {
		if flusher, ok := w.(interface{ FlushError() error }); ok {
			return flusher.FlushError()
		}
		if flusher, ok := w.(http.Flusher); ok {
			if !isGinResponseWriter(w) {
				flusher.Flush()
				return nil
			}
			// 在到达 Gin 的传输层之前匹配其 Flush 的头部记账。
			w.(interface{ WriteHeaderNow() }).WriteHeaderNow()
		}
		unwrapper, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return http.NewResponseController(w).Flush()
		}
		w = unwrapper.Unwrap()
	}
}

func isGinResponseWriter(w http.ResponseWriter) bool {
	// Gin 的具体 writer 未导出。接口检查也会匹配包装器，
	// 它们内嵌了 gin.ResponseWriter，其 Flush 可能会排空缓冲区或压缩器。
	// 将这一例外限制在确切的包/类型上；切勿检查其字段。
	t := reflect.TypeOf(w)
	return t != nil && t.Kind() == reflect.Pointer &&
		t.Elem().PkgPath() == "github.com/gin-gonic/gin" && t.Elem().Name() == "responseWriter"
}
