package eventwire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestActivityConversionMaterializesPrivateCanonicalCorpus(t *testing.T) {
	records := []ActivitySourceRecord{
		{DeliveryID: "linear-new", PayloadAvailable: true, Event: ActivityEvent{Type: "Comment", Action: "create", ReceivedAt: time.Unix(2, 0).UTC()}},
		{DeliveryID: "linear-old", Event: ActivityEvent{Type: "Issue", Action: "update", ReceivedAt: time.Unix(1, 0).UTC()}},
	}
	payloads := map[string][]byte{"linear-new": []byte(`{"private":"ENG-47"}`)}
	projection, corpus, err := ConvertActivity(9, records, payloads)
	if err != nil {
		t.Fatal(err)
	}
	if projection.Total != 9 || len(projection.Events) != 2 || projection.Events[0].DeliveryID != "linear-new" || projection.Events[1].DeliveryID != "linear-old" {
		t.Fatalf("projection changed lifetime or retained order: %#v", projection)
	}
	encoded := mustJSON(t, projection)
	if strings.Contains(encoded, "ENG-47") || strings.Contains(encoded, "private") {
		t.Fatalf("projection leaked private body: %s", encoded)
	}

	root := filepath.Join(t.TempDir(), "canonical-events")
	if err := MaterializeActivity(root, projection, corpus); err != nil {
		t.Fatal(err)
	}
	reopened, reopenedCorpus, err := ReadActivity(root)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened, projection) || !reflect.DeepEqual(reopenedCorpus, corpus) {
		t.Fatalf("reopened activity differs: projection=%#v corpus=%#v", reopened, reopenedCorpus)
	}
	for _, path := range []string{
		filepath.Join(root, activityProjectionFile),
		filepath.Join(root, activityPayloadDirectory, projection.Events[0].Payload.File),
	} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("private artifact mode for %s = %v, %v", path, info, err)
		}
	}
}

func TestActivityConversionRejectsMissingOrOrphanedPayloads(t *testing.T) {
	record := ActivitySourceRecord{DeliveryID: "linear-1", PayloadAvailable: true, Event: ActivityEvent{Type: "Issue", Action: "update", ReceivedAt: time.Unix(1, 0).UTC()}}
	for name, payloads := range map[string]map[string][]byte{
		"missing": nil,
		"invalid": {"linear-1": []byte(`{`)},
		"orphan":  {"linear-1": []byte(`{}`), "linear-2": []byte(`{}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := ConvertActivity(1, []ActivitySourceRecord{record}, payloads); err == nil {
				t.Fatal("invalid payload corpus was accepted")
			}
		})
	}
}

func TestReadActivityRejectsTamperingAndOrphans(t *testing.T) {
	for name, mutate := range map[string]func(string, ActivityProjection) error{
		"tampered body": func(root string, projection ActivityProjection) error {
			return os.WriteFile(filepath.Join(root, activityPayloadDirectory, projection.Events[0].Payload.File), []byte(`{"changed":true}`), 0o600)
		},
		"orphan body": func(root string, _ ActivityProjection) error {
			return os.WriteFile(filepath.Join(root, activityPayloadDirectory, strings.Repeat("a", 64)+".json"), []byte(`{}`), 0o600)
		},
		"unsafe mode": func(root string, projection ActivityProjection) error {
			return os.Chmod(filepath.Join(root, activityPayloadDirectory, projection.Events[0].Payload.File), 0o644)
		},
	} {
		t.Run(name, func(t *testing.T) {
			record := ActivitySourceRecord{DeliveryID: "linear-1", PayloadAvailable: true, Event: ActivityEvent{Type: "Issue", Action: "update", ReceivedAt: time.Unix(1, 0).UTC()}}
			projection, corpus, err := ConvertActivity(1, []ActivitySourceRecord{record}, map[string][]byte{"linear-1": []byte(`{"n":1}`)})
			if err != nil {
				t.Fatal(err)
			}
			root := filepath.Join(t.TempDir(), "events")
			if err := MaterializeActivity(root, projection, corpus); err != nil {
				t.Fatal(err)
			}
			if err := mutate(root, projection); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ReadActivity(root); err == nil {
				t.Fatal("tampered activity corpus opened")
			}
		})
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
