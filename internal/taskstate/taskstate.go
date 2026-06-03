// Package taskstate persists Orpheus-owned per-task execution state.
package taskstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	orstate "github.com/hea3ven/orpheus/internal/state"
)

const schemaVersion = 1

// RunStatus is the M3 status for an attached run attempt.
type RunStatus string

const (
	// RunStatusRunning means an attached agent attempt was started and has not been recorded as finished.
	RunStatusRunning RunStatus = "running"

	// RunStatusSucceeded means the attached agent attempt exited successfully.
	RunStatusSucceeded RunStatus = "succeeded"

	// RunStatusFailed means the attached agent attempt failed or could not start.
	RunStatusFailed RunStatus = "failed"
)

// EventType is a trace/audit event type stored in the per-task state file.
type EventType string

const (
	EventWorktreeCreated   EventType = "worktree_created"
	EventWorktreeReused    EventType = "worktree_reused"
	EventWorktreeRecreated EventType = "worktree_recreated"
	EventRunStarted        EventType = "run_started"
	EventRunFinished       EventType = "run_finished"
	EventRunStartFailed    EventType = "run_start_failed"
)

var (
	// ErrActiveRun indicates the latest run attempt is still running.
	ErrActiveRun = errors.New("latest run attempt is still running")
)

// Service is the small task-state API consumed by orchestration and projections.
type Service interface {
	Path(repoID, taskID string) (string, error)
	Load(repoID, taskID string) (TaskState, error)
	LatestRun(repoID, taskID string) (RunAttempt, bool, error)
	ActiveRun(repoID, taskID string) (RunAttempt, bool, error)
	RecordWorktreeEvent(repoID, taskID string, eventType EventType, opts WorktreeEventOptions) (Event, error)
	StartRun(repoID, taskID string, opts StartRunOptions) (RunAttempt, error)
	FinishRun(repoID, taskID string, attempt int, status RunStatus) (RunAttempt, error)
	FailRunStart(repoID, taskID string, attempt int, cause error) (RunAttempt, error)
	Events(repoID, taskID string) ([]Event, error)
}

// Store is a YAML-backed per-task state store under the Orpheus data root.
type Store struct {
	paths orstate.Paths
	now   func() time.Time
}

var _ Service = Store{}

// TaskState is the human-readable YAML schema for one task's Orpheus state.
type TaskState struct {
	Version int    `yaml:"version"`
	RepoID  string `yaml:"repo_id"`
	TaskID  string `yaml:"task_id"`

	Runs   []RunAttempt `yaml:"runs,omitempty"`
	Events []Event      `yaml:"events,omitempty"`
}

// RunAttempt records one attached execution attempt.
type RunAttempt struct {
	Attempt int       `yaml:"attempt"`
	Status  RunStatus `yaml:"status"`

	Agent    string   `yaml:"agent,omitempty"`
	Command  string   `yaml:"command,omitempty"`
	Args     []string `yaml:"args,omitempty"`
	Branch   string   `yaml:"branch,omitempty"`
	Worktree string   `yaml:"worktree,omitempty"`

	StartedAt  time.Time  `yaml:"started_at"`
	FinishedAt *time.Time `yaml:"finished_at,omitempty"`
}

// Event records a small trace/audit event for a task.
type Event struct {
	Type EventType `yaml:"type"`
	At   time.Time `yaml:"at"`

	Attempt int       `yaml:"attempt,omitempty"`
	Status  RunStatus `yaml:"status,omitempty"`
	Agent   string    `yaml:"agent,omitempty"`

	Branch   string `yaml:"branch,omitempty"`
	Worktree string `yaml:"worktree,omitempty"`
	Error    string `yaml:"error,omitempty"`
}

// WorktreeEventOptions describes worktree context for a trace event.
type WorktreeEventOptions struct {
	Branch   string
	Worktree string
}

// StartRunOptions describes the run attempt being started.
type StartRunOptions struct {
	Agent    string
	Command  string
	Args     []string
	Branch   string
	Worktree string
}

