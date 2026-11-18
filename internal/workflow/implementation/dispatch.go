package implementation

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/pathutil"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/readiness"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskbranch"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// DispatchBackend is the backend capability set needed to dispatch a task run.
type DispatchBackend interface {
	task.DispatchBackend
	task.Lister
}

// Dispatch selects implementation admission, reservation and attached execution.
// Its frozen inputs are invocation-local, never a durable executable plan.
type Dispatch struct {
	Paths           state.Paths
	history         taskstate.TaskState
	Logger          *slog.Logger
	UsageCaptureEnv map[string]string
	// Parents resolves authoritative parent facts, including across repositories.
	// Nil uses the dispatch backend for repository-local parent reads.
	Parents   task.Getter
	plan      *dispatchStartPlan
	prepared  *agent.PreparedExecution
	owner     *work.AttemptRef
	executed  bool
	Run       func(context.Context, ImplementationRun) agent.ExecutionResult
	BeforeRun func() error
	Results   phaseexec.AgentResults
}

// DispatchStartOptions describes the task run to start.
type DispatchStartOptions struct {
	// CheckInvocation retains workflow's route/command authorization at admission.
	CheckInvocation func(task.Task, taskstate.TaskState) error
	TaskID          string
	Source          task.RepositorySource
	Backend         DispatchBackend
	Task            task.Task
	AgentName       string
	MainMode        bool
	RepoRootMode    bool
}

// DispatchStartResult reports the prepared task run.
type DispatchStartResult struct {
	Repository   task.Repository
	Task         task.Task
	Setup        gitmeta.TaskWorktreeSetupResult
	Prepared     agent.PreparedExecution
	Attempt      taskstate.RunAttempt
	ExecutionDir string
}

type dispatchStartPlan struct {
	repository task.Repository
	taskItem   task.Task
	expected   gitmeta.TaskWorktreeSetupResult
	targetKind tasktarget.TargetKind
	followUp   *dispatchFollowUpPlan
	continuing bool
}

type dispatchFollowUpPlan struct {
	reviewAttempt          int
	findingIndexes         []int
	advisoryFindingIndexes []int
	targetKind             tasktarget.TargetKind
}

func (s Dispatch) startPhase(ctx context.Context, msg string, repoID string, taskID string, attrs ...slog.Attr) logging.Span {
	return logging.Start(ctx, s.Logger, msg, append(dispatchContextAttrs(repoID, taskID), attrs...)...)
}

func dispatchContextAttrs(repoID string, taskID string) []slog.Attr {
	return []slog.Attr{
		slog.String("component", "workflow"),
		slog.String("operation", "task_dispatch"),
		slog.String("repo_id", repoID),
		slog.String("task_id", taskID),
	}
}

func (s Dispatch) validateStartPhase(ctx context.Context, opts DispatchStartOptions) (dispatchStartPlan, error) {
	span := s.startPhase(ctx, "dispatch setup", opts.Source.Repository.ID, opts.TaskID)
	plan, err := s.validateStart(ctx, opts)
	if err != nil {
		span.FinishError(ctx, err)
		return dispatchStartPlan{}, err
	}
	span.Finish(ctx, logging.StatusSuccess,
		slog.String("target_kind", string(plan.targetKind)),
		slog.Bool("review_follow_up", plan.followUp != nil),
	)
	return plan, nil
}

