package usage

import (
	"context"
	"sync"
	"sync/atomic"
)

type streamDeliveryKey struct{}

type streamDelivery struct {
	supported atomic.Int32
	once      sync.Once
	done      chan struct{}
	err       error
}

// WithStreamDelivery 让 HTTP 消费者独立于请求取消
// 确认投递。消费者必须在每条退出路径上确定结果。
func WithStreamDelivery(ctx context.Context) (context.Context, func(error)) {
	delivery := &streamDelivery{done: make(chan struct{})}
	return context.WithValue(ctx, streamDeliveryKey{}, delivery), func(err error) {
		delivery.once.Do(func() {
			delivery.err = err
			close(delivery.done)
		})
	}
}

// WithoutStreamDelivery 将内部执行与其调用方的 HTTP
// 确认隔离。内部消费者保留基于 EOF 的计量完成机制；
// 取消与所有无关上下文值仍会继承。
func WithoutStreamDelivery(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, streamDeliveryKey{}, struct{}{})
}

// StreamDeliveryTracked 报告消费者是否会确认投递。
func StreamDeliveryTracked(ctx context.Context) bool {
	_, ok := ctx.Value(streamDeliveryKey{}).(*streamDelivery)
	return ok
}

// WaitStreamDelivery 等待消费者（而非上游读取方）结束。
// 生产者必须先关闭其输出 channel 再等待，以免死锁。
func WaitStreamDelivery(ctx context.Context) (error, bool) {
	delivery, ok := ctx.Value(streamDeliveryKey{}).(*streamDelivery)
	if !ok {
		return nil, false
	}
	<-delivery.done
	return delivery.err, true
}

// SupportStreamDelivery 让活跃生产者选择支持提前终止取消。
// 生产者必须协调取消与 WaitStreamDelivery。
func SupportStreamDelivery(ctx context.Context) func() {
	delivery, ok := ctx.Value(streamDeliveryKey{}).(*streamDelivery)
	if !ok {
		return func() {}
	}
	delivery.supported.Add(1)
	var once sync.Once
	return func() { once.Do(func() { delivery.supported.Add(-1) }) }
}

// StreamDeliverySupported 报告活跃生产者是否理解投递协议。
func StreamDeliverySupported(ctx context.Context) bool {
	delivery, ok := ctx.Value(streamDeliveryKey{}).(*streamDelivery)
	return ok && delivery.supported.Load() > 0
}