// NewStore creates a per-task state store using paths.
func NewStore(paths orstate.Paths) Store {
	return Store{paths: paths, now: func() time.Time { return time.Now().UTC() }}
}

// NewStoreWithClock creates a store with a deterministic clock for tests.
func NewStoreWithClock(paths orstate.Paths, now func() time.Time) Store {
	store := NewStore(paths)
	if now != nil {
		store.now = now
	}
	return store
}

// Path returns the absolute YAML file path for one task state file.
func (s Store) Path(repoID, taskID string) (string, error) {
	rel, err := taskStateRelPath(repoID, taskID)
	if err != nil {
		return "", err
	}
	return s.paths.DataPath(rel)
}

// Load reads a task state file. Missing files load as an empty task state.
func (s Store) Load(repoID, taskID string) (TaskState, error) {
	repoID, taskID, rel, err := normalizedLocation(repoID, taskID)
	if err != nil {
		return TaskState{}, err
	}

	loaded := emptyTaskState(repoID, taskID)
	if err := s.paths.ReadDataYAML(rel, &loaded); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyTaskState(repoID, taskID), nil
		}
		return TaskState{}, fmt.Errorf("load task state %s/%s: %w", repoID, taskID, err)
	}

	if err := validateLoadedState(loaded, repoID, taskID); err != nil {
		return TaskState{}, fmt.Errorf("load task state %s/%s: %w", repoID, taskID, err)
	}
	return normalizeState(loaded, repoID, taskID), nil
}

// LatestRun returns the highest-numbered run attempt for a task.
func (s Store) LatestRun(repoID, taskID string) (RunAttempt, bool, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return RunAttempt{}, false, err
	}
	latest, ok := LatestRun(state)
	return latest, ok, nil
}

// ActiveRun returns the latest attempt only when it is still running.
func (s Store) ActiveRun(repoID, taskID string) (RunAttempt, bool, error) {
	latest, ok, err := s.LatestRun(repoID, taskID)
	if err != nil || !ok || latest.Status != RunStatusRunning {
		return RunAttempt{}, false, err
	}
	return latest, true, nil
}

// RecordWorktreeEvent appends a worktree lifecycle event.
func (s Store) RecordWorktreeEvent(repoID, taskID string, eventType EventType, opts WorktreeEventOptions) (Event, error) {
	switch eventType {
	case EventWorktreeCreated, EventWorktreeReused, EventWorktreeRecreated:
	default:
		return Event{}, fmt.Errorf("record worktree event for task %s/%s: unsupported worktree event type %q", repoID, taskID, eventType)
	}

	return s.appendEvent(repoID, taskID, Event{
		Type:     eventType,
		Branch:   strings.TrimSpace(opts.Branch),
		Worktree: strings.TrimSpace(opts.Worktree),
	})
}

// StartRun appends a new running attempt and a run_started event.
func (s Store) StartRun(repoID, taskID string, opts StartRunOptions) (RunAttempt, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return RunAttempt{}, err
	}
	if active, ok := ActiveRun(state); ok {
		return RunAttempt{}, fmt.Errorf("start run attempt for task %s/%s: %w (attempt %d)", repoID, taskID, ErrActiveRun, active.Attempt)
	}

	now := s.nowUTC()
	attempt := RunAttempt{
		Attempt:   nextAttemptNumber(state),
		Status:    RunStatusRunning,
		Agent:     strings.TrimSpace(opts.Agent),
		Command:   strings.TrimSpace(opts.Command),
		Args:      cloneStrings(opts.Args),
		Branch:    strings.TrimSpace(opts.Branch),
		Worktree:  strings.TrimSpace(opts.Worktree),
		StartedAt: now,
	}
	state.Runs = append(state.Runs, attempt)
	state.Events = append(state.Events, Event{
		Type:     EventRunStarted,
		At:       now,
		Attempt:  attempt.Attempt,
		Status:   RunStatusRunning,
		Agent:    attempt.Agent,
		Branch:   attempt.Branch,
		Worktree: attempt.Worktree,
	})

	if err := s.save(state); err != nil {
		return RunAttempt{}, err
	}
	return attempt, nil
}

