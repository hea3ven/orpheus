package workflow

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/readiness"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

const (
	dispatchSetupLockOperation        = "task run setup"
	dispatchFinalizationLockOperation = "task run finalization"
)

// DispatchBackend is the backend capability set needed to dispatch a task run.
type DispatchBackend interface {
	task.DispatchBackend
	task.Lister
}

// DispatchRunStore persists and reads task run facts.
type DispatchRunStore interface {
	Path(repoID, taskID string) (string, error)
	Load(repoID, taskID string) (taskstate.TaskState, error)
	LatestRun(repoID, taskID string) (taskstate.RunAttempt, bool, error)
	ActiveRun(repoID, taskID string) (taskstate.RunAttempt, bool, error)
	RecordSetupEvent(
		repoID,
		taskID string,
		eventType taskstate.EventType,
		opts taskstate.SetupEventOptions,
	) (taskstate.Event, error)
	StartRun(repoID, taskID string, opts taskstate.StartRunOptions) (taskstate.RunAttempt, error)
	TargetReviewFindings(repoID, taskID string, reviewAttempt int, findingIndexes []int, runAttempt int) (taskstate.ReviewAttempt, error)
	FinishRun(repoID, taskID string, attempt int, status taskstate.RunStatus) (taskstate.RunAttempt, error)
	FailRunStart(repoID, taskID string, attempt int, cause error) (taskstate.RunAttempt, error)
}

// DispatchCommand records the agent command selected by the caller.
type DispatchCommand struct {
	AgentName string
	Command   string
	Args      []string
}

// DispatchService prepares task run targets and records dispatch state.
type DispatchService struct {
	Paths    state.Paths
	RunStore DispatchRunStore
}

// DispatchStartOptions describes the task run to start.
type DispatchStartOptions struct {
	TaskID                 string
	Source                 task.RepositorySource
	Backend                DispatchBackend
	Command                DispatchCommand
	ResolveCommand         func() (DispatchCommand, error)
	ResolveFollowUpCommand func() (DispatchCommand, error)
	MainMode               bool
	RepoRootMode           bool
}

// DispatchStartResult reports the prepared task run.
type DispatchStartResult struct {
	Repository   task.Repository
	Task         task.Task
	Setup        gitmeta.TaskWorktreeSetupResult
	Command      DispatchCommand
	Attempt      taskstate.RunAttempt
	ExecutionDir string
}

type dispatchStartPlan struct {
	taskItem task.Task
	expected gitmeta.TaskWorktreeSetupResult
	followUp *dispatchFollowUpPlan
}

type dispatchFollowUpPlan struct {
	reviewAttempt  int
	findingIndexes []int
	targetKind     TargetKind
}

// DispatchFailureOptions describes how a failed dispatch attempt ended.
type DispatchFailureOptions struct {
	RepoID      string
	TaskID      string
	Attempt     int
	Cause       error
	StartFailed bool
}

// Start prepares the run target, marks the backend task in progress, and records
// a running attempt while holding the global mutation lock.
func (s DispatchService) Start(ctx context.Context, opts DispatchStartOptions) (DispatchStartResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.validate(); err != nil {
		return DispatchStartResult{}, err
	}

	var result DispatchStartResult
	err := state.WithGlobalMutationLock(s.Paths, dispatchSetupLockOperation, func() error {
		started, err := s.startLocked(ctx, opts)
		if err != nil {
			return err
		}
		result = started
		return nil
	})
	if err != nil {
		return DispatchStartResult{}, err
	}
	return result, nil
}

// Finish records a successful attached run while holding the global mutation lock.
func (s DispatchService) Finish(repoID string, taskID string, attempt int) error {
	if err := s.validate(); err != nil {
		return err
	}

	return state.WithGlobalMutationLock(s.Paths, dispatchFinalizationLockOperation, func() error {
		latest, ok, err := s.RunStore.LatestRun(repoID, taskID)
		if err != nil {
			return err
		}
		if ok && latest.Attempt == attempt && latest.Status == taskstate.RunStatusSucceeded && latest.Completion != nil {
			return nil
		}

		_, err = s.RunStore.FinishRun(repoID, taskID, attempt, taskstate.RunStatusSucceeded)
		return err
	})
}

