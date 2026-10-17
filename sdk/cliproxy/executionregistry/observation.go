package executionregistry

import "time"

// Observation 是不可变的在途执行快照条目。
type Observation struct {
	RequestID    string
	CredentialID string
	Model        string
	RequestKind  string
	StartedAt    time.Time
	Accounted    bool
}

// Freeze 是不可变的在途执行快照。
type Freeze struct {
	Revision        int64
	BarrierRevision int64
	Executions      []Observation
}

// ObserveBarrier 记录最新的 Home 观测屏障。
func (r *Registry) ObserveBarrier(revision int64) {
	if r == nil || revision <= 0 {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if revision > r.observedBarrier {
		r.observedBarrier = revision
		r.pendingBarrierSequence = r.next
	}
}

// FreezeInFlight 将所有活动执行复制为不可变快照。
func (r *Registry) FreezeInFlight(_ time.Time) Freeze {
	if r == nil {
		return Freeze{}
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.observedBarrier > r.publishedBarrier {
		blocked := false
		for sequence := range r.pending {
			if sequence <= r.pendingBarrierSequence {
				blocked = true
				break
			}
		}
		if !blocked {
			r.publishedBarrier = r.observedBarrier
		}
	}

	r.snapshotRevision++
	freeze := Freeze{
		Revision:        r.snapshotRevision,
		BarrierRevision: r.publishedBarrier,
		Executions:      make([]Observation, 0, len(r.scopes)),
	}
	for _, scope := range r.scopes {
		freeze.Executions = append(freeze.Executions, Observation{
			RequestID:    scope.spec.RequestID,
			CredentialID: scope.spec.CredentialID,
			Model:        scope.spec.Model,
			RequestKind:  scope.spec.Kind,
			StartedAt:    scope.spec.StartedAt,
			Accounted:    scope.spec.Accounted,
		})
	}
	return freeze
}