// FinishRun records a succeeded or failed attached process exit and appends run_finished.
func (s Store) FinishRun(repoID, taskID string, attempt int, status RunStatus) (RunAttempt, error) {
	if status != RunStatusSucceeded && status != RunStatusFailed {
		return RunAttempt{}, fmt.Errorf("finish run attempt for task %s/%s: status must be %q or %q, got %q", repoID, taskID, RunStatusSucceeded, RunStatusFailed, status)
	}
	return s.completeRun(repoID, taskID, attempt, status, EventRunFinished, "")
}

// FailRunStart records that an attempt failed before the agent process started.
func (s Store) FailRunStart(repoID, taskID string, attempt int, cause error) (RunAttempt, error) {
	errorText := ""
	if cause != nil {
		errorText = cause.Error()
	}
	return s.completeRun(repoID, taskID, attempt, RunStatusFailed, EventRunStartFailed, errorText)
}

// Events returns a copy of trace/audit events for a task.
func (s Store) Events(repoID, taskID string) ([]Event, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return nil, err
	}
	return cloneEvents(state.Events), nil
}

// LatestRun returns the highest-numbered run attempt from state.
func LatestRun(state TaskState) (RunAttempt, bool) {
	if len(state.Runs) == 0 {
		return RunAttempt{}, false
	}

	latest := state.Runs[0]
	for _, run := range state.Runs[1:] {
		if run.Attempt > latest.Attempt {
			latest = run
		}
	}
	return latest, true
}

// ActiveRun returns the latest attempt only when it is running.
func ActiveRun(state TaskState) (RunAttempt, bool) {
	latest, ok := LatestRun(state)
	if !ok || latest.Status != RunStatusRunning {
		return RunAttempt{}, false
	}
	return latest, true
}

func (s Store) appendEvent(repoID, taskID string, event Event) (Event, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Event{}, err
	}
	event.At = nonZeroTime(event.At, s.nowUTC())
	if err := validateEvent(event); err != nil {
		return Event{}, fmt.Errorf("append event for task %s/%s: %w", repoID, taskID, err)
	}
	state.Events = append(state.Events, event)
	if err := s.save(state); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (s Store) completeRun(repoID, taskID string, attempt int, status RunStatus, eventType EventType, errorText string) (RunAttempt, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return RunAttempt{}, err
	}

	index := -1
	for i, run := range state.Runs {
		if run.Attempt == attempt {
			index = i
			break
		}
	}
	if index < 0 {
		return RunAttempt{}, fmt.Errorf("complete run attempt for task %s/%s: attempt %d was not found", repoID, taskID, attempt)
	}

	now := s.nowUTC()
	finished := now
	state.Runs[index].Status = status
	state.Runs[index].FinishedAt = &finished
	updated := state.Runs[index]
	state.Events = append(state.Events, Event{
		Type:     eventType,
		At:       now,
		Attempt:  updated.Attempt,
		Status:   status,
		Agent:    updated.Agent,
		Branch:   updated.Branch,
		Worktree: updated.Worktree,
		Error:    strings.TrimSpace(errorText),
	})

	if err := s.save(state); err != nil {
		return RunAttempt{}, err
	}
	return updated, nil
}

func (s Store) save(taskState TaskState) error {
	normalized, err := normalizeStateForSave(taskState)
	if err != nil {
		return err
	}
	rel, err := taskStateRelPath(normalized.RepoID, normalized.TaskID)
	if err != nil {
		return err
	}
	if err := s.paths.WriteDataYAML(rel, normalized); err != nil {
		return fmt.Errorf("save task state %s/%s: %w", normalized.RepoID, normalized.TaskID, err)
	}
	return nil
}

func (s Store) nowUTC() time.Time {
	return s.now().UTC()
}

func emptyTaskState(repoID, taskID string) TaskState {
	return TaskState{Version: schemaVersion, RepoID: repoID, TaskID: taskID}
}