// Fail records a failed attached run while holding the global mutation lock.
func (s DispatchService) Fail(opts DispatchFailureOptions) error {
	if err := s.validate(); err != nil {
		return err
	}

	return state.WithGlobalMutationLock(s.Paths, dispatchFinalizationLockOperation, func() error {
		if opts.StartFailed {
			_, err := s.RunStore.FailRunStart(opts.RepoID, opts.TaskID, opts.Attempt, opts.Cause)
			return err
		}
		_, err := s.RunStore.FinishRun(opts.RepoID, opts.TaskID, opts.Attempt, taskstate.RunStatusFailed)
		return err
	})
}

func (s DispatchService) validate() error {
	if s.RunStore == nil {
		return errors.New("task dispatch run store is required")
	}
	return nil
}

func (s DispatchService) startLocked(
	ctx context.Context,
	opts DispatchStartOptions,
) (DispatchStartResult, error) {
	if opts.MainMode && opts.RepoRootMode {
		return DispatchStartResult{}, errors.New("task dispatch cannot combine main mode and repo-root mode")
	}

	plan, err := s.validateStart(ctx, opts)
	if err != nil {
		return DispatchStartResult{}, err
	}

	command, err := resolveDispatchCommand(opts, plan.followUp != nil)
	if err != nil {
		return DispatchStartResult{}, err
	}

	setup, err := s.setupTarget(ctx, opts, plan.followUp)
	if err != nil {
		return DispatchStartResult{}, err
	}
	if err := opts.Backend.MarkInProgress(ctx, opts.TaskID, setup.Branch, setup.WorktreePath); err != nil {
		return DispatchStartResult{}, fmt.Errorf("mark task in progress: %w", err)
	}

	attempt, err := s.recordStart(opts, setup, command, plan.followUp)
	if err != nil {
		return DispatchStartResult{}, err
	}

	return DispatchStartResult{
		Repository:   opts.Source.Repository,
		Task:         plan.taskItem,
		Setup:        setup,
		Command:      command,
		Attempt:      attempt,
		ExecutionDir: plan.expected.WorktreePath,
	}, nil
}

func (s DispatchService) validateStart(
	ctx context.Context,
	opts DispatchStartOptions,
) (dispatchStartPlan, error) {
	if opts.Backend == nil {
		return dispatchStartPlan{}, errors.New("task dispatch backend is required")
	}

	taskItem, err := queryDispatchTask(ctx, opts.Source, opts.TaskID, opts.Backend)
	if err != nil {
		return dispatchStartPlan{}, err
	}
	if err := ensureDispatchParentEpicGate(ctx, opts.Backend, taskItem); err != nil {
		return dispatchStartPlan{}, err
	}

	repo := opts.Source.Repository
	if active, ok, err := s.RunStore.ActiveRun(repo.ID, opts.TaskID); err != nil {
		return dispatchStartPlan{}, fmt.Errorf("inspect task state: %w", err)
	} else if ok {
		return dispatchStartPlan{}, activeDispatchRunError(
			s.RunStore,
			repo.ID,
			opts.TaskID,
			active,
		)
	}

	reviewPlan, err := s.resolveReviewFollowUpPlan(repo.ID, opts.TaskID, repo, taskItem, opts)
	if err != nil {
		return dispatchStartPlan{}, err
	}

	expected, err := s.expectedSetup(opts, taskItem, reviewPlan)
	if err != nil {
		return dispatchStartPlan{}, err
	}
	if err := ensureDispatchEligible(taskItem, expected, repo, opts.MainMode, opts.RepoRootMode, reviewPlan != nil); err != nil {
		return dispatchStartPlan{}, err
	}
	if opts.MainMode || opts.RepoRootMode || followUpTargetIs(reviewPlan, TargetMainSolo) || followUpTargetIs(reviewPlan, TargetRepoRootTeam) {
		if err := ensureRepoRootDispatchAvailable(ctx, opts.Backend, repo, opts.TaskID, expected); err != nil {
			return dispatchStartPlan{}, err
		}
	}
	return dispatchStartPlan{taskItem: taskItem, expected: expected, followUp: reviewPlan}, nil
}