func (s Dispatch) resolveCommandPhase(
	ctx context.Context,
	opts DispatchStartOptions,
	plan dispatchStartPlan,
) (agent.PreparedExecution, error) {
	span := s.startPhase(ctx, "agent command resolution", opts.Source.Repository.ID, opts.TaskID)
	config, err := agent.LoadConfig(s.Paths)
	if err != nil {
		span.FinishError(ctx, err)
		return agent.PreparedExecution{}, err
	}
	preparation := agent.ImplementationPreparation{
		Profile: opts.AgentName, SessionName: dispatchSessionName(plan.taskItem, plan.followUp),
		Workdir: plan.expected.WorktreePath, UsageEnvironment: s.usageCaptureEnvironment(),
	}
	if plan.followUp != nil {
		preparation.RepairHistory = &s.history
	}
	prepared, err := agent.PrepareImplementation(config, preparation)
	if err != nil {
		span.FinishError(ctx, err)
		return agent.PreparedExecution{}, err
	}
	execution := prepared.Execution()
	span.Finish(ctx, logging.StatusSuccess,
		slog.String("agent", execution.Agent),
		slog.String("profile", execution.Profile),
		slog.String("harness", execution.Harness),
		slog.String("launch_mode", dispatchLaunchMode(execution.Launch)),
	)
	return prepared, nil
}

func dispatchSessionName(taskItem task.Task, followUp *dispatchFollowUpPlan) string {
	if followUp != nil {
		return taskItem.FollowUpSessionName()
	}
	return taskItem.SessionName()
}

func (s Dispatch) usageCaptureEnvironment() map[string]string {
	if s.UsageCaptureEnv != nil {
		return s.UsageCaptureEnv
	}
	return agent.UsageCaptureEnvironment()
}

func dispatchLaunchMode(launch *taskstate.AgentLaunch) string {
	if launch == nil {
		return "fresh"
	}
	return string(launch.Mode)
}

func (s Dispatch) validateStart(
	ctx context.Context,
	opts DispatchStartOptions,
) (dispatchStartPlan, error) {
	if err := validateDispatchStartOptions(opts); err != nil {
		return dispatchStartPlan{}, err
	}
	taskItem, err := resolveDispatchStartTask(ctx, opts)
	if err != nil {
		return dispatchStartPlan{}, err
	}
	if err := s.validateInitialDispatchParent(ctx, opts.Backend, taskItem); err != nil {
		return dispatchStartPlan{}, err
	}
	lockedTarget, hasLockedTarget, err := s.resolveDispatchStartTargetLock(opts, taskItem)
	if err != nil {
		return dispatchStartPlan{}, err
	}
	return s.validateDispatchStartTarget(ctx, opts, taskItem, lockedTarget, hasLockedTarget)
}

func validateDispatchStartOptions(opts DispatchStartOptions) error {
	if err := validateDispatchTargetMode(opts); err != nil {
		return err
	}
	if opts.Backend == nil {
		return errors.New("task dispatch backend is required")
	}
	return nil
}

func validateDispatchTargetMode(opts DispatchStartOptions) error {
	if opts.MainMode {
		return errors.New("--main is no longer supported; use --repo-root to work in the registered repository root and publish through a pull request")
	}
	return nil
}

func resolveDispatchStartTask(ctx context.Context, opts DispatchStartOptions) (task.Task, error) {
	taskItem := opts.Task.Clone()
	if taskItem.ID == "" {
		var err error
		taskItem, err = queryDispatchTask(ctx, opts.Source, opts.TaskID, opts.Backend)
		if err != nil {
			return task.Task{}, err
		}
	}
	if taskItem.ID != opts.TaskID {
		return task.Task{}, fmt.Errorf("task dispatch preloaded task id %q does not match requested task id %q", taskItem.ID, opts.TaskID)
	}
	return taskItem, nil
}

// Initial eligibility may have been read before another command blocked the
// parent. Recheck under the dispatch mutation lock, never using related rows
// from the preloaded task. Existing in-progress retries retain their policy.
func (s Dispatch) validateInitialDispatchParent(ctx context.Context, backend task.Getter, item task.Task) error {
	parentID := strings.TrimSpace(item.Relations.ParentID)
	if item.Status != task.StatusOpen || parentID == "" {
		return nil
	}
	reader := s.Parents
	if reader == nil {
		reader = backend
	}
	parent, err := reader.Get(ctx, parentID)
	if err != nil {
		return fmt.Errorf("cannot inspect immediate parent epic %s before dispatching %s: %w", parentID, item.ID, err)
	}
	gate := readiness.EvaluateParentEpicGate(item, []task.Task{parent})
	if gate.State != readiness.ParentEpicGateAllowed {
		return fmt.Errorf("task %s is not eligible for dispatch: %s", item.ID, gate.Detail())
	}
	return nil
}

