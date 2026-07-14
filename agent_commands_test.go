package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/settings"
)

func TestLoadRunSettingsUsesValidatedSharedStateRoot(t *testing.T) {
	stateRoot := t.TempDir()
	runID := "run-test"
	runDirectory := filepath.Join(stateRoot, "runs", runID)
	t.Setenv("FACTORY_RUN_ID", runID)
	t.Setenv("FACTORY_RUN_DIR", runDirectory)

	defaults, err := loadRunSettings(runDirectory)
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if defaults.Agents.Principal.Model != "gpt-5.6-sol" {
		t.Fatalf("default settings = %#v", defaults)
	}
	store, err := settings.Open(filepath.Join(stateRoot, "data", "settings.json"), settings.Defaults(3))
	if err != nil {
		t.Fatalf("open settings: %v", err)
	}
	candidate := store.Snapshot()
	candidate.Agents.Principal.Model = "gpt-custom"
	if _, err := store.Update(candidate.Revision, candidate, time.Now()); err != nil {
		t.Fatalf("update settings: %v", err)
	}
	loaded, err := loadRunSettings(runDirectory)
	if err != nil || loaded.Agents.Principal.Model != "gpt-custom" {
		t.Fatalf("loaded settings = %#v, %v", loaded, err)
	}
	if _, err := loadRunSettings(filepath.Join(stateRoot, "other", runID)); err == nil {
		t.Fatal("invalid run directory was accepted")
	}
}

func TestGitHubEventsHelperReturnsMatchingJournalEvents(t *testing.T) {
	stateRoot := t.TempDir()
	runID := "run-test"
	runDirectory := filepath.Join(stateRoot, "runs", runID)
	journalPath := filepath.Join(stateRoot, "data", "system-events.jsonl")
	journal, err := eventwire.Open(journalPath, 10, nil)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	if _, _, err := wire.Publish(context.Background(), githubhook.ToWire(githubhook.Event{
		DeliveryID:   "delivery-1",
		Type:         "check_run",
		Action:       "completed",
		Repository:   "tomnagengast/network",
		PullRequests: []int{42},
		ReceivedAt:   time.Now(),
	})); err != nil {
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

func TestLinearCommentsHelperReturnsMatchingJournalEvents(t *testing.T) {
	stateRoot := t.TempDir()
	runID := "run-test"
	runDirectory := filepath.Join(stateRoot, "runs", runID)
	journalPath := filepath.Join(stateRoot, "data", "system-events.jsonl")
	journal, err := eventwire.Open(journalPath, 10, nil)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	if _, _, err := wire.Publish(context.Background(), linearhook.ToWire(linearhook.Event{
		DeliveryID:      "delivery-1",
		CommentID:       "comment-1",
		IssueID:         "issue-42",
		IssueIdentifier: "ENG-42",
		ReceivedAt:      time.Now(),
	})); err != nil {
		t.Fatalf("add event: %v", err)
	}
	t.Setenv("FACTORY_RUN_ID", runID)
	t.Setenv("FACTORY_RUN_DIR", runDirectory)

	for _, args := range [][]string{
		{"--issue", "eng-42", "--wait", "0s"},
		{"--issue-id", "issue-42", "--wait", "0s"},
	} {
		var output bytes.Buffer
		if code := runLinearCommentsHelper(context.Background(), args, &output); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		var batch linearhook.Batch
		if err := json.NewDecoder(&output).Decode(&batch); err != nil {
			t.Fatalf("decode batch: %v", err)
		}
		if batch.Cursor != 1 || len(batch.Events) != 1 || batch.Events[0].DeliveryID != "delivery-1" {
			t.Fatalf("batch = %#v", batch)
		}
	}
}

func TestLinearCommentsHelperRejectsInvalidRunEnvironment(t *testing.T) {
	t.Setenv("FACTORY_RUN_ID", "run-test")
	t.Setenv("FACTORY_RUN_DIR", filepath.Join(t.TempDir(), "wrong", "run-test"))
	if code := runLinearCommentsHelper(context.Background(), []string{"--issue", "ENG-42", "--wait", "0s"}, &bytes.Buffer{}); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestEventsHelperFiltersUnifiedJournal(t *testing.T) {
	stateRoot := t.TempDir()
	runID := "run-test"
	runDirectory := filepath.Join(stateRoot, "runs", runID)
	journal, err := eventwire.Open(filepath.Join(stateRoot, "data", "system-events.jsonl"), 10, nil)
	if err != nil {
		t.Fatalf("open journal: %v", err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatalf("new wire: %v", err)
	}
	for _, event := range []eventwire.Event{
		{ID: "telemetry:start", Source: eventwire.Source("telemetry"), Type: "service", Action: "started", Attributes: map[string][]string{"status": {"ok"}}, ReceivedAt: time.Now()},
		{ID: "github:delivery", Source: eventwire.SourceGitHub, Type: "ping", Action: "received", ReceivedAt: time.Now()},
	} {
		if _, _, err := wire.Publish(context.Background(), event); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	direct, err := eventwire.Read(filepath.Join(stateRoot, "data", "system-events.jsonl"), eventwire.Filter{}, 0)
	if err != nil || len(direct.Events) != 2 {
		t.Fatalf("direct batch = %#v, %v", direct, err)
	}
	filtered, err := eventwire.Read(filepath.Join(stateRoot, "data", "system-events.jsonl"), eventwire.Filter{Source: eventwire.Source("telemetry"), Type: "service", Attributes: map[string]string{"status": "ok"}}, 0)
	if err != nil || len(filtered.Events) != 1 {
		t.Fatalf("filtered batch = %#v, %v", filtered, err)
	}
	t.Setenv("FACTORY_RUN_ID", runID)
	t.Setenv("FACTORY_RUN_DIR", runDirectory)
	var output bytes.Buffer
	code := runEventsHelper(context.Background(), []string{
		"--source", "telemetry",
		"--type", "service",
		"--match", "status=ok",
		"--wait", "0s",
	}, &output)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	var batch eventwire.Batch
	if err := json.NewDecoder(&output).Decode(&batch); err != nil {
		t.Fatalf("decode batch: %v", err)
	}
	if batch.Cursor != 2 || len(batch.Events) != 1 || batch.Events[0].ID != "telemetry:start" {
		t.Fatalf("batch = %#v", batch)
	}
}