func ensureDispatchParentEpicGate(ctx context.Context, backend DispatchBackend, taskItem task.Task) error {
	if strings.TrimSpace(taskItem.Relations.ParentID) == "" {
		return nil
	}
	tasks, err := backend.List(ctx)
	if err != nil {
		return fmt.Errorf("inspect immediate parent epic: %w", err)
	}
	gate := readiness.EvaluateParentEpicGate(taskItem, tasks)
	if gate.State == readiness.ParentEpicGateAllowed {
		return nil
	}
	return fmt.Errorf("task %s is not eligible for dispatch: %s", taskItem.ID, gate.Detail())
}

func (s DispatchService) resolveReviewFollowUpPlan(
	repoID string,
	taskID string,
	repo task.Repository,
	taskItem task.Task,
	opts DispatchStartOptions,
) (*dispatchFollowUpPlan, error) {
	state, err := s.RunStore.Load(repoID, taskID)
	if err != nil {
		return nil, fmt.Errorf("inspect review state: %w", err)
	}
	latestReview, ok := taskstate.LatestReview(state)
	if !ok {
		return nil, nil
	}

	switch latestReview.Status {
	case taskstate.ReviewStatusBlocked:
		indexes := untargetedBlockingFindingIndexes(latestReview)
		if len(indexes) == 0 {
			return nil, fmt.Errorf(
				"latest review attempt %d for task %s has no untargeted blocking findings; run `orpheus task review %s` before another `orpheus task run %s`",
				latestReview.Attempt,
				taskID,
				taskID,
				taskID,
			)
		}
		targetKind, err := followUpTargetKind(repo, taskItem)
		if err != nil {
			return nil, err
		}
		if opts.MainMode && targetKind != TargetMainSolo {
			return nil, fmt.Errorf("task %s follow-up must preserve current %s target; retry without --main", taskID, targetKind.DisplayName())
		}
		if opts.RepoRootMode && targetKind != TargetRepoRootTeam {
			return nil, fmt.Errorf("task %s follow-up must preserve current %s target; retry without --repo-root", taskID, targetKind.DisplayName())
		}
		return &dispatchFollowUpPlan{
			reviewAttempt:  latestReview.Attempt,
			findingIndexes: indexes,
			targetKind:     targetKind,
		}, nil
	case taskstate.ReviewStatusAborted:
		return nil, fmt.Errorf("latest review attempt %d for task %s was aborted; rerun `orpheus task review %s`", latestReview.Attempt, taskID, taskID)
	case taskstate.ReviewStatusFailed:
		return nil, fmt.Errorf("latest review attempt %d for task %s failed operationally; rerun `orpheus task review %s`", latestReview.Attempt, taskID, taskID)
	case taskstate.ReviewStatusPassed:
		return nil, fmt.Errorf("latest review attempt %d for task %s passed; run `orpheus task done %s`", latestReview.Attempt, taskID, taskID)
	case taskstate.ReviewStatusRunning:
		return nil, fmt.Errorf("latest review attempt %d for task %s is still running; wait for it to finish or rerun `orpheus task review %s` after repairing state", latestReview.Attempt, taskID, taskID)
	default:
		return nil, fmt.Errorf("latest review attempt %d for task %s has unsupported status %q", latestReview.Attempt, taskID, latestReview.Status)
	}
}

func untargetedBlockingFindingIndexes(review taskstate.ReviewAttempt) []int {
	var indexes []int
	for index, finding := range review.Findings {
		if finding.Type != taskstate.FindingTypeBlocking {
			continue
		}
		if strings.TrimSpace(finding.Waiver) != "" || strings.TrimSpace(finding.CreatedTaskID) != "" {
			continue
		}
		if finding.TargetedByRunAttempt != 0 {
			continue
		}
		indexes = append(indexes, index)
	}
	return indexes
}

