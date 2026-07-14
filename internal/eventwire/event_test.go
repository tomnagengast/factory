package eventwire

import (
	"path/filepath"
	"testing"
)

func TestEventAcceptsOpenSourceAndCanonicalizesDirectCausation(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.jsonl")
	journal, err := Open(path, 10, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	event := testEvent("telemetry:one", Source("telemetry"), "metric")
	record, added, err := journal.Add(event)
	if err != nil || !added {
		t.Fatalf("add = %#v, %t, %v", record, added, err)
	}
	if record.Event.RootEventID != event.ID || record.Event.Hop != 0 || len(record.Event.AncestorRuleIDs) != 0 {
		t.Fatalf("canonical event = %#v", record.Event)
	}
	reopened, err := Open(path, 10, nil)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	persisted, found := reopened.Record(record.Sequence)
	if !found || persisted.Event.Source != Source("telemetry") || persisted.Event.RootEventID != event.ID {
		t.Fatalf("reopened event = %#v, found=%t", persisted.Event, found)
	}

	invalidJournal, err := Open(filepath.Join(t.TempDir(), "other.jsonl"), 10, nil)
	if err != nil {
		t.Fatalf("open second journal: %v", err)
	}
	if _, _, err := invalidJournal.Add(testEvent("bad:one", Source("Telemetry"), "metric")); err == nil {
		t.Fatal("uppercase source was accepted")
	}
}

func TestEventValidatesDerivedCausation(t *testing.T) {
	t.Parallel()
	valid := testEvent("factory:child", SourceFactory, "agent-run")
	valid.RootEventID = "linear:root"
	valid.ParentInvocationID = "invocation-1"
	valid.ParentRunID = "run-1"
	valid.Hop = 2
	valid.AncestorRuleIDs = []string{"rule-a", "rule-b"}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid derived event: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{name: "missing root", mutate: func(event *Event) { event.RootEventID = "" }},
		{name: "missing parent invocation", mutate: func(event *Event) { event.ParentInvocationID = "" }},
		{name: "hop mismatch", mutate: func(event *Event) { event.Hop = 1 }},
		{name: "duplicate ancestor", mutate: func(event *Event) { event.AncestorRuleIDs[1] = "rule-a" }},
		{name: "invalid ancestor", mutate: func(event *Event) { event.AncestorRuleIDs[1] = "Rule_B" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := cloneEvent(valid)
			test.mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatalf("invalid event was accepted: %#v", event)
			}
		})
	}
}

func TestEventCloneIsolatesCausationAndAttributes(t *testing.T) {
	t.Parallel()
	event := testEvent("factory:clone", SourceFactory, "agent-run")
	event.Attributes = map[string][]string{"status": {"running"}}
	event.RootEventID = "linear:root"
	event.ParentInvocationID = "invocation-1"
	event.Hop = 1
	event.AncestorRuleIDs = []string{"rule-a"}

	cloned := cloneEvent(event)
	cloned.Attributes["status"][0] = "failed"
	cloned.AncestorRuleIDs[0] = "rule-b"
	if event.Attributes["status"][0] != "running" || event.AncestorRuleIDs[0] != "rule-a" {
		t.Fatalf("clone mutated source event: %#v", event)
	}
}
