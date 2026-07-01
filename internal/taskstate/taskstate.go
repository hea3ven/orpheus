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

// ReviewStatus is the status for a local review attempt.
type ReviewStatus string

const (
	ReviewStatusRunning ReviewStatus = "running"
	ReviewStatusBlocked ReviewStatus = "blocked"
	ReviewStatusFailed  ReviewStatus = "failed"
	ReviewStatusPassed  ReviewStatus = "passed"
	ReviewStatusAborted ReviewStatus = "aborted"
)

// FindingType classifies a human-recorded review finding.
type FindingType string

const (
	FindingTypeBlocking     FindingType = "blocking"
	FindingTypeAdvisory     FindingType = "advisory"
	FindingTypeSeparateTask FindingType = "separate_task"
)

// EventType is a trace/audit event type stored in the per-task state file.
type EventType string

const (
	EventWorktreeCreated    EventType = "worktree_created"
	EventTaskBranchCreated  EventType = "task_branch_created"
	EventWorktreeReused     EventType = "worktree_reused"
	EventWorktreeRecreated  EventType = "worktree_recreated"
	EventRunStarted         EventType = "run_started"
	EventRunFinished        EventType = "run_finished"
	EventRunStartFailed     EventType = "run_start_failed"
	EventCompletionRecorded EventType = "completion_recorded"
	EventCompletionRepeated EventType = "completion_repeated"
	EventChangesPushed      EventType = "changes_pushed"
	EventPRCreated          EventType = "pr_created"
	EventPRRecovered        EventType = "pr_recovered"
	EventFinalizationFailed EventType = "finalization_failed"
	EventTaskClosed         EventType = "task_closed"
)

const (
	// PushTargetMain identifies a publication to the registered default branch.
	PushTargetMain = "main"

	// PushTargetBranch identifies a publication to a feature branch.
	PushTargetBranch = "branch"

	// CloseReasonDefaultBranchPublished identifies closure after a default-branch push.
	CloseReasonDefaultBranchPublished = "default_branch_published"

	// CloseReasonPRMerged identifies closure after a recorded pull request is merged.
	CloseReasonPRMerged = "pr_merged"
)

var (
	// ErrActiveRun indicates the latest run attempt is still running.
	ErrActiveRun = errors.New("latest run attempt is still running")

	// ErrCompletionConflict indicates a run already has different completion content.
	ErrCompletionConflict = errors.New("run completion already recorded with different summary/description/detailed_description")

	// ErrFinalizationConflict indicates finalization facts already contain different data.
	ErrFinalizationConflict = errors.New("task finalization already recorded with different facts")
)

// Service is the small task-state API consumed by orchestration and projections.
type Service interface {
	Path(repoID, taskID string) (string, error)
	Load(repoID, taskID string) (TaskState, error)
	LatestRun(repoID, taskID string) (RunAttempt, bool, error)
	ActiveRun(repoID, taskID string) (RunAttempt, bool, error)
	RecordSetupEvent(repoID, taskID string, eventType EventType, opts SetupEventOptions) (Event, error)
	StartRun(repoID, taskID string, opts StartRunOptions) (RunAttempt, error)
	CompleteRun(repoID, taskID string, attempt int, opts CompleteRunOptions) (RunAttempt, error)
	RecordRepeatedCompletion(repoID, taskID string, attempt int, opts RepeatedCompletionOptions) (Event, error)
	FinishRun(repoID, taskID string, attempt int, status RunStatus) (RunAttempt, error)
	FailRunStart(repoID, taskID string, attempt int, cause error) (RunAttempt, error)
	StartReview(repoID, taskID string) (ReviewAttempt, error)
	StartReviewWithOptions(repoID, taskID string, opts StartReviewOptions) (ReviewAttempt, error)
	RecordReviewStep(repoID, taskID string, attempt int, opts RecordReviewStepOptions) (ReviewAttempt, error)
	RecordReviewFinding(repoID, taskID string, attempt int, finding ReviewFinding) (ReviewAttempt, error)
	TargetReviewFindings(repoID, taskID string, reviewAttempt int, findingIndexes []int, runAttempt int) (ReviewAttempt, error)
	FinishReview(repoID, taskID string, attempt int, status ReviewStatus) (ReviewAttempt, error)
	RecordFinalizationCommit(repoID, taskID string, commit string) (Finalization, error)
	RecordFinalizationPush(repoID, taskID string, opts FinalizationPushOptions) (Finalization, error)
	RecordFinalizationClose(repoID, taskID string, opts FinalizationCloseOptions) (Finalization, error)
	RecordFinalizationFailure(repoID, taskID string, cause error) (Event, error)
	RecordFeatureBranchPR(repoID, taskID string, opts FeatureBranchPROptions) (Event, error)
	RecordTaskClosed(repoID, taskID string, opts TaskClosedOptions) (Event, error)
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

	Runs    []RunAttempt    `yaml:"runs,omitempty"`
	Reviews []ReviewAttempt `yaml:"reviews,omitempty"`
	Events  []Event         `yaml:"events,omitempty"`

	Finalization *Finalization `yaml:"finalization,omitempty"`
}

