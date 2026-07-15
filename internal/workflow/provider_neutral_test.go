package workflow

import (
	"strings"
	"testing"
	"time"
)

func TestProviderNeutralDefaultIsStableAndScoped(t *testing.T) {
	definition := ProviderNeutralDefault(time.Now())
	if err := definition.Validate(); err != nil {
		t.Fatal(err)
	}
	digest, err := Digest(definition)
	if err != nil {
		t.Fatal(err)
	}
	if definition.ID != ProviderNeutralID || digest != ProviderNeutralDigest() {
		t.Fatalf("provider-neutral definition = %#v digest=%s", definition, digest)
	}
	for _, required := range []string{"factory agent task show", "factory agent task gate open", "Create a merge commit", "exact verified-head"} {
		if !strings.Contains(definition.Markdown, required) {
			t.Fatalf("provider-neutral workflow is missing %q", required)
		}
	}
}