func (s Dispatch) resolveDispatchStartTargetLock(
	opts DispatchStartOptions,
	taskItem task.Task,
) (tasktarget.Target, bool, error) {
	repo := opts.Source.Repository
	if active, ok := taskstate.ActiveRun(s.history); ok {
		return tasktarget.Target{}, false, s.activeDispatchRunError(repo.ID, opts.TaskID, active)
	}
	state := s.history
	if opts.CheckInvocation != nil {
		if err := opts.CheckInvocation(taskItem, state); err != nil {
			return tasktarget.Target{}, false, err
		}
	}

	if _, hasRun := taskstate.LatestRun(state); hasRun && strings.TrimSpace(state.BaselineCommit) == "" {
		return tasktarget.Target{}, false, fmt.Errorf("task %s has prior run history but no recorded task baseline; preserve the checkout and explicitly repair the baseline before retrying", opts.TaskID)
	}
	lockedTarget, hasLockedTarget, permitRepoRootRetry, err := resolveDispatchLockedTarget(repo, taskItem, state, s.Paths, opts.RepoRootMode)
	if err != nil {
		return tasktarget.Target{}, false, err
	}
	if hasLockedTarget && opts.RepoRootMode && !permitRepoRootRetry {
		return tasktarget.Target{}, false, fmt.Errorf(
			"task %s already has target branch %q and worktree %q; retry without --repo-root",
			opts.TaskID,
			lockedTarget.Branch,
			lockedTarget.Worktree,
		)
	}
	return lockedTarget, hasLockedTarget, nil
}

func (s Dispatch) validateDispatchStartTarget(
	ctx context.Context,
	opts DispatchStartOptions,
	taskItem task.Task,
	lockedTarget tasktarget.Target,
	hasLockedTarget bool,
) (dispatchStartPlan, error) {
	repo := opts.Source.Repository
	reviewPlan, err := s.resolveReviewFollowUpPlan(opts.TaskID, lockedTarget, hasLockedTarget)
	if err != nil {
		return dispatchStartPlan{}, err
	}
	expected, targetKind, err := s.expectedSetup(opts, taskItem, lockedTarget, hasLockedTarget, reviewPlan)
	if err != nil {
		return dispatchStartPlan{}, err
	}
	if targetKind == tasktarget.TargetMainSolo {
		return dispatchStartPlan{}, errors.New("recorded repository-root context is on the default branch; preserve its files and repair the task context before dispatching on a task branch")
	}
	continuing := s.continuingRepoRootRun(taskItem, expected, targetKind)
	snapshot := s.history
	restarted := snapshot.Work != nil && snapshot.Work.ActiveWorkflow() != nil && (snapshot.Work.ActiveWorkflow().ResetRequestedAt != nil || snapshot.CurrentWorkflow > 0 && snapshot.Work.ActiveWorkflow().Context() != nil)
	if err := ensureDispatchEligible(taskItem, expected, repo, opts.RepoRootMode, reviewPlan != nil || continuing || restarted); err != nil {
		return dispatchStartPlan{}, err
	}
	if targetKind == tasktarget.TargetWorktreeTeam || targetKind == tasktarget.TargetRepoRootTeam {
		if err := tasktarget.CheckBranchOwnership(ctx, opts.Backend, opts.TaskID, expected.Branch); err != nil {
			return dispatchStartPlan{}, err
		}
	}
	if targetKind == tasktarget.TargetMainSolo || targetKind == tasktarget.TargetRepoRootTeam {
		if err := tasktarget.CheckRepoRootAvailable(ctx, opts.Backend, repo, opts.TaskID, expected); err != nil {
			return dispatchStartPlan{}, err
		}
	}
	return dispatchStartPlan{repository: repo, taskItem: taskItem, expected: expected, targetKind: targetKind, followUp: reviewPlan, continuing: continuing}, nil
}