// RunAttempt records one attached execution attempt.
type RunAttempt struct {
	Attempt int       `yaml:"attempt"`
	Status  RunStatus `yaml:"status"`

	Agent       string   `yaml:"agent,omitempty"`
	Command     string   `yaml:"command,omitempty"`
	Args        []string `yaml:"args,omitempty"`
	SessionName string   `yaml:"session_name,omitempty"`
	Branch      string   `yaml:"branch,omitempty"`
	Worktree    string   `yaml:"worktree,omitempty"`

	StartedAt  time.Time   `yaml:"started_at"`
	FinishedAt *time.Time  `yaml:"finished_at,omitempty"`
	Completion *Completion `yaml:"completion,omitempty"`

	ReviewFollowUp *ReviewFollowUp `yaml:"review_follow_up,omitempty"`
}

// ReviewFollowUp records which review attempt caused a follow-up run.
type ReviewFollowUp struct {
	ReviewAttempt  int   `yaml:"review_attempt"`
	FindingIndexes []int `yaml:"finding_indexes,omitempty"`
}

// Completion records agent-authored completion facts for a run attempt.
type Completion struct {
	Summary             string    `yaml:"summary"`
	Description         string    `yaml:"description"`
	DetailedDescription string    `yaml:"detailed_description"`
	CompletedAt         time.Time `yaml:"completed_at"`
	Commit              string    `yaml:"commit,omitempty"`
	CommitError         string    `yaml:"commit_error,omitempty"`
}

// ReviewAttempt records one local review pipeline attempt.
type ReviewAttempt struct {
	Attempt int          `yaml:"attempt"`
	Status  ReviewStatus `yaml:"status"`

	Pipeline string `yaml:"pipeline"`
	Step     string `yaml:"step"`

	StartedAt  time.Time       `yaml:"started_at"`
	FinishedAt *time.Time      `yaml:"finished_at,omitempty"`
	Steps      []ReviewStep    `yaml:"steps,omitempty"`
	Findings   []ReviewFinding `yaml:"findings,omitempty"`
}

// ReviewStep records one executed review pipeline step.
type ReviewStep struct {
	Kind     string   `yaml:"kind"`
	Name     string   `yaml:"name"`
	Command  string   `yaml:"command,omitempty"`
	Args     []string `yaml:"args,omitempty"`
	ExitCode *int     `yaml:"exit_code,omitempty"`
}

// ReviewFinding records one review finding.
type ReviewFinding struct {
	Type        FindingType `yaml:"type"`
	Title       string      `yaml:"title"`
	Description string      `yaml:"description"`

	Step            string `yaml:"step,omitempty"`
	SuggestedAction string `yaml:"suggested_action,omitempty"`
	Waiver          string `yaml:"waiver,omitempty"`
	TaskProposal    string `yaml:"task_proposal,omitempty"`
	CreatedTaskID   string `yaml:"created_task_id,omitempty"`

	TargetedByRunAttempt int `yaml:"targeted_by_run_attempt,omitempty"`
}

// Finalization records factual data from human-side main/solo finalization.
type Finalization struct {
	CommittedAt *time.Time `yaml:"committed_at,omitempty"`
	Commit      string     `yaml:"commit,omitempty"`
	PushedAt    *time.Time `yaml:"pushed_at,omitempty"`
	ClosedAt    *time.Time `yaml:"closed_at,omitempty"`
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

	Message                      string `yaml:"message,omitempty"`
	RequestedSummary             string `yaml:"requested_summary,omitempty"`
	RequestedDescription         string `yaml:"requested_description,omitempty"`
	RequestedDetailedDescription string `yaml:"requested_detailed_description,omitempty"`

	PRURL           string `yaml:"pr_url,omitempty"`
	ObservedPRState string `yaml:"observed_pr_state,omitempty"`
	PushTarget      string `yaml:"push_target,omitempty"`
	CloseReason     string `yaml:"close_reason,omitempty"`
}

// DisplayName returns the concise human-readable name for an audit event.
func (e Event) DisplayName() string {
	switch e.Type {
	case EventWorktreeCreated:
		return "Worktree created"
	case EventTaskBranchCreated:
		return "Task branch created"
	case EventWorktreeReused:
		return "Worktree reused"
	case EventWorktreeRecreated:
		return "Worktree recreated"
	case EventRunStarted:
		return "Run started"
	case EventRunFinished:
		return "Run finished"
	case EventRunStartFailed:
		return "Run start failed"
	case EventCompletionRecorded:
		return "Completion recorded"
	case EventCompletionRepeated:
		return "Completion repeated"
	case EventChangesPushed:
		return "Pushed " + e.PushTarget
	case EventPRCreated:
		return "PR created"
	case EventPRRecovered:
		return "PR recovered"
	case EventFinalizationFailed:
		return "Finalization failed"
	case EventTaskClosed:
		return "Task closed"
	default:
		return string(e.Type)
	}
}

// SetupEventOptions describes task execution target context for a setup event.
type SetupEventOptions struct {
	Branch   string
	Worktree string
}

// StartRunOptions describes the run attempt being started.
type StartRunOptions struct {
	Agent       string
	Command     string
	Args        []string
	SessionName string
	Branch      string
	Worktree    string

	ReviewFollowUp *ReviewFollowUp
}

// CompleteRunOptions describes the agent-authored completion payload.
type CompleteRunOptions struct {
	Summary             string
	Description         string
	DetailedDescription string
	Commit              string
	CommitError         string
}

type completeRunPayload struct {
	summary             string
	description         string
	detailedDescription string
	commit              string
	commitError         string
}

// RepeatedCompletionOptions describes an ignored repeated agent completion payload.
type RepeatedCompletionOptions struct {
	Summary             string
	Description         string
	DetailedDescription string
}

// StartReviewOptions describes the selected review pipeline.
type StartReviewOptions struct {
	Pipeline string
	Step     string
}

