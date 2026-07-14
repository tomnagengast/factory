package eventwire

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

type Source string

const (
	SourceLinear  Source = "linear"
	SourceGitHub  Source = "github"
	SourceFactory Source = "factory"
)

const (
	maxIDLength        = 256
	maxFieldLength     = 256
	maxAttributeCount  = 32
	maxAttributeValues = 32
	maxAncestorRules   = 32
)

var tokenPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

type Event struct {
	ID                 string              `json:"id"`
	Source             Source              `json:"source"`
	Type               string              `json:"type"`
	Action             string              `json:"action"`
	Subject            string              `json:"subject,omitempty"`
	Attributes         map[string][]string `json:"attributes,omitempty"`
	Channels           []string            `json:"channels,omitempty"`
	RootEventID        string              `json:"rootEventId,omitempty"`
	ParentInvocationID string              `json:"parentInvocationId,omitempty"`
	ParentRunID        string              `json:"parentRunId,omitempty"`
	Hop                int                 `json:"hop,omitempty"`
	AncestorRuleIDs    []string            `json:"ancestorRuleIds,omitempty"`
	ReceivedAt         time.Time           `json:"receivedAt"`
}

func (e Event) Validate() error {
	if strings.TrimSpace(e.ID) == "" || len(e.ID) > maxIDLength {
		return errors.New("event wire: event ID is invalid")
	}
	if !ValidSource(e.Source) {
		return fmt.Errorf("event wire: invalid source %q", e.Source)
	}
	for name, value := range map[string]string{
		"type":    e.Type,
		"action":  e.Action,
		"subject": e.Subject,
	} {
		if (name != "subject" && strings.TrimSpace(value) == "") || len(value) > maxFieldLength {
			return fmt.Errorf("event wire: %s is invalid", name)
		}
	}
	if e.ReceivedAt.IsZero() {
		return errors.New("event wire: received time is required")
	}
	if err := e.validateCausation(); err != nil {
		return err
	}
	if len(e.Attributes) > maxAttributeCount {
		return errors.New("event wire: too many attributes")
	}
	for key, values := range e.Attributes {
		if strings.TrimSpace(key) == "" || len(key) > maxFieldLength || len(values) > maxAttributeValues {
			return errors.New("event wire: invalid attributes")
		}
		for _, value := range values {
			if len(value) > maxFieldLength {
				return errors.New("event wire: invalid attribute value")
			}
		}
	}
	seenChannels := make(map[string]bool, len(e.Channels))
	for _, channel := range e.Channels {
		if strings.TrimSpace(channel) == "" || len(channel) > maxFieldLength || seenChannels[channel] {
			return errors.New("event wire: invalid channels")
		}
		seenChannels[channel] = true
	}
	return nil
}

func ValidSource(source Source) bool {
	return tokenPattern.MatchString(string(source))
}

func (e Event) validateCausation() error {
	if e.RootEventID != "" && (strings.TrimSpace(e.RootEventID) != e.RootEventID || len(e.RootEventID) > maxIDLength) {
		return errors.New("event wire: root event ID is invalid")
	}
	for _, value := range []string{e.ParentInvocationID, e.ParentRunID} {
		if value != "" && (strings.TrimSpace(value) != value || len(value) > maxIDLength) {
			return errors.New("event wire: parent identity is invalid")
		}
	}
	if e.Hop < 0 || e.Hop > maxAncestorRules || len(e.AncestorRuleIDs) != e.Hop {
		return errors.New("event wire: causal hop is invalid")
	}
	seen := make(map[string]bool, len(e.AncestorRuleIDs))
	for _, id := range e.AncestorRuleIDs {
		if !tokenPattern.MatchString(id) || seen[id] {
			return errors.New("event wire: ancestor rule path is invalid")
		}
		seen[id] = true
	}
	if e.Hop == 0 {
		if e.RootEventID != "" && e.RootEventID != e.ID {
			return errors.New("event wire: direct event root is invalid")
		}
		if e.ParentInvocationID != "" || e.ParentRunID != "" {
			return errors.New("event wire: direct event has parent identity")
		}
		return nil
	}
	if e.RootEventID == "" || e.ParentInvocationID == "" {
		return errors.New("event wire: derived event causation is incomplete")
	}
	return nil
}

func (e Event) Values(key string) []string {
	return slices.Clone(e.Attributes[key])
}

func (e Event) Has(key, value string) bool {
	return slices.Contains(e.Attributes[key], value)
}

type Filter struct {
	Source     Source
	Type       string
	Action     string
	Subject    string
	Attributes map[string]string
}

func (f Filter) Matches(event Event) bool {
	if f.Source != "" && f.Source != event.Source {
		return false
	}
	if f.Type != "" && f.Type != event.Type {
		return false
	}
	if f.Action != "" && f.Action != event.Action {
		return false
	}
	if f.Subject != "" && f.Subject != event.Subject {
		return false
	}
	for key, value := range f.Attributes {
		if !event.Has(key, value) {
			return false
		}
	}
	return true
}

func canonicalEvent(event Event) Event {
	if event.RootEventID == "" && event.Hop == 0 && event.ParentInvocationID == "" && event.ParentRunID == "" && len(event.AncestorRuleIDs) == 0 {
		event.RootEventID = event.ID
	}
	return event
}
