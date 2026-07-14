package triggerregistry

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/settings"
)

const (
	SchemaVersion         = 1
	MaxRules              = 32
	MaxSchedules          = 32
	DefaultMaxHop         = 4
	MaximumMaxHop         = 8
	DefaultMaxOutstanding = 10
	MaximumMaxOutstanding = 100
	DefaultAdmissionsHour = 120
	MaximumAdmissionsHour = 10_000
	AttributeActorID      = eventwire.AttributeActorID
	AttributeProvenance   = eventwire.AttributeProvenance
	AttributeProducer     = eventwire.AttributeProducer
	AttributeAddedLabel   = "addedLabelName"
	AttributeIssue        = "issueIdentifier"
	AttributeScheduleID   = "scheduleId"
	AttributeScheduleRev  = "scheduleRevision"
	AttributeScheduledAt  = "scheduledAt"
	TargetFixedIssue      = "fixed"
	TargetEventSubject    = "subject"
	TargetEventAttribute  = "attribute"
	maxNameBytes          = 80
	maxFieldBytes         = 256
	maxAttributes         = 32
	maxCronBytes          = 128
)

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

var scheduleParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

var reservedScheduleAttributes = map[string]bool{
	AttributeScheduleID:  true,
	AttributeScheduleRev: true,
	AttributeScheduledAt: true,
	AttributeProducer:    true,
	AttributeProvenance:  true,
}

type Snapshot struct {
	Schema                     int        `json:"schema"`
	Revision                   uint64     `json:"revision"`
	UpdatedAt                  time.Time  `json:"updatedAt,omitempty"`
	LegacyRollbackIncompatible bool       `json:"legacyRollbackIncompatible,omitempty"`
	Rules                      []Rule     `json:"rules"`
	Schedules                  []Schedule `json:"schedules"`
}

type Rule struct {
	ID             string       `json:"id"`
	Revision       uint64       `json:"revision"`
	Name           string       `json:"name"`
	Enabled        bool         `json:"enabled"`
	Filter         Filter       `json:"filter"`
	WorkflowID     string       `json:"workflowId"`
	Target         TargetPolicy `json:"target"`
	MaxHop         int          `json:"maxHop"`
	MaxOutstanding int          `json:"maxOutstanding"`
	AdmissionsHour int          `json:"admissionsPerHour"`
}