// RecordReviewStepOptions describes one executed review step.
type RecordReviewStepOptions struct {
	Kind     string
	Name     string
	Command  string
	Args     []string
	ExitCode *int
}

// TaskClosedOptions describes the facts recorded when a task is closed.
type TaskClosedOptions struct {
	Reason          string
	PRURL           string
	ObservedPRState string
}

// FinalizationPushOptions describes the successful publication boundary.
type FinalizationPushOptions struct {
	Branch     string
	PushTarget string
}

// FinalizationCloseOptions describes why a successful task finalization closed a task.
type FinalizationCloseOptions struct {
	Reason string
}

// FeatureBranchPROptions describes a created or recovered feature-branch PR.
type FeatureBranchPROptions struct {
	PRURL        string
	Branch       string
	WasRecovered bool
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

// RecordSetupEvent appends a durable task execution setup event.
func (s Store) RecordSetupEvent(repoID, taskID string, eventType EventType, opts SetupEventOptions) (Event, error) {
	switch eventType {
	case EventWorktreeCreated, EventTaskBranchCreated, EventWorktreeRecreated:
	default:
		return Event{}, fmt.Errorf("record setup event for task %s/%s: unsupported setup event type %q", repoID, taskID, eventType)
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
		Attempt:        nextAttemptNumber(state),
		Status:         RunStatusRunning,
		Agent:          strings.TrimSpace(opts.Agent),
		Command:        strings.TrimSpace(opts.Command),
		Args:           cloneStrings(opts.Args),
		SessionName:    opts.SessionName,
		Branch:         strings.TrimSpace(opts.Branch),
		Worktree:       strings.TrimSpace(opts.Worktree),
		StartedAt:      now,
		ReviewFollowUp: normalizeReviewFollowUp(opts.ReviewFollowUp),
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

// TargetReviewFindings marks findings from a review as addressed by a run attempt.
func (s Store) TargetReviewFindings(
	repoID,
	taskID string,
	reviewAttempt int,
	findingIndexes []int,
	runAttempt int,
) (ReviewAttempt, error) {
	if reviewAttempt <= 0 {
		return ReviewAttempt{}, fmt.Errorf("target review findings for task %s/%s: review attempt must be positive", repoID, taskID)
	}
	if runAttempt <= 0 {
		return ReviewAttempt{}, fmt.Errorf("target review findings for task %s/%s: run attempt must be positive", repoID, taskID)
	}
	if len(findingIndexes) == 0 {
		return ReviewAttempt{}, fmt.Errorf("target review findings for task %s/%s: at least one finding index is required", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	reviewIndex := reviewAttemptIndex(state, reviewAttempt)
	if reviewIndex < 0 {
		return ReviewAttempt{}, fmt.Errorf("target review findings for task %s/%s: review attempt %d was not found", repoID, taskID, reviewAttempt)
	}
	for _, findingIndex := range findingIndexes {
		if findingIndex < 0 || findingIndex >= len(state.Reviews[reviewIndex].Findings) {
			return ReviewAttempt{}, fmt.Errorf("target review findings for task %s/%s: finding index %d is out of range", repoID, taskID, findingIndex)
		}
		finding := state.Reviews[reviewIndex].Findings[findingIndex]
		if finding.TargetedByRunAttempt != 0 && finding.TargetedByRunAttempt != runAttempt {
			return ReviewAttempt{}, fmt.Errorf(
				"target review findings for task %s/%s: finding index %d is already targeted by run attempt %d",
				repoID,
				taskID,
				findingIndex,
				finding.TargetedByRunAttempt,
			)
		}
		state.Reviews[reviewIndex].Findings[findingIndex].TargetedByRunAttempt = runAttempt
	}

	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[reviewIndex], nil
}

// FinishRun records a succeeded or failed attached process exit and appends run_finished.
func (s Store) FinishRun(repoID, taskID string, attempt int, status RunStatus) (RunAttempt, error) {
	if status != RunStatusSucceeded && status != RunStatusFailed {
		return RunAttempt{}, fmt.Errorf("finish run attempt for task %s/%s: status must be %q or %q, got %q", repoID, taskID, RunStatusSucceeded, RunStatusFailed, status)
	}
	return s.completeRun(repoID, taskID, attempt, status, EventRunFinished, "")
}

// CompleteRun records agent-authored completion facts without finishing the attached run.
func (s Store) CompleteRun(repoID, taskID string, attempt int, opts CompleteRunOptions) (RunAttempt, error) {
	payload, err := completeRunPayloadFromOptions(repoID, taskID, opts)
	if err != nil {
		return RunAttempt{}, err
	}

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

	run := state.Runs[index]
	if run.Completion != nil {
		return s.completeExistingRun(state, index, repoID, taskID, payload)
	}

	if run.Status != RunStatusRunning {
		return RunAttempt{}, fmt.Errorf(
			"complete run attempt for task %s/%s: attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			run.Status,
			RunStatusRunning,
		)
	}

	now := s.nowUTC()
	completedAt := now
	state.Runs[index].Completion = &Completion{
		Summary:             payload.summary,
		Description:         payload.description,
		DetailedDescription: payload.detailedDescription,
		CompletedAt:         completedAt,
		Commit:              payload.commit,
		CommitError:         payload.commitError,
	}
	state.Events = append(state.Events, runEvent(run, EventCompletionRecorded, now, run.Status, ""))

	if err := s.save(state); err != nil {
		return RunAttempt{}, err
	}
	return state.Runs[index], nil
}

func completeRunPayloadFromOptions(repoID, taskID string, opts CompleteRunOptions) (completeRunPayload, error) {
	summary := strings.TrimSpace(opts.Summary)
	if summary == "" {
		return completeRunPayload{}, fmt.Errorf("complete run attempt for task %s/%s: summary is required", repoID, taskID)
	}
	description := strings.TrimSpace(opts.Description)
	if description == "" {
		return completeRunPayload{}, fmt.Errorf("complete run attempt for task %s/%s: description is required", repoID, taskID)
	}
	if strings.TrimSpace(opts.DetailedDescription) == "" {
		return completeRunPayload{}, fmt.Errorf("complete run attempt for task %s/%s: detailed_description is required", repoID, taskID)
	}
	return completeRunPayload{
		summary:             summary,
		description:         description,
		detailedDescription: opts.DetailedDescription,
		commit:              strings.TrimSpace(opts.Commit),
		commitError:         strings.TrimSpace(opts.CommitError),
	}, nil
}

func (s Store) completeExistingRun(
	state TaskState,
	index int,
	repoID string,
	taskID string,
	payload completeRunPayload,
) (RunAttempt, error) {
	run := state.Runs[index]
	completion, changed, err := mergeCompletionPayload(*run.Completion, payload)
	if err != nil {
		return RunAttempt{}, fmt.Errorf("complete run attempt for task %s/%s: %w", repoID, taskID, err)
	}
	if !changed {
		return run, nil
	}

	state.Runs[index].Completion = &completion
	if err := s.save(state); err != nil {
		return RunAttempt{}, err
	}
	return state.Runs[index], nil
}

func mergeCompletionPayload(completion Completion, payload completeRunPayload) (Completion, bool, error) {
	if completion.Summary != payload.summary ||
		completion.Description != payload.description ||
		completion.DetailedDescription != payload.detailedDescription {
		return Completion{}, false, ErrCompletionConflict
	}

	changed, err := mergeCompletionOptionalFact(&completion.Commit, payload.commit)
	if err != nil {
		return Completion{}, false, err
	}
	commitErrorChanged, err := mergeCompletionOptionalFact(&completion.CommitError, payload.commitError)
	if err != nil {
		return Completion{}, false, err
	}
	return completion, changed || commitErrorChanged, nil
}

func mergeCompletionOptionalFact(existing *string, requested string) (bool, error) {
	if requested == "" {
		return false, nil
	}
	if strings.TrimSpace(*existing) != "" && *existing != requested {
		return false, ErrCompletionConflict
	}
	changed := *existing != requested
	*existing = requested
	return changed, nil
}

// RecordRepeatedCompletion records a local diagnostic for an ignored repeated agent completion.
func (s Store) RecordRepeatedCompletion(
	repoID,
	taskID string,
	attempt int,
	opts RepeatedCompletionOptions,
) (Event, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Event{}, err
	}

	index := -1
	for i, run := range state.Runs {
		if run.Attempt == attempt {
			index = i
			break
		}
	}
	if index < 0 {
		return Event{}, fmt.Errorf("record repeated completion for task %s/%s: attempt %d was not found", repoID, taskID, attempt)
	}

	run := state.Runs[index]
	if run.Completion == nil {
		return Event{}, fmt.Errorf("record repeated completion for task %s/%s: attempt %d has no recorded completion", repoID, taskID, attempt)
	}

	now := s.nowUTC()
	event := runEvent(run, EventCompletionRepeated, now, run.Status, "")
	event.Message = "agent done repeated after completion already recorded; preserved first completion"
	event.RequestedSummary = strings.TrimSpace(opts.Summary)
	event.RequestedDescription = strings.TrimSpace(opts.Description)
	event.RequestedDetailedDescription = opts.DetailedDescription
	state.Events = append(state.Events, event)

	if err := s.save(state); err != nil {
		return Event{}, err
	}
	return event, nil
}

// StartReview appends a new running local review attempt for the built-in pipeline.
func (s Store) StartReview(repoID, taskID string) (ReviewAttempt, error) {
	return s.StartReviewWithOptions(repoID, taskID, StartReviewOptions{
		Pipeline: "default",
		Step:     "local-review",
	})
}

// StartReviewWithOptions appends a new running local review attempt.
func (s Store) StartReviewWithOptions(repoID, taskID string, opts StartReviewOptions) (ReviewAttempt, error) {
	pipeline := strings.TrimSpace(opts.Pipeline)
	if pipeline == "" {
		return ReviewAttempt{}, fmt.Errorf("start review attempt for task %s/%s: pipeline is required", repoID, taskID)
	}
	step := strings.TrimSpace(opts.Step)
	if step == "" {
		return ReviewAttempt{}, fmt.Errorf("start review attempt for task %s/%s: step is required", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}

	now := s.nowUTC()
	attempt := ReviewAttempt{
		Attempt:   nextReviewAttemptNumber(state),
		Status:    ReviewStatusRunning,
		Pipeline:  pipeline,
		Step:      step,
		StartedAt: now,
	}
	state.Reviews = append(state.Reviews, attempt)
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return attempt, nil
}

// RecordReviewStep appends an executed step record to a running review attempt.
func (s Store) RecordReviewStep(
	repoID,
	taskID string,
	attempt int,
	opts RecordReviewStepOptions,
) (ReviewAttempt, error) {
	step, err := normalizeReviewStep(ReviewStep{
		Kind:     opts.Kind,
		Name:     opts.Name,
		Command:  opts.Command,
		Args:     cloneStrings(opts.Args),
		ExitCode: cloneIntPointer(opts.ExitCode),
	})
	if err != nil {
		return ReviewAttempt{}, fmt.Errorf("record review step for task %s/%s: %w", repoID, taskID, err)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("record review step for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if state.Reviews[index].Status != ReviewStatusRunning {
		return ReviewAttempt{}, fmt.Errorf(
			"record review step for task %s/%s: review attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
			ReviewStatusRunning,
		)
	}

	state.Reviews[index].Step = step.Name
	state.Reviews[index].Steps = append(state.Reviews[index].Steps, step)
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// RecordReviewFinding appends a finding to a running review attempt.
func (s Store) RecordReviewFinding(
	repoID,
	taskID string,
	attempt int,
	finding ReviewFinding,
) (ReviewAttempt, error) {
	normalizedFinding, err := normalizeReviewFinding(finding)
	if err != nil {
		return ReviewAttempt{}, fmt.Errorf("record review finding for task %s/%s: %w", repoID, taskID, err)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("record review finding for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if state.Reviews[index].Status != ReviewStatusRunning {
		return ReviewAttempt{}, fmt.Errorf(
			"record review finding for task %s/%s: review attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
			ReviewStatusRunning,
		)
	}

	state.Reviews[index].Findings = append(state.Reviews[index].Findings, normalizedFinding)
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// FinishReview records the terminal status for a running review attempt.
func (s Store) FinishReview(repoID, taskID string, attempt int, status ReviewStatus) (ReviewAttempt, error) {
	if status == ReviewStatusRunning || !validReviewStatus(status) {
		return ReviewAttempt{}, fmt.Errorf("finish review attempt for task %s/%s: unsupported terminal status %q", repoID, taskID, status)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("finish review attempt for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if state.Reviews[index].Status != ReviewStatusRunning {
		return ReviewAttempt{}, fmt.Errorf(
			"finish review attempt for task %s/%s: review attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
			ReviewStatusRunning,
		)
	}

	now := s.nowUTC()
	finished := now
	state.Reviews[index].Status = status
	state.Reviews[index].FinishedAt = &finished
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// RecordFinalizationCommit records the commit created by task finalization.
func (s Store) RecordFinalizationCommit(repoID, taskID string, commit string) (Finalization, error) {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return Finalization{}, fmt.Errorf("record finalization commit for task %s/%s: commit is required", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Finalization{}, err
	}
	finalization := ensureFinalization(state.Finalization)
	if strings.TrimSpace(finalization.Commit) != "" {
		if finalization.Commit != commit {
			return Finalization{}, fmt.Errorf(
				"record finalization commit for task %s/%s: %w",
				repoID,
				taskID,
				ErrFinalizationConflict,
			)
		}
		return finalization, nil
	}

	now := s.nowUTC()
	finalization.Commit = commit
	finalization.CommittedAt = &now
	state.Finalization = &finalization
	if err := s.save(state); err != nil {
		return Finalization{}, err
	}
	return finalization, nil
}

// RecordFinalizationPush records that the finalization commit was pushed.
func (s Store) RecordFinalizationPush(repoID, taskID string, opts FinalizationPushOptions) (Finalization, error) {
	branch := strings.TrimSpace(opts.Branch)
	pushTarget := strings.TrimSpace(opts.PushTarget)
	if branch == "" {
		return Finalization{}, fmt.Errorf("record finalization push for task %s/%s: branch is required", repoID, taskID)
	}
	if !validPushTarget(pushTarget) {
		return Finalization{}, fmt.Errorf("record finalization push for task %s/%s: unsupported push target %q", repoID, taskID, pushTarget)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Finalization{}, err
	}
	finalization := ensureFinalization(state.Finalization)
	if strings.TrimSpace(finalization.Commit) == "" {
		return Finalization{}, fmt.Errorf("record finalization push for task %s/%s: finalization commit is required", repoID, taskID)
	}
	if finalization.PushedAt != nil {
		return finalization, nil
	}

	now := s.nowUTC()
	finalization.PushedAt = &now
	state.Finalization = &finalization
	state.Events = append(state.Events, Event{
		Type:       EventChangesPushed,
		At:         now,
		Branch:     branch,
		PushTarget: pushTarget,
	})
	if err := s.save(state); err != nil {
		return Finalization{}, err
	}
	return finalization, nil
}

// RecordFinalizationClose records that the backend task was closed.
func (s Store) RecordFinalizationClose(repoID, taskID string, opts FinalizationCloseOptions) (Finalization, error) {
	reason := strings.TrimSpace(opts.Reason)
	if reason == "" {
		return Finalization{}, fmt.Errorf("record finalization close for task %s/%s: reason is required", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Finalization{}, err
	}
	finalization := ensureFinalization(state.Finalization)
	if strings.TrimSpace(finalization.Commit) == "" {
		return Finalization{}, fmt.Errorf("record finalization close for task %s/%s: finalization commit is required", repoID, taskID)
	}
	if finalization.PushedAt == nil {
		return Finalization{}, fmt.Errorf("record finalization close for task %s/%s: finalization push is required", repoID, taskID)
	}
	if finalization.ClosedAt != nil {
		return finalization, nil
	}

	now := s.nowUTC()
	finalization.ClosedAt = &now
	state.Finalization = &finalization
	state.Events = append(state.Events, Event{
		Type:        EventTaskClosed,
		At:          now,
		CloseReason: reason,
	})
	if err := s.save(state); err != nil {
		return Finalization{}, err
	}
	return finalization, nil
}

// RecordFeatureBranchPR appends an idempotent audit event after the backend
// task has recorded a feature-branch PR URL.
func (s Store) RecordFeatureBranchPR(repoID, taskID string, opts FeatureBranchPROptions) (Event, error) {
	prURL := strings.TrimSpace(opts.PRURL)
	branch := strings.TrimSpace(opts.Branch)
	if prURL == "" {
		return Event{}, fmt.Errorf("record feature branch PR for task %s/%s: PR URL is required", repoID, taskID)
	}
	if branch == "" {
		return Event{}, fmt.Errorf("record feature branch PR for task %s/%s: branch is required", repoID, taskID)
	}

	eventType := EventPRCreated
	if opts.WasRecovered {
		eventType = EventPRRecovered
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Event{}, err
	}
	for _, event := range state.Events {
		if event.Type == eventType && strings.TrimSpace(event.PRURL) == prURL {
			return event, nil
		}
	}

	event := Event{
		Type:   eventType,
		At:     s.nowUTC(),
		Branch: branch,
		PRURL:  prURL,
	}
	state.Events = append(state.Events, event)
	if err := s.save(state); err != nil {
		return Event{}, err
	}
	return event, nil
}

// RecordFinalizationFailure appends a durable diagnostic for a failed task done
// publication/finalization attempt.
func (s Store) RecordFinalizationFailure(repoID, taskID string, cause error) (Event, error) {
	var message string
	if cause != nil {
		message = strings.TrimSpace(cause.Error())
	}
	if message == "" {
		return Event{}, fmt.Errorf("record finalization failure for task %s/%s: error is required", repoID, taskID)
	}

	return s.appendEvent(repoID, taskID, Event{
		Type:  EventFinalizationFailed,
		Error: message,
	})
}

// RecordTaskClosed appends an idempotent local audit event after a backend task
// is closed. PR facts are recorded when the closure followed a merged PR.
func (s Store) RecordTaskClosed(repoID, taskID string, opts TaskClosedOptions) (Event, error) {
	reason := strings.TrimSpace(opts.Reason)
	if reason == "" {
		return Event{}, fmt.Errorf("record task closed event for task %s/%s: reason is required", repoID, taskID)
	}
	prURL := strings.TrimSpace(opts.PRURL)
	observedState := strings.TrimSpace(opts.ObservedPRState)
	if reason == CloseReasonPRMerged && prURL == "" {
		return Event{}, fmt.Errorf("record task closed event for task %s/%s: PR URL is required for merged PR closure", repoID, taskID)
	}
	if reason == CloseReasonPRMerged && observedState == "" {
		return Event{}, fmt.Errorf("record task closed event for task %s/%s: observed PR state is required for merged PR closure", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Event{}, err
	}
	for _, event := range state.Events {
		if event.Type == EventTaskClosed &&
			strings.TrimSpace(event.CloseReason) == reason &&
			strings.TrimSpace(event.PRURL) == prURL &&
			strings.TrimSpace(event.ObservedPRState) == observedState {
			return event, nil
		}
	}

	event := Event{
		Type:            EventTaskClosed,
		At:              s.nowUTC(),
		CloseReason:     reason,
		PRURL:           prURL,
		ObservedPRState: observedState,
	}
	if err := validateEvent(event); err != nil {
		return Event{}, fmt.Errorf("record task closed event for task %s/%s: %w", repoID, taskID, err)
	}
	state.Events = append(state.Events, event)
	if err := s.save(state); err != nil {
		return Event{}, err
	}
	return event, nil
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

// LatestReview returns the highest-numbered review attempt from state.
func LatestReview(state TaskState) (ReviewAttempt, bool) {
	if len(state.Reviews) == 0 {
		return ReviewAttempt{}, false
	}

	latest := state.Reviews[0]
	for _, review := range state.Reviews[1:] {
		if review.Attempt > latest.Attempt {
			latest = review
		}
	}
	return latest, true
}

// LatestFinalizationFailure returns the latest recorded task done
// publication/finalization failure event from state.
func LatestFinalizationFailure(state TaskState) (Event, bool) {
	var latest Event
	for _, event := range state.Events {
		if event.Type != EventFinalizationFailed {
			continue
		}
		if latest.Type == "" || event.At.After(latest.At) {
			latest = event
		}
	}
	return latest, latest.Type != ""
}

// ActiveRun returns the latest attempt only when it is running.
func ActiveRun(state TaskState) (RunAttempt, bool) {
	latest, ok := LatestRun(state)
	if !ok || latest.Status != RunStatusRunning {
		return RunAttempt{}, false
	}
	return latest, true
}

// FinalizationFacts returns a value copy of any recorded finalization facts.
func FinalizationFacts(state TaskState) Finalization {
	if state.Finalization == nil {
		return Finalization{}
	}
	return ensureFinalization(state.Finalization)
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
	state.Events = append(state.Events, runEvent(updated, eventType, now, status, errorText))

	if err := s.save(state); err != nil {
		return RunAttempt{}, err
	}
	return updated, nil
}

func runEvent(run RunAttempt, eventType EventType, at time.Time, status RunStatus, errorText string) Event {
	return Event{
		Type:     eventType,
		At:       at,
		Attempt:  run.Attempt,
		Status:   status,
		Agent:    run.Agent,
		Branch:   run.Branch,
		Worktree: run.Worktree,
		Error:    strings.TrimSpace(errorText),
	}
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
	for _, review := range taskState.Reviews {
		if err := validateReview(review); err != nil {
			return err
		}
	}
	if err := validateFinalization(taskState.Finalization); err != nil {
		return fmt.Errorf("finalization is invalid: %w", err)
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
	if err := validateCommandArgsForSave(taskState); err != nil {
		return TaskState{}, err
	}
	return normalizeState(taskState, repoID, taskID), nil
}

func normalizeState(taskState TaskState, repoID, taskID string) TaskState {
	taskState.Version = schemaVersion
	taskState.RepoID = repoID
	taskState.TaskID = taskID
	if taskState.Finalization != nil {
		finalization := ensureFinalization(taskState.Finalization)
		taskState.Finalization = &finalization
	}
	return taskState
}

func validateRun(run RunAttempt) error {
	if run.Attempt <= 0 {
		return fmt.Errorf("run attempt must be positive, got %d", run.Attempt)
	}
	if !validRunStatus(run.Status) {
		return fmt.Errorf("run attempt %d has unsupported status %q", run.Attempt, run.Status)
	}
	if run.Completion != nil {
		if err := validateCompletion(*run.Completion); err != nil {
			return fmt.Errorf("run attempt %d has invalid completion: %w", run.Attempt, err)
		}
	}
	if run.ReviewFollowUp != nil {
		if err := validateReviewFollowUp(*run.ReviewFollowUp); err != nil {
			return fmt.Errorf("run attempt %d has invalid review follow-up: %w", run.Attempt, err)
		}
	}
	return nil
}

func validateCompletion(completion Completion) error {
	if strings.TrimSpace(completion.Summary) == "" {
		return errors.New("summary is required")
	}
	if strings.TrimSpace(completion.Description) == "" {
		return errors.New("description is required")
	}
	if strings.TrimSpace(completion.DetailedDescription) == "" {
		return errors.New("detailed_description is required")
	}
	if completion.CompletedAt.IsZero() {
		return errors.New("completed_at is required")
	}
	if strings.TrimSpace(completion.CommitError) != "" && strings.TrimSpace(completion.Commit) != "" {
		return errors.New("commit_error cannot be recorded with commit")
	}
	return nil
}

func validateReview(review ReviewAttempt) error {
	if review.Attempt <= 0 {
		return fmt.Errorf("review attempt must be positive, got %d", review.Attempt)
	}
	if !validReviewStatus(review.Status) {
		return fmt.Errorf("review attempt %d has unsupported status %q", review.Attempt, review.Status)
	}
	if strings.TrimSpace(review.Pipeline) == "" {
		return fmt.Errorf("review attempt %d requires pipeline", review.Attempt)
	}
	if strings.TrimSpace(review.Step) == "" {
		return fmt.Errorf("review attempt %d requires step", review.Attempt)
	}
	if review.StartedAt.IsZero() {
		return fmt.Errorf("review attempt %d requires started_at", review.Attempt)
	}
	if review.Status == ReviewStatusRunning && review.FinishedAt != nil {
		return fmt.Errorf("review attempt %d cannot have finished_at while running", review.Attempt)
	}
	if review.Status != ReviewStatusRunning && (review.FinishedAt == nil || review.FinishedAt.IsZero()) {
		return fmt.Errorf("review attempt %d requires finished_at for status %q", review.Attempt, review.Status)
	}
	for _, step := range review.Steps {
		if _, err := normalizeReviewStep(step); err != nil {
			return fmt.Errorf("review attempt %d has invalid step: %w", review.Attempt, err)
		}
	}
	for _, finding := range review.Findings {
		if _, err := normalizeReviewFinding(finding); err != nil {
			return fmt.Errorf("review attempt %d has invalid finding: %w", review.Attempt, err)
		}
	}
	return nil
}

func normalizeReviewStep(step ReviewStep) (ReviewStep, error) {
	step.Kind = strings.TrimSpace(step.Kind)
	step.Name = strings.TrimSpace(step.Name)
	step.Command = strings.TrimSpace(step.Command)
	step.Args = cloneStrings(step.Args)
	step.ExitCode = cloneIntPointer(step.ExitCode)

	if step.Kind == "" {
		return ReviewStep{}, errors.New("kind is required")
	}
	if step.Name == "" {
		return ReviewStep{}, errors.New("name is required")
	}
	if step.ExitCode != nil && *step.ExitCode < 0 {
		return ReviewStep{}, errors.New("exit_code cannot be negative")
	}
	return step, nil
}

func validateCommandArgsForSave(taskState TaskState) error {
	for _, run := range taskState.Runs {
		if err := validateCommandArgs(run.Args); err != nil {
			return fmt.Errorf("run attempt %d has invalid args: %w", run.Attempt, err)
		}
	}
	for _, review := range taskState.Reviews {
		for _, step := range review.Steps {
			if err := validateCommandArgs(step.Args); err != nil {
				return fmt.Errorf("review attempt %d step %q has invalid args: %w", review.Attempt, step.Name, err)
			}
		}
	}
	return nil
}

func validateCommandArgs(args []string) error {
	for index, arg := range args {
		if strings.HasPrefix(arg, " - ") && strings.Contains(arg, "\n") {
			return fmt.Errorf("arg %d cannot be a multi-line value starting with %q", index, " - ")
		}
	}
	return nil
}

func normalizeReviewFinding(finding ReviewFinding) (ReviewFinding, error) {
	finding.Type = FindingType(strings.TrimSpace(string(finding.Type)))
	finding.Title = strings.TrimSpace(finding.Title)
	finding.Description = strings.TrimSpace(finding.Description)
	finding.Step = strings.TrimSpace(finding.Step)
	finding.SuggestedAction = strings.TrimSpace(finding.SuggestedAction)
	finding.Waiver = strings.TrimSpace(finding.Waiver)
	finding.TaskProposal = strings.TrimSpace(finding.TaskProposal)
	finding.CreatedTaskID = strings.TrimSpace(finding.CreatedTaskID)

	if !validFindingType(finding.Type) {
		return ReviewFinding{}, fmt.Errorf("unsupported finding type %q", finding.Type)
	}
	if finding.Title == "" {
		return ReviewFinding{}, errors.New("title is required")
	}
	if finding.Description == "" {
		return ReviewFinding{}, errors.New("description is required")
	}
	if finding.Type == FindingTypeSeparateTask && finding.TaskProposal == "" {
		return ReviewFinding{}, errors.New("task_proposal is required for separate-task findings")
	}
	if finding.TargetedByRunAttempt < 0 {
		return ReviewFinding{}, errors.New("targeted_by_run_attempt cannot be negative")
	}
	return finding, nil
}

func normalizeReviewFollowUp(followUp *ReviewFollowUp) *ReviewFollowUp {
	if followUp == nil {
		return nil
	}
	clone := ReviewFollowUp{
		ReviewAttempt:  followUp.ReviewAttempt,
		FindingIndexes: cloneInts(followUp.FindingIndexes),
	}
	return &clone
}

func validateReviewFollowUp(followUp ReviewFollowUp) error {
	if followUp.ReviewAttempt <= 0 {
		return errors.New("review_attempt must be positive")
	}
	if len(followUp.FindingIndexes) == 0 {
		return errors.New("finding_indexes is required")
	}
	for _, index := range followUp.FindingIndexes {
		if index < 0 {
			return errors.New("finding index cannot be negative")
		}
	}
	return nil
}

func validateFinalization(finalization *Finalization) error {
	if finalization == nil {
		return nil
	}

	commit := strings.TrimSpace(finalization.Commit)
	if commit == "" {
		if finalization.CommittedAt != nil || finalization.PushedAt != nil || finalization.ClosedAt != nil {
			return errors.New("commit is required when any finalization timestamp is recorded")
		}
		return nil
	}
	if finalization.CommittedAt == nil || finalization.CommittedAt.IsZero() {
		return errors.New("committed_at is required when commit is recorded")
	}
	if finalization.PushedAt != nil && finalization.PushedAt.IsZero() {
		return errors.New("pushed_at must be non-zero when recorded")
	}
	if finalization.ClosedAt != nil && finalization.ClosedAt.IsZero() {
		return errors.New("closed_at must be non-zero when recorded")
	}
	if finalization.ClosedAt != nil && finalization.PushedAt == nil {
		return errors.New("pushed_at is required when closed_at is recorded")
	}
	return nil
}

func ensureFinalization(finalization *Finalization) Finalization {
	if finalization == nil {
		return Finalization{}
	}
	clone := *finalization
	clone.Commit = strings.TrimSpace(clone.Commit)
	return clone
}

func validateEvent(event Event) error {
	if !validEventType(event.Type) {
		return fmt.Errorf("unsupported event type %q", event.Type)
	}
	if event.Status != "" && !validRunStatus(event.Status) {
		return fmt.Errorf("event %q has unsupported run status %q", event.Type, event.Status)
	}
	if event.Type == EventChangesPushed && !validPushTarget(event.PushTarget) {
		return fmt.Errorf("event %q has unsupported push target %q", event.Type, event.PushTarget)
	}
	if event.Type == EventTaskClosed && strings.TrimSpace(event.CloseReason) == "" {
		return fmt.Errorf("event %q requires a close reason", event.Type)
	}
	if event.Type == EventFinalizationFailed && strings.TrimSpace(event.Error) == "" {
		return fmt.Errorf("event %q requires an error", event.Type)
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

func validReviewStatus(status ReviewStatus) bool {
	switch status {
	case ReviewStatusRunning, ReviewStatusBlocked, ReviewStatusFailed, ReviewStatusPassed, ReviewStatusAborted:
		return true
	default:
		return false
	}
}

func validFindingType(findingType FindingType) bool {
	switch findingType {
	case FindingTypeBlocking, FindingTypeAdvisory, FindingTypeSeparateTask:
		return true
	default:
		return false
	}
}

func validEventType(eventType EventType) bool {
	switch eventType {
	case EventWorktreeCreated, EventTaskBranchCreated, EventWorktreeReused, EventWorktreeRecreated, EventRunStarted, EventRunFinished, EventRunStartFailed, EventCompletionRecorded, EventCompletionRepeated, EventChangesPushed, EventPRCreated, EventPRRecovered, EventFinalizationFailed, EventTaskClosed:
		return true
	default:
		return false
	}
}

func validPushTarget(pushTarget string) bool {
	return pushTarget == PushTargetMain || pushTarget == PushTargetBranch
}

func nextAttemptNumber(state TaskState) int {
	latest, ok := LatestRun(state)
	if !ok {
		return 1
	}
	return latest.Attempt + 1
}

func nextReviewAttemptNumber(state TaskState) int {
	latest, ok := LatestReview(state)
	if !ok {
		return 1
	}
	return latest.Attempt + 1
}

func reviewAttemptIndex(state TaskState, attempt int) int {
	for i, review := range state.Reviews {
		if review.Attempt == attempt {
			return i
		}
	}
	return -1
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

func cloneInts(values []int) []int {
	if values == nil {
		return nil
	}
	clone := make([]int, len(values))
	copy(clone, values)
	return clone
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
