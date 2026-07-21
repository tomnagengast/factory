package quiescence

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tomnagengast/factory/api/internal/deployment"
	"github.com/tomnagengast/factory/api/internal/eventwire"
	"github.com/tomnagengast/factory/api/internal/state"
	"github.com/tomnagengast/factory/api/internal/store"
)

type transition struct {
	kind   string
	reason string
	active int
}

type transitionLog struct {
	mu     sync.Mutex
	values []transition
}

func (l *transitionLog) hooks() Hooks {
	return Hooks{
		Quiescing: func(active int) error {
			l.add(transition{kind: "quiescing", active: active})
			return nil
		},
		Quiesced: func(active int) error {
			l.add(transition{kind: "quiesced", active: active})
			return nil
		},
		Resuming: func(reason string, active int) error {
			l.add(transition{kind: "resumed", reason: reason, active: active})
			return nil
		},
	}
}

func (l *transitionLog) add(value transition) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.values = append(l.values, value)
}

func (l *transitionLog) snapshot() []transition {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]transition(nil), l.values...)
}

func TestAcquireStopsAdmissionWaitsForDrainAndRecordsTransitions(t *testing.T) {
	log := &transitionLog{}
	controller := New(log.hooks())
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
	waitFor(t, func() bool { return len(log.snapshot()) == 1 }, "quiescing transition")
	if got := log.snapshot(); len(got) != 1 || got[0] != (transition{kind: "quiescing", active: 1}) {
		t.Fatalf("transitions = %#v", got)
	}
	if controller.TryStart() {
		t.Fatal("admission remained open while draining")
	}
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
	if got := log.snapshot(); len(got) != 2 || got[1] != (transition{kind: "quiesced"}) {
		t.Fatalf("transitions = %#v", got)
	}
	if controller.TryStart() {
		t.Fatal("admission resumed while lease remained held")
	}
	released, err := controller.Release(lease.Token)
	if err != nil || !released {
		t.Fatalf("release = %v, %v", released, err)
	}
	if got := log.snapshot(); len(got) != 3 || got[2] != (transition{kind: "resumed", reason: ReasonReleased}) {
		t.Fatalf("transitions = %#v", got)
	}
	if !controller.TryStart() {
		t.Fatal("admission did not resume after release")
	}
	controller.Done(nil)
}

func TestAcquireCancellationResumesWithActiveWorkAndNeverQuiesces(t *testing.T) {
	log := &transitionLog{}
	controller := New(log.hooks())
	if !controller.TryStart() {
		t.Fatal("initial admission was blocked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := controller.Acquire(ctx, time.Second)
		result <- err
	}()
	waitFor(t, func() bool { return len(log.snapshot()) == 1 }, "quiescing transition")
	cancel()
	if err := receive(t, result, "canceled acquisition"); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire error = %v", err)
	}
	if got := log.snapshot(); len(got) != 2 ||
		got[0] != (transition{kind: "quiescing", active: 1}) ||
		got[1] != (transition{kind: "resumed", reason: ReasonCanceled, active: 1}) {
		t.Fatalf("transitions = %#v", got)
	}
	if !controller.TryStart() {
		t.Fatal("admission did not resume after cancellation")
	}
	controller.Done(nil)
	controller.Done(nil)
	if len(log.snapshot()) != 2 {
		t.Fatalf("old completion recorded a stale quiesced transition: %#v", log.snapshot())
	}
}

func TestDoneVersusCancellationRecordsQuiescedBeforeZeroActiveResumption(t *testing.T) {
	log := &transitionLog{}
	quiesced := make(chan struct{})
	continueQuiesced := make(chan struct{})
	hooks := log.hooks()
	hooks.Quiesced = func(active int) error {
		close(quiesced)
		<-continueQuiesced
		log.add(transition{kind: "quiesced", active: active})
		return nil
	}
	controller := New(hooks)
	if !controller.TryStart() {
		t.Fatal("initial admission was blocked")
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := controller.Acquire(ctx, time.Second)
		result <- err
	}()
	waitFor(t, func() bool { return len(log.snapshot()) == 1 }, "quiescing transition")
	done := make(chan struct{})
	go func() {
		controller.Done(nil)
		close(done)
	}()
	receive(t, quiesced, "blocked quiesced transition")
	cancel()
	close(continueQuiesced)
	receive(t, done, "active operation completion")
	if err := receive(t, result, "canceled acquisition"); !errors.Is(err, context.Canceled) {
		t.Fatalf("acquire error = %v", err)
	}
	if got := log.snapshot(); len(got) != 3 ||
		got[0] != (transition{kind: "quiescing", active: 1}) ||
		got[1] != (transition{kind: "quiesced"}) ||
		got[2] != (transition{kind: "resumed", reason: ReasonCanceled}) {
		t.Fatalf("transitions = %#v", got)
	}
	if !controller.Accepting() {
		t.Fatal("admission did not resume after cancellation")
	}
}

