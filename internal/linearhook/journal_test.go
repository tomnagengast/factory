package linearhook

import (
	"context"
	"errors"
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
		{DeliveryID: "one", CommentID: "comment-1", IssueID: "issue-1", IssueIdentifier: "ENG-1"},
		{DeliveryID: "two", CommentID: "comment-2", IssueID: "issue-2", IssueIdentifier: "ENG-2"},
		{DeliveryID: "three", CommentID: "comment-3", IssueID: "issue-3", IssueIdentifier: "ENG-3"},
	}
	for _, event := range events {
		if added, addErr := journal.Add(event); addErr != nil || !added {
			t.Fatalf("add %s = %t, %v", event.DeliveryID, added, addErr)
		}
	}
	if added, addErr := journal.Add(events[2]); addErr != nil || added {
		t.Fatalf("duplicate = %t, %v", added, addErr)
	}

	batch, err := Read(path, Filter{IssueIdentifier: "eng-3"}, 0)
	if err != nil {
		t.Fatalf("read identifier: %v", err)
	}
	if batch.Cursor != 3 || len(batch.Events) != 1 || batch.Events[0].DeliveryID != "three" {
		t.Fatalf("batch = %#v", batch)
	}
	batch, err = Read(path, Filter{IssueID: "issue-2"}, 0)
	if err != nil {
		t.Fatalf("read ID: %v", err)
	}
	if len(batch.Events) != 1 || batch.Events[0].DeliveryID != "two" {
		t.Fatalf("ID batch = %#v", batch)
	}
	batch, err = Read(path, Filter{IssueID: "issue-1"}, 0)
	if err != nil || len(batch.Events) != 0 {
		t.Fatalf("pruned batch = %#v, %v", batch, err)
	}
}

func TestWaitReturnsNewEventAndTimeoutCursor(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.json")
	journal, err := Open(path, 10)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan Batch, 1)
	go func() {
		batch, waitErr := Wait(ctx, path, Filter{IssueIdentifier: "ENG-7"}, 0, time.Millisecond)
		if waitErr == nil {
			result <- batch
		}
	}()
	if _, err := journal.Add(Event{DeliveryID: "one", CommentID: "comment", IssueID: "issue", IssueIdentifier: "ENG-7"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	select {
	case batch := <-result:
		if batch.Cursor != 1 || len(batch.Events) != 1 {
			t.Fatalf("batch = %#v", batch)
		}
	case <-ctx.Done():
		t.Fatal("wait did not return event")
	}

	timeoutCtx, timeoutCancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer timeoutCancel()
	batch, err := Wait(timeoutCtx, path, Filter{IssueIdentifier: "ENG-8"}, 0, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) || batch.Cursor != 1 || len(batch.Events) != 0 {
		t.Fatalf("timeout batch = %#v, %v", batch, err)
	}
}

func TestReadMissingJournal(t *testing.T) {
	t.Parallel()
	batch, err := Read(filepath.Join(t.TempDir(), "missing.json"), Filter{IssueIdentifier: "ENG-1"}, 4)
	if err != nil || batch.Cursor != 4 || len(batch.Events) != 0 {
		t.Fatalf("batch = %#v, %v", batch, err)
	}
}
