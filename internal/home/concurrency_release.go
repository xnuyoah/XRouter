package home

import (
	"context"
	"sync"
	"time"

	internalconfig "github.com/xnuyoah/XRouter/v8/internal/config"
	"github.com/xnuyoah/XRouter/v8/sdk/cliproxy/executionregistry"
)

// ConcurrencyReleaseFrame 是 Home 为一个凭据和模型接受的累计释放量。
type ConcurrencyReleaseFrame struct {
	CredentialID string `json:"credential_id"`
	Model        string `json:"model"`
	ReleaseSeq   int64  `json:"release_seq"`
}

type releaseState struct {
	Latest  int64
	Acked   int64
	waiters map[int64][]chan struct{}
}

type releaseFlusher struct {
	mu             sync.Mutex
	groups         map[executionregistry.ReleaseGroup]releaseState
	flushInterval  time.Duration
	maxBackoff     time.Duration
	configProvider func() internalconfig.CredentialConcurrencyConfig
	send           func(context.Context, ConcurrencyReleaseFrame) error
	wake           chan struct{}
	force          chan context.Context
}

func newReleaseFlusher(flushInterval, maxBackoff time.Duration, send func(context.Context, ConcurrencyReleaseFrame) error) *releaseFlusher {
	return &releaseFlusher{
		groups:        make(map[executionregistry.ReleaseGroup]releaseState),
		flushInterval: flushInterval,
		maxBackoff:    maxBackoff,
		send:          send,
		wake:          make(chan struct{}, 1),
		force:         make(chan context.Context, 1),
	}
}

// NewReleaseFlusher 创建一个刷新器，它从当前限流器配置读取时序更新。
func NewReleaseFlusher(configProvider func() internalconfig.CredentialConcurrencyConfig, send func(context.Context, ConcurrencyReleaseFrame) error) *releaseFlusher {
	flusher := newReleaseFlusher(0, 0, send)
	flusher.SetConfigProvider(configProvider)
	return flusher
}

func (f *releaseFlusher) SetConfigProvider(provider func() internalconfig.CredentialConcurrencyConfig) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.configProvider = provider
	f.mu.Unlock()
	f.signal()
}

// SetSender 替换后续释放尝试所用的 Home 生命周期。
func (f *releaseFlusher) SetSender(send func(context.Context, ConcurrencyReleaseFrame) error) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.send = send
	f.mu.Unlock()
	f.signal()
}

// MarkDirty 记录某个释放组的最新累计序号，并返回一个
// 在 Home 确认该序号后完成的票据。
func (f *releaseFlusher) MarkDirty(group executionregistry.ReleaseGroup, sequence int64) *executionregistry.ReleaseTicket {
	if f == nil || sequence <= 0 || group.CredentialID == "" || group.Model == "" {
		return nil
	}

	done := make(chan struct{})
	f.mu.Lock()
	state := f.groups[group]
	if sequence <= state.Acked {
		close(done)
	} else {
		if state.waiters == nil {
			state.waiters = make(map[int64][]chan struct{})
		}
		state.waiters[sequence] = append(state.waiters[sequence], done)
		if sequence > state.Latest {
			state.Latest = sequence
		}
		f.groups[group] = state
	}
	f.mu.Unlock()
	f.signal()
	return executionregistry.NewReleaseTicket(group, sequence, done)
}

// Run 发送脏的组，直到其生命周期被取消。
func (f *releaseFlusher) Run(ctx context.Context) {
	if f == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	timer := time.NewTimer(0)
	defer timer.Stop()
	delay := f.timings().flushInterval
	backingOff := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.wake:
			if !backingOff {
				resetReleaseTimer(timer, 0)
			}
		case forceCtx := <-f.force:
			resetReleaseTimer(timer, 0)
			failed := f.flush(forceCtx)
			delay, backingOff = f.nextDelay(delay, failed)
			resetReleaseTimer(timer, delay)
		case <-timer.C:
			failed := f.flush(ctx)
			delay, backingOff = f.nextDelay(delay, failed)
			timer.Reset(delay)
		}
	}
}

func (f *releaseFlusher) nextDelay(delay time.Duration, failed bool) (time.Duration, bool) {
	timings := f.timings()
	if !failed {
		return timings.flushInterval, false
	}
	delay *= 2
	if delay < timings.flushInterval {
		delay = timings.flushInterval
	}
	if delay > timings.maxBackoff {
		delay = timings.maxBackoff
	}
	return delay, true
}

func resetReleaseTimer(timer *time.Timer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}

type releaseFlusherTimings struct {
	flushInterval time.Duration
	maxBackoff    time.Duration
}

func (f *releaseFlusher) timings() releaseFlusherTimings {
	defaults := internalconfig.CredentialConcurrencyConfig{}.WithDefaults()
	timings := releaseFlusherTimings{flushInterval: f.flushInterval, maxBackoff: f.maxBackoff}

	f.mu.Lock()
	provider := f.configProvider
	f.mu.Unlock()
	if provider != nil {
		cfg := provider().WithDefaults()
		timings.flushInterval = cfg.ReleaseFlushInterval
		timings.maxBackoff = cfg.ReleaseMaxBackoff
	}
	if timings.flushInterval <= 0 {
		timings.flushInterval = defaults.ReleaseFlushInterval
	}
	if timings.maxBackoff < timings.flushInterval {
		timings.maxBackoff = timings.flushInterval
	}
	return timings
}

func (f *releaseFlusher) flush(ctx context.Context) bool {
	if f == nil {
		return false
	}

	f.mu.Lock()
	send := f.send
	pending := make(map[executionregistry.ReleaseGroup]int64, len(f.groups))
	for group, state := range f.groups {
		if state.Latest > state.Acked {
			pending[group] = state.Latest
		}
	}
	f.mu.Unlock()
	if send == nil {
		return false
	}

	failed := false
	for group, sequence := range pending {
		errSend := send(ctx, ConcurrencyReleaseFrame{
			CredentialID: group.CredentialID,
			Model:        group.Model,
			ReleaseSeq:   sequence,
		})
		if errSend != nil {
			failed = true
			continue
		}
		f.mu.Lock()
		state := f.groups[group]
		if sequence > state.Acked {
			state.Acked = sequence
			for waiterSequence, waiters := range state.waiters {
				if waiterSequence <= state.Acked {
					for _, done := range waiters {
						close(done)
					}
					delete(state.waiters, waiterSequence)
				}
			}
		}
		f.groups[group] = state
		f.mu.Unlock()
	}
	return failed
}

// Flush 等待所有当前为脏的组在 ctx 内被确认。
func (f *releaseFlusher) Flush(ctx context.Context) error {
	if f == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f.forceFlush(ctx)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if f.idle() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (f *releaseFlusher) idle() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, state := range f.groups {
		if state.Latest > state.Acked {
			return false
		}
	}
	return true
}

func (f *releaseFlusher) signal() {
	if f == nil {
		return
	}
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *releaseFlusher) forceFlush(ctx context.Context) {
	if f == nil {
		return
	}
	select {
	case f.force <- ctx:
	default:
	}
}
