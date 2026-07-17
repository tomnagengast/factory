package migration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tomnagengast/factory/internal/activity"
	"github.com/tomnagengast/factory/internal/agentrun"
	"github.com/tomnagengast/factory/internal/eventwire"
	"github.com/tomnagengast/factory/internal/githubhook"
	"github.com/tomnagengast/factory/internal/linearhook"
	"github.com/tomnagengast/factory/internal/projectsetup"
	"github.com/tomnagengast/factory/internal/settings"
	"github.com/tomnagengast/factory/internal/taskcompat"
	"github.com/tomnagengast/factory/internal/taskcontrol"
	"github.com/tomnagengast/factory/internal/taskstore"
	"github.com/tomnagengast/factory/internal/triggerregistry"
	"github.com/tomnagengast/factory/internal/triggerrouter"
	"github.com/tomnagengast/factory/internal/triggerscheduler"
	"github.com/tomnagengast/factory/internal/workflow"
)

const validationLimit = 1_000_000

type Options struct {
	TriggerActorID string
	Now            time.Time
	Inject         func(string) error
}

type activityRecord struct {
	DeliveryID       string `json:"deliveryId"`
	PayloadAvailable bool   `json:"payloadAvailable,omitempty"`
	activity.Event
}

type activityState struct {
	Total  uint64           `json:"total"`
	Events []activityRecord `json:"events"`
}

type identityState struct {
	Version  int                     `json:"version"`
	Bindings []linearIdentityBinding `json:"bindings"`
}

type linearIdentityBinding struct {
	Identifier string `json:"identifier"`
	UUID       string `json:"uuid"`
}

type projectState struct {
	Version int                  `json:"version"`
	Entries []projectsetup.Entry `json:"entries"`
}

type runState struct {
	Version int            `json:"version"`
	Total   uint64         `json:"total"`
	Runs    []agentrun.Run `json:"runs"`
}

type cursorState struct {
	Schema  int                       `json:"schema"`
	Cursors []triggerscheduler.Cursor `json:"cursors"`
}

type agentCursorState struct {
	Version  int               `json:"version"`
	Offsets  map[string]int64  `json:"offsets"`
	Prefixes map[string]string `json:"prefixes,omitempty"`
}

// The provider journals embed event fields beside sequence on disk. Explicit
// concrete records keep strict decoding without adding a runtime abstraction.
type githubRecord struct {
	Sequence uint64 `json:"sequence"`
	githubhook.Event
}

type linearRecord struct {
	Sequence uint64 `json:"sequence"`
	linearhook.Event
}

type githubState struct {
	Version int            `json:"version"`
	Total   uint64         `json:"total"`
	Events  []githubRecord `json:"events"`
}

type linearState struct {
	Version int            `json:"version"`
	Total   uint64         `json:"total"`
	Events  []linearRecord `json:"events"`
}

type sourceState struct {
	settings       settings.Snapshot
	registry       triggerregistry.Snapshot
	routing        triggerrouter.Snapshot
	projects       projectState
	runs           runState
	tasks          taskstore.Snapshot
	taskControl    taskcontrol.Snapshot
	identities     identityState
	activity       activityState
	drafts         workflow.DraftSnapshot
	wireTotal      uint64
	wireDispatched uint64
	wireRecords    []eventwire.Record
	cursors        cursorState
	agentCursors   agentCursorState
	githubEvents   githubState
	linearComments linearState
	taskBoundary   taskcompat.Marker
	payloadHashes  map[string]string
	hashes         []SourceHash
	directories    []SourceDirectory
}

