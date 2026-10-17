package logging

import (
	"context"
	"io"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// requestIDKey 是用于存储/获取请求 ID 的上下文键。
type requestIDKey struct{}

// ginRequestIDKey 是请求 ID 的 Gin 上下文键。
const ginRequestIDKey = "__request_id__"

// GenerateRequestID 使用默认随机源创建一个新的 UUIDv7 请求 ID 字符串。
// 若底层随机源失败则返回错误。
func GenerateRequestID() (string, error) {
	return GenerateRequestIDFromReader(nil)
}

// GenerateRequestIDFromReader 使用提供的 reader 创建一个新的 UUIDv7 请求 ID 字符串。
// 若 r 为 nil，则使用默认的 crypto/rand 源。
func GenerateRequestIDFromReader(r io.Reader) (string, error) {
	var id uuid.UUID
	var errNewV7 error
	if r == nil {
		id, errNewV7 = uuid.NewV7()
	} else {
		id, errNewV7 = uuid.NewV7FromReader(r)
	}
	if errNewV7 != nil {
		return "", errNewV7
	}
	return id.String(), nil
}

// ShortRequestID 返回请求 ID 末尾的 8 个字符。
// 若 requestID 不超过 8 个字符，则原样返回。
func ShortRequestID(requestID string) string {
	requestID = strings.TrimSpace(requestID)
	if len(requestID) > 8 {
		return requestID[len(requestID)-8:]
	}
	return requestID
}

// WithRequestID 返回附带请求 ID 的新上下文。
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}

// GetRequestID 从上下文中获取请求 ID。
// 若未找到则返回空字符串。
func GetRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if id, ok := ctx.Value(requestIDKey{}).(string); ok {
		return id
	}
	return ""
}

// SetGinRequestID 将请求 ID 存入 Gin 上下文。
func SetGinRequestID(c *gin.Context, requestID string) {
	if c != nil {
		c.Set(ginRequestIDKey, requestID)
	}
}

// GetGinRequestID 从 Gin 上下文中获取请求 ID。
func GetGinRequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if id, exists := c.Get(ginRequestIDKey); exists {
		if s, ok := id.(string); ok {
			return s
		}
	}
	return ""
}
