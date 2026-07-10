package githubhook

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestJournalDeduplicatesPrunesAndFilters(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.json")
	journal, err := Open(path, 2)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	events := []Event{
		{DeliveryID: "one", Type: "pull_request", Repository: "tom/repo", PullRequests: []int{1}, ReceivedAt: time.Unix(1, 0)},
		{DeliveryID: "two", Type: "check_run", Repository: "tom/repo", PullRequests: []int{2}, ReceivedAt: time.Unix(2, 0)},
		{DeliveryID: "three", Type: "check_run", Repository: "tom/repo", HeadBranch: "eng-3", ReceivedAt: time.Unix(3, 0)},
	}
	for _, event := range events {
		if added, addErr := journal.Add(event); addErr != nil || !added {
			t.Fatalf("add %s = %t, %v", event.DeliveryID, added, addErr)
		}
	}
	if added, addErr := journal.Add(events[2]); addErr != nil || added {
		t.Fatalf("duplicate = %t, %v", added, addErr)
	}

	batch, err := Read(path, Filter{Repository: "TOM/REPO", HeadBranch: "eng-3"}, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if batch.Cursor != 3 || len(batch.Events) != 1 || batch.Events[0].DeliveryID != "three" {
		t.Fatalf("batch = %#v", batch)
	}
	batch, err = Read(path, Filter{Repository: "tom/repo", PullRequest: 1}, 0)
	if err != nil {
		t.Fatalf("read pruned: %v", err)
	}
	if len(batch.Events) != 0 {
		t.Fatalf("pruned events = %#v", batch.Events)
	}
}

func TestWaitReturnsNewMatchingEvent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.json")
	journal, err := Open(path, 10)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result := make(chan Batch, 1)
	errors := make(chan error, 1)
	go func() {
		batch, waitErr := Wait(ctx, path, Filter{Repository: "tom/repo", PullRequest: 42}, 0, time.Millisecond)
		if waitErr != nil {
			errors <- waitErr
			return
		}
		result <- batch
	}()
	if _, err := journal.Add(Event{DeliveryID: "delivery", Type: "check_run", Repository: "tom/repo", PullRequests: []int{42}, ReceivedAt: time.Now()}); err != nil {
		t.Fatalf("add: %v", err)
	}
	select {
	case err := <-errors:
		t.Fatalf("wait: %v", err)
	case batch := <-result:
		if batch.Cursor != 1 || len(batch.Events) != 1 || batch.Events[0].DeliveryID != "delivery" {
			t.Fatalf("batch = %#v", batch)
		}
	case <-ctx.Done():
		t.Fatal("wait timed out")
	}
}

func TestReadNeverRegressesCursorAfterJournalReset(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.json")
	journal, err := Open(path, 10)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := journal.Add(Event{DeliveryID: "delivery", Type: "check_run", Repository: "tom/repo", PullRequests: []int{42}}); err != nil {
		t.Fatalf("add: %v", err)
	}
	batch, err := Read(path, Filter{Repository: "tom/repo", PullRequest: 42}, 50)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if batch.Cursor != 50 || len(batch.Events) != 0 {
		t.Fatalf("batch = %#v", batch)
	}
}
