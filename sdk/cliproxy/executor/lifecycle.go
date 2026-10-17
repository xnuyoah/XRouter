package executor

import (
	"errors"
	"io"
	"sync"
)

// ExecutionLifecycle 持有与一次执行尝试相关联的资源。
type ExecutionLifecycle interface {
	Bind(func() error) error
	End(string)
}

// BindExecutionResource 将一个 closer 绑定到执行生命周期。
func BindExecutionResource(opts Options, closer io.Closer) error {
	if opts.ExecutionLifecycle == nil || closer == nil {
		return nil
	}

	var closeOnce sync.Once
	var closeErr error
	closeResource := func() error {
		closeOnce.Do(func() {
			closeErr = closer.Close()
		})
		return closeErr
	}
	if errBind := opts.ExecutionLifecycle.Bind(closeResource); errBind != nil {
		return errors.Join(errBind, closeResource())
	}
	return nil
}
