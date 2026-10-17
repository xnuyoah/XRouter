package executor

import (
	"context"
	"errors"
	"fmt"
)

// ConfiguredTLSHandshakeTimeoutError 标识一次 TLS 握手失败，
// 其原因是内部配置的建连时限，而非调用方的请求上下文。
// 传输层只会在确认父上下文仍然存活、
// 且自身的握手子上下文已过期之后，
// 才构造它。
//
// 该具体类型特意通过这个中立的执行器包共享，
// 以便传输辅助代码和认证重试分类都能识别它，
// 而无需互相导入或依赖错误文本。
type ConfiguredTLSHandshakeTimeoutError struct {
	cause error
}

// NewConfiguredTLSHandshakeTimeoutError 包装由内部配置的
// TLS 握手截止时间所导致的握手失败。
func NewConfiguredTLSHandshakeTimeoutError(cause error) error {
	if cause == nil {
		return nil
	}
	return &ConfiguredTLSHandshakeTimeoutError{cause: cause}
}

func (e *ConfiguredTLSHandshakeTimeoutError) Error() string {
	if e == nil || e.cause == nil {
		return "tls handshake timeout"
	}
	return fmt.Sprintf("tls handshake timeout: %v", e.cause)
}

// Unwrap 保留原始的握手错误链（包括 context.DeadlineExceeded），
// 以供诊断和 errors.Is 检查使用。
func (e *ConfiguredTLSHandshakeTimeoutError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// IsConfiguredTLSHandshakeTimeoutError 报告 err 是否完全
// 由配置的 TLS 握手截止时间所导致。
//
// 错误树中每一处 context.Canceled 或 context.DeadlineExceeded
// 都必须位于某个 ConfiguredTLSHandshakeTimeoutError 子树内，
// 并且至少有一个配置超时子树自身包含截止时间。
// 因此，任何位于此类子树之外、由调用方拥有的取消或截止时间
// 都优先——包括通过 errors.Join 与配置超时组合在一起的情况。
// 仅凭错误文本匹配
// 永远不足够。
func IsConfiguredTLSHandshakeTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	foundConfiguredDeadline, hasUnownedContextError := inspectConfiguredTLSHandshakeTimeout(err)
	return foundConfiguredDeadline && !hasUnownedContextError
}

func inspectConfiguredTLSHandshakeTimeout(err error) (foundConfiguredDeadline bool, hasUnownedContextError bool) {
	if err == nil {
		return false, false
	}
	if configured, ok := err.(*ConfiguredTLSHandshakeTimeoutError); ok && configured != nil {
		// 传输层已经证明该子树的截止时间是
		// 内部拥有的，且其父上下文仍然存活。
		return errors.Is(configured.cause, context.DeadlineExceeded), false
	}
	// 某个节点可以通过自身的 Is 方法断言调用方上下文身份，
	// 而 Unwrap 暴露的却是无关的子节点。在每个非配置节点上，
	// 都要在向下遍历之前检查该自身身份，这样这类调用方
	// 错误就无法隐藏在通过 Join 组合的配置超时之后。
	hasUnownedContextError = hasOwnContextIdentity(err)
	for _, child := range unwrapChildErrors(err) {
		childFound, childUnowned := inspectConfiguredTLSHandshakeTimeout(child)
		foundConfiguredDeadline = foundConfiguredDeadline || childFound
		hasUnownedContextError = hasUnownedContextError || childUnowned
	}
	return foundConfiguredDeadline, hasUnownedContextError
}

func hasOwnContextIdentity(err error) bool {
	if err == context.Canceled || err == context.DeadlineExceeded {
		return true
	}
	if checker, ok := err.(interface{ Is(error) bool }); ok {
		return checker.Is(context.Canceled) || checker.Is(context.DeadlineExceeded)
	}
	return false
}

func unwrapChildErrors(err error) []error {
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		if children := multi.Unwrap(); len(children) > 0 {
			return children
		}
	}
	if single, ok := err.(interface{ Unwrap() error }); ok {
		if child := single.Unwrap(); child != nil {
			return []error{child}
		}
	}
	return nil
}