type Filter struct {
	Source     eventwire.Source  `json:"source,omitempty"`
	Type       string            `json:"type,omitempty"`
	Action     string            `json:"action,omitempty"`
	Subject    *string           `json:"subject,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

type TargetPolicy struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
}

type Schedule struct {
	ID         string              `json:"id"`
	Revision   uint64              `json:"revision"`
	Name       string              `json:"name"`
	Enabled    bool                `json:"enabled"`
	Cron       string              `json:"cron"`
	Timezone   string              `json:"timezone"`
	Subject    string              `json:"subject,omitempty"`
	Attributes map[string][]string `json:"attributes,omitempty"`
}

func (s Snapshot) Clone() Snapshot {
	clone := s
	clone.Rules = make([]Rule, len(s.Rules))
	for i, rule := range s.Rules {
		clone.Rules[i] = rule.Clone()
	}
	clone.Schedules = make([]Schedule, len(s.Schedules))
	for i, schedule := range s.Schedules {
		clone.Schedules[i] = schedule.Clone()
	}
	return clone
}

func (s Snapshot) Rule(id string) (Rule, bool) {
	for _, rule := range s.Rules {
		if rule.ID == id {
			return rule.Clone(), true
		}
	}
	return Rule{}, false
}

func (s Snapshot) Validate(configuration settings.Snapshot) error {
	if s.Schema != SchemaVersion {
		return fmt.Errorf("trigger registry: schema is %d, want %d", s.Schema, SchemaVersion)
	}
	if len(s.Rules) > MaxRules {
		return fmt.Errorf("trigger registry: rule count exceeds %d", MaxRules)
	}
	if len(s.Schedules) > MaxSchedules {
		return fmt.Errorf("trigger registry: schedule count exceeds %d", MaxSchedules)
	}
	workflowByID := make(map[string]settings.Workflow, len(configuration.Workflows))
	for _, workflow := range configuration.Workflows {
		workflowByID[workflow.ID] = workflow
	}
	ids := make(map[string]bool, len(s.Rules)+len(s.Schedules))
	for i, rule := range s.Rules {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("trigger registry: rule %d: %w", i+1, err)
		}
		if ids[rule.ID] {
			return fmt.Errorf("trigger registry: ID %q is duplicated", rule.ID)
		}
		ids[rule.ID] = true
		workflow, found := workflowByID[rule.WorkflowID]
		if rule.Enabled && (!found || !workflow.Enabled) {
			return fmt.Errorf("trigger registry: rule %q workflow %q is unavailable", rule.ID, rule.WorkflowID)
		}
	}
	for i, schedule := range s.Schedules {
		if err := schedule.Validate(); err != nil {
			return fmt.Errorf("trigger registry: schedule %d: %w", i+1, err)
		}
		if ids[schedule.ID] {
			return fmt.Errorf("trigger registry: ID %q is duplicated", schedule.ID)
		}
		ids[schedule.ID] = true
	}
	return nil
}

func (r Rule) Clone() Rule {
	clone := r
	clone.Filter = r.Filter.Clone()
	return clone
}

func (r Rule) Validate() error {
	if !identifierPattern.MatchString(r.ID) {
		return errors.New("rule ID is invalid")
	}
	if r.Revision == 0 {
		return errors.New("rule revision is required")
	}
	if !validText(r.Name, maxNameBytes) {
		return errors.New("rule name is invalid")
	}
	if err := r.Filter.Validate(); err != nil {
		return err
	}
	if !identifierPattern.MatchString(r.WorkflowID) {
		return errors.New("workflow ID is invalid")
	}
	if err := r.Target.Validate(); err != nil {
		return err
	}
	if r.MaxHop < 1 || r.MaxHop > MaximumMaxHop {
		return fmt.Errorf("max hop must be between 1 and %d", MaximumMaxHop)
	}
	if r.MaxOutstanding < 1 || r.MaxOutstanding > MaximumMaxOutstanding {
		return fmt.Errorf("max outstanding must be between 1 and %d", MaximumMaxOutstanding)
	}
	if r.AdmissionsHour < 1 || r.AdmissionsHour > MaximumAdmissionsHour {
		return fmt.Errorf("admissions per hour must be between 1 and %d", MaximumAdmissionsHour)
	}
	return nil
}

func (r Rule) SemanticEqual(other Rule) bool {
	return r.Enabled == other.Enabled &&
		r.WorkflowID == other.WorkflowID &&
		r.Target == other.Target &&
		r.MaxHop == other.MaxHop &&
		r.MaxOutstanding == other.MaxOutstanding &&
		r.AdmissionsHour == other.AdmissionsHour &&
		r.Filter.Equal(other.Filter)
}

func (f Filter) Clone() Filter {
	clone := f
	if f.Subject != nil {
		subject := *f.Subject
		clone.Subject = &subject
	}
	clone.Attributes = make(map[string]string, len(f.Attributes))
	for key, value := range f.Attributes {
		clone.Attributes[key] = value
	}
	return clone
}

func (f Filter) Validate() error {
	if f.Source != "" && !eventwire.ValidSource(f.Source) {
		return errors.New("filter source is invalid")
	}
	for name, value := range map[string]string{"type": f.Type, "action": f.Action} {
		if value != "" && !validField(value, false) {
			return fmt.Errorf("filter %s is invalid", name)
		}
	}
	if f.Subject != nil && !validField(*f.Subject, true) {
		return errors.New("filter subject is invalid")
	}
	if len(f.Attributes) > maxAttributes {
		return errors.New("filter has too many attributes")
	}
	for key, value := range f.Attributes {
		if !validField(key, false) || len(value) > maxFieldBytes {
			return errors.New("filter attributes are invalid")
		}
	}
	return nil
}

func (f Filter) Matches(event eventwire.Event) bool {
	if f.Source != "" && f.Source != event.Source {
		return false
	}
	if f.Type != "" && f.Type != event.Type {
		return false
	}
	if f.Action != "" && f.Action != event.Action {
		return false
	}
	if f.Subject != nil && *f.Subject != event.Subject {
		return false
	}
	for key, value := range f.Attributes {
		if !event.Has(key, value) {
			return false
		}
	}
	return true
}

func (f Filter) Equal(other Filter) bool {
	if f.Source != other.Source || f.Type != other.Type || f.Action != other.Action {
		return false
	}
	if (f.Subject == nil) != (other.Subject == nil) || (f.Subject != nil && *f.Subject != *other.Subject) {
		return false
	}
	if len(f.Attributes) != len(other.Attributes) {
		return false
	}
	for key, value := range f.Attributes {
		if other.Attributes[key] != value {
			return false
		}
	}
	return true
}

func (t TargetPolicy) Validate() error {
	switch t.Kind {
	case TargetFixedIssue:
		if !agentrun.ValidIssueIdentifier(strings.ToUpper(t.Value)) || t.Value != strings.ToUpper(t.Value) {
			return errors.New("fixed target must be a canonical Linear issue identifier")
		}
	case TargetEventSubject:
		if t.Value != "" {
			return errors.New("subject target cannot have a value")
		}
	case TargetEventAttribute:
		if !validField(t.Value, false) {
			return errors.New("attribute target key is invalid")
		}
	default:
		return errors.New("target kind is invalid")
	}
	return nil
}

func (s Schedule) Clone() Schedule {
	clone := s
	clone.Attributes = make(map[string][]string, len(s.Attributes))
	for key, values := range s.Attributes {
		clone.Attributes[key] = slices.Clone(values)
	}
	return clone
}

func (s Schedule) Validate() error {
	if !identifierPattern.MatchString(s.ID) {
		return errors.New("schedule ID is invalid")
	}
	if s.Revision == 0 {
		return errors.New("schedule revision is required")
	}
	if !validText(s.Name, maxNameBytes) {
		return errors.New("schedule name is invalid")
	}
	if !validCronShape(s.Cron) {
		return errors.New("schedule cron must be five fields without descriptors or timezone directives")
	}
	if _, err := scheduleParser.Parse(s.Cron); err != nil {
		return errors.New("schedule cron is invalid")
	}
	if s.Timezone == "" || s.Timezone != strings.TrimSpace(s.Timezone) {
		return errors.New("schedule timezone is invalid")
	}
	if _, err := time.LoadLocation(s.Timezone); err != nil {
		return errors.New("schedule timezone is invalid")
	}
	if !validField(s.Subject, true) {
		return errors.New("schedule subject is invalid")
	}
	if len(s.Attributes) > maxAttributes {
		return errors.New("schedule has too many attributes")
	}
	for key, values := range s.Attributes {
		if reservedScheduleAttributes[key] || !validField(key, false) || len(values) > maxAttributes {
			return errors.New("schedule attributes are invalid")
		}
		seen := make(map[string]bool, len(values))
		for _, value := range values {
			if len(value) > maxFieldBytes {
				return errors.New("schedule attribute value is invalid")
			}
			if seen[value] {
				return errors.New("schedule attribute values contain duplicates")
			}
			seen[value] = true
		}
	}
	return nil
}

func (s Schedule) SemanticEqual(other Schedule) bool {
	if s.Enabled != other.Enabled || s.Cron != other.Cron || s.Timezone != other.Timezone || s.Subject != other.Subject || len(s.Attributes) != len(other.Attributes) {
		return false
	}
	for key, values := range s.Attributes {
		if !slices.Equal(values, other.Attributes[key]) {
			return false
		}
	}
	return true
}

func Sort(snapshot *Snapshot) {
	sort.Slice(snapshot.Rules, func(i, j int) bool { return snapshot.Rules[i].ID < snapshot.Rules[j].ID })
	sort.Slice(snapshot.Schedules, func(i, j int) bool { return snapshot.Schedules[i].ID < snapshot.Schedules[j].ID })
	for index := range snapshot.Schedules {
		for key := range snapshot.Schedules[index].Attributes {
			sort.Strings(snapshot.Schedules[index].Attributes[key])
		}
	}
}

func CanonicalFold(value string) string {
	value = strings.TrimSpace(value)
	var builder strings.Builder
	builder.Grow(len(value))
	for _, current := range value {
		canonical := current
		for next := unicode.SimpleFold(current); next != current; next = unicode.SimpleFold(next) {
			if next < canonical {
				canonical = next
			}
		}
		builder.WriteRune(canonical)
	}
	return builder.String()
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

func validField(value string, emptyAllowed bool) bool {
	if value == "" {
		return emptyAllowed
	}
	return len(value) <= maxFieldBytes && value == strings.TrimSpace(value) && utf8.ValidString(value)
}

func validCronShape(expression string) bool {
	if expression == "" || expression != strings.TrimSpace(expression) || len(expression) > maxCronBytes || strings.HasPrefix(expression, "@") || strings.Contains(expression, "CRON_TZ=") || strings.Contains(expression, "TZ=") {
		return false
	}
	return len(strings.Fields(expression)) == 5
}