// Continuing an unfinished implementation reuses the files, not the agent session.
// Backend metadata alone cannot authorize dirty reuse without local run history.
func (s Dispatch) continuingRepoRootRun(taskItem task.Task, expected gitmeta.TaskWorktreeSetupResult, targetKind tasktarget.TargetKind) bool {
	if targetKind != tasktarget.TargetMainSolo && targetKind != tasktarget.TargetRepoRootTeam {
		return false
	}
	latest, ok := taskstate.LatestRun(s.history)
	if !ok || latest.Status == taskstate.RunStatusSucceeded && latest.Completion != nil {
		return false
	}
	switch latest.Status {
	case taskstate.RunStatusSucceeded, taskstate.RunStatusFailed, taskstate.RunStatusInterrupted:
	default:
		return false
	}
	facts, hasFacts := taskstate.GitFactsFor(s.history)
	directory, hasDirectory := taskstate.WorkDirectoryFor(s.history)
	return hasFacts && hasDirectory && strings.TrimSpace(facts.Branch) == expected.Branch && cleanDispatchPath(facts.Worktree) == cleanDispatchPath(expected.WorktreePath) && cleanDispatchPath(directory.Path) == cleanDispatchPath(expected.WorktreePath) && dispatchMetadataMatches(taskItem.OrpheusMetadata(), expected)
}

func dispatchLockedTarget(repo task.Repository, state taskstate.TaskState) (tasktarget.Target, bool, error) {
	locked, ok := taskstate.GitFactsFor(state)
	if !ok {
		return tasktarget.Target{}, false, nil
	}
	branch := strings.TrimSpace(locked.Branch)
	worktree := cleanDispatchPath(locked.Worktree)
	kind := tasktarget.ClassifyRunTarget(repo, branch, worktree)
	if kind == tasktarget.TargetUnknown {
		return tasktarget.Target{}, false, fmt.Errorf(
			"task %s has unsupported taskstate target branch %q and worktree %q",
			state.TaskID,
			locked.Branch,
			locked.Worktree,
		)
	}
	return tasktarget.Target{
		Kind:     kind,
		Branch:   branch,
		Worktree: worktree,
	}, true, nil
}

// resolveDispatchLockedTarget prefers local Git facts and falls back to backend
// metadata when setup completed before taskstate persistence. A recovered
// main/repo-root target permits the same explicit mode on its first retry;
// persistent local facts retain the normal retry-mode guard.
func resolveDispatchLockedTarget(
	repo task.Repository,
	taskItem task.Task,
	state taskstate.TaskState,
	paths state.Paths,
	repoRootMode bool,
) (tasktarget.Target, bool, bool, error) {
	locked, ok, err := dispatchLockedTarget(repo, state)
	if err != nil || ok {
		return locked, ok, false, err
	}

	recorded, ok, err := dispatchRecordedMetadataTarget(repo, taskItem, paths)
	if err != nil || !ok {
		return recorded, ok, false, err
	}
	if !repoRootMode && recorded.Kind == tasktarget.TargetMainSolo {
		return tasktarget.Target{}, false, false, nil
	}
	return recorded, true, recorded.Kind == tasktarget.TargetMainSolo || recorded.Kind == tasktarget.TargetRepoRootTeam, nil
}