func followUpTargetKind(repo task.Repository, taskItem task.Task) (TargetKind, error) {
	metadata := taskItem.OrpheusMetadata()
	if !metadata.HasBranch || strings.TrimSpace(metadata.Branch) == "" {
		return TargetUnknown, fmt.Errorf("task %s follow-up cannot preserve target: %s is missing", taskItem.ID, task.MetadataBranch)
	}
	if !metadata.HasWorktree || strings.TrimSpace(metadata.Worktree) == "" {
		return TargetUnknown, fmt.Errorf("task %s follow-up cannot preserve target: %s is missing", taskItem.ID, task.MetadataWorktree)
	}

	worktree := cleanDispatchPath(metadata.Worktree)
	repoPath := cleanDispatchPath(repo.Path)
	branch := strings.TrimSpace(metadata.Branch)
	if worktree == repoPath {
		if branch == strings.TrimSpace(repo.DefaultBranch) {
			return TargetMainSolo, nil
		}
		return TargetRepoRootTeam, nil
	}
	return TargetWorktreeTeam, nil
}

func (s DispatchService) recordStart(
	opts DispatchStartOptions,
	setup gitmeta.TaskWorktreeSetupResult,
	command DispatchCommand,
	followUp *dispatchFollowUpPlan,
) (taskstate.RunAttempt, error) {
	setupEvent, hasSetupEvent, err := dispatchSetupEvent(setup.Lifecycle)
	if err != nil {
		return taskstate.RunAttempt{}, err
	}

	repo := opts.Source.Repository
	if hasSetupEvent {
		if _, err := s.RunStore.RecordSetupEvent(
			repo.ID,
			opts.TaskID,
			setupEvent,
			taskstate.SetupEventOptions{
				Branch:   setup.Branch,
				Worktree: setup.WorktreePath,
			},
		); err != nil {
			return taskstate.RunAttempt{}, fmt.Errorf("record setup event: %w", err)
		}
	}

	attempt, err := s.RunStore.StartRun(repo.ID, opts.TaskID, taskstate.StartRunOptions{
		Agent:          command.AgentName,
		Command:        command.Command,
		Args:           command.Args,
		Branch:         setup.Branch,
		Worktree:       setup.WorktreePath,
		ReviewFollowUp: taskstateReviewFollowUp(followUp),
	})
	if err != nil {
		if errors.Is(err, taskstate.ErrActiveRun) {
			return taskstate.RunAttempt{}, fmt.Errorf("%w; M3 cannot reconcile stale attached runs automatically", err)
		}
		return taskstate.RunAttempt{}, fmt.Errorf("record run start: %w", err)
	}
	if followUp != nil {
		if _, err := s.RunStore.TargetReviewFindings(repo.ID, opts.TaskID, followUp.reviewAttempt, followUp.findingIndexes, attempt.Attempt); err != nil {
			return taskstate.RunAttempt{}, fmt.Errorf("target review findings: %w", err)
		}
	}
	return attempt, nil
}

func taskstateReviewFollowUp(followUp *dispatchFollowUpPlan) *taskstate.ReviewFollowUp {
	if followUp == nil {
		return nil
	}
	return &taskstate.ReviewFollowUp{
		ReviewAttempt:  followUp.reviewAttempt,
		FindingIndexes: cloneInts(followUp.findingIndexes),
	}
}

func resolveDispatchCommand(opts DispatchStartOptions, followUp bool) (DispatchCommand, error) {
	if followUp && opts.ResolveFollowUpCommand != nil {
		return opts.ResolveFollowUpCommand()
	}
	if opts.ResolveCommand == nil {
		return opts.Command, nil
	}
	return opts.ResolveCommand()
}

