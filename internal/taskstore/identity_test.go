package taskstore

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	linearUUIDOne = "11111111-1111-4111-8111-111111111111"
	linearUUIDTwo = "22222222-2222-4222-8222-222222222222"
)

func TestTaskStoreOwnsLinearIdentityBijection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tasks.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if added, err := store.BindLinearIdentity("eng-47", strings.ToUpper(linearUUIDOne)); err != nil || !added {
		t.Fatalf("bind = %t, %v", added, err)
	}
	if added, err := store.BindLinearIdentity("ENG-47", linearUUIDOne); err != nil || added {
		t.Fatalf("replay = %t, %v", added, err)
	}
	if got, found := store.LinearUUID(" eng-47 "); !found || got != linearUUIDOne {
		t.Fatalf("UUID lookup = %q, %t", got, found)
	}
	if got, found := store.LinearIdentifier(strings.ToUpper(linearUUIDOne)); !found || got != "ENG-47" {
		t.Fatalf("identifier lookup = %q, %t", got, found)
	}
	for _, pair := range [][2]string{{"ENG-47", linearUUIDTwo}, {"ENG-48", linearUUIDOne}} {
		if _, err := store.BindLinearIdentity(pair[0], pair[1]); !errors.Is(err, ErrLinearIdentityConflict) {
			t.Fatalf("BindLinearIdentity(%q, %q) = %v", pair[0], pair[1], err)
		}
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.LinearBindings(); !reflect.DeepEqual(got, []LinearBinding{{Identifier: "ENG-47", UUID: linearUUIDOne}}) {
		t.Fatalf("reopened bindings = %#v", got)
	}
	if err := reopened.Compact(); err != nil {
		t.Fatal(err)
	}
	compacted, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := compacted.LinearBindings(); !reflect.DeepEqual(got, reopened.LinearBindings()) {
		t.Fatalf("compacted bindings = %#v", got)
	}
}

func TestConvertLinearBindingsIsCompleteCanonicalAndPure(t *testing.T) {
	source := Snapshot{Schema: SchemaVersion, NextSequence: 1}
	converted, err := ConvertLinearBindings(source, []LinearBinding{
		{Identifier: "eng-48", UUID: strings.ToUpper(linearUUIDTwo)},
		{Identifier: "ENG-47", UUID: linearUUIDOne},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []LinearBinding{{Identifier: "ENG-47", UUID: linearUUIDOne}, {Identifier: "ENG-48", UUID: linearUUIDTwo}}
	if !reflect.DeepEqual(converted.LinearBindings, want) {
		t.Fatalf("converted bindings = %#v, want %#v", converted.LinearBindings, want)
	}
	if len(source.LinearBindings) != 0 {
		t.Fatalf("source was mutated: %#v", source)
	}
	for name, bindings := range map[string][]LinearBinding{
		"duplicate identifier": {{Identifier: "ENG-47", UUID: linearUUIDOne}, {Identifier: "eng-47", UUID: linearUUIDTwo}},
		"duplicate UUID":       {{Identifier: "ENG-47", UUID: linearUUIDOne}, {Identifier: "ENG-48", UUID: linearUUIDOne}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ConvertLinearBindings(source, bindings); err == nil {
				t.Fatal("conflicting conversion was accepted")
			}
		})
	}
	if _, err := ConvertLinearBindings(converted, nil); err == nil {
		t.Fatal("second canonical conversion was accepted")
	}
}

func TestTaskStoreRejectsTamperedLinearIdentityEvidence(t *testing.T) {
	for name, mutate := range map[string]func(string) string{
		"duplicate identifier": func(line string) string {
			return strings.Replace(line, `"linearBindings":[`, `"linearBindings":[{"identifier":"ENG-47","uuid":"`+linearUUIDTwo+`"},`, 1)
		},
		"changed case": func(line string) string { return strings.Replace(line, "ENG-47", "eng-47", 1) },
		"invalid UUID": func(line string) string { return strings.Replace(line, linearUUIDOne, "not-a-uuid", 1) },
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tasks.jsonl")
			store, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.BindLinearIdentity("ENG-47", linearUUIDOne); err != nil {
				t.Fatal(err)
			}
			if err := store.Compact(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(mutate(string(data))), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(path); err == nil {
				t.Fatal("tampered checkpoint opened")
			}
		})
	}
}
