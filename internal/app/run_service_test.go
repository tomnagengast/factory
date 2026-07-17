package app

import (
	"context"
	"sync"
	"testing"
	"time"
)

type recordingRunReconciler struct {
	mu    sync.Mutex
	calls chan struct{}
}

func (r *recordingRunReconciler) Reconcile(context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case r.calls <- struct{}{}:
	default:
	}
}

func TestRunServiceReconcilesAtStartupAndOnCoalescedWake(t *testing.T) {
	t.Parallel()
	manager := &recordingRunReconciler{calls: make(chan struct{}, 8)}
	service, err := NewRunService(manager, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	service.Notify()
	service.Notify()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	requireRunServiceCall(t, manager.calls)
	requireRunServiceCall(t, manager.calls)
	select {
	case <-manager.calls:
		t.Fatal("duplicate pending wakes were not coalesced")
	case <-time.After(20 * time.Millisecond):
	}
	service.Notify()
	requireRunServiceCall(t, manager.calls)
	cancel()
	if err := <-done; err != context.Canceled {
		t.Fatalf("Run error = %v", err)
	}
}

func TestRunServiceValidatesConstruction(t *testing.T) {
	t.Parallel()
	manager := &recordingRunReconciler{calls: make(chan struct{}, 1)}
	if _, err := NewRunService(nil, time.Second); err == nil {
		t.Fatal("nil manager accepted")
	}
	if _, err := NewRunService(manager, 0); err == nil {
		t.Fatal("zero interval accepted")
	}
	var service *RunService
	if err := service.Run(t.Context()); err == nil {
		t.Fatal("nil service accepted")
	}
}

func requireRunServiceCall(t *testing.T, calls <-chan struct{}) {
	t.Helper()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Run reconciliation")
	}
}
