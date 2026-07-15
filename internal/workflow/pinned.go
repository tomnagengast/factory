package workflow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

type Pinned struct {
	ID        string     `json:"id"`
	Revision  uint64     `json:"revision,omitempty"`
	Name      string     `json:"name,omitempty"`
	Enabled   bool       `json:"enabled,omitempty"`
	Markdown  string     `json:"markdown,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
	Runner    string     `json:"runner,omitempty"`
	Steps     []string   `json:"steps,omitempty"`
}

type PinnedSnapshot struct {
	Workflow Pinned `json:"workflow"`
	Digest   string `json:"digest"`
}

func Pin(definition Definition) Pinned {
	updatedAt := definition.UpdatedAt
	return Pinned{
		ID:        definition.ID,
		Revision:  definition.Revision,
		Name:      definition.Name,
		Enabled:   definition.Enabled,
		Markdown:  definition.Markdown,
		UpdatedAt: &updatedAt,
	}
}

func PinLegacy(definition LegacyDefinition) Pinned {
	return Pinned{
		ID: definition.ID, Name: definition.Name, Enabled: definition.Enabled,
		Runner: definition.Runner, Steps: append([]string(nil), definition.Steps...),
	}
}

func (p Pinned) Clone() Pinned {
	p.Steps = append([]string(nil), p.Steps...)
	if p.UpdatedAt != nil {
		updatedAt := *p.UpdatedAt
		p.UpdatedAt = &updatedAt
	}
	return p
}

func (p Pinned) IsLegacy() bool { return p.Revision == 0 && p.Markdown == "" }

func (p Pinned) Validate() error {
	if p.IsLegacy() {
		return p.LegacyDefinition().Validate()
	}
	if p.Runner != "" || len(p.Steps) != 0 {
		return errors.New("workflow pin mixes legacy and Markdown fields")
	}
	return p.Definition().Validate()
}

func (p Pinned) Definition() Definition {
	var updatedAt time.Time
	if p.UpdatedAt != nil {
		updatedAt = *p.UpdatedAt
	}
	return Definition{
		ID: p.ID, Revision: p.Revision, Name: p.Name, Enabled: p.Enabled,
		Markdown: p.Markdown, UpdatedAt: updatedAt,
	}
}

func (p Pinned) LegacyDefinition() LegacyDefinition {
	return LegacyDefinition{
		ID: p.ID, Name: p.Name, Enabled: p.Enabled, Runner: p.Runner,
		Steps: append([]string(nil), p.Steps...),
	}
}

func (p Pinned) Digest() (string, error) {
	if p.IsLegacy() {
		data, err := json.Marshal(p.LegacyDefinition())
		if err != nil {
			return "", fmt.Errorf("legacy workflow digest: encode: %w", err)
		}
		return digestBytes(data), nil
	}
	return Digest(p.Definition())
}

func (p Pinned) Complete() bool {
	if p.Name == "" || !p.Enabled {
		return false
	}
	if p.IsLegacy() {
		return p.Runner != "" && len(p.Steps) != 0
	}
	return p.Revision > 0 && p.Markdown != ""
}

func (p Pinned) Compact() Pinned {
	return Pinned{ID: p.ID, Revision: p.Revision}
}

func EncodePinnedSnapshot(pin Pinned, digest string) PinnedSnapshot {
	return PinnedSnapshot{Workflow: pin.Clone(), Digest: digest}
}

func DecodePinnedSnapshot(data []byte) (PinnedSnapshot, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot PinnedSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return PinnedSnapshot{}, fmt.Errorf("decode pinned workflow: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return PinnedSnapshot{}, errors.New("decode pinned workflow: trailing content")
	}
	if err := snapshot.Workflow.Validate(); err != nil || !snapshot.Workflow.Enabled {
		return PinnedSnapshot{}, errors.New("pinned workflow is invalid")
	}
	digest, err := snapshot.Workflow.Digest()
	if err != nil {
		return PinnedSnapshot{}, err
	}
	if snapshot.Digest == "" || snapshot.Digest != digest {
		return PinnedSnapshot{}, errors.New("pinned workflow digest mismatch")
	}
	return snapshot, nil
}