// dispatchRecordedMetadataTarget recovers the target persisted by the backend
// when Git setup and MarkInProgress completed but local taskstate persistence
// did not. It never renders current configuration, so recorded targets remain
// authoritative after a template change.
func dispatchRecordedMetadataTarget(repo task.Repository, taskItem task.Task, paths state.Paths) (tasktarget.Target, bool, error) {
	metadata := taskItem.OrpheusMetadata()
	if !metadata.HasBranch && !metadata.HasWorktree {
		return tasktarget.Target{}, false, nil
	}
	if !metadata.HasBranch || strings.TrimSpace(metadata.Branch) == "" {
		return tasktarget.Target{}, false, fmt.Errorf("task %s has incomplete recorded metadata: %s is missing", taskItem.ID, task.MetadataBranch)
	}
	if !metadata.HasWorktree || strings.TrimSpace(metadata.Worktree) == "" {
		return tasktarget.Target{}, false, fmt.Errorf("task %s has incomplete recorded metadata: %s is missing", taskItem.ID, task.MetadataWorktree)
	}

	targets, err := tasktarget.ExpectedTargetsForTaskBranch(repo, taskItem.ID, metadata.Branch, paths)
	if err != nil {
		return tasktarget.Target{}, false, fmt.Errorf("task %s has invalid recorded metadata target: %w", taskItem.ID, err)
	}
	if strings.TrimSpace(metadata.Branch) == targets.MainSolo.Branch &&
		cleanDispatchPath(metadata.Worktree) == targets.MainSolo.Worktree {
		return targets.MainSolo, true, nil
	}
	target, err := tasktarget.ClassifyMetadataTarget(metadata, targets)
	if err != nil {
		return tasktarget.Target{}, false, fmt.Errorf("task %s has invalid recorded metadata target: %w", taskItem.ID, err)
	}
	return target, true, nil
}

func (s Dispatch) resolveReviewFollowUpPlan(
	taskID string,
	lockedTarget tasktarget.Target,
	hasLockedTarget bool,
) (*dispatchFollowUpPlan, error) {
	state := s.history
	latestReview, ok := taskstate.LatestReview(state)
	if !ok {
		return nil, nil
	}
	if state.Work != nil && state.Work.ActiveWorkflow() != nil {
		if effort, ok := state.Work.ActiveWorkflow().LastPhase().(*work.ImplementationPhase); ok && len(effort.TargetFindings) == 0 {
			return nil, nil
		}
	}

	switch latestReview.Status {
	case taskstate.ReviewStatusBlocked:
		return blockedReviewFollowUpPlan(taskID, state, latestReview, lockedTarget, hasLockedTarget)
	case taskstate.ReviewStatusAborted:
		return nil, fmt.Errorf("latest review attempt %d for task %s was aborted; rerun `orpheus task run %s`", latestReview.Attempt, taskID, taskID)
	case taskstate.ReviewStatusFailed:
		return nil, fmt.Errorf("latest review attempt %d for task %s failed operationally; rerun `orpheus task run %s`", latestReview.Attempt, taskID, taskID)
	case taskstate.ReviewStatusPassed:
		return nil, fmt.Errorf("latest review attempt %d for task %s passed; run `orpheus task done %s`", latestReview.Attempt, taskID, taskID)
	case taskstate.ReviewStatusRunning:
		return nil, fmt.Errorf("latest review attempt %d for task %s is still running; wait for it to finish or rerun `orpheus task run %s` after repairing state", latestReview.Attempt, taskID, taskID)
	case taskstate.ReviewStatusWaitingForManual:
		return nil, fmt.Errorf(
			"latest review attempt %d for task %s is waiting for manual step %q; run `orpheus task run %s` to resume it",
			latestReview.Attempt,
			taskID,
			latestReview.Step,
			taskID,
		)
	default:
		return nil, fmt.Errorf("latest review attempt %d for task %s has unsupported status %q", latestReview.Attempt, taskID, latestReview.Status)
	}
}