func readSources(root string, options Options) (sourceState, error) {
	if options.Now.IsZero() {
		return sourceState{}, errors.New("migration: observation time is required")
	}
	root, err := secureRoot(root)
	if err != nil {
		return sourceState{}, err
	}
	if err := inject(options, "before-hash"); err != nil {
		return sourceState{}, err
	}
	hashes, err := hashTreeInjected(root, options)
	if err != nil {
		return sourceState{}, err
	}
	if err := inject(options, "after-hash"); err != nil {
		return sourceState{}, err
	}
	directories, err := directoryModes(root)
	if err != nil {
		return sourceState{}, err
	}

	state := sourceState{hashes: hashes, directories: directories, payloadHashes: make(map[string]string)}
	if err := inject(options, "before-decode"); err != nil {
		return sourceState{}, err
	}
	if err := inject(options, "read:settings.json"); err != nil {
		return sourceState{}, err
	}
	if err := decodeFile(root, "settings.json", &state.settings); err != nil {
		return sourceState{}, err
	}
	if err := state.settings.Validate(); err != nil {
		return sourceState{}, err
	}

	registryPath := filepath.Join(root, "triggers.json")
	if _, err := os.Lstat(registryPath); errors.Is(err, os.ErrNotExist) {
		if options.TriggerActorID == "" {
			return sourceState{}, errors.New("migration: trigger actor is required when registry is implicit")
		}
		state.registry = triggerregistry.Defaults(state.settings, options.TriggerActorID)
	} else if err != nil {
		return sourceState{}, fmt.Errorf("migration: inspect triggers.json: %w", err)
	} else if err := decodeFile(root, "triggers.json", &state.registry); err != nil {
		return sourceState{}, err
	}
	if err := state.registry.Validate(state.settings); err != nil {
		return sourceState{}, err
	}

	if err := replayCopy(root, "trigger-routing.jsonl", func(path string) error {
		store, err := triggerrouter.Open(path)
		if err == nil {
			state.routing = store.Snapshot()
		}
		return err
	}); err != nil {
		return sourceState{}, err
	}
	if err := decodeFile(root, "project-setups.json", &state.projects); err != nil {
		return sourceState{}, err
	}
	if state.projects.Version != 1 {
		return sourceState{}, fmt.Errorf("migration: unsupported project setup version %d", state.projects.Version)
	}
	if err := validateProjectCopy(root, options.Now); err != nil {
		return sourceState{}, err
	}
	if err := decodeFile(root, "agent-runs.json", &state.runs); err != nil {
		return sourceState{}, err
	}
	if state.runs.Version != 2 || state.runs.Total < uint64(len(state.runs.Runs)) {
		return sourceState{}, errors.New("migration: invalid Run snapshot totals")
	}
	if err := validateRunCopy(root); err != nil {
		return sourceState{}, err
	}
	if err := replayCopy(root, "native-tasks.jsonl", func(path string) error {
		store, err := taskstore.Open(path)
		if err == nil {
			state.tasks = store.Snapshot()
		}
		return err
	}); err != nil {
		return sourceState{}, err
	}
	if err := decodeFile(root, "native-task-control.json", &state.taskControl); err != nil {
		return sourceState{}, err
	}
	if err := validateTaskControlCopy(root); err != nil {
		return sourceState{}, err
	}
	if err := decodeFile(root, "linear-task-identities.json", &state.identities); err != nil {
		return sourceState{}, err
	}
	if state.identities.Version != 1 {
		return sourceState{}, fmt.Errorf("migration: unsupported Linear identity version %d", state.identities.Version)
	}
	if err := decodeFile(root, "linear-activity.json", &state.activity); err != nil {
		return sourceState{}, err
	}
	if state.activity.Total < uint64(len(state.activity.Events)) {
		return sourceState{}, errors.New("migration: activity retained count exceeds lifetime total")
	}
	if err := readPayloads(root, &state); err != nil {
		return sourceState{}, err
	}
	if err := decodeFile(root, "workflow-drafts.json", &state.drafts); err != nil {
		return sourceState{}, err
	}
	if err := validateDraftCopy(root); err != nil {
		return sourceState{}, err
	}
	if err := replayCopy(root, "system-events.jsonl", func(path string) error {
		journal, err := eventwire.Open(path, validationLimit, nil)
		if err == nil {
			state.wireTotal, state.wireDispatched, _, state.wireRecords = journal.Snapshot()
		}
		return err
	}); err != nil {
		return sourceState{}, err
	}
	if state.wireTotal != state.wireDispatched {
		return sourceState{}, fmt.Errorf("migration: pending wire records: total=%d dispatched=%d", state.wireTotal, state.wireDispatched)
	}
	cursorPath := filepath.Join(root, "trigger-cursors.json")
	if _, err := os.Lstat(cursorPath); errors.Is(err, os.ErrNotExist) {
		state.cursors = cursorState{Schema: 1, Cursors: []triggerscheduler.Cursor{}}
	} else if err != nil {
		return sourceState{}, fmt.Errorf("migration: inspect trigger-cursors.json: %w", err)
	} else if err := decodeFile(root, "trigger-cursors.json", &state.cursors); err != nil {
		return sourceState{}, err
	}
	if state.cursors.Schema != 1 {
		return sourceState{}, fmt.Errorf("migration: unsupported trigger cursor schema %d", state.cursors.Schema)
	}
	if err := decodeFile(root, "agent-event-offsets.json", &state.agentCursors); err != nil {
		return sourceState{}, err
	}
	if state.agentCursors.Version != 1 || state.agentCursors.Offsets == nil {
		return sourceState{}, errors.New("migration: invalid agent event cursor state")
	}
	if err := decodeFile(root, "github-events.json", &state.githubEvents); err != nil {
		return sourceState{}, err
	}
	if err := validateProviderJournal(state.githubEvents.Version, state.githubEvents.Total, githubSequences(state.githubEvents.Events)); err != nil {
		return sourceState{}, fmt.Errorf("migration: GitHub journal: %w", err)
	}
	if err := decodeFile(root, "linear-comments.json", &state.linearComments); err != nil {
		return sourceState{}, err
	}
	if err := validateProviderJournal(state.linearComments.Version, state.linearComments.Total, linearSequences(state.linearComments.Events)); err != nil {
		return sourceState{}, fmt.Errorf("migration: Linear journal: %w", err)
	}
	if err := decodeFile(root, "task-source-neutral.json", &state.taskBoundary); err != nil {
		return sourceState{}, err
	}
	if state.taskBoundary.Version != 1 || state.taskBoundary.Boundary != "source-neutral-task-v1" || state.taskBoundary.CrossedAt.IsZero() {
		return sourceState{}, errors.New("migration: invalid task compatibility boundary")
	}
	if err := ensureEmptyStages(root); err != nil {
		return sourceState{}, err
	}
	if err := inject(options, "after-decode"); err != nil {
		return sourceState{}, err
	}
	return state, nil
}