func normalizedLocation(repoID, taskID string) (string, string, string, error) {
	normalizedRepoID, err := cleanPathComponent("repo id", repoID)
	if err != nil {
		return "", "", "", err
	}
	normalizedTaskID, err := cleanPathComponent("task id", taskID)
	if err != nil {
		return "", "", "", err
	}
	rel, err := taskStateRelPath(normalizedRepoID, normalizedTaskID)
	if err != nil {
		return "", "", "", err
	}
	return normalizedRepoID, normalizedTaskID, rel, nil
}

func taskStateRelPath(repoID, taskID string) (string, error) {
	repoID, err := cleanPathComponent("repo id", repoID)
	if err != nil {
		return "", err
	}
	taskID, err = cleanPathComponent("task id", taskID)
	if err != nil {
		return "", err
	}
	return filepath.Join("repos", repoID, "tasks", taskID+".yaml"), nil
}

func cleanPathComponent(label string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if value == "." || value == ".." || strings.ContainsAny(value, `/\\`) || filepath.VolumeName(value) != "" {
		return "", fmt.Errorf("%s %q cannot be used in task state path", label, value)
	}
	return value, nil
}

func validateLoadedState(taskState TaskState, repoID, taskID string) error {
	if strings.TrimSpace(taskState.RepoID) != repoID {
		return fmt.Errorf("repo_id is %q, expected %q", taskState.RepoID, repoID)
	}
	if strings.TrimSpace(taskState.TaskID) != taskID {
		return fmt.Errorf("task_id is %q, expected %q", taskState.TaskID, taskID)
	}
	if taskState.Version != 0 && taskState.Version != schemaVersion {
		return fmt.Errorf("unsupported task state version %d", taskState.Version)
	}
	for _, run := range taskState.Runs {
		if err := validateRun(run); err != nil {
			return err
		}
	}
	for _, event := range taskState.Events {
		if err := validateEvent(event); err != nil {
			return err
		}
	}
	return nil
}

func normalizeStateForSave(taskState TaskState) (TaskState, error) {
	repoID, taskID, _, err := normalizedLocation(taskState.RepoID, taskState.TaskID)
	if err != nil {
		return TaskState{}, err
	}
	if taskState.Version == 0 {
		taskState.Version = schemaVersion
	}
	if err := validateLoadedState(taskState, repoID, taskID); err != nil {
		return TaskState{}, err
	}
	return normalizeState(taskState, repoID, taskID), nil
}

func normalizeState(taskState TaskState, repoID, taskID string) TaskState {
	taskState.Version = schemaVersion
	taskState.RepoID = repoID
	taskState.TaskID = taskID
	return taskState
}

func validateRun(run RunAttempt) error {
	if run.Attempt <= 0 {
		return fmt.Errorf("run attempt must be positive, got %d", run.Attempt)
	}
	if !validRunStatus(run.Status) {
		return fmt.Errorf("run attempt %d has unsupported status %q", run.Attempt, run.Status)
	}
	return nil
}

func validateEvent(event Event) error {
	if !validEventType(event.Type) {
		return fmt.Errorf("unsupported event type %q", event.Type)
	}
	if event.Status != "" && !validRunStatus(event.Status) {
		return fmt.Errorf("event %q has unsupported run status %q", event.Type, event.Status)
	}
	return nil
}

func validRunStatus(status RunStatus) bool {
	switch status {
	case RunStatusRunning, RunStatusSucceeded, RunStatusFailed:
		return true
	default:
		return false
	}
}

func validEventType(eventType EventType) bool {
	switch eventType {
	case EventWorktreeCreated, EventWorktreeReused, EventWorktreeRecreated, EventRunStarted, EventRunFinished, EventRunStartFailed:
		return true
	default:
		return false
	}
}

func nextAttemptNumber(state TaskState) int {
	latest, ok := LatestRun(state)
	if !ok {
		return 1
	}
	return latest.Attempt + 1
}

func nonZeroTime(value time.Time, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value.UTC()
}

func cloneEvents(events []Event) []Event {
	if events == nil {
		return nil
	}
	clone := make([]Event, len(events))
	copy(clone, events)
	return clone
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	clone := make([]string, len(values))
	copy(clone, values)
	return clone
}