func blockedReviewFollowUpPlan(
	taskID string,
	state taskstate.TaskState,
	latestReview taskstate.ReviewAttempt,
	lockedTarget tasktarget.Target,
	hasLockedTarget bool,
) (*dispatchFollowUpPlan, error) {
	if latestReview.AutomatedBlockerDecisionInterrupted {
		return nil, interruptedAutomatedBlockerDecisionError(latestReview, taskID)
	}
	indexes, followUpEligible := taskstate.UntargetedBlockingFindingIndexesForFollowUpInState(state, latestReview)
	if !followUpEligible {
		return nil, unkeptAutomatedBlockerDecisionError(latestReview, taskID)
	}
	if len(indexes) == 0 {
		return nil, fmt.Errorf(
			"latest review attempt %d for task %s has no untargeted blocking findings; rerun `orpheus task run %s` after repairing state",
			latestReview.Attempt,
			taskID,
			taskID,
		)
	}
	if !hasLockedTarget {
		return nil, fmt.Errorf("task %s follow-up cannot preserve target: taskstate target is missing", taskID)
	}
	return &dispatchFollowUpPlan{
		reviewAttempt:          latestReview.Attempt,
		findingIndexes:         indexes,
		advisoryFindingIndexes: taskstate.EligibleAdvisoryFindingIndexes(latestReview),
		targetKind:             lockedTarget.Kind,
	}, nil
}

func interruptedAutomatedBlockerDecisionError(latestReview taskstate.ReviewAttempt, taskID string) error {
	return fmt.Errorf(
		"latest review attempt %d for task %s has interrupted automated blocker decisions; run `orpheus task run %s` to recover the persisted findings and record explicit dispositions",
		latestReview.Attempt,
		taskID,
		taskID,
	)
}

func unkeptAutomatedBlockerDecisionError(latestReview taskstate.ReviewAttempt, taskID string) error {
	return fmt.Errorf(
		"latest review attempt %d for task %s has automated blockers without an explicit keep decision; run `orpheus task run %s` to recover the persisted findings and record explicit dispositions",
		latestReview.Attempt,
		taskID,
		taskID,
	)
}

func taskstateReviewFollowUp(followUp *dispatchFollowUpPlan) *taskstate.ReviewFollowUp {
	if followUp == nil {
		return nil
	}
	return &taskstate.ReviewFollowUp{
		ReviewAttempt:          followUp.reviewAttempt,
		FindingIndexes:         cloneInts(followUp.findingIndexes),
		AdvisoryFindingIndexes: cloneInts(followUp.advisoryFindingIndexes),
	}
}

func (s Dispatch) expectedSetup(
	opts DispatchStartOptions,
	taskItem task.Task,
	lockedTarget tasktarget.Target,
	hasLockedTarget bool,
	followUp *dispatchFollowUpPlan,
) (gitmeta.TaskWorktreeSetupResult, tasktarget.TargetKind, error) {
	targetKind := tasktarget.TargetWorktreeTeam
	branch := ""
	switch {
	case hasLockedTarget:
		targetKind = lockedTarget.Kind
		branch = lockedTarget.Branch
	case followUp != nil:
		targetKind = followUp.targetKind
		branch = lockedTarget.Branch
	case opts.MainMode || opts.RepoRootMode:
		targetKind = tasktarget.TargetRepoRootTeam
	}

	if branch == "" {
		targets, err := tasktarget.ExpectedTargetsForTaskItem(opts.Source.Repository, taskItem, s.Paths)
		if err != nil {
			return gitmeta.TaskWorktreeSetupResult{}, tasktarget.TargetUnknown, err
		}
		return expectedDispatchSetup(targets, targetKind, taskItem.ID)
	}
	targets, err := tasktarget.ExpectedTargetsForTaskBranch(opts.Source.Repository, taskItem.ID, branch, s.Paths)
	if err != nil {
		return gitmeta.TaskWorktreeSetupResult{}, tasktarget.TargetUnknown, err
	}
	return expectedDispatchSetup(targets, targetKind, taskItem.ID)
}

