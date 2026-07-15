package workflow

import (
	"strings"
	"testing"
	"time"
)

func TestDefinitionCanonicalValidationAndDigest(t *testing.T) {
	now := time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC)
	definition := Default(now)
	if err := definition.Validate(); err != nil {
		t.Fatalf("default workflow is invalid: %v", err)
	}
	first, err := Digest(definition)
	if err != nil {
		t.Fatal(err)
	}
	definition.Markdown += "\n"
	second, err := Digest(definition)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("digest did not change with execution content")
	}
	definition.Markdown = strings.ReplaceAll(definition.Markdown, "\n", "\r\n")
	if err := definition.Validate(); err == nil {
		t.Fatal("noncanonical Markdown passed validation")
	}
}

func TestValidateDefinitionsRejectsDuplicateAndAggregateLimit(t *testing.T) {
	now := time.Date(2026, 7, 14, 20, 0, 0, 0, time.UTC)
	definition := Default(now)
	if err := ValidateDefinitions([]Definition{definition, definition}); err == nil {
		t.Fatal("duplicate definitions passed validation")
	}
	definitions := make([]Definition, MaxDefinitions)
	for i := range definitions {
		definitions[i] = definition
		definitions[i].ID = "workflow-" + string(rune('a'+i))
		definitions[i].Markdown = strings.Repeat("x", MaxMarkdownBytes)
	}
	if err := ValidateDefinitions(definitions); err == nil {
		t.Fatal("aggregate Markdown limit passed validation")
	}
}
