package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/taskmodel"
)

const (
	SchemaVersion         = 1
	MaxWorkflows          = 8
	MaxRules              = 32
	MaxSchedules          = 32
	MaxWorkflowNameBytes  = 80
	MaxWorkflowBytes      = 128 << 10
	MaxWorkflowBytesTotal = 768 << 10
	MaximumMaxHop         = 8
	MaximumMaxOutstanding = 100
	MaximumAdmissionsHour = 10_000

	TargetFixedIssue     = "fixed"
	TargetEventSubject   = "subject"
	TargetEventAttribute = "attribute"
)

var (
	identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	modelPattern      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,63}$`)
	projectIDPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	scheduleParser    = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
)

var reservedScheduleAttributes = map[string]bool{
	"scheduleId":       true,
	"scheduleRevision": true,
	"scheduledAt":      true,
	"producer":         true,
	"provenance":       true,
}

// Model is the canonical on-disk representation. Call NewSnapshot before use;
// Snapshot owns a deep copy and never exposes mutable backing collections.
type Model struct {
	Schema             int                       `json:"schema"`
	Generation         uint64                    `json:"generation"`
	Settings           Settings                  `json:"settings"`
	ProtectedWorkflows ProtectedWorkflowBindings `json:"protectedWorkflows"`
	Workflows          []Workflow                `json:"workflows"`
	Registry           Registry                  `json:"registry"`
	TaskControl        TaskControl               `json:"taskControl"`
}

type Settings struct {
	Revision  uint64          `json:"revision"`
	UpdatedAt time.Time       `json:"updatedAt,omitempty"`
	Agents    AgentSettings   `json:"agents"`
	Runtime   RuntimeSettings `json:"runtime"`
}

type ProtectedWorkflowBindings struct {
	LinearFeedback WorkflowBinding `json:"linearFeedback"`
}

type WorkflowBinding struct {
	WorkflowID string `json:"workflowId"`
}

type AgentSettings struct {
	Principal   PrincipalSettings `json:"principal"`
	CodexChild  ProviderSettings  `json:"codexChild"`
	ClaudeChild ProviderSettings  `json:"claudeChild"`
}

type PrincipalSettings struct {
	ProviderSettings
	MaxAttempts int `json:"maxAttempts"`
}

type ProviderSettings struct {
	Model  string `json:"model"`
	Effort string `json:"effort"`
}

type RuntimeSettings struct {
	MaxConcurrentRuns int `json:"maxConcurrentRuns"`
}

type Workflow struct {
	ID        string    `json:"id"`
	Revision  uint64    `json:"revision"`
	Name      string    `json:"name"`
	Enabled   bool      `json:"enabled"`
	Markdown  string    `json:"markdown"`
	UpdatedAt time.Time `json:"updatedAt,omitempty"`
}

type Registry struct {
	Revision  uint64     `json:"revision"`
	UpdatedAt time.Time  `json:"updatedAt,omitempty"`
	Rules     []Rule     `json:"rules"`
	Schedules []Schedule `json:"schedules"`
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
	Provider taskmodel.Source `json:"provider,omitempty"`
	Kind     string           `json:"kind"`
	Value    string           `json:"value,omitempty"`
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

type TaskControl struct {
	Revision          uint64    `json:"revision"`
	UpdatedAt         time.Time `json:"updatedAt,omitempty"`
	EnabledProjectIDs []string  `json:"enabledProjectIds"`
}

// Snapshot is an immutable policy value. Accessors return values or deep
// copies, so a caller cannot mutate a Store snapshot after releasing its lock.
type Snapshot struct {
	model Model
}

func NewSnapshot(model Model) (Snapshot, error) {
	canonicalizeModel(&model)
	if err := validateModel(model); err != nil {
		return Snapshot{}, err
	}
	return Snapshot{model: cloneModel(model)}, nil
}

func (s Snapshot) Model() Model { return cloneModel(s.model) }

func (s Snapshot) Schema() int { return s.model.Schema }

func (s Snapshot) Generation() uint64 { return s.model.Generation }

func (s Snapshot) Settings() Settings { return s.model.Settings }

func (s Snapshot) ProtectedWorkflows() ProtectedWorkflowBindings {
	return s.model.ProtectedWorkflows
}

func (s Snapshot) Workflows() []Workflow {
	return slices.Clone(s.model.Workflows)
}

func (s Snapshot) Workflow(id string) (Workflow, bool) {
	for _, definition := range s.model.Workflows {
		if definition.ID == id {
			return definition, true
		}
	}
	return Workflow{}, false
}

func (s Snapshot) Registry() Registry { return cloneRegistry(s.model.Registry) }

func (s Snapshot) TaskControl() TaskControl { return cloneTaskControl(s.model.TaskControl) }

func (s Snapshot) Validate() error { return validateModel(s.model) }

func (s Snapshot) Digest() (string, error) {
	data, err := json.Marshal(s.model)
	if err != nil {
		return "", fmt.Errorf("policy: encode digest: %w", err)
	}
	return digestBytes(data), nil
}

func WorkflowDigest(definition Workflow) (string, error) {
	canonical := struct {
		ID       string `json:"id"`
		Revision uint64 `json:"revision"`
		Name     string `json:"name"`
		Enabled  bool   `json:"enabled"`
		Markdown string `json:"markdown"`
	}{
		ID: definition.ID, Revision: definition.Revision, Name: definition.Name,
		Enabled: definition.Enabled, Markdown: canonicalizeMarkdown(definition.Markdown),
	}
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("policy: encode workflow digest: %w", err)
	}
	return digestBytes(data), nil
}

func RuleDigest(rule Rule) (string, error) {
	rule = cloneRule(rule)
	rule.Target = canonicalTarget(rule.Target)
	data, err := json.Marshal(rule)
	if err != nil {
		return "", fmt.Errorf("policy: encode rule digest: %w", err)
	}
	return digestBytes(data), nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validateModel(model Model) error {
	if model.Schema != SchemaVersion {
		return fmt.Errorf("policy: schema is %d, want %d", model.Schema, SchemaVersion)
	}
	if model.Generation == 0 {
		return errors.New("policy: generation is required")
	}
	if err := validateSettings(model.Settings); err != nil {
		return err
	}
	if len(model.Workflows) == 0 || len(model.Workflows) > MaxWorkflows {
		return fmt.Errorf("policy: workflow count must be between 1 and %d", MaxWorkflows)
	}
	workflowByID := make(map[string]Workflow, len(model.Workflows))
	totalMarkdown := 0
	for index, definition := range model.Workflows {
		if err := validateWorkflow(definition); err != nil {
			return fmt.Errorf("policy: workflow %d: %w", index+1, err)
		}
		if _, duplicate := workflowByID[definition.ID]; duplicate {
			return fmt.Errorf("policy: workflow ID %q is duplicated", definition.ID)
		}
		workflowByID[definition.ID] = definition
		totalMarkdown += len(definition.Markdown)
	}
	if totalMarkdown > MaxWorkflowBytesTotal {
		return fmt.Errorf("policy: published workflow Markdown exceeds %d bytes", MaxWorkflowBytesTotal)
	}
	bindingID := model.ProtectedWorkflows.LinearFeedback.WorkflowID
	bound, found := workflowByID[bindingID]
	if !validIdentifier(bindingID) || !found || !bound.Enabled {
		return errors.New("policy: protected Linear feedback must reference an enabled workflow")
	}
	if err := validateRegistry(model.Registry, workflowByID); err != nil {
		return err
	}
	if err := validateTaskControl(model.TaskControl); err != nil {
		return err
	}
	return nil
}

func validateSettings(value Settings) error {
	if err := validateProvider("principal", value.Agents.Principal.ProviderSettings, codexEffort); err != nil {
		return err
	}
	if value.Agents.Principal.MaxAttempts < 1 || value.Agents.Principal.MaxAttempts > 5 {
		return errors.New("policy: principal max attempts must be between 1 and 5")
	}
	if err := validateProvider("Codex child", value.Agents.CodexChild, codexEffort); err != nil {
		return err
	}
	if err := validateProvider("Claude child", value.Agents.ClaudeChild, claudeEffort); err != nil {
		return err
	}
	if value.Runtime.MaxConcurrentRuns < 1 || value.Runtime.MaxConcurrentRuns > 10 {
		return errors.New("policy: max concurrent runs must be between 1 and 10")
	}
	return nil
}

func validateProvider(name string, provider ProviderSettings, effort func(string) bool) error {
	if !modelPattern.MatchString(provider.Model) {
		return fmt.Errorf("policy: %s model is invalid", name)
	}
	if !effort(provider.Effort) {
		return fmt.Errorf("policy: %s effort %q is unsupported", name, provider.Effort)
	}
	return nil
}

func codexEffort(value string) bool {
	return value == "low" || value == "medium" || value == "high" || value == "xhigh"
}

func claudeEffort(value string) bool {
	return value == "low" || value == "medium" || value == "high" || value == "max"
}

func validateWorkflow(definition Workflow) error {
	if !validIdentifier(definition.ID) {
		return errors.New("workflow ID is invalid")
	}
	if definition.Revision == 0 {
		return fmt.Errorf("workflow %q revision is required", definition.ID)
	}
	if !validText(definition.Name, MaxWorkflowNameBytes) {
		return fmt.Errorf("workflow %q has an invalid name", definition.ID)
	}
	if definition.Markdown != canonicalizeMarkdown(definition.Markdown) {
		return fmt.Errorf("workflow %q Markdown is not canonical", definition.ID)
	}
	if !utf8.ValidString(definition.Markdown) || strings.TrimSpace(definition.Markdown) == "" || strings.IndexByte(definition.Markdown, 0) >= 0 {
		return fmt.Errorf("workflow %q Markdown is invalid", definition.ID)
	}
	if len(definition.Markdown) > MaxWorkflowBytes {
		return fmt.Errorf("workflow %q Markdown exceeds %d bytes", definition.ID, MaxWorkflowBytes)
	}
	return nil
}

func validateRegistry(registry Registry, workflows map[string]Workflow) error {
	if len(registry.Rules) > MaxRules {
		return fmt.Errorf("policy: rule count exceeds %d", MaxRules)
	}
	if len(registry.Schedules) > MaxSchedules {
		return fmt.Errorf("policy: schedule count exceeds %d", MaxSchedules)
	}
	ids := make(map[string]bool, len(registry.Rules)+len(registry.Schedules))
	for index, rule := range registry.Rules {
		if err := validateRule(rule); err != nil {
			return fmt.Errorf("policy: rule %d: %w", index+1, err)
		}
		if ids[rule.ID] {
			return fmt.Errorf("policy: registry ID %q is duplicated", rule.ID)
		}
		ids[rule.ID] = true
		definition, found := workflows[rule.WorkflowID]
		if rule.Enabled && (!found || !definition.Enabled) {
			return fmt.Errorf("policy: rule %q workflow %q is unavailable", rule.ID, rule.WorkflowID)
		}
	}
	for index, schedule := range registry.Schedules {
		if err := validateSchedule(schedule); err != nil {
			return fmt.Errorf("policy: schedule %d: %w", index+1, err)
		}
		if ids[schedule.ID] {
			return fmt.Errorf("policy: registry ID %q is duplicated", schedule.ID)
		}
		ids[schedule.ID] = true
	}
	return nil
}

func validateRule(rule Rule) error {
	if !validIdentifier(rule.ID) {
		return errors.New("rule ID is invalid")
	}
	if rule.Revision == 0 {
		return errors.New("rule revision is required")
	}
	if !validText(rule.Name, 80) {
		return errors.New("rule name is invalid")
	}
	if err := validateFilter(rule.Filter); err != nil {
		return err
	}
	if !validIdentifier(rule.WorkflowID) {
		return errors.New("workflow ID is invalid")
	}
	if err := validateTarget(rule.Target); err != nil {
		return err
	}
	if rule.MaxHop < 1 || rule.MaxHop > MaximumMaxHop {
		return fmt.Errorf("max hop must be between 1 and %d", MaximumMaxHop)
	}
	if rule.MaxOutstanding < 1 || rule.MaxOutstanding > MaximumMaxOutstanding {
		return fmt.Errorf("max outstanding must be between 1 and %d", MaximumMaxOutstanding)
	}
	if rule.AdmissionsHour < 1 || rule.AdmissionsHour > MaximumAdmissionsHour {
		return fmt.Errorf("admissions per hour must be between 1 and %d", MaximumAdmissionsHour)
	}
	return nil
}

func validateFilter(filter Filter) error {
	if filter.Source != "" && !eventwire.ValidSource(filter.Source) {
		return errors.New("filter source is invalid")
	}
	for name, value := range map[string]string{"type": filter.Type, "action": filter.Action} {
		if value != "" && !validField(value, false) {
			return fmt.Errorf("filter %s is invalid", name)
		}
	}
	if filter.Subject != nil && !validField(*filter.Subject, true) {
		return errors.New("filter subject is invalid")
	}
	if len(filter.Attributes) > 32 {
		return errors.New("filter has too many attributes")
	}
	for key, value := range filter.Attributes {
		if !validField(key, false) || len(value) > 256 {
			return errors.New("filter attributes are invalid")
		}
	}
	return nil
}

func validateTarget(target TargetPolicy) error {
	target = canonicalTarget(target)
	if target.Provider != taskmodel.SourceLinear {
		return errors.New("target provider is invalid")
	}
	switch target.Kind {
	case TargetFixedIssue:
		if !taskmodel.ValidLinearIdentifier(strings.ToUpper(target.Value)) || target.Value != strings.ToUpper(target.Value) {
			return errors.New("fixed target must be a canonical Linear issue identifier")
		}
	case TargetEventSubject:
		if target.Value != "" {
			return errors.New("subject target cannot have a value")
		}
	case TargetEventAttribute:
		if !validField(target.Value, false) {
			return errors.New("attribute target key is invalid")
		}
	default:
		return errors.New("target kind is invalid")
	}
	return nil
}

func validateSchedule(schedule Schedule) error {
	if !validIdentifier(schedule.ID) {
		return errors.New("schedule ID is invalid")
	}
	if schedule.Revision == 0 {
		return errors.New("schedule revision is required")
	}
	if !validText(schedule.Name, 80) {
		return errors.New("schedule name is invalid")
	}
	if !validCronShape(schedule.Cron) {
		return errors.New("schedule cron must be five fields without descriptors or timezone directives")
	}
	if _, err := scheduleParser.Parse(schedule.Cron); err != nil {
		return errors.New("schedule cron is invalid")
	}
	if schedule.Timezone == "" || schedule.Timezone != strings.TrimSpace(schedule.Timezone) {
		return errors.New("schedule timezone is invalid")
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return errors.New("schedule timezone is invalid")
	}
	if !validField(schedule.Subject, true) {
		return errors.New("schedule subject is invalid")
	}
	if len(schedule.Attributes) > 32 {
		return errors.New("schedule has too many attributes")
	}
	for key, values := range schedule.Attributes {
		if reservedScheduleAttributes[key] || !validField(key, false) || len(values) > 32 {
			return errors.New("schedule attributes are invalid")
		}
		seen := make(map[string]bool, len(values))
		for _, value := range values {
			if len(value) > 256 || seen[value] {
				return errors.New("schedule attribute values are invalid")
			}
			seen[value] = true
		}
	}
	return nil
}

func validateTaskControl(control TaskControl) error {
	seen := make(map[string]bool, len(control.EnabledProjectIDs))
	for _, projectID := range control.EnabledProjectIDs {
		if !projectIDPattern.MatchString(projectID) || seen[projectID] {
			return errors.New("policy: enabled projects are invalid")
		}
		seen[projectID] = true
	}
	if !sort.StringsAreSorted(control.EnabledProjectIDs) {
		return errors.New("policy: enabled projects are not canonical")
	}
	return nil
}

func canonicalizeModel(model *Model) {
	model.Settings.UpdatedAt = model.Settings.UpdatedAt.UTC()
	model.Registry.UpdatedAt = model.Registry.UpdatedAt.UTC()
	model.TaskControl.UpdatedAt = model.TaskControl.UpdatedAt.UTC()
	for index := range model.Workflows {
		model.Workflows[index].Markdown = canonicalizeMarkdown(model.Workflows[index].Markdown)
		model.Workflows[index].UpdatedAt = model.Workflows[index].UpdatedAt.UTC()
	}
	sort.Slice(model.Workflows, func(i, j int) bool { return model.Workflows[i].ID < model.Workflows[j].ID })
	for index := range model.Registry.Rules {
		model.Registry.Rules[index].Target = canonicalTarget(model.Registry.Rules[index].Target)
	}
	sort.Slice(model.Registry.Rules, func(i, j int) bool { return model.Registry.Rules[i].ID < model.Registry.Rules[j].ID })
	for index := range model.Registry.Schedules {
		for key := range model.Registry.Schedules[index].Attributes {
			sort.Strings(model.Registry.Schedules[index].Attributes[key])
		}
	}
	sort.Slice(model.Registry.Schedules, func(i, j int) bool { return model.Registry.Schedules[i].ID < model.Registry.Schedules[j].ID })
	sort.Strings(model.TaskControl.EnabledProjectIDs)
}

func canonicalTarget(target TargetPolicy) TargetPolicy {
	if target.Provider == "" {
		target.Provider = taskmodel.SourceLinear
	}
	return target
}

func canonicalizeMarkdown(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func cloneModel(model Model) Model {
	clone := model
	clone.Workflows = slices.Clone(model.Workflows)
	clone.Registry = cloneRegistry(model.Registry)
	clone.TaskControl = cloneTaskControl(model.TaskControl)
	return clone
}

func cloneRegistry(registry Registry) Registry {
	clone := registry
	clone.Rules = make([]Rule, len(registry.Rules))
	for index, rule := range registry.Rules {
		clone.Rules[index] = cloneRule(rule)
	}
	clone.Schedules = make([]Schedule, len(registry.Schedules))
	for index, schedule := range registry.Schedules {
		clone.Schedules[index] = cloneSchedule(schedule)
	}
	return clone
}

func cloneRule(rule Rule) Rule {
	clone := rule
	clone.Filter = cloneFilter(rule.Filter)
	return clone
}

func cloneFilter(filter Filter) Filter {
	clone := filter
	if filter.Subject != nil {
		subject := *filter.Subject
		clone.Subject = &subject
	}
	clone.Attributes = make(map[string]string, len(filter.Attributes))
	for key, value := range filter.Attributes {
		clone.Attributes[key] = value
	}
	return clone
}

func cloneSchedule(schedule Schedule) Schedule {
	clone := schedule
	clone.Attributes = make(map[string][]string, len(schedule.Attributes))
	for key, values := range schedule.Attributes {
		clone.Attributes[key] = slices.Clone(values)
	}
	return clone
}

func cloneTaskControl(control TaskControl) TaskControl {
	control.EnabledProjectIDs = slices.Clone(control.EnabledProjectIDs)
	return control
}

func ruleSemanticEqual(left, right Rule) bool {
	return left.Enabled == right.Enabled && left.WorkflowID == right.WorkflowID &&
		canonicalTarget(left.Target) == canonicalTarget(right.Target) &&
		left.MaxHop == right.MaxHop && left.MaxOutstanding == right.MaxOutstanding &&
		left.AdmissionsHour == right.AdmissionsHour && filterEqual(left.Filter, right.Filter)
}

func filterEqual(left, right Filter) bool {
	if left.Source != right.Source || left.Type != right.Type || left.Action != right.Action {
		return false
	}
	if (left.Subject == nil) != (right.Subject == nil) || (left.Subject != nil && *left.Subject != *right.Subject) {
		return false
	}
	if len(left.Attributes) != len(right.Attributes) {
		return false
	}
	for key, value := range left.Attributes {
		if right.Attributes[key] != value {
			return false
		}
	}
	return true
}

func scheduleSemanticEqual(left, right Schedule) bool {
	if left.Enabled != right.Enabled || left.Cron != right.Cron || left.Timezone != right.Timezone ||
		left.Subject != right.Subject || len(left.Attributes) != len(right.Attributes) {
		return false
	}
	for key, values := range left.Attributes {
		if !slices.Equal(values, right.Attributes[key]) {
			return false
		}
	}
	return true
}

func validIdentifier(value string) bool { return identifierPattern.MatchString(value) }

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
	return len(value) <= 256 && value == strings.TrimSpace(value) && utf8.ValidString(value)
}

func validCronShape(expression string) bool {
	if expression == "" || expression != strings.TrimSpace(expression) || len(expression) > 128 ||
		strings.HasPrefix(expression, "@") || strings.Contains(expression, "CRON_TZ=") || strings.Contains(expression, "TZ=") {
		return false
	}
	return len(strings.Fields(expression)) == 5
}
