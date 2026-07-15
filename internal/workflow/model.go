package workflow

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	DefaultID             = "full-sdlc"
	ProviderNeutralID     = "full-sdlc-provider-neutral"
	MaxDefinitions        = 8
	MaxNameBytes          = 80
	MaxMarkdownBytes      = 128 << 10
	MaxAggregateMarkdown  = 768 << 10
	MaxAuthoringBodyBytes = 1 << 20
)

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

type Definition struct {
	ID        string    `json:"id"`
	Revision  uint64    `json:"revision"`
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	Markdown  string    `json:"markdown"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

type Summary struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
}

type Draft struct {
	WorkflowID           string    `json:"workflowId"`
	Revision             uint64    `json:"revision"`
	BaseWorkflowRevision uint64    `json:"baseWorkflowRevision"`
	Name                 string    `json:"name"`
	Enabled              bool      `json:"enabled"`
	Markdown             string    `json:"markdown"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

type LegacyDefinition struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Runner  string   `json:"runner"`
	Steps   []string `json:"steps"`
}

func (d Definition) Clone() Definition { return d }

func (d Definition) Summary() Summary {
	return Summary{ID: d.ID, Revision: d.Revision, Name: d.Name, Enabled: d.Enabled}
}

func (d Definition) Validate() error {
	if !ValidID(d.ID) {
		return errors.New("workflow ID is invalid")
	}
	if d.Revision == 0 {
		return fmt.Errorf("workflow %q revision is required", d.ID)
	}
	if !validText(d.Name, MaxNameBytes) {
		return fmt.Errorf("workflow %q has an invalid name", d.ID)
	}
	if d.Markdown != CanonicalizeMarkdown(d.Markdown) {
		return fmt.Errorf("workflow %q Markdown is not canonical", d.ID)
	}
	if err := ValidateMarkdown(d.Markdown); err != nil {
		return fmt.Errorf("workflow %q: %w", d.ID, err)
	}
	return nil
}

func (d Draft) Validate() error {
	if !ValidID(d.WorkflowID) {
		return errors.New("workflow draft ID is invalid")
	}
	if d.Revision == 0 {
		return fmt.Errorf("workflow draft %q revision is required", d.WorkflowID)
	}
	if !validText(d.Name, MaxNameBytes) {
		return fmt.Errorf("workflow draft %q has an invalid name", d.WorkflowID)
	}
	if d.Markdown != CanonicalizeMarkdown(d.Markdown) {
		return fmt.Errorf("workflow draft %q Markdown is not canonical", d.WorkflowID)
	}
	if err := ValidateMarkdown(d.Markdown); err != nil {
		return fmt.Errorf("workflow draft %q: %w", d.WorkflowID, err)
	}
	if d.UpdatedAt.IsZero() {
		return fmt.Errorf("workflow draft %q update time is required", d.WorkflowID)
	}
	return nil
}

func (d Draft) Definition(revision uint64, now time.Time) Definition {
	return Definition{
		ID:        d.WorkflowID,
		Revision:  revision,
		Name:      d.Name,
		Enabled:   d.Enabled,
		Markdown:  CanonicalizeMarkdown(d.Markdown),
		UpdatedAt: now.UTC(),
	}
}

func (d LegacyDefinition) Validate() error {
	if !ValidID(d.ID) || !validText(d.Name, MaxNameBytes) {
		return errors.New("legacy workflow identity is invalid")
	}
	if d.Runner != "do" {
		return fmt.Errorf("legacy workflow %q runner must be do", d.ID)
	}
	if len(d.Steps) == 0 || len(d.Steps) > 20 {
		return fmt.Errorf("legacy workflow %q must have between 1 and 20 steps", d.ID)
	}
	for i, step := range d.Steps {
		if !validText(step, 240) {
			return fmt.Errorf("legacy workflow %q step %d is invalid", d.ID, i+1)
		}
	}
	return nil
}

func ValidID(id string) bool { return identifierPattern.MatchString(id) }

func CanonicalizeMarkdown(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func ValidateMarkdown(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("Markdown must be valid UTF-8")
	}
	if len(value) > MaxMarkdownBytes {
		return fmt.Errorf("Markdown exceeds %d bytes", MaxMarkdownBytes)
	}
	if strings.TrimSpace(value) == "" {
		return errors.New("Markdown cannot be blank")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return errors.New("Markdown cannot contain NUL")
	}
	return nil
}

func ValidateDefinitions(definitions []Definition) error {
	if len(definitions) == 0 || len(definitions) > MaxDefinitions {
		return fmt.Errorf("workflow count must be between 1 and %d", MaxDefinitions)
	}
	seen := make(map[string]bool, len(definitions))
	total := 0
	for i, definition := range definitions {
		if err := definition.Validate(); err != nil {
			return fmt.Errorf("workflow %d: %w", i+1, err)
		}
		if seen[definition.ID] {
			return fmt.Errorf("workflow ID %q is duplicated", definition.ID)
		}
		seen[definition.ID] = true
		total += len(definition.Markdown)
	}
	if total > MaxAggregateMarkdown {
		return fmt.Errorf("published workflow Markdown exceeds %d bytes", MaxAggregateMarkdown)
	}
	return nil
}

func Digest(definition Definition) (string, error) {
	canonical := struct {
		ID       string `json:"id"`
		Revision uint64 `json:"revision"`
		Name     string `json:"name"`
		Enabled  bool   `json:"enabled"`
		Markdown string `json:"markdown"`
	}{
		ID:       definition.ID,
		Revision: definition.Revision,
		Name:     definition.Name,
		Enabled:  definition.Enabled,
		Markdown: CanonicalizeMarkdown(definition.Markdown),
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("workflow digest: encode: %w", err)
	}
	return digestBytes(data), nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func PublishedEqual(left, right Definition) bool {
	return left.ID == right.ID && left.Name == right.Name && left.Enabled == right.Enabled &&
		CanonicalizeMarkdown(left.Markdown) == CanonicalizeMarkdown(right.Markdown)
}

func validText(value string, maximum int) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
