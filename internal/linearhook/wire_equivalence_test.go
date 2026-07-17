package linearhook

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/tomnagengast/factory/internal/eventwire"
)

func TestWireProjectionMatchesLegacyLinearJournal(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, "linear-comments.json")
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
		{DeliveryID: "delivery-1", CommentID: "comment-1", IssueID: "11111111-1111-4111-8111-111111111111", IssueIdentifier: "ENG-47", URL: "https://linear.app/comment/1", ReceivedAt: time.Unix(1, 0).UTC()},
		{DeliveryID: "delivery-2", CommentID: "comment-2", IssueID: "11111111-1111-4111-8111-111111111111", IssueIdentifier: "ENG-47", ParentID: "comment-1", URL: "https://linear.app/comment/2", ReceivedAt: time.Unix(2, 0).UTC()},
		{DeliveryID: "delivery-3", CommentID: "comment-3", IssueID: "22222222-2222-4222-8222-222222222222", IssueIdentifier: "ENG-48", ReceivedAt: time.Unix(3, 0).UTC()},
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
	filter := Filter{IssueIdentifier: "eng-47"}
	legacyBatch, err := Read(legacyPath, filter, 0)
	if err != nil {
		t.Fatal(err)
	}
	wireBatch, err := ReadWire(wirePath, filter, 0)
	if err != nil {
		t.Fatal(err)
	}
	if legacyBatch.Cursor != wireBatch.Cursor || !reflect.DeepEqual(legacyBatch.Events, wireBatch.Events) {
		t.Fatalf("Linear projections differ: legacy=%#v wire=%#v", legacyBatch, wireBatch)
	}
}
