package taskmodel

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type Source string

const (
	SourceFactory Source = "factory"
	SourceLinear  Source = "linear"
)

var (
	linearIdentifierPattern  = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[1-9][0-9]*$`)
	factoryIdentifierPattern = regexp.MustCompile(`^FAC-[1-9][0-9]*$`)
	factoryProviderIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
)

// TaskRef is the canonical owner of task-scoped Factory state. ProviderID is
// authoritative; Identifier is a human-facing provider label.
type TaskRef struct {
	Source     Source `json:"source"`
	ProviderID string `json:"providerId"`
	Identifier string `json:"identifier"`
}

func (r TaskRef) Normalize() (TaskRef, error) {
	r.Source = Source(strings.ToLower(strings.TrimSpace(string(r.Source))))
	r.ProviderID = strings.TrimSpace(r.ProviderID)
	r.Identifier = strings.TrimSpace(r.Identifier)

	switch r.Source {
	case SourceLinear:
		r.ProviderID = strings.ToUpper(r.ProviderID)
		r.Identifier = strings.ToUpper(r.Identifier)
		if !linearIdentifierPattern.MatchString(r.ProviderID) || !linearIdentifierPattern.MatchString(r.Identifier) {
			return TaskRef{}, errors.New("task reference: invalid Linear identifier")
		}
		if r.ProviderID != r.Identifier {
			return TaskRef{}, errors.New("task reference: Linear provider ID and identifier conflict")
		}
	case SourceFactory:
		if !factoryProviderIDPattern.MatchString(r.ProviderID) {
			return TaskRef{}, errors.New("task reference: invalid Factory provider ID")
		}
		r.Identifier = strings.ToUpper(r.Identifier)
		if !factoryIdentifierPattern.MatchString(r.Identifier) {
			return TaskRef{}, errors.New("task reference: invalid Factory identifier")
		}
	default:
		return TaskRef{}, fmt.Errorf("task reference: invalid source %q", r.Source)
	}
	return r, nil
}

func (r TaskRef) Validate() error {
	normalized, err := r.Normalize()
	if err != nil {
		return err
	}
	if normalized != r {
		return errors.New("task reference: value is not canonical")
	}
	return nil
}

func (r TaskRef) IsZero() bool {
	return r.Source == "" && r.ProviderID == "" && r.Identifier == ""
}

func (r TaskRef) OwnershipKey() string {
	return string(r.Source) + ":" + r.ProviderID
}

func (r TaskRef) Equal(other TaskRef) bool {
	return r.Source == other.Source && r.ProviderID == other.ProviderID
}

// BranchPrefix returns the exact provider-isolated prefix required for every
// branch and pull-request head owned by the task.
func (r TaskRef) BranchPrefix() (string, error) {
	normalized, err := r.Normalize()
	if err != nil {
		return "", err
	}
	if normalized.Source == SourceLinear {
		return strings.ToLower(normalized.Identifier) + "-", nil
	}
	return "factory-" + strings.ToLower(normalized.ProviderID) + "-", nil
}

func LegacyLinear(identifier string) (TaskRef, error) {
	identifier = strings.ToUpper(strings.TrimSpace(identifier))
	return (TaskRef{Source: SourceLinear, ProviderID: identifier, Identifier: identifier}).Normalize()
}

// ResolveCompatibilityIdentity accepts either a current TaskRef, a legacy
// Linear identifier, or both when they describe the same canonical owner.
func ResolveCompatibilityIdentity(current TaskRef, legacyIdentifier string) (TaskRef, error) {
	var resolved TaskRef
	if !current.IsZero() {
		var err error
		resolved, err = current.Normalize()
		if err != nil {
			return TaskRef{}, err
		}
	}
	legacyIdentifier = strings.TrimSpace(legacyIdentifier)
	if legacyIdentifier != "" {
		if !resolved.IsZero() {
			if !strings.EqualFold(resolved.Identifier, legacyIdentifier) {
				return TaskRef{}, errors.New("task reference: current and legacy identities conflict")
			}
		} else {
			legacy, err := LegacyLinear(legacyIdentifier)
			if err != nil {
				return TaskRef{}, err
			}
			resolved = legacy
		}
	}
	if resolved.IsZero() {
		return TaskRef{}, errors.New("task reference: identity is required")
	}
	return resolved, nil
}

func ValidLinearIdentifier(value string) bool {
	return linearIdentifierPattern.MatchString(value)
}
