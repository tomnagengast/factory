package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/network/apps/factory/internal/githubhook"
)

func TestGitHubEventsHelperReturnsMatchingJournalEvents(t *testing.T) {
	stateRoot := t.TempDir()
	runID := "run-test"
	runDirectory := filepath.Join(stateRoot, "runs", runID)
	journalPath := filepath.Join(stateRoot, "data", "github-events.json")
	journal, err := githubhook.Open(journalPath, 10)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	if _, err := journal.Add(githubhook.Event{
		DeliveryID:   "delivery-1",
		Type:         "check_run",
		Action:       "completed",
		Repository:   "tomnagengast/network",
		PullRequests: []int{42},
		ReceivedAt:   time.Now(),
	}); err != nil {
		t.Fatalf("add event: %v", err)
	}
	t.Setenv("FACTORY_RUN_ID", runID)
	t.Setenv("FACTORY_RUN_DIR", runDirectory)

	var output bytes.Buffer
	code := runGitHubEventsHelper(context.Background(), []string{
		"--repo", "tomnagengast/network",
		"--pr", "42",
		"--wait", "0s",
	}, &output)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var batch githubhook.Batch
	if err := json.NewDecoder(&output).Decode(&batch); err != nil {
		t.Fatalf("decode batch: %v", err)
	}
	if batch.Cursor != 1 || len(batch.Events) != 1 || batch.Events[0].DeliveryID != "delivery-1" {
		t.Fatalf("batch = %#v", batch)
	}
}
