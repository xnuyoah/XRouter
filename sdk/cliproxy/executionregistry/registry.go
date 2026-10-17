// Package executionregistry 跟踪一个订阅者生命周期内由 Home 分派的执行。
package executionregistry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

var (
	ErrRegistryNotAccepting          = errors.New("execution registry is not accepting dispatches")
	ErrRegistryClosed                = errors.New("execution registry is closed")
	ErrInvalidPendingDispatch        = errors.New("invalid pending dispatch")
	ErrInvalidExecutionResource      = errors.New("invalid execution resource")
	ErrExecutionResourceAlreadyBound = errors.New("execution resource is already bound")
)

// State 是 Registry 的生命周期状态。
type State uint32

const (
	StateAccepting State = iota
	StateDraining
	StateClosed
)

// Registry 拥有在一个 Home 订阅者生命周期内接受的所有分派。
type Registry struct {
	state atomic.Uint32

	mu                     sync.Mutex
	next                   uint64
	snapshotRevision       int64
	observedBarrier        int64
	pendingBarrierSequence uint64
	publishedBarrier       int64
	pending                map[uint64]*PendingDispatch
	scopes                 map[uint64]*Scope
	releaseSequences       map[ReleaseGroup]int64
	releaseSink            ReleaseSink
	changed                chan struct{}

	closeMu      sync.Mutex
	closeStarted bool
	closeDone    chan struct{}
	closeErr     error
}

// PendingDispatch 预留一个执行槽位，直到它被装入或结束。
type PendingDispatch struct {
	id       uint64
	registry *Registry
	mu       sync.Mutex
	once     sync.Once
}

// ScopeSpec 描述一次由 Home 分派的执行。
type ScopeSpec struct {
	RequestID    string
	CredentialID string
	Model        string
	Kind         string
	StartedAt    time.Time
	Accounted    bool
}

// ReleaseGroup 标识某个被计入的凭据和模型所对应的累积释放序列。
type ReleaseGroup struct {
	CredentialID string
	Model        string
}

// ReleaseTicket 在 Home 确认一个累积释放序列后完成。
type ReleaseTicket struct {
	Group    ReleaseGroup
	Sequence int64
	done     <-chan struct{}
}

// NewReleaseTicket 创建一个由 done 支持的票据。done 通道为 nil 表示
// 该释放接收端不支持确认。
func NewReleaseTicket(group ReleaseGroup, sequence int64, done <-chan struct{}) *ReleaseTicket {
	if sequence <= 0 || done == nil {
		return nil
	}
	return &ReleaseTicket{Group: group, Sequence: sequence, done: done}
}