func TestDoneVersusExpiryRecordsQuiescedBeforeZeroActiveResumption(t *testing.T) {
	log := &transitionLog{}
	quiesced := make(chan struct{})
	continueQuiesced := make(chan struct{})
	hooks := log.hooks()
	hooks.Quiesced = func(active int) error {
		close(quiesced)
		<-continueQuiesced
		log.add(transition{kind: "quiesced", active: active})
		return nil
	}
	controller := New(hooks)
	controller.token = func() (string, error) { return "lease-token", nil }
	if !controller.TryStart() {
		t.Fatal("initial admission was blocked")
	}
	type acquireResult struct {
		lease Lease
		err   error
	}
	acquired := make(chan acquireResult, 1)
	go func() {
		lease, err := controller.Acquire(context.Background(), time.Second)
		acquired <- acquireResult{lease: lease, err: err}
	}()
	waitFor(t, func() bool { return len(log.snapshot()) == 1 }, "quiescing transition")
	done := make(chan struct{})
	go func() {
		controller.Done(nil)
		close(done)
	}()
	receive(t, quiesced, "blocked quiesced transition")
	type releaseResult struct {
		found bool
		err   error
	}
	expired := make(chan releaseResult, 1)
	go func() {
		found, err := controller.release("lease-token", ReasonExpired)
		expired <- releaseResult{found: found, err: err}
	}()
	close(continueQuiesced)
	receive(t, done, "active operation completion")
	if got := receive(t, expired, "lease expiry"); !got.found || got.err != nil {
		t.Fatalf("expiry release = %v, %v", got.found, got.err)
	}
	gotAcquire := receive(t, acquired, "acquisition result")
	if gotAcquire.err != nil && !errors.Is(gotAcquire.err, ErrExpired) {
		t.Fatalf("acquire error = %v", gotAcquire.err)
	}
	if gotAcquire.err == nil && gotAcquire.lease.Token != "lease-token" {
		t.Fatalf("lease = %#v", gotAcquire.lease)
	}
	if got := log.snapshot(); len(got) != 3 ||
		got[0] != (transition{kind: "quiescing", active: 1}) ||
		got[1] != (transition{kind: "quiesced"}) ||
		got[2] != (transition{kind: "resumed", reason: ReasonExpired}) {
		t.Fatalf("transitions = %#v", got)
	}
	if !controller.Accepting() {
		t.Fatal("admission did not resume after expiry")
	}
}