func (s DispatchService) expectedSetup(
	opts DispatchStartOptions,
	taskItem task.Task,
	followUp *dispatchFollowUpPlan,
) (gitmeta.TaskWorktreeSetupResult, error) {
	if followUp != nil {
		return s.followUpExpectedSetup(opts, taskItem, followUp)
	}
	if opts.MainMode {
		return gitmeta.ExpectedRepoRoot(dispatchRepoRootOptions(opts.Source.Repository, false))
	}
	if opts.RepoRootMode {
		return gitmeta.ExpectedRepoRootTaskBranch(dispatchTaskWorktreeOptions(s.Paths, opts.Source.Repository, opts.TaskID, false))
	}
	return gitmeta.ExpectedTaskWorktree(dispatchTaskWorktreeOptions(s.Paths, opts.Source.Repository, opts.TaskID, false))
}

func (s DispatchService) followUpExpectedSetup(
	opts DispatchStartOptions,
	taskItem task.Task,
	followUp *dispatchFollowUpPlan,
) (gitmeta.TaskWorktreeSetupResult, error) {
	switch followUp.targetKind {
	case TargetMainSolo:
		return gitmeta.ExpectedRepoRoot(dispatchRepoRootOptions(opts.Source.Repository, false))
	case TargetRepoRootTeam:
		return gitmeta.ExpectedRepoRootTaskBranch(dispatchTaskWorktreeOptions(s.Paths, opts.Source.Repository, opts.TaskID, false))
	case TargetWorktreeTeam:
		return gitmeta.ExpectedTaskWorktree(dispatchTaskWorktreeOptions(s.Paths, opts.Source.Repository, opts.TaskID, false))
	default:
		return gitmeta.TaskWorktreeSetupResult{}, fmt.Errorf("task %s follow-up has unsupported target %q", taskItem.ID, followUp.targetKind)
	}
}

func (s DispatchService) setupTarget(
	ctx context.Context,
	opts DispatchStartOptions,
	followUp *dispatchFollowUpPlan,
) (gitmeta.TaskWorktreeSetupResult, error) {
	if opts.MainMode || followUpTargetIs(followUp, TargetMainSolo) {
		return gitmeta.SetupRepoRoot(ctx, dispatchRepoRootOptions(opts.Source.Repository, followUp != nil))
	}
	if opts.RepoRootMode || followUpTargetIs(followUp, TargetRepoRootTeam) {
		return gitmeta.SetupRepoRootTaskBranch(ctx, dispatchTaskWorktreeOptions(s.Paths, opts.Source.Repository, opts.TaskID, followUp != nil))
	}
	return gitmeta.SetupTaskWorktree(ctx, dispatchTaskWorktreeOptions(s.Paths, opts.Source.Repository, opts.TaskID, false))
}

func followUpTargetIs(followUp *dispatchFollowUpPlan, target TargetKind) bool {
	return followUp != nil && followUp.targetKind == target
}

func queryDispatchTask(
	ctx context.Context,
	source task.RepositorySource,
	taskID string,
	backend task.Getter,
) (task.Task, error) {
	taskItem, err := backend.Get(ctx, taskID)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			return task.Task{}, fmt.Errorf(
				"task was not found in repo %s (%s; prefix %s); check the task id or inspect the repo backend directory: %w",
				source.Repository.ID,
				source.Repository.Name,
				source.Repository.TaskIDPrefix,
				err,
			)
		}
		return task.Task{}, fmt.Errorf(
			"query repo %s (%s; prefix %s): %w",
			source.Repository.ID,
			source.Repository.Name,
			source.Repository.TaskIDPrefix,
			err,
		)
	}
	return taskItem, nil
}

func dispatchTaskWorktreeOptions(paths state.Paths, repo task.Repository, taskID string, allowDirty bool) gitmeta.TaskWorktreeOptions {
	return gitmeta.TaskWorktreeOptions{
		RepoID:        repo.ID,
		RepoName:      repo.Name,
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
		TaskID:        taskID,
		Paths:         paths,
		AllowDirty:    allowDirty,
	}
}

func dispatchRepoRootOptions(repo task.Repository, allowDirty bool) gitmeta.RepoRootOptions {
	return gitmeta.RepoRootOptions{
		RepoID:        repo.ID,
		RepoName:      repo.Name,
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
		AllowDirty:    allowDirty,
	}
}