// Wait 阻塞直到 Home 确认释放或 ctx 过期。
func (t *ReleaseTicket) Wait(ctx context.Context) error {
	if t == nil || t.done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-t.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReleaseSink 接收某个释放组的最新累积序列，并
// 可选地返回一个确认票据。
type ReleaseSink func(ReleaseGroup, int64) *ReleaseTicket

// Scope 拥有一次已装入执行的资源。
type Scope struct {
	id       uint64
	registry *Registry
	spec     ScopeSpec

	mu            sync.Mutex
	closeFn       func() error
	closeDone     chan struct{}
	releaseTicket *ReleaseTicket
	active        bool
	ended         sync.Once
}

// New 创建一个可接受新分派的注册表。
func New() *Registry {
	registry := &Registry{
		pending:          make(map[uint64]*PendingDispatch),
		scopes:           make(map[uint64]*Scope),
		releaseSequences: make(map[ReleaseGroup]int64),
		changed:          make(chan struct{}),
	}
	registry.state.Store(uint32(StateAccepting))
	return registry
}

// BeginDispatch 在注册表接受流量期间预留一个分派令牌。
func (r *Registry) BeginDispatch() (*PendingDispatch, error) {
	if r == nil || State(r.state.Load()) != StateAccepting {
		return nil, ErrRegistryNotAccepting
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if State(r.state.Load()) != StateAccepting {
		return nil, ErrRegistryNotAccepting
	}

	r.next++
	pending := &PendingDispatch{id: r.next, registry: r}
	r.pending[pending.id] = pending
	return pending, nil
}

// WaitPending 等待直到所有 Home 响应尚未解决的分派都已结束或已装入。
func (r *Registry) WaitPending(ctx context.Context) error {
	if r == nil {
		return ErrRegistryClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	r.mu.Lock()
	for len(r.pending) != 0 {
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		r.mu.Lock()
	}
	r.mu.Unlock()
	return nil
}

// End 释放一个未被装入的分派令牌。
func (p *PendingDispatch) End() {
	if p == nil || p.registry == nil {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.once.Do(func() {
		p.registry.mu.Lock()
		delete(p.registry.pending, p.id)
		p.registry.signalLocked()
		p.registry.mu.Unlock()
	})
}

// Install 原子地将待处理的分派令牌转为活跃的执行作用域。
func (r *Registry) Install(pending *PendingDispatch, spec ScopeSpec) (*Scope, error) {
	if r == nil || pending == nil || pending.registry != r {
		return nil, ErrInvalidPendingDispatch
	}

	pending.mu.Lock()
	defer pending.mu.Unlock()
	r.mu.Lock()
	defer r.mu.Unlock()

	if State(r.state.Load()) != StateAccepting {
		pending.once.Do(func() {})
		delete(r.pending, pending.id)
		r.signalLocked()
		return nil, ErrRegistryNotAccepting
	}
	if _, exists := r.pending[pending.id]; !exists {
		return nil, ErrInvalidPendingDispatch
	}

	pending.once.Do(func() {})
	delete(r.pending, pending.id)
	scope := &Scope{id: pending.id, registry: r, spec: spec, active: true}
	r.scopes[scope.id] = scope
	r.signalLocked()
	return scope, nil
}

// SetReleaseSink 替换累积释放接收端并重放所有已知的组。
// 旧式回调仍受支持，但无法提供确认票据。
func (r *Registry) SetReleaseSink(rawSink any) {
	if r == nil {
		return
	}

	var sink ReleaseSink
	switch typed := rawSink.(type) {
	case nil:
	case ReleaseSink:
		sink = typed
	case func(ReleaseGroup, int64) *ReleaseTicket:
		sink = ReleaseSink(typed)
	case func(ReleaseGroup, int64):
		sink = func(group ReleaseGroup, sequence int64) *ReleaseTicket {
			typed(group, sequence)
			return nil
		}
	default:
		return
	}

	r.mu.Lock()
	r.releaseSink = sink
	sequences := make(map[ReleaseGroup]int64, len(r.releaseSequences))
	for group, sequence := range r.releaseSequences {
		sequences[group] = sequence
	}
	r.mu.Unlock()

	if sink == nil {
		return
	}
	for group, sequence := range sequences {
		if sequence > 0 {
			sink(group, sequence)
		}
	}
}

// Bind 挂接执行资源。一个作用域只接受一个资源。
func (s *Scope) Bind(closeFn func() error) error {
	if s == nil || s.registry == nil || closeFn == nil {
		return ErrInvalidExecutionResource
	}

	s.registry.mu.Lock()
	defer s.registry.mu.Unlock()
	if State(s.registry.state.Load()) != StateAccepting || !s.active {
		return ErrRegistryNotAccepting
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeFn != nil || s.closeDone != nil {
		return ErrExecutionResourceAlreadyBound
	}
	s.closeFn = closeFn
	return nil
}

// End 关闭已绑定的资源并恰好释放该执行作用域一次。
func (s *Scope) End(reason string) {
	_ = s.EndWithRelease(reason)
}

// EndWithRelease 关闭该作用域并返回释放确认票据。
// 调用释放接收端时不持有注册表互斥锁。
func (s *Scope) EndWithRelease(_ string) *ReleaseTicket {
	if s == nil || s.registry == nil {
		return nil
	}

	var ticket *ReleaseTicket
	s.ended.Do(func() {
		s.registry.mu.Lock()
		s.mu.Lock()
		s.active = false
		s.mu.Unlock()
		s.registry.mu.Unlock()

		s.waitForBoundResourceClose()

		s.registry.mu.Lock()
		releaseSink, releaseGroup, releaseSequence := s.registry.markReleasedLocked(s)
		s.registry.mu.Unlock()

		if releaseSink != nil && releaseSequence > 0 {
			ticket = releaseSink(releaseGroup, releaseSequence)
		}

		s.mu.Lock()
		s.releaseTicket = ticket
		s.mu.Unlock()

		s.registry.mu.Lock()
		delete(s.registry.scopes, s.id)
		s.registry.signalLocked()
		s.registry.mu.Unlock()
	})

	s.mu.Lock()
	ticket = s.releaseTicket
	s.mu.Unlock()
	return ticket
}

func (r *Registry) markReleasedLocked(scope *Scope) (ReleaseSink, ReleaseGroup, int64) {
	if scope == nil || !scope.spec.Accounted {
		return nil, ReleaseGroup{}, 0
	}
	group := ReleaseGroup{CredentialID: scope.spec.CredentialID, Model: scope.spec.Model}
	r.releaseSequences[group]++
	return r.releaseSink, group, r.releaseSequences[group]
}

func (s *Scope) startBoundResourceClose() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closeDone != nil {
		return s.closeDone
	}
	closeFn := s.closeFn
	if closeFn == nil {
		return nil
	}
	closeDone := make(chan struct{})
	s.closeFn = nil
	s.closeDone = closeDone
	go func() {
		s.closeResource(closeFn)
		close(closeDone)
	}()
	return closeDone
}

func (s *Scope) waitForBoundResourceClose() {
	if closeDone := s.startBoundResourceClose(); closeDone != nil {
		<-closeDone
	}
}

func (s *Scope) closeResource(closeFn func() error) {
	if closeFn == nil {
		return
	}
	if errClose := closeFn(); errClose != nil {
		log.WithError(errClose).Warn("Home execution resource close failed")
	}
}

// Drain 拒绝新工作、取消活跃资源，并等待所有持有者结束。
func (r *Registry) Drain(ctx context.Context) error {
	if r == nil {
		return ErrRegistryClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}

	if !r.state.CompareAndSwap(uint32(StateAccepting), uint32(StateDraining)) && State(r.state.Load()) != StateDraining {
		return ErrRegistryClosed
	}

	r.mu.Lock()
	scopes := make([]*Scope, 0, len(r.scopes))
	for _, scope := range r.scopes {
		scopes = append(scopes, scope)
	}
	r.mu.Unlock()

	for _, scope := range scopes {
		scope.startBoundResourceClose()
	}

	r.mu.Lock()
	for len(r.pending) != 0 || len(r.scopes) != 0 {
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		r.mu.Lock()
	}
	r.state.Store(uint32(StateClosed))
	r.mu.Unlock()
	return nil
}

// Close 永久拒绝新工作并关闭所有当前已绑定的资源。
func (r *Registry) Close() error {
	if r == nil {
		return ErrRegistryClosed
	}

	r.closeMu.Lock()
	if r.closeStarted {
		closeDone := r.closeDone
		r.closeMu.Unlock()
		<-closeDone
		r.closeMu.Lock()
		errClose := r.closeErr
		r.closeMu.Unlock()
		return errClose
	}
	if State(r.state.Load()) == StateClosed {
		r.closeMu.Unlock()
		return nil
	}
	r.closeStarted = true
	r.closeDone = make(chan struct{})
	closeDone := r.closeDone
	r.closeMu.Unlock()

	for {
		state := State(r.state.Load())
		if state == StateClosed || r.state.CompareAndSwap(uint32(state), uint32(StateClosed)) {
			break
		}
	}

	r.mu.Lock()
	scopes := make([]*Scope, 0, len(r.scopes))
	for _, scope := range r.scopes {
		scopes = append(scopes, scope)
	}
	r.mu.Unlock()
	for _, scope := range scopes {
		scope.waitForBoundResourceClose()
	}

	r.closeMu.Lock()
	errClose := r.closeErr
	close(closeDone)
	r.closeMu.Unlock()
	return errClose
}

func (r *Registry) signalLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}
