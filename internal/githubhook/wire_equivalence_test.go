package githubhook

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

func TestWireProjectionMatchesLegacyGitHubJournal(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, "github-events.json")
	wirePath := filepath.Join(root, "system-events.jsonl")
	legacy, err := Open(legacyPath, 100)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := eventwire.Open(wirePath, 100, nil)
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{DeliveryID: "delivery-1", Type: "pull_request", Action: "synchronize", Repository: "tomnagengast/factory", PullRequests: []int{18}, HeadBranch: "eng-47-branch", HeadSHA: "abc123", URL: "https://github.com/tomnagengast/factory/pull/18", ReceivedAt: time.Unix(1, 0).UTC()},
		{DeliveryID: "delivery-2", Type: "check_run", Action: "completed", Repository: "tomnagengast/factory", PullRequests: []int{18, 19}, HeadBranch: "eng-47-branch", HeadSHA: "def456", Status: "completed", Conclusion: "success", URL: "https://github.com/check/2", ReceivedAt: time.Unix(2, 0).UTC()},
		{DeliveryID: "delivery-3", Type: "push", Action: "received", Repository: "tomnagengast/network", HeadBranch: "main", HeadSHA: "fedcba", ReceivedAt: time.Unix(3, 0).UTC()},
	}
	for index, event := range events {
		projected := ToWire(event)
		if roundTrip, ok := FromWire(projected); !ok || !reflect.DeepEqual(roundTrip, event) {
			t.Fatalf("wire round trip %d = %#v, %t", index, roundTrip, ok)
		}
		if _, _, err := wire.Add(projected); err != nil {
			t.Fatal(err)
		}
		if added, err := legacy.AddAt(uint64(index+1), event); err != nil || !added {
			t.Fatalf("legacy add %d = %t, %v", index, added, err)
		}
	}
	if err := wire.Acknowledge(uint64(len(events)), map[string]uint64{WireChannel: uint64(len(events))}); err != nil {
		t.Fatal(err)
	}
	filter := Filter{Repository: "tomnagengast/factory", PullRequest: 18}
	legacyBatch, err := Read(legacyPath, filter, 0)
	if err != nil {
		t.Fatal(err)
	}
	wireBatch, err := ReadWire(wirePath, filter, 0)
	if err != nil {
		t.Fatal(err)
	}
	if legacyBatch.Cursor != wireBatch.Cursor || !reflect.DeepEqual(legacyBatch.Events, wireBatch.Events) {
		t.Fatalf("GitHub projections differ: legacy=%#v wire=%#v", legacyBatch, wireBatch)
	}
}
