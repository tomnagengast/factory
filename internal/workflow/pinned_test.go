package workflow

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPinnedSupportsMarkdownAndLegacyShapes(t *testing.T) {
	definition := Default(time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC))
	markdown := Pin(definition)
	if err := markdown.Validate(); err != nil || markdown.IsLegacy() {
		t.Fatalf("Markdown pin = %#v, %v", markdown, err)
	}
	legacy := PinLegacy(LegacyDefinition{
		ID: "full-sdlc", Name: "Full SDLC", Enabled: true, Runner: "do", Steps: []string{"Research"},
	})
	if err := legacy.Validate(); err != nil || !legacy.IsLegacy() {
		t.Fatalf("legacy pin = %#v, %v", legacy, err)
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"id":"full-sdlc","name":"Full SDLC","enabled":true,"runner":"do","steps":["Research"]}` {
		t.Fatalf("legacy JSON changed: %s", data)
	}
}

func TestPinnedSnapshotValidatesDigest(t *testing.T) {
	pin := Pin(Default(time.Time{}))
	digest, err := pin.Digest()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(EncodePinnedSnapshot(pin, digest))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePinnedSnapshot(data); err != nil {
		t.Fatal(err)
	}
	data, _ = json.Marshal(EncodePinnedSnapshot(pin, "wrong"))
	if _, err := DecodePinnedSnapshot(data); err == nil {
		t.Fatal("digest mismatch passed")
	}
}
