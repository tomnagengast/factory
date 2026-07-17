package app

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/triggerregistry"
)

type fixedProviderCoordinator struct{ issue projectsetup.ProviderIssue }

func (c fixedProviderCoordinator) Ensure(context.Context, projectsetup.Spec) (projectsetup.ProviderIssue, error) {
	return c.issue, nil
}

func TestProviderAgentStarterPublishesDeterministicCanonicalAdmission(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := eventwire.MaterializeActivity(filepath.Join(root, "activity"), eventwire.ActivityProjection{
		Schema: eventwire.ActivitySchemaVersion, Events: []eventwire.ActivityRecord{},
	}, map[string][]byte{}); err != nil {
		t.Fatal(err)
	}
	activity, err := eventwire.OpenActivityStore(filepath.Join(root, "activity"), 10)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := eventwire.Open(filepath.Join(root, "wire.jsonl"), 10, nil)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := eventwire.New(journal)
	if err != nil {
		t.Fatal(err)
	}
	var records []eventwire.Record
	if err := wire.Handle(eventwire.Filter{Source: eventwire.SourceLinear}, func(_ context.Context, record eventwire.Record) error {
		records = append(records, record)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.July, 17, 16, 0, 0, 0, time.UTC)
	starter, err := NewProviderAgentStarter(
		fixedProviderCoordinator{issue: projectsetup.ProviderIssue{ID: "issue-1", Identifier: "ENG-48"}},
		activity, wire, "actor-1", "Factory", func() time.Time { return now },
	)
	if err != nil {
		t.Fatal(err)
	}
	spec := projectsetup.Spec{ProjectID: "project-1", Repository: "tomnagengast/widget", CloudURL: "https://widget.nags.cloud"}
	if err := starter.Start(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if err := starter.Start(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d", len(records))
	}
	record := records[0]
	if record.Event.Subject != "ENG-48" || record.Event.Values(triggerregistry.AttributeActorID)[0] != "actor-1" ||
		record.Event.Values(triggerregistry.AttributeAddedLabel)[0] != "FACTORY" {
		t.Fatalf("event = %+v", record.Event)
	}
	deliveryID := record.Event.Values("deliveryId")[0]
	if body, err := activity.StagedPayload(deliveryID); err != nil || len(body) == 0 {
		t.Fatalf("staged payload = %q, %v", body, err)
	}
}