func expectedDispatchSetup(targets tasktarget.ExpectedTargets, targetKind tasktarget.TargetKind, taskID string) (gitmeta.TaskWorktreeSetupResult, tasktarget.TargetKind, error) {
	var target tasktarget.Target
	switch targetKind {
	case tasktarget.TargetMainSolo:
		target = targets.MainSolo
	case tasktarget.TargetRepoRootTeam:
		target = targets.RepoRootTeam
	case tasktarget.TargetWorktreeTeam:
		target = targets.WorktreeTeam
	default:
		return gitmeta.TaskWorktreeSetupResult{}, tasktarget.TargetUnknown, fmt.Errorf("task %s has unsupported target %q", taskID, targetKind)
	}
	return gitmeta.TaskWorktreeSetupResult{Branch: target.Branch, WorktreePath: target.Worktree}, targetKind, nil
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

func ensureDispatchEligible(
	taskItem task.Task,
	expected gitmeta.TaskWorktreeSetupResult,
	repo task.Repository,
	repoRootMode bool,
	preserveCurrentTarget bool,
) error {
	metadata := taskItem.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" && !preserveCurrentTarget {
		return fmt.Errorf("task %s is not eligible for dispatch: %s is already set", taskItem.ID, task.MetadataPRURL)
	}

	switch taskItem.Status {
	case task.StatusOpen:
		if !preserveCurrentTarget && !repoRootMode && dispatchMetadataMatchesRepoRoot(metadata, repo) {
			return repoRootRetryRequiresRepoRootError(taskItem.ID, metadata)
		}
		if !preserveCurrentTarget && !repoRootMode && dispatchMetadataMatchesRepoRootTaskBranch(metadata, repo) {
			return repoRootRetryRequiresRepoRootError(taskItem.ID, metadata)
		}
		return nil
	case task.StatusInProgress:
		if dispatchMetadataMatches(metadata, expected) {
			return nil
		}
		if !preserveCurrentTarget && !repoRootMode && dispatchMetadataMatchesRepoRoot(metadata, repo) {
			return repoRootRetryRequiresRepoRootError(taskItem.ID, metadata)
		}
		if !preserveCurrentTarget && !repoRootMode && dispatchMetadataMatchesRepoRootTaskBranch(metadata, repo) {
			return repoRootRetryRequiresRepoRootError(taskItem.ID, metadata)
		}

		target := "the deterministic Orpheus branch/worktree"
		if repoRootMode {
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

func repoRootRetryRequiresRepoRootError(taskID string, metadata task.OrpheusMetadata) error {
	return fmt.Errorf(
		"task %s is tied to repository-root metadata (%s=%q, %s=%q); retry with `orpheus task run --repo-root %s`",
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

func (s Dispatch) activeDispatchRunError(
	repoID string,
	taskID string,
	active taskstate.RunAttempt,
) error {
	statePath, pathErr := taskstate.NewStore(s.Paths).Path(repoID, taskID)
	if pathErr != nil {
		statePath = "the per-task Orpheus state file"
	}
	return fmt.Errorf(
		"latest run attempt %d is still running; "+
			"attached supervision has not recorded a terminal boundary; "+
			"wait for the attached agent to finish or repair %s manually",
		active.Attempt,
		statePath,
	)
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
	if filepath.IsAbs(path) {
		if canonicalPath, err := pathutil.CanonicalAbs(path); err == nil {
			return canonicalPath
		}
	}
	return filepath.Clean(path)
}

func validateImplementationReference(repo task.Repository, item task.Task) error {
	title := publication.RequiresExternalRef(repo.TitleTemplate)
	branch := taskbranch.RequiresExternalRef(repo.BranchTemplate)
	if (!title && !branch) || strings.TrimSpace(item.ExternalRef) != "" {
		return nil
	}
	requirement := "publication title template"
	if branch && title {
		requirement = "publication title and task branch templates"
	} else if branch {
		requirement = "task branch template"
	}
	return fmt.Errorf("task run %s: %s requires a task external reference; set it with `orpheus task edit %s --external-ref <reference>`", item.ID, requirement, item.ID)
}

func formatDispatchField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}
