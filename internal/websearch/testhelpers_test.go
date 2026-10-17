package websearch

import "errors"

// asGatewayError 是测试用的 errors.As 包装，避免每处都写类型断言样板。
func asGatewayError(err error, target **GatewayError) bool {
	return errors.As(err, target)
}

// asMCPUpstreamError 是测试用的 errors.As 包装。
func asMCPUpstreamError(err error, target **mcpUpstreamError) bool {
	return errors.As(err, target)
}
