package objectstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestMemoryStoreCopiesAndListsObjects(t *testing.T) {
	ctx := context.Background()
	store := NewMemory()
	content := []byte("one")
	if err := store.Put(ctx, "workflows/one.js", content, "application/javascript"); err != nil {
		t.Fatal(err)
	}
	content[0] = 'x'
	if err := store.Put(ctx, "workflows/nested/two.js", []byte("two"), "application/javascript"); err != nil {
		t.Fatal(err)
	}
	keys, err := store.List(ctx, "workflows")
	if err != nil || !reflect.DeepEqual(keys, []string{"workflows/nested/two.js", "workflows/one.js"}) {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	stored, err := store.Get(ctx, "workflows/one.js")
	if err != nil || string(stored) != "one" {
		t.Fatalf("stored = %q, %v", stored, err)
	}
	store.Delete("workflows/one.js")
	if _, err := store.Get(ctx, "workflows/one.js"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error = %v", err)
	}
}

func TestStoreRejectsUnsafeKeys(t *testing.T) {
	store := NewMemory()
	for _, key := range []string{"", ".", "../secret", "workflow/../secret", "workflow\\secret", "workflow\nsecret"} {
		if err := store.Put(context.Background(), key, nil, ""); err == nil {
			t.Fatalf("unsafe key %q was accepted", key)
		}
	}
}