func ensureDispatchEligible(
	taskItem task.Task,
	expected gitmeta.TaskWorktreeSetupResult,
	repo task.Repository,
	mainMode bool,
	repoRootMode bool,
	preserveCurrentTarget bool,
) error {
	metadata := taskItem.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return fmt.Errorf("task %s is not eligible for dispatch: %s is already set", taskItem.ID, task.MetadataPRURL)
	}

	switch taskItem.Status {
	case task.StatusOpen:
		if !preserveCurrentTarget && !mainMode && !repoRootMode && dispatchMetadataMatchesRepoRoot(metadata, repo) {
			return repoRootRetryRequiresMainError(taskItem.ID, metadata)
		}
		if !preserveCurrentTarget && !repoRootMode && dispatchMetadataMatchesRepoRootTaskBranch(metadata, repo) {
			return repoRootRetryRequiresRepoRootError(taskItem.ID, metadata)
		}
		return nil
	case task.StatusInProgress:
		if dispatchMetadataMatches(metadata, expected) {
			return nil
		}
		if !preserveCurrentTarget && !mainMode && !repoRootMode && dispatchMetadataMatchesRepoRoot(metadata, repo) {
			return repoRootRetryRequiresMainError(taskItem.ID, metadata)
		}
		if !preserveCurrentTarget && !repoRootMode && dispatchMetadataMatchesRepoRootTaskBranch(metadata, repo) {
			return repoRootRetryRequiresRepoRootError(taskItem.ID, metadata)
		}

		target := "the deterministic Orpheus branch/worktree"
		if mainMode {
			target = "the registered default branch/repo root"
		} else if repoRootMode {
			target = "the task branch/repo root"
		}
		return fmt.Errorf(
			"task %s is in_progress but is not tied to %s: %s",
			taskItem.ID,
			target,
			dispatchMetadataMismatchDetail(metadata, expected),
		)
	case task.StatusClosed:
		return fmt.Errorf("task %s is not eligible for dispatch: task is closed", taskItem.ID)
	default:
		return fmt.Errorf(
			"task %s is not eligible for dispatch: status %s is not open or Orpheus-owned in_progress",
			taskItem.ID,
			formatDispatchField(string(taskItem.Status)),
		)
	}
}

func repoRootRetryRequiresMainError(taskID string, metadata task.OrpheusMetadata) error {
	return fmt.Errorf(
		"task %s is tied to repo-root/default-branch metadata (%s=%q, %s=%q); retry with `orpheus task run --main %s`",
		taskID,
		task.MetadataBranch,
		metadata.Branch,
		task.MetadataWorktree,
		metadata.Worktree,
		taskID,
	)
}

func repoRootRetryRequiresRepoRootError(taskID string, metadata task.OrpheusMetadata) error {
	return fmt.Errorf(
		"task %s is tied to repo-root/task-branch metadata (%s=%q, %s=%q); retry with `orpheus task run --repo-root %s`",
		taskID,
		task.MetadataBranch,
		metadata.Branch,
		task.MetadataWorktree,
		metadata.Worktree,
		taskID,
	)
}

func dispatchMetadataMatches(metadata task.OrpheusMetadata, expected gitmeta.TaskWorktreeSetupResult) bool {
	return metadata.HasBranch && strings.TrimSpace(metadata.Branch) == expected.Branch &&
		metadata.HasWorktree && cleanDispatchPath(metadata.Worktree) == cleanDispatchPath(expected.WorktreePath)
}

func dispatchMetadataMatchesRepoRoot(metadata task.OrpheusMetadata, repo task.Repository) bool {
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	repoPath := cleanDispatchPath(repo.Path)
	if defaultBranch == "" || repoPath == "" {
		return false
	}
	return metadata.HasBranch && strings.TrimSpace(metadata.Branch) == defaultBranch &&
		metadata.HasWorktree && cleanDispatchPath(metadata.Worktree) == repoPath
}

