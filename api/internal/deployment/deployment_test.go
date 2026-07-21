package deployment

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tomnagengast/factory/api/internal/state"
	"github.com/tomnagengast/factory/api/internal/store"
)

func TestFromEnvironmentAndRecorderPayloads(t *testing.T) {
	t.Setenv("FACTORY_RELEASE_COMMIT", "commit-1")
	t.Setenv("FACTORY_RELEASE_TREE", "tree-1")
	t.Setenv("FACTORY_RELEASE_BUILD", "build-1")
	t.Setenv("FACTORY_RELEASE_DEPLOYMENT", "deployment-1")
	t.Setenv("FACTORY_RELEASE_CONTRACT", "1")
	identity := FromEnvironment()
	wantIdentity := state.ReleaseIdentity{
		Commit: "commit-1", Tree: "tree-1", BuildID: "build-1",
		DeploymentID: "deployment-1", ContractVersion: "1",
	}
	if identity != wantIdentity {
		t.Fatalf("identity = %#v, want %#v", identity, wantIdentity)
	}
	eventStore := openTestStore(t)
	defer eventStore.Close()
	recorder := NewRecorder(eventStore, identity)
	for _, record := range []struct {
		eventType string
		active    int
		reason    string
		call      func() error
	}{
		{state.DeploymentStarted, 0, "", recorder.Started},
		{state.DeploymentQuiescing, 2, "", func() error { return recorder.Quiescing(2) }},
		{state.DeploymentQuiesced, 0, "", func() error { return recorder.Quiesced(0) }},
		{state.DeploymentResumed, 1, "canceled", func() error { return recorder.Resumed("canceled", 1) }},
	} {
		if err := record.call(); err != nil {
			t.Fatal(err)
		}
		events, err := eventStore.EventsBefore(0, 1)
		if err != nil {
			t.Fatal(err)
		}
		var data state.DeploymentData
		if len(events) != 1 || events[0].Type != record.eventType || json.Unmarshal(events[0].Data, &data) != nil {
			t.Fatalf("event = %#v", events)
		}
		wantData := Data(identity, record.active, record.reason)
		if !reflect.DeepEqual(data, wantData) {
			t.Fatalf("data = %#v, want %#v", data, wantData)
		}
	}
}

func TestDeploymentStartedSurvivesStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factory.db")
	eventStore, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewRecorder(eventStore, state.ReleaseIdentity{}).Started(); err != nil {
		t.Fatal(err)
	}
	if err := eventStore.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	events, err := reopened.EventsAfter(0, 10)
	if err != nil || len(events) != 1 || events[0].Type != state.DeploymentStarted {
		t.Fatalf("events = %#v, %v", events, err)
	}
}

func TestRecorderReturnsRequiredBoundaryWriteFailure(t *testing.T) {
	eventStore := openTestStore(t)
	recorder := NewRecorder(eventStore, state.ReleaseIdentity{})
	if err := eventStore.Close(); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Started(); !errors.Is(err, store.ErrClosed) {
		t.Fatalf("startup append error = %v", err)
	}
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	eventStore, err := store.Open(filepath.Join(t.TempDir(), "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	return eventStore
}
