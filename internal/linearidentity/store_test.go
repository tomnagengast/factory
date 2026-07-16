package linearidentity

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

const (
	firstUUID  = "11111111-1111-4111-8111-111111111111"
	secondUUID = "22222222-2222-4222-8222-222222222222"
)

func TestStorePersistsBijectionAndExactReplay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "linear-task-identities.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if created, err := store.Bind("eng-46", firstUUID); err != nil || !created {
		t.Fatalf("Bind() = %t, %v", created, err)
	}
	if created, err := store.Bind("ENG-46", firstUUID); err != nil || created {
		t.Fatalf("replay Bind() = %t, %v", created, err)
	}
	if _, err := Open(path); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, err = %v", info.Mode().Perm(), err)
	}
}

func TestStoreRejectsBothConflictDirections(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "linear-task-identities.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Bind("ENG-46", firstUUID); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{{"ENG-46", secondUUID}, {"ENG-47", firstUUID}} {
		if _, err := store.Bind(pair[0], pair[1]); !errors.Is(err, ErrConflict) {
			t.Fatalf("Bind(%q, %q) = %v, want conflict", pair[0], pair[1], err)
		}
	}
}

func TestOpenRejectsCorruptDuplicateOrInsecureSnapshot(t *testing.T) {
	for name, data := range map[string]struct {
		data string
		mode os.FileMode
	}{
		"corrupt":   {data: `{`, mode: 0o600},
		"duplicate": {data: `{"version":1,"bindings":[{"identifier":"ENG-46","uuid":"` + firstUUID + `"},{"identifier":"ENG-46","uuid":"` + firstUUID + `"}]}`, mode: 0o600},
		"insecure":  {data: `{"version":1,"bindings":[]}`, mode: 0o644},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "linear-task-identities.json")
			if err := os.WriteFile(path, []byte(data.data), data.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil {
				t.Fatal("invalid snapshot unexpectedly opened")
			}
		})
	}
}
