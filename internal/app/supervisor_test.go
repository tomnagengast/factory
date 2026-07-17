package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSupervisorPropagatesFailureAndJoinsEveryComponent(t *testing.T) {
	failure := errors.New("runtime failed")
	started := make(chan string, 3)
	joined := make(chan string, 3)
	block := func(name string) Component {
		return Component{Name: name, Run: func(ctx context.Context) error {
			started <- name
			<-ctx.Done()
			joined <- name
			return ctx.Err()
		}}
	}
	failing := Component{Name: "wire", Run: func(ctx context.Context) error {
		started <- "wire"
		for len(started) != 3 {
			time.Sleep(time.Millisecond)
		}
		joined <- "wire"
		return failure
	}}
	supervisor, err := NewSupervisor(block("http"), block("runs"), failing)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Run(context.Background()); !errors.Is(err, failure) || !strings.Contains(err.Error(), "wire") {
		t.Fatalf("supervisor error = %v", err)
	}
	seen := map[string]bool{}
	for range 3 {
		seen[<-joined] = true
	}
	if !seen["http"] || !seen["runs"] || !seen["wire"] {
		t.Fatalf("not every component joined: %#v", seen)
	}
}

func TestSupervisorExternalCancellationIsClean(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var joined sync.WaitGroup
	joined.Add(2)
	component := func(name string) Component {
		return Component{Name: name, Run: func(ctx context.Context) error {
			defer joined.Done()
			<-ctx.Done()
			return ctx.Err()
		}}
	}
	supervisor, err := NewSupervisor(component("one"), component("two"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("canceled supervisor = %v", err)
	}
	joined.Wait()
}

func TestSupervisorRejectsEarlySuccessAndInvalidComponents(t *testing.T) {
	supervisor, err := NewSupervisor(Component{Name: "short", Run: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "stopped before") {
		t.Fatalf("early return = %v", err)
	}
	for _, components := range [][]Component{
		nil,
		{{Name: "", Run: func(context.Context) error { return nil }}},
		{{Name: "same", Run: func(context.Context) error { return nil }}, {Name: "same", Run: func(context.Context) error { return nil }}},
	} {
		if _, err := NewSupervisor(components...); err == nil {
			t.Fatalf("accepted invalid components: %#v", components)
		}
	}
}

func TestWaitForReadyHonorsGateAndCancellation(t *testing.T) {
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- WaitForReady(context.Background(), ready) }()
	close(ready)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WaitForReady(ctx, make(chan struct{})); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled readiness wait = %v", err)
	}
}
