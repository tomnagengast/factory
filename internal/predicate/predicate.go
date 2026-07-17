// Package predicate evaluates named evidence without performing the work that
// collects it. Callers remain responsible for I/O, mutation, retry policy, and
// error classification.
package predicate

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
)

var (
	atomPattern    = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)
	profilePattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

// Atom is the stable identity of one indivisible evidence claim.
type Atom string

func (a Atom) Validate() error {
	if !atomPattern.MatchString(string(a)) {
		return fmt.Errorf("predicate: invalid atom %q", a)
	}
	return nil
}

// Parameters supplies comparison context without coupling predicates to a
// caller's lifecycle structs.
type Parameters map[string]string

func (p Parameters) Clone() Parameters {
	return maps.Clone(p)
}

func (p Parameters) validate() error {
	for key := range p {
		if strings.TrimSpace(key) == "" {
			return errors.New("predicate: parameter names must not be empty")
		}
	}
	return nil
}

// Fact records the result of evaluating one atom.
type Fact struct {
	Atom       Atom       `json:"atom"`
	Parameters Parameters `json:"parameters,omitempty"`
	Passed     bool       `json:"passed"`
	Failure    string     `json:"failure,omitempty"`
}

func (f Fact) clone() Fact {
	f.Parameters = f.Parameters.Clone()
	return f
}

// Source resolves already-collected evidence for one atom. Implementations
// must return the requested atom and parameters exactly.
type Source interface {
	Evaluate(context.Context, Atom, Parameters) (Fact, error)
}

type SourceFunc func(context.Context, Atom, Parameters) (Fact, error)

func (f SourceFunc) Evaluate(ctx context.Context, atom Atom, parameters Parameters) (Fact, error) {
	return f(ctx, atom, parameters)
}

// Requirement declares an ordered atom and its observable failure text.
type Requirement struct {
	Atom       Atom       `json:"atom"`
	Parameters Parameters `json:"parameters,omitempty"`
	Failure    string     `json:"failure"`
}

func (r Requirement) validate() error {
	if err := r.Atom.Validate(); err != nil {
		return err
	}
	if err := r.Parameters.validate(); err != nil {
		return err
	}
	if r.Failure == "" {
		return fmt.Errorf("predicate: failure text is required for %s", r.Atom)
	}
	return nil
}

type Mode string

const (
	All Mode = "all"
	Any Mode = "any"
)

// Profile is an ordered collection of evidence requirements.
type Profile struct {
	Name         string        `json:"name"`
	Mode         Mode          `json:"mode"`
	Requirements []Requirement `json:"requirements"`
}

func (p Profile) Validate() error {
	if !profilePattern.MatchString(p.Name) {
		return fmt.Errorf("predicate: invalid profile name %q", p.Name)
	}
	if p.Mode != All && p.Mode != Any {
		return fmt.Errorf("predicate: invalid profile mode %q", p.Mode)
	}
	if len(p.Requirements) == 0 {
		return errors.New("predicate: profile requirements are required")
	}
	seen := make(map[string]struct{}, len(p.Requirements))
	for _, requirement := range p.Requirements {
		if err := requirement.validate(); err != nil {
			return err
		}
		key := factKey(requirement.Atom, requirement.Parameters)
		if _, ok := seen[key]; ok {
			return fmt.Errorf("predicate: duplicate requirement %s", requirement.Atom)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// Evaluation retains the ordered facts and failures that determined a
// profile's result.
type Evaluation struct {
	Profile  string   `json:"profile"`
	Passed   bool     `json:"passed"`
	Facts    []Fact   `json:"facts"`
	Failures []string `json:"failures,omitempty"`
}

func Evaluate(ctx context.Context, profile Profile, source Source) (Evaluation, error) {
	if err := profile.Validate(); err != nil {
		return Evaluation{}, err
	}
	if source == nil {
		return Evaluation{}, errors.New("predicate: evidence source is required")
	}

	result := Evaluation{Profile: profile.Name}
	for _, requirement := range profile.Requirements {
		if err := ctx.Err(); err != nil {
			return Evaluation{}, err
		}
		parameters := requirement.Parameters.Clone()
		fact, err := source.Evaluate(ctx, requirement.Atom, parameters)
		if err != nil {
			return Evaluation{}, fmt.Errorf("predicate: evaluate %s: %w", requirement.Atom, err)
		}
		if fact.Atom != requirement.Atom || !maps.Equal(fact.Parameters, requirement.Parameters) {
			return Evaluation{}, fmt.Errorf("predicate: evidence source returned mismatched fact for %s", requirement.Atom)
		}
		fact = fact.clone()
		if fact.Passed {
			fact.Failure = ""
		} else {
			fact.Failure = requirement.Failure
			result.Failures = append(result.Failures, requirement.Failure)
		}
		result.Facts = append(result.Facts, fact)

		if profile.Mode == Any && fact.Passed {
			result.Passed = true
			result.Failures = nil
			return result, nil
		}
	}
	if profile.Mode == All {
		result.Passed = len(result.Failures) == 0
	}
	return result, nil
}

// StaticSource makes a validated set of facts available to an evaluator. It
// is useful for adapters and recorded parity tests that already hold evidence
// in memory.
type StaticSource struct {
	facts map[string]Fact
}

func NewStaticSource(facts []Fact) (*StaticSource, error) {
	source := &StaticSource{facts: make(map[string]Fact, len(facts))}
	for _, fact := range facts {
		if err := fact.Atom.Validate(); err != nil {
			return nil, err
		}
		if err := fact.Parameters.validate(); err != nil {
			return nil, err
		}
		key := factKey(fact.Atom, fact.Parameters)
		if _, ok := source.facts[key]; ok {
			return nil, fmt.Errorf("predicate: duplicate fact %s", fact.Atom)
		}
		source.facts[key] = fact.clone()
	}
	return source, nil
}

func (s *StaticSource) Evaluate(_ context.Context, atom Atom, parameters Parameters) (Fact, error) {
	if s == nil {
		return Fact{}, errors.New("static predicate source is nil")
	}
	fact, ok := s.facts[factKey(atom, parameters)]
	if !ok {
		return Fact{}, fmt.Errorf("missing fact %s", atom)
	}
	return fact.clone(), nil
}

func factKey(atom Atom, parameters Parameters) string {
	keys := slices.Sorted(maps.Keys(parameters))
	var builder strings.Builder
	builder.WriteString(string(atom))
	for _, key := range keys {
		builder.WriteByte(0)
		builder.WriteString(key)
		builder.WriteByte(0)
		builder.WriteString(parameters[key])
	}
	return builder.String()
}