func secureRoot(root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("migration: data root must be absolute")
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil {
		return "", fmt.Errorf("migration: inspect data root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return "", errors.New("migration: data root must be a private nonsymlink directory")
	}
	return root, nil
}

func decodeFile(root, name string, target any) error {
	data, err := readPrivateFile(root, name)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("migration: decode %s: %w", name, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("migration: decode %s: trailing content", name)
	}
	return nil
}

func readPrivateFile(root, name string) ([]byte, error) {
	if filepath.Base(name) != name || name == "." {
		return nil, errors.New("migration: unsafe source path")
	}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("migration: inspect %s: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("migration: %s must be a private regular file", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("migration: read %s: %w", name, err)
	}
	return data, nil
}

func replayCopy(root, name string, open func(string) error) error {
	data, err := readPrivateFile(root, name)
	if err != nil {
		return err
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return fmt.Errorf("migration: %s has an incomplete tail", name)
	}
	directory, err := os.MkdirTemp("", "factory-migration-validation-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	if err := open(path); err != nil {
		return fmt.Errorf("migration: validate %s: %w", name, err)
	}
	return nil
}

func validateProjectCopy(root string, now time.Time) error {
	data, err := readPrivateFile(root, "project-setups.json")
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "factory-project-validation-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "project-setups.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	_, err = projectsetup.Open(path, now)
	return err
}

func validateRunCopy(root string) error {
	data, err := readPrivateFile(root, "agent-runs.json")
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "factory-run-validation-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "agent-runs.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	_, err = agentrun.Open(path, validationLimit)
	return err
}

func validateTaskControlCopy(root string) error {
	data, err := readPrivateFile(root, "native-task-control.json")
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "factory-task-control-validation-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "native-task-control.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	_, err = taskcontrol.Open(path)
	return err
}

func validateDraftCopy(root string) error {
	data, err := readPrivateFile(root, "workflow-drafts.json")
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "factory-draft-validation-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(directory)
	path := filepath.Join(directory, "workflow-drafts.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return err
	}
	_, err = workflow.OpenDraftStore(path)
	return err
}

func readPayloads(root string, state *sourceState) error {
	payloadRoot := filepath.Join(root, "linear-activity-payloads")
	info, err := os.Lstat(payloadRoot)
	if err != nil {
		return fmt.Errorf("migration: inspect payload directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("migration: payload directory must be private and nonsymlinked")
	}
	referenced := make(map[string]bool)
	for _, record := range state.activity.Events {
		if !record.PayloadAvailable {
			continue
		}
		digest := sha256.Sum256([]byte(record.DeliveryID))
		name := hex.EncodeToString(digest[:]) + ".json"
		referenced[name] = true
		data, err := readPayload(payloadRoot, name)
		if err != nil {
			return err
		}
		if !json.Valid(data) {
			return fmt.Errorf("migration: payload %s is not valid JSON", name)
		}
		hash := sha256.Sum256(data)
		state.payloadHashes[record.DeliveryID] = hex.EncodeToString(hash[:])
	}
	entries, err := os.ReadDir(payloadRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !referenced[entry.Name()] {
			return fmt.Errorf("migration: orphan private payload %s", entry.Name())
		}
	}
	return nil
}

func readPayload(root, name string) ([]byte, error) {
	if filepath.Ext(name) != ".json" || filepath.Base(name) != name {
		return nil, errors.New("migration: unsafe payload path")
	}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("migration: missing private payload %s: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("migration: private payload %s has unsafe mode or type", name)
	}
	return os.ReadFile(path)
}

func ensureEmptyStages(root string) error {
	path := filepath.Join(root, "task-operations")
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("migration: inspect task stages: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return errors.New("migration: task stage directory must be private and nonsymlinked")
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("migration: incomplete native task stage")
	}
	return nil
}

func hashTree(root string) ([]SourceHash, error) {
	return hashTreeInjected(root, Options{})
}

func hashTreeInjected(root string, options Options) ([]SourceHash, error) {
	var hashes []SourceHash
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("migration: symlink source is unsafe: %s", path)
		}
		if info.IsDir() {
			if info.Mode().Perm() != 0o700 {
				return fmt.Errorf("migration: source directory is not private: %s", path)
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return fmt.Errorf("migration: source file is not private: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("migration: source escaped data root")
		}
		if err := inject(options, "hash:"+filepath.ToSlash(relative)); err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		hashes = append(hashes, SourceHash{Path: filepath.ToSlash(relative), SHA256: hex.EncodeToString(digest[:]), Mode: uint32(info.Mode().Perm()), Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(hashes, func(a, b SourceHash) int { return compare(a.Path, b.Path) })
	return hashes, nil
}

func directoryModes(root string) ([]SourceDirectory, error) {
	var directories []SourceDirectory
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
			return fmt.Errorf("migration: source directory is unsafe: %s", path)
		}
		relative := "."
		if path != root {
			relative, err = filepath.Rel(root, path)
			if err != nil {
				return err
			}
		}
		directories = append(directories, SourceDirectory{Path: filepath.ToSlash(relative), Mode: uint32(info.Mode().Perm())})
		return nil
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(directories, func(a, b SourceDirectory) int { return compare(a.Path, b.Path) })
	return directories, nil
}

func githubSequences(records []githubRecord) []uint64 {
	sequences := make([]uint64, len(records))
	for index, record := range records {
		sequences[index] = record.Sequence
	}
	return sequences
}

func linearSequences(records []linearRecord) []uint64 {
	sequences := make([]uint64, len(records))
	for index, record := range records {
		sequences[index] = record.Sequence
	}
	return sequences
}

func validateProviderJournal(version int, total uint64, sequences []uint64) error {
	if version != 1 || total < uint64(len(sequences)) {
		return errors.New("invalid version or retained total")
	}
	var previous uint64
	for index, sequence := range sequences {
		if sequence == 0 || sequence > total || (index > 0 && sequence >= previous) {
			return errors.New("invalid retained sequence")
		}
		previous = sequence
	}
	return nil
}

func inject(options Options, point string) error {
	if options.Inject == nil {
		return nil
	}
	if err := options.Inject(point); err != nil {
		return fmt.Errorf("migration: injected %s failure: %w", point, err)
	}
	return nil
}