func dispatchMetadataMatchesRepoRootTaskBranch(metadata task.OrpheusMetadata, repo task.Repository) bool {
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	repoPath := cleanDispatchPath(repo.Path)
	branch := strings.TrimSpace(metadata.Branch)
	if defaultBranch == "" || repoPath == "" || branch == "" {
		return false
	}
	return metadata.HasBranch && branch != defaultBranch &&
		metadata.HasWorktree && cleanDispatchPath(metadata.Worktree) == repoPath
}

func ensureRepoRootDispatchAvailable(
	ctx context.Context,
	backend DispatchBackend,
	repo task.Repository,
	currentTaskID string,
	expected gitmeta.TaskWorktreeSetupResult,
) error {
	tasks, err := backend.List(ctx)
	if err != nil {
		return fmt.Errorf("inspect repo-root/default-branch ownership: %w", err)
	}

	for _, taskItem := range tasks {
		if strings.TrimSpace(taskItem.ID) == currentTaskID || taskItem.Status == task.StatusClosed {
			continue
		}
		metadata := taskItem.OrpheusMetadata()
		if !dispatchMetadataMatches(metadata, expected) &&
			cleanDispatchPath(metadata.Worktree) != cleanDispatchPath(expected.WorktreePath) {
			continue
		}
		return fmt.Errorf(
			"repo %s (%s) already has non-closed task %s owning repo-root metadata (%s=%q, %s=%q); finish local review or clear that metadata before running another task from the repo root",
			repo.ID,
			repo.Name,
			taskItem.ID,
			task.MetadataBranch,
			metadata.Branch,
			task.MetadataWorktree,
			metadata.Worktree,
		)
	}
	return nil
}

func activeDispatchRunError(
	store DispatchRunStore,
	repoID string,
	taskID string,
	active taskstate.RunAttempt,
) error {
	statePath, pathErr := store.Path(repoID, taskID)
	if pathErr != nil {
		statePath = "the per-task Orpheus state file"
	}
	return fmt.Errorf(
		"latest run attempt %d is still running; "+
			"M3 cannot reconcile stale attached runs automatically; "+
			"wait for the attached agent to finish or repair %s manually",
		active.Attempt,
		statePath,
	)
}

func dispatchSetupEvent(lifecycle gitmeta.TaskWorktreeLifecycle) (taskstate.EventType, bool, error) {
	switch lifecycle {
	case gitmeta.TaskWorktreeLifecycleCreated:
		return taskstate.EventWorktreeCreated, true, nil
	case gitmeta.TaskWorktreeLifecycleTaskBranchCreated:
		return taskstate.EventTaskBranchCreated, true, nil
	case gitmeta.TaskWorktreeLifecycleReused:
		return "", false, nil
	case gitmeta.TaskWorktreeLifecycleRecreated:
		return taskstate.EventWorktreeRecreated, true, nil
	default:
		return "", false, fmt.Errorf("unknown worktree lifecycle %q", lifecycle)
	}
}

func dispatchMetadataMismatchDetail(metadata task.OrpheusMetadata, expected gitmeta.TaskWorktreeSetupResult) string {
	problems := make([]string, 0, 2)
	if !metadata.HasBranch {
		problems = append(problems, task.MetadataBranch+" is missing")
	} else if strings.TrimSpace(metadata.Branch) != expected.Branch {
		problems = append(problems, fmt.Sprintf("%s is %q, expected %q", task.MetadataBranch, metadata.Branch, expected.Branch))
	}

	if !metadata.HasWorktree {
		problems = append(problems, task.MetadataWorktree+" is missing")
	} else if strings.TrimSpace(metadata.Worktree) != expected.WorktreePath {
		problems = append(problems, fmt.Sprintf("%s is %q, expected %q", task.MetadataWorktree, metadata.Worktree, expected.WorktreePath))
	}

	if len(problems) == 0 {
		return "metadata does not match"
	}
	return strings.Join(problems, "; ")
}

func cloneInts(values []int) []int {
	if values == nil {
		return nil
	}
	clone := make([]int, len(values))
	copy(clone, values)
	return clone
}

func cleanDispatchPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func formatDispatchField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}
