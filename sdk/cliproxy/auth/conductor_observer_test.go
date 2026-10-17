package auth

import (
	"context"
	"testing"
	"time"
)

func TestManagerResultObserverReceivesCountedResults(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "observer-auth", Provider: "nvidia"}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	var observed Result
	var observedAt time.Time
	manager.SetResultObserver(func(_ context.Context, result Result, at time.Time) {
		observed = result
		observedAt = at
	})
	manager.MarkResult(context.Background(), Result{
		AuthID:   "observer-auth",
		Provider: "nvidia",
		Model:    "nemotron-3-ultra",
		Success:  true,
	})
	if observed.AuthID != "observer-auth" || observed.Provider != "nvidia" || !observed.Success {
		t.Fatalf("observed result = %+v", observed)
	}
	if observedAt.IsZero() {
		t.Fatal("observed time is zero")
	}
}