func TestLeaseExpiryRecordsResumptionAfterAndBeforeDrain(t *testing.T) {
	t.Run("after drain", func(t *testing.T) {
		log := &transitionLog{}
		controller := New(log.hooks())
		lease, err := controller.Acquire(context.Background(), 20*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if released, releaseErr := controller.Release("wrong"); releaseErr != nil || released {
			t.Fatalf("wrong release = %v, %v", released, releaseErr)
		}
		waitFor(t, controller.Accepting, "lease expiry")
		if got := log.snapshot(); len(got) != 3 ||
			got[0].kind != "quiescing" || got[1].kind != "quiesced" ||
			got[2] != (transition{kind: "resumed", reason: ReasonExpired}) {
			t.Fatalf("transitions = %#v", got)
		}
		if released, releaseErr := controller.Release(lease.Token); releaseErr != nil || released {
			t.Fatalf("expired release = %v, %v", released, releaseErr)
		}
	})

	t.Run("before drain", func(t *testing.T) {
		log := &transitionLog{}
		controller := New(log.hooks())
		if !controller.TryStart() {
			t.Fatal("initial admission was blocked")
		}
		result := make(chan error, 1)
		go func() {
			_, err := controller.Acquire(context.Background(), 20*time.Millisecond)
			result <- err
		}()
		if err := receive(t, result, "expired acquisition"); !errors.Is(err, ErrExpired) {
			t.Fatalf("acquire error = %v", err)
		}
		if got := log.snapshot(); len(got) != 2 ||
			got[0] != (transition{kind: "quiescing", active: 1}) ||
			got[1] != (transition{kind: "resumed", reason: ReasonExpired, active: 1}) {
			t.Fatalf("transitions = %#v", got)
		}
		controller.Done(nil)
		if len(log.snapshot()) != 2 {
			t.Fatalf("old completion recorded a stale quiesced transition: %#v", log.snapshot())
		}
	})
}

func TestConcurrentAcquireIsRejected(t *testing.T) {
	controller := New(Hooks{})
	lease, err := controller.Acquire(context.Background(), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.Acquire(context.Background(), time.Second); !errors.Is(err, ErrAlreadyHeld) {
		t.Fatalf("second acquire error = %v", err)
	}
	if released, releaseErr := controller.Release(lease.Token); releaseErr != nil || !released {
		t.Fatalf("release = %v, %v", released, releaseErr)
	}
}

func TestDrainFailureBlocksQuiescenceAndNewAdmission(t *testing.T) {
	controller := New(Hooks{})
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

func TestDeploymentHookFailuresRemainFailClosed(t *testing.T) {
	for _, test := range []struct {
		name  string
		hooks Hooks
		act   func(*Controller) error
	}{
		{
			name:  "quiescing",
			hooks: Hooks{Quiescing: func(int) error { return errors.New("quiescing append") }},
			act: func(controller *Controller) error {
				_, err := controller.Acquire(context.Background(), time.Second)
				return err
			},
		},
		{
			name:  "quiesced",
			hooks: Hooks{Quiesced: func(int) error { return errors.New("quiesced append") }},
			act: func(controller *Controller) error {
				_, err := controller.Acquire(context.Background(), time.Second)
				return err
			},
		},
		{
			name:  "resuming",
			hooks: Hooks{Resuming: func(string, int) error { return errors.New("resumed append") }},
			act: func(controller *Controller) error {
				lease, err := controller.Acquire(context.Background(), time.Second)
				if err != nil {
					return err
				}
				_, err = controller.Release(lease.Token)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := New(test.hooks)
			if err := test.act(controller); !errors.Is(err, ErrDrainFailed) {
				t.Fatalf("transition error = %v", err)
			}
			if controller.Accepting() || controller.TryStart() {
				t.Fatal("hook failure reopened admission")
			}
		})
	}
}

func TestDeploymentExpiryHookFailureRemainsFailClosed(t *testing.T) {
	called := make(chan struct{}, 1)
	controller := New(Hooks{
		Resuming: func(reason string, active int) error {
			called <- struct{}{}
			return errors.New("expired resumption append")
		},
	})
	lease, err := controller.Acquire(context.Background(), 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	receive(t, called, "expiry resumption hook")
	if controller.Accepting() || controller.TryStart() {
		t.Fatal("failed expiry hook reopened admission")
	}
	if found, releaseErr := controller.Release(lease.Token); !found || !errors.Is(releaseErr, ErrDrainFailed) {
		t.Fatalf("release after failed expiry = %v, %v", found, releaseErr)
	}
}

func TestDeploymentQuiescingMayFollowTerminalEventWhileCountingItsSlot(t *testing.T) {
	eventStore := openStore(t)
	defer eventStore.Close()
	entered := make(chan struct{})
	releaseHook := make(chan struct{})
	recorder := deployment.NewRecorder(eventStore, state.ReleaseIdentity{})
	controller := New(Hooks{
		Quiescing: func(active int) error {
			close(entered)
			<-releaseHook
			return recorder.Quiescing(active)
		},
		Quiesced: recorder.Quiesced,
		Resuming: recorder.Resumed,
	})
	if !controller.TryStart() {
		t.Fatal("admission was blocked")
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
	<-entered
	terminal, err := eventStore.Append("workflow.authoring.completed", map[string]any{"workflowId": 1})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		controller.Done(nil)
		close(done)
	}()
	close(releaseHook)
	lease := receive(t, acquired, "drained lease")
	<-done
	events := allEvents(t, eventStore)
	quiescing := eventOfType(t, events, state.DeploymentQuiescing)
	quiesced := eventOfType(t, events, state.DeploymentQuiesced)
	var data state.DeploymentData
	if err := json.Unmarshal(quiescing.Data, &data); err != nil {
		t.Fatal(err)
	}
	if !(terminal.ID < quiescing.ID && quiescing.ID < quiesced.ID) || data.WorkflowActive != 1 {
		t.Fatalf("terminal=%d quiescing=%d quiesced=%d data=%#v", terminal.ID, quiescing.ID, quiesced.ID, data)
	}
	if released, releaseErr := controller.Release(lease.Token); releaseErr != nil || !released {
		t.Fatalf("release = %v, %v", released, releaseErr)
	}
}

func TestConditionalClaimCanDrainWithoutWorkflowLifecycleFacts(t *testing.T) {
	eventStore := openStore(t)
	defer eventStore.Close()
	if _, err := eventStore.Append("release.ready", map[string]bool{"ready": true}); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := eventStore.LastID()
	if err != nil {
		t.Fatal(err)
	}
	recorder := deployment.NewRecorder(eventStore, state.ReleaseIdentity{})
	controller := New(Hooks{
		Quiescing: recorder.Quiescing,
		Quiesced:  recorder.Quiesced,
		Resuming:  recorder.Resumed,
	})
	if !controller.TryStart() {
		t.Fatal("conditional claim admission was blocked")
	}
	acquired := make(chan Lease, 1)
	go func() {
		lease, acquireErr := controller.Acquire(context.Background(), time.Second)
		if acquireErr != nil {
			t.Errorf("acquire: %v", acquireErr)
			return
		}
		acquired <- lease
	}()
	waitFor(t, func() bool {
		return eventTypeCount(t, eventStore, state.DeploymentQuiescing) == 1
	}, "quiescing event")
	_, published, err := eventStore.AppendIfCurrent(
		checkpoint, state.WorkflowRunStarted, state.WorkflowRunData{TriggerID: 1, SourceEventID: 1},
	)
	if err != nil || published {
		t.Fatalf("conditional append = %v, %v", published, err)
	}
	controller.Done(nil)
	lease := receive(t, acquired, "drained lease")
	events := allEvents(t, eventStore)
	quiescing := eventOfType(t, events, state.DeploymentQuiescing)
	quiesced := eventOfType(t, events, state.DeploymentQuiesced)
	var data state.DeploymentData
	if json.Unmarshal(quiescing.Data, &data) != nil || data.WorkflowActive != 1 || quiesced.ID <= quiescing.ID {
		t.Fatalf("events = %#v, data = %#v", events, data)
	}
	for _, eventType := range []string{
		state.WorkflowRunStarted, state.WorkflowRunResumed,
		state.WorkflowRunCompleted, state.WorkflowRunFailed,
	} {
		if eventTypeCount(t, eventStore, eventType) != 0 {
			t.Fatalf("unexpected %s event: %#v", eventType, events)
		}
	}
	if released, releaseErr := controller.Release(lease.Token); releaseErr != nil || !released {
		t.Fatalf("release = %v, %v", released, releaseErr)
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	eventStore, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	return eventStore
}

func allEvents(t *testing.T, eventStore *store.Store) []eventwire.Event {
	t.Helper()
	events, err := eventStore.EventsAfter(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func eventOfType(t *testing.T, events []eventwire.Event, eventType string) eventwire.Event {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType {
			return event
		}
	}
	t.Fatalf("event %s missing from %#v", eventType, events)
	return eventwire.Event{}
}

func eventTypeCount(t *testing.T, eventStore *store.Store, eventType string) int {
	t.Helper()
	count := 0
	for _, event := range allEvents(t, eventStore) {
		if event.Type == eventType {
			count++
		}
	}
	return count
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
