package eventwire

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestActivityStorePersistsPayloadsAndPrunesAtomically(t *testing.T) {
	root := newActivityStoreRoot(t)
	store, err := OpenActivityStore(root, 2)
	if err != nil {
		t.Fatalf("open activity store: %v", err)
	}
	firstBody := []byte(`{"issue":"ENG-47"}`)
	if err := store.StagePayload("linear-1", firstBody); err != nil {
		t.Fatalf("stage first payload: %v", err)
	}
	if added, err := store.AddStaged("linear-1", activityStoreEvent(1)); err != nil || !added {
		t.Fatalf("add first payload event: added=%v err=%v", added, err)
	}
	if added, err := store.Add("github-1", activityStoreEvent(2)); err != nil || !added {
		t.Fatalf("add body-free event: added=%v err=%v", added, err)
	}
	secondBody := []byte(`{"issue":"ENG-48"}`)
	if err := store.StagePayload("linear-2", secondBody); err != nil {
		t.Fatalf("stage second payload: %v", err)
	}
	if added, err := store.AddStaged("linear-2", activityStoreEvent(3)); err != nil || !added {
		t.Fatalf("add second payload event: added=%v err=%v", added, err)
	}

	snapshot := store.Snapshot()
	if snapshot.Total != 3 || len(snapshot.Events) != 2 || snapshot.Events[0].DeliveryID != "linear-2" || snapshot.Events[1].DeliveryID != "github-1" {
		t.Fatalf("unexpected projection: %+v", snapshot)
	}
	if _, err := os.Stat(filepath.Join(root, activityPayloadDirectory, activityPayloadFile("linear-1"))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pruned private payload remains: %v", err)
	}
	if body, err := store.StagedPayload("linear-2"); err != nil || !reflect.DeepEqual(body, secondBody) {
		t.Fatalf("read retained payload: body=%q err=%v", body, err)
	}
	if added, err := store.AddStaged("linear-2", activityStoreEvent(3)); err != nil || added {
		t.Fatalf("duplicate delivery was not idempotent: added=%v err=%v", added, err)
	}

	reopened, err := OpenActivityStore(root, 2)
	if err != nil {
		t.Fatalf("reopen activity store: %v", err)
	}
	if !reflect.DeepEqual(reopened.Snapshot(), snapshot) {
		t.Fatalf("reopened projection changed")
	}
	if _, err := os.Stat(filepath.Join(root, activityPendingFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending intent remains after commit: %v", err)
	}
}

func TestActivityStoreRecoversCommittedPayloadOperation(t *testing.T) {
	root := newActivityStoreRoot(t)
	body := []byte(`{"committed":true}`)
	deliveryID := "linear-committed"
	record := activityStoreRecord(deliveryID, body, activityStoreEvent(1))
	projection := ActivityProjection{Schema: ActivitySchemaVersion, Total: 1, Events: []ActivityRecord{record}}
	if err := writePrivateActivityFile(filepath.Join(root, activityPayloadDirectory, record.Payload.File), body); err != nil {
		t.Fatal(err)
	}
	if err := writeActivityProjection(filepath.Join(root, activityProjectionFile), projection); err != nil {
		t.Fatal(err)
	}
	if err := writeActivityJSON(filepath.Join(root, activityPendingFile), activityPending{Schema: 1, DeliveryID: deliveryID, Payload: true}); err != nil {
		t.Fatal(err)
	}

	store, err := OpenActivityStore(root, 10)
	if err != nil {
		t.Fatalf("recover committed operation: %v", err)
	}
	if !reflect.DeepEqual(store.Snapshot(), projection) {
		t.Fatalf("committed projection changed")
	}
	if _, err := os.Stat(filepath.Join(root, activityPendingFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed pending intent remains: %v", err)
	}
}

func TestActivityStoreRecoversUncommittedPayloadForReplay(t *testing.T) {
	root := newActivityStoreRoot(t)
	body := []byte(`{"committed":false}`)
	deliveryID := "linear-uncommitted"
	file := activityPayloadFile(deliveryID)
	if err := writePrivateActivityFile(filepath.Join(root, activityPayloadDirectory, file), body); err != nil {
		t.Fatal(err)
	}
	if err := writeActivityJSON(filepath.Join(root, activityPendingFile), activityPending{Schema: 1, DeliveryID: deliveryID, Payload: true}); err != nil {
		t.Fatal(err)
	}

	store, err := OpenActivityStore(root, 10)
	if err != nil {
		t.Fatalf("recover uncommitted operation: %v", err)
	}
	if store.Snapshot().Total != 0 {
		t.Fatalf("uncommitted operation advanced the projection")
	}
	if staged, err := store.StagedPayload(deliveryID); err != nil || !reflect.DeepEqual(staged, body) {
		t.Fatalf("recovered staged payload: body=%q err=%v", staged, err)
	}
	if added, err := store.AddStaged(deliveryID, activityStoreEvent(1)); err != nil || !added {
		t.Fatalf("replay recovered operation: added=%v err=%v", added, err)
	}
	if _, err := OpenActivityStore(root, 10); err != nil {
		t.Fatalf("strict reopen after replay: %v", err)
	}
}

func TestActivityStoreRejectsUnexplainedOrConflictingBodies(t *testing.T) {
	t.Run("final orphan", func(t *testing.T) {
		root := newActivityStoreRoot(t)
		if err := writePrivateActivityFile(filepath.Join(root, activityPayloadDirectory, activityPayloadFile("orphan")), []byte(`{"orphan":true}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenActivityStore(root, 10); err == nil {
			t.Fatal("opened an unexplained final payload")
		}
	})

	t.Run("staged conflict", func(t *testing.T) {
		root := newActivityStoreRoot(t)
		store, err := OpenActivityStore(root, 10)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.StagePayload("delivery", []byte(`{"value":1}`)); err != nil {
			t.Fatal(err)
		}
		if err := store.StagePayload("delivery", []byte(`{"value":2}`)); err == nil {
			t.Fatal("accepted a conflicting body for one delivery")
		}
	})
}

func newActivityStoreRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "activity")
	if err := MaterializeActivity(root, ActivityProjection{Schema: ActivitySchemaVersion, Events: []ActivityRecord{}}, map[string][]byte{}); err != nil {
		t.Fatalf("materialize activity: %v", err)
	}
	return root
}

func activityStoreEvent(offset int) ActivityEvent {
	return ActivityEvent{Type: "Issue", Action: "update", ReceivedAt: time.Date(2026, time.July, 16, 12, offset, 0, 0, time.UTC)}
}

func activityStoreRecord(deliveryID string, body []byte, event ActivityEvent) ActivityRecord {
	digest := sha256.Sum256(body)
	return ActivityRecord{
		DeliveryID:    deliveryID,
		Payload:       &ActivityPayloadReference{File: activityPayloadFile(deliveryID), SHA256: hex.EncodeToString(digest[:]), Size: int64(len(body))},
		ActivityEvent: event,
	}
}
