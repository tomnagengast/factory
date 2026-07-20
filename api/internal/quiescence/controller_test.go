package quiescence

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAcquireStopsAdmissionAndWaitsForActiveWork(t *testing.T) {
	controller := New()
	if !controller.TryStart() {
		t.Fatal("initial admission was blocked")
	}
	acquired := make(chan Lease, 1)
	go func() {
		lease, err := controller.Acquire(context.Background(), time.Second)
		if err != nil {
			t.Errorf("acquire: %v", err)
			return
		}
		acquired <- lease
	}()
	waitFor(t, func() bool { return !controller.Accepting() }, "admission to stop")
	select {
	case <-acquired:
		t.Fatal("lease returned before active work drained")
	default:
	}
	controller.Done(nil)
	lease := receive(t, acquired, "drained lease")
	if lease.Token == "" || lease.ExpiresAt.IsZero() {
		t.Fatalf("lease = %#v", lease)
	}
	if controller.TryStart() {
		t.Fatal("admission resumed while lease remained held")
	}
	if !controller.Release(lease.Token) {
		t.Fatal("lease did not release")
	}
	if !controller.TryStart() {
		t.Fatal("admission did not resume after release")
	}
	controller.Done(nil)
}

func TestAcquireReleasesOnCancellation(t *testing.T) {
	controller := New()
	if !controller.TryStart() {
		t.Fatal("initial admission was blocked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := controller.Acquire(ctx, time.Second)
		result <- err
	}()
	waitFor(t, func() bool { return !controller.Accepting() }, "admission to stop")
	cancel()
	if err := receive(t, result, "cancelled acquisition"); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire error = %v", err)
	}
	if !controller.TryStart() {
		t.Fatal("admission did not resume after cancellation")
	}
	controller.Done(nil)
	controller.Done(nil)
}

func TestLeaseExpiresAndRejectsWrongToken(t *testing.T) {
	controller := New()
	lease, err := controller.Acquire(context.Background(), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if controller.Release("wrong") {
		t.Fatal("wrong token released lease")
	}
	waitFor(t, controller.Accepting, "lease expiry")
	if !controller.TryStart() {
		t.Fatal("admission did not resume after expiry")
	}
	controller.Done(nil)
	if controller.Release(lease.Token) {
		t.Fatal("expired token released a later state")
	}
}

func TestConcurrentAcquireIsRejected(t *testing.T) {
	controller := New()
	lease, err := controller.Acquire(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Acquire(context.Background(), time.Second); !errors.Is(err, ErrAlreadyHeld) {
		t.Fatalf("second acquire error = %v", err)
	}
	controller.Release(lease.Token)
}

func TestDrainFailureBlocksQuiescenceAndNewAdmission(t *testing.T) {
	controller := New()
	if !controller.TryStart() {
		t.Fatal("initial admission was blocked")
	}
	result := make(chan error, 1)
	go func() {
		_, err := controller.Acquire(context.Background(), time.Second)
		result <- err
	}()
	waitFor(t, func() bool { return !controller.Accepting() }, "admission to stop")
	controller.Done(errors.New("wire unavailable"))
	if err := receive(t, result, "failed drain"); !errors.Is(err, ErrDrainFailed) {
		t.Fatalf("acquire error = %v", err)
	}
	if controller.Accepting() || controller.TryStart() {
		t.Fatal("coordinator admitted work after a fatal drain failure")
	}
}

func waitFor(t *testing.T, check func() bool, description string) {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		if check() {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for %s", description)
		case <-time.After(time.Millisecond):
		}
	}
}

func receive[T any](t *testing.T, values <-chan T, description string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", description)
		var zero T
		return zero
	}
}
