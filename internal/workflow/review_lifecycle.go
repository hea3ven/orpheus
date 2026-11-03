package workflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/work"
)

// ReviewLifecycleBackend is the backend capability set needed by the review
// lifecycle. It intentionally describes backend semantics, not a CLI adapter.
type ReviewLifecycleBackend interface {
	DispatchBackend
	FinalizationBackend
	task.CreateMutator
}

// ReviewLifecycleBackendFactory creates a review-lifecycle backend for one repository.
type ReviewLifecycleBackendFactory func(task.RepositorySource) (ReviewLifecycleBackend, error)

// ReviewLifecycleStore persists and reads lifecycle state used by review orchestration.
type ReviewLifecycleStore interface {
	ImplementationRunRecoveryStore
	DispatchRunStore
	review.PipelineStore
	ReviewChildPIDStore
	StartReviewWithOptions(repoID, taskID string, opts taskstate.StartReviewOptions) (taskstate.ReviewAttempt, error)
	RecordRepairBudgetStop(work.AttemptRef, int, int) (taskstate.ReviewAttempt, error)
	RecordReviewFailure(work.AttemptRef, error) (taskstate.ReviewAttempt, error)
	RecordReviewAbort(work.AttemptRef, string) (taskstate.ReviewAttempt, error)
	PreparePublication(work.AttemptRef, work.PublicationTarget, string) (work.AttemptRef, error)
	RecordFindingCreatedTask(work.AttemptRef, work.FindingRef, string) (taskstate.ReviewAttempt, error)
	InterruptReviewExecution(repoID, taskID string, attempt int, stepName string, opts taskstate.InterruptReviewExecutionOptions) (taskstate.ReviewAttempt, error)
}

// ImplementationRun describes an attached implementation/fix run that the
// frontend must execute using its chosen process launcher and streams.
type ImplementationRun struct {
	RepoID string
	TaskID string
	Start  DispatchStartResult
	Prompt string
}

// ImplementationRunner executes an attached implementation/fix agent.
type ImplementationRunner interface {
	RunImplementation(ctx context.Context, run ImplementationRun) agent.ExecutionResult
}

// ReviewPipelinePresentation contains frontend-owned review presentation hooks.
// Core pipeline execution facts are assembled by workflow from ReviewAttemptContext.
type ReviewPipelinePresentation struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	OpenOutput func(rolling bool) review.StepOutput
	Report     func(review.Notice, review.Step) error

	PauseBeforeManual       bool
	RenderManualStep        func(ReviewManualStepContext) error
	ConfirmManualCommand    func(step review.Step) (bool, error)
	PromptManualStep        func(ReviewManualStepPrompt) (review.ManualResult, error)
	PromptAutomatedBlockers func(review.AutomatedBlockerReview) ([]review.AutomatedBlockerDecision, error)
}

// ReviewLifecycleFrontend contains all operator-facing review interactions.
// Implementations may render to terminals, prompt users, and choose output
// streams; workflow only consumes typed decisions and callbacks.
type ReviewLifecycleFrontend interface {
	PipelinePresentation(ReviewAttemptContext) (ReviewPipelinePresentation, error)
	ReviewResumed(ReviewAttemptContext) error
	AutonomousFollowUp(ReviewAttemptContext, int, []int) error
	AutonomousBudgetExhausted(ReviewAttemptContext, int, int) error
	FollowUpRunIncomplete(ReviewAttemptContext, int) error
	SelectSeparateTaskCandidates(ReviewAttemptContext, []SeparateTaskCandidate) ([]SeparateTaskCandidate, error)
	SeparateTaskCreated(ReviewAttemptContext, SeparateTaskCandidate, task.Task) error
	ContinueAfterFollowUpCreationFailure(ReviewAttemptContext, SeparateTaskCandidate, error) (bool, error)
	PromptFreshReviewBlockerDispositions(ReviewContext, []FreshReviewBlocker) ([]FreshReviewBlockerDisposition, error)
}

// ReviewLifecycleService coordinates specialized review execution and its typed
// records. Phase progression and repair-loop policy belong to Controller.
type ReviewLifecycleService struct {
	Progress               func(Progress) error
	ReviewEffects          review.Effects
	ReviewStatus           func(context.Context, string) (string, error)
	Paths                  state.Paths
	Sources                []task.RepositorySource
	BackendFactory         ReviewLifecycleBackendFactory
	RunStore               ReviewLifecycleStore
	CandidateGit           ReviewCandidateGit
	OutputGit              OutputGit
	DispatchGit            DispatchGit
	AgentRunner            ImplementationRunner
	AgentLauncher          agentexec.Launcher
	Environment            []string
	UsageCaptureEnv        map[string]string
	Frontend               ReviewLifecycleFrontend
	PipelineRunner         func(review.PipelineRunOptions) (review.PipelineOutcome, error)
	ResolveCommand         func(DispatchCommandContext, string) (DispatchCommand, string, error)
	ResolveFollowUpCommand func(DispatchCommandContext, string) (DispatchCommand, string, error)
	Logger                 *slog.Logger
	ProcessProbe           ProcessProbe
	Parents                task.Getter
}

// ReviewLifecycleOutcomeKind identifies the terminal lifecycle outcome.
type ReviewLifecycleOutcomeKind string

const (
	ReviewLifecycleOutcomeWaitingForManual            ReviewLifecycleOutcomeKind = "waiting_for_manual"
	ReviewLifecycleOutcomeWaitingForAutomatedDecision ReviewLifecycleOutcomeKind = "waiting_for_automated_decision"
	ReviewLifecycleOutcomeBlocked                     ReviewLifecycleOutcomeKind = "blocked"
	ReviewLifecycleOutcomeExhausted                   ReviewLifecycleOutcomeKind = "exhausted"
	ReviewLifecycleOutcomeOperationalFail             ReviewLifecycleOutcomeKind = "operational_failure"
	ReviewLifecycleOutcomePassed                      ReviewLifecycleOutcomeKind = "passed"
	ReviewLifecycleOutcomePublicationPending          ReviewLifecycleOutcomeKind = "publication_pending"
	ReviewLifecycleOutcomePublicationRetry            ReviewLifecycleOutcomeKind = "publication_retry"
	ReviewLifecycleOutcomeAborted                     ReviewLifecycleOutcomeKind = "aborted"
	ReviewLifecycleOutcomeExecutionRecovered          ReviewLifecycleOutcomeKind = "review_recovered"
	ReviewLifecycleOutcomeExecutionLive               ReviewLifecycleOutcomeKind = "review_live"
	ReviewLifecycleOutcomeExecutionUnknown            ReviewLifecycleOutcomeKind = "review_unverifiable"
)

// ReviewLifecycleOutcome is the typed result returned to frontends.
type ReviewLifecycleOutcome struct {
	Kind               ReviewLifecycleOutcomeKind
	Context            ReviewAttemptContext
	Finalization       FinalizationResult
	RecoveryInspection AttachedExecutionInspection
	Err                error
}

// ReviewContext contains inputs that can be reused across review attempts.
// It deliberately excludes pipeline execution and continuation state.
type ReviewContext struct {
	owner   *work.AttemptRef
	paths   state.Paths
	store   ReviewLifecycleStore
	Source  task.RepositorySource
	Task    task.Task
	Workdir string
	Target  tasktarget.Target
}

// ReviewAttemptContext carries the prepared pipeline and execution state for one attempt.
type ReviewAttemptContext struct {
	ReviewContext
	Review      taskstate.ReviewAttempt
	Pipeline    review.Pipeline
	AgentConfig agent.Config
	execution   review.ExecutionRequest
}

// Resumed reports whether this execution continues a paused review.
func (c ReviewAttemptContext) Resumed() bool { return c.execution.Resumed() }

// ResumesAutomatedBlockerDecision reports which paused interaction to present.
func (c ReviewAttemptContext) ResumesAutomatedBlockerDecision() bool {
	return c.execution.ResumesAutomatedDecision()
}

// ReviewManualStepContext contains read-only facts needed to render a manual review step.
type ReviewManualStepContext struct {
	Source    task.RepositorySource
	Task      task.Task
	Workdir   string
	Review    taskstate.ReviewAttempt
	TaskState taskstate.TaskState
	Step      review.Step
	GitStatus string
}

// RepoID returns the registered repository id.
func (c ReviewManualStepContext) RepoID() string { return c.Source.Repository.ID }

// TaskID returns the resolved backend task id.
func (c ReviewManualStepContext) TaskID() string { return c.Task.ID }

// ReviewManualStepPrompt contains frontend input facts for a manual review prompt.
type ReviewManualStepPrompt struct {
	Source    task.RepositorySource
	Task      task.Task
	Review    taskstate.ReviewAttempt
	Step      review.Step
	HunkNotes []review.HunkNote
	Recorder  ReviewManualStepRecorder
}

// TaskID returns the resolved backend task id.
func (p ReviewManualStepPrompt) TaskID() string { return p.Task.ID }

// ReviewManualStepRecorder is the narrow workflow-owned persistence port for manual review input.
type ReviewManualStepRecorder interface {
	LatestReview() (taskstate.ReviewAttempt, error)
	RecordFinding(taskstate.ReviewFinding) (taskstate.ReviewAttempt, error)
	PromoteAdvisoryFinding(index int) (taskstate.ReviewAttempt, error)
}

// RepoID returns the registered repository id.
func (c ReviewContext) RepoID() string { return c.Source.Repository.ID }

// TaskID returns the resolved backend task id.
func (c ReviewContext) TaskID() string { return c.Task.ID }

// FreshReviewBlocker identifies an unresolved finding that would otherwise be
// superseded by a new authoritative review attempt.
type FreshReviewBlocker struct {
	Index   int
	Finding taskstate.ReviewFinding
}

// FreshReviewBlockerAction is the explicit disposition for a prior blocker.
type FreshReviewBlockerAction string

const (
	FreshReviewBlockerActionKeep              FreshReviewBlockerAction = "keep"
	FreshReviewBlockerActionAddressedManually FreshReviewBlockerAction = "addressed_manually"
	FreshReviewBlockerActionWaive             FreshReviewBlockerAction = "waive"
)

// FreshReviewBlockerDisposition applies an operator decision to a prior finding.
type FreshReviewBlockerDisposition struct {
	FindingIndex int
	Action       FreshReviewBlockerAction
	Reason       string
}

// ErrFreshReviewBlockerDispositionInterrupted means the operator did not
// finish deciding how to preserve or resolve the prior blockers.
var ErrFreshReviewBlockerDispositionInterrupted = errors.New("fresh review blocker disposition interrupted")

// SeparateTaskCandidate describes a review finding that can become a standalone task.
type SeparateTaskCandidate struct {
	Index   int
	Finding taskstate.ReviewFinding
}

func (c SeparateTaskCandidate) CreateOptions(ctx ReviewAttemptContext) task.CreateOptions {
	proposal := c.Finding.TaskProposal
	return task.CreateOptions{
		Title:              proposal.Title,
		Description:        ReviewFollowUpTaskDescription(ctx, c),
		AcceptanceCriteria: proposal.AcceptanceCriteria,
		IssueType:          task.IssueTypeTask,
	}
}

// ReviewFollowUpTaskDescription renders the persisted provenance for a separate-task finding.
func ReviewFollowUpTaskDescription(ctx ReviewAttemptContext, candidate SeparateTaskCandidate) string {
	proposal := candidate.Finding.TaskProposal
	provenance := fmt.Sprintf(
		"Discovered during review of %s in repository %s (review attempt %d, finding %d).",
		ctx.TaskID(),
		ctx.RepoID(),
		ctx.Review.Attempt,
		candidate.Index+1,
	)
	if strings.TrimSpace(candidate.Finding.Step) != "" {
		provenance += " Review step: " + candidate.Finding.Step + "."
	}
	return strings.TrimSpace(proposal.Description) + "\n\nProvenance:\n" + provenance
}

func (s ReviewLifecycleService) validate() error {
	if s.BackendFactory == nil {
		return errors.New("review lifecycle backend factory is required")
	}
	if s.RunStore == nil {
		return errors.New("review lifecycle run store is required")
	}
	if s.Frontend == nil {
		return errors.New("review lifecycle frontend is required")
	}
	return nil
}

func reviewLifecycleStartError(start ReviewAttemptContext, err error) (ReviewLifecycleOutcome, error) {
	if outcome, handled := freshReviewBlockerGuardOutcome(start, err); handled {
		return outcome, nil
	}
	return reviewLifecycleOperationalFailure(start, err), err
}

func reviewLifecycleOperationalFailure(ctx ReviewAttemptContext, err error) ReviewLifecycleOutcome {
	return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeOperationalFail, Context: ctx, Err: err}
}

func reviewLifecycleOutcome(ctx ReviewAttemptContext, inspection AttachedExecutionInspection) ReviewLifecycleOutcome {
	switch inspection.Condition {
	case AttachedExecutionRecoverable, AttachedExecutionAlreadyRecovered:
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeExecutionRecovered, Context: ctx, RecoveryInspection: inspection}
	case AttachedExecutionLive:
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeExecutionLive, Context: ctx, RecoveryInspection: inspection}
	default:
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeExecutionUnknown, Context: ctx, RecoveryInspection: inspection}
	}
}

func (s ReviewLifecycleService) resolveReviewTask(ctx context.Context, taskID string) (ReviewContext, error) {
	resolved, err := task.ResolveTaskSource(s.Sources, strings.TrimSpace(taskID))
	if err != nil {
		return ReviewContext{}, err
	}
	backend, err := s.BackendFactory(resolved.Source)
	if err != nil {
		return ReviewContext{}, fmt.Errorf(
			"task run %s: create backend for repo %s (%s; prefix %s): %w",
			resolved.TaskID,
			resolved.Source.Repository.ID,
			resolved.Source.Repository.Name,
			resolved.Source.Repository.TaskIDPrefix,
			err,
		)
	}
	taskItem, err := fetchReviewTask(ctx, backend, resolved)
	if err != nil {
		return ReviewContext{}, err
	}
	return ReviewContext{
		paths: s.Paths, store: s.RunStore, Source: resolved.Source, Task: taskItem,
	}, nil
}

func reviewStartupStopsForInspection(inspection AttachedExecutionInspection) bool {
	switch inspection.Condition {
	case AttachedExecutionRecoverable, AttachedExecutionAlreadyRecovered,
		AttachedExecutionLive, AttachedExecutionUnverifiable:
		return true
	default:
		return false
	}
}

func freshReviewBlockerGuardOutcome(ctx ReviewAttemptContext, err error) (ReviewLifecycleOutcome, bool) {
	var guardErr freshReviewBlockerGuardError
	if !errors.As(err, &guardErr) {
		return ReviewLifecycleOutcome{}, false
	}
	kind := ReviewLifecycleOutcomeBlocked
	if guardErr.interrupted {
		kind = ReviewLifecycleOutcomeAborted
	}
	return ReviewLifecycleOutcome{Kind: kind, Context: ctx}, true
}

func (s ReviewLifecycleService) pipelineRunOptions(runCtx context.Context, ctx ReviewAttemptContext) (review.PipelineRunOptions, error) {
	if err := ctx.execution.ValidateRunning(ctx.Pipeline, ctx.Review); err != nil {
		return review.PipelineRunOptions{}, err
	}
	presentation, err := s.Frontend.PipelinePresentation(ctx)
	if err != nil {
		return review.PipelineRunOptions{}, fmt.Errorf("task run %s: %w", ctx.TaskID(), err)
	}
	promptManualStep := s.manualStepPrompt(ctx, presentation.PromptManualStep)
	snapshot, err := ctx.store.Load(ctx.RepoID(), ctx.TaskID())
	if err != nil {
		return review.PipelineRunOptions{}, err
	}
	candidate, err := committedCandidate(snapshot, ctx.Review.Address)
	if err != nil {
		return review.PipelineRunOptions{}, err
	}
	return review.PipelineRunOptions{
		Effects: s.ReviewEffects,
		RecordManualResult: func(owner work.AttemptRef, result review.ManualResult) error {
			return s.recordManualResponse(runCtx, owner, result)
		},
		RecordFindingDecisions: func(owner work.AttemptRef, decisions []review.AutomatedBlockerDecision) error {
			return s.recordFindingResponses(runCtx, owner, decisions)
		},
		Candidate:               candidate,
		Context:                 runCtx,
		Store:                   ctx.store,
		Logger:                  s.Logger,
		RepoID:                  ctx.RepoID(),
		TaskID:                  ctx.TaskID(),
		Branch:                  ctx.Target.Branch,
		Workdir:                 ctx.Workdir,
		Attempt:                 ctx.Review,
		Pipeline:                ctx.Pipeline,
		SessionName:             ctx.Task.ReviewSessionName(),
		Stdout:                  presentation.Stdout,
		Stderr:                  presentation.Stderr,
		Stdin:                   presentation.Stdin,
		OpenOutput:              presentation.OpenOutput,
		Report:                  presentation.Report,
		StepStarted:             func(step review.Step) error { return s.notify(ctx.RepoID(), ctx.Task, ctx.Resumed(), nil) },
		AgentConfig:             ctx.AgentConfig,
		AgentLauncher:           s.AgentLauncher,
		Environment:             s.Environment,
		UsageCaptureEnv:         s.UsageCaptureEnv,
		RecordChildPID:          s.RecordReviewChildPID,
		Execution:               ctx.execution,
		PauseBeforeManual:       presentation.PauseBeforeManual,
		RenderManualStep:        s.manualStepRenderer(runCtx, ctx, presentation.RenderManualStep),
		ConfirmManualCommand:    presentation.ConfirmManualCommand,
		PromptManualStep:        promptManualStep,
		PromptAutomatedBlockers: presentation.PromptAutomatedBlockers,
	}, nil
}

// RecordReviewChildPID persists a newly started reviewer's
// direct PID under the same mutation protection used for implementation runs.
func (s ReviewLifecycleService) RecordReviewChildPID(ref work.AttemptRef, pid int) error {
	return RecordReviewChildPID(s.Paths, s.Logger, s.RunStore, ref, pid)
}

func (s ReviewLifecycleService) manualStepPrompt(
	ctx ReviewAttemptContext,
	prompt func(ReviewManualStepPrompt) (review.ManualResult, error),
) func(review.ManualStep) (review.ManualResult, error) {
	if prompt == nil {
		return nil
	}
	return func(step review.ManualStep) (review.ManualResult, error) {
		current, err := ctx.store.CurrentReviewAt(ctx.Review.Address)
		if err != nil {
			return review.ManualResult{}, err
		}
		if len(current.Steps) == 0 || current.Steps[len(current.Steps)-1].Name != step.Step.Name || !current.Steps[len(current.Steps)-1].Address.Equal(step.Address) {
			return review.ManualResult{}, errors.New("manual prompt no longer owns its pipeline step")
		}
		recorder := &reviewManualStepRecorder{owner: step.Address, store: ctx.store, repoID: ctx.RepoID(), taskID: ctx.TaskID(), attempt: ctx.Review.Attempt, step: step.Step.Name}
		return prompt(ReviewManualStepPrompt{
			Source:    ctx.Source,
			Task:      ctx.Task,
			Review:    ctx.Review,
			Step:      step.Step,
			HunkNotes: step.HunkNotes,
			Recorder:  recorder,
		})
	}
}

func (s ReviewLifecycleService) manualStepContext(
	runCtx context.Context,
	ctx ReviewAttemptContext,
	step review.Step,
) (ReviewManualStepContext, error) {
	taskState, err := ctx.store.Load(ctx.RepoID(), ctx.TaskID())
	if err != nil {
		return ReviewManualStepContext{}, fmt.Errorf("load task state: %w", err)
	}
	latest, ok := taskstate.LatestRun(taskState)
	if !ok {
		return ReviewManualStepContext{}, fmt.Errorf("task has no Orpheus run attempts; run `orpheus task run %s` first", ctx.TaskID())
	}
	if latest.Completion == nil {
		return ReviewManualStepContext{}, fmt.Errorf("latest run attempt %d has no completion block; run `orpheus agent done` first", latest.Attempt)
	}
	readStatus := s.ReviewStatus
	if readStatus == nil {
		readStatus = gitmeta.ShortStatus
	}
	status, err := readStatus(runCtx, ctx.Workdir)
	if err != nil {
		return ReviewManualStepContext{}, fmt.Errorf("read git status: %w", err)
	}
	return ReviewManualStepContext{
		Source:    ctx.Source,
		Task:      ctx.Task,
		Workdir:   ctx.Workdir,
		Review:    ctx.Review,
		TaskState: taskState,
		Step:      step,
		GitStatus: status,
	}, nil
}

type reviewManualStepRecorder struct {
	owner   work.AttemptRef
	store   ReviewLifecycleStore
	repoID  string
	taskID  string
	attempt int
	step    string
}

func (r reviewManualStepRecorder) LatestReview() (taskstate.ReviewAttempt, error) {
	taskState, err := r.store.Load(r.repoID, r.taskID)
	if err != nil {
		return taskstate.ReviewAttempt{}, fmt.Errorf("load review state: %w", err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Address.Workflow != r.owner.Workflow || latest.Address.Phase != r.owner.Phase || latest.Attempt != r.attempt || len(latest.Steps) == 0 || latest.Steps[len(latest.Steps)-1].Address.Step == nil || r.owner.Step == nil || *latest.Steps[len(latest.Steps)-1].Address.Step != *r.owner.Step {
		return taskstate.ReviewAttempt{}, fmt.Errorf("latest review attempt no longer matches attempt %d", r.attempt)
	}
	return latest, nil
}

func (r reviewManualStepRecorder) RecordFinding(finding taskstate.ReviewFinding) (taskstate.ReviewAttempt, error) {
	latest, err := r.LatestReview()
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	if len(latest.Steps) == 0 || latest.Steps[len(latest.Steps)-1].Name != r.step {
		return taskstate.ReviewAttempt{}, fmt.Errorf("manual step no longer owns this finding")
	}
	finding.Actor = &r.owner
	finding.Step = r.step
	return r.store.RecordReviewFinding(r.repoID, r.taskID, r.attempt, finding)
}

func (r reviewManualStepRecorder) PromoteAdvisoryFinding(index int) (taskstate.ReviewAttempt, error) {
	latest, err := r.LatestReview()
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	if index < 0 || index >= len(latest.Findings) || latest.Findings[index].Address == nil {
		return taskstate.ReviewAttempt{}, errors.New("manual promotion requires an owned advisory")
	}
	return r.store.RecordFindingDecision(r.owner, *latest.Findings[index].Address, &work.PromotedFinding{})
}

func (s ReviewLifecycleService) executeReviewAttempt(runCtx context.Context, ctx ReviewAttemptContext) (taskstate.ReviewStatus, error) {
	runner := s.PipelineRunner
	if runner == nil {
		runner = review.RunPipeline
	}
	opts, err := s.pipelineRunOptions(runCtx, ctx)
	if err != nil {
		if !preservesReviewDecision(err) {
			_, _ = ctx.store.RecordReviewFailure(ctx.Review.Address, err)
		}
		return "", err
	}
	span := logging.Start(runCtx, s.Logger, "review attempt execution",
		slog.String("component", "workflow"),
		slog.String("operation", "review_attempt"),
		slog.String("repo_id", ctx.RepoID()),
		slog.String("task_id", ctx.TaskID()),
		slog.Int("attempt", ctx.Review.Attempt),
		slog.String("pipeline", ctx.Pipeline.Name),
	)
	outcome, err := runner(opts)
	if err != nil {
		span.FinishError(runCtx, err)
		if !preservesReviewDecision(err) {
			if _, recordErr := ctx.store.RecordReviewFailure(ctx.Review.Address, err); recordErr != nil {
				err = errors.Join(err, fmt.Errorf("task run %s: record failed review: %w", ctx.TaskID(), recordErr))
			}
		}
		return "", err
	}
	span.Finish(runCtx, logging.StatusSuccess, slog.String("review_status", string(outcome.Status)))
	if outcome.Status == taskstate.ReviewStatusWaitingForManual || outcome.Status == taskstate.ReviewStatusWaitingForAutomatedDecision {
		return outcome.Status, nil
	}
	if _, err := validatedReviewOutcome(ctx.store, ctx.Review.Address, outcome.Status); err != nil {
		_, _ = ctx.store.RecordReviewFailure(ctx.Review.Address, err)
		return "", fmt.Errorf("task run %s: record %s review: %w", ctx.TaskID(), outcome.Status, err)
	}
	return outcome.Status, nil
}

func (s ReviewLifecycleService) validateReviewCandidate(
	ctx context.Context,
	base ReviewContext,
) (ReviewContext, error) {
	target, err := ReviewTarget(s.RunStore, s.Paths, base)
	if err != nil {
		return ReviewContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	base.Target = target
	base.Workdir = target.Worktree
	if err := s.inspectReviewCandidate(ctx, base, target.Worktree); err != nil {
		return ReviewContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	return base, nil
}

func fetchReviewTask(
	ctx context.Context,
	backend ReviewLifecycleBackend,
	resolved task.ResolvedTaskSource,
) (task.Task, error) {
	taskItem, err := backend.Get(ctx, resolved.TaskID)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			return task.Task{}, fmt.Errorf(
				"task run %s: task was not found in repo %s (%s; prefix %s): %w",
				resolved.TaskID,
				resolved.Source.Repository.ID,
				resolved.Source.Repository.Name,
				resolved.Source.Repository.TaskIDPrefix,
				err,
			)
		}
		return task.Task{}, fmt.Errorf(
			"task run %s: query repo %s (%s; prefix %s): %w",
			resolved.TaskID,
			resolved.Source.Repository.ID,
			resolved.Source.Repository.Name,
			resolved.Source.Repository.TaskIDPrefix,
			err,
		)
	}
	if !task.IsM2TaskViewItem(taskItem) {
		return task.Task{}, fmt.Errorf(
			"task run %s: item is out of scope for M2 task views; expected an active item, got issue_type=%s status=%s",
			resolved.TaskID,
			formatLifecycleTaskField(string(taskItem.IssueType)),
			formatLifecycleTaskField(string(taskItem.Status)),
		)
	}
	return taskItem, nil
}

type freshReviewBlockerGuardError struct {
	interrupted bool
}

func (e freshReviewBlockerGuardError) Error() string {
	if e.interrupted {
		return "fresh review blocker disposition interrupted"
	}
	return "fresh review blocked by preserved findings"
}

func (s ReviewLifecycleService) guardFreshReviewBlockers(base ReviewContext) error {
	taskState, err := base.store.Load(base.RepoID(), base.TaskID())
	if err != nil {
		return fmt.Errorf("task run %s: load prior review blockers: %w", base.TaskID(), err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok {
		return nil
	}
	indexes := taskstate.UntargetedBlockingFindingIndexes(latest)
	if len(indexes) == 0 {
		return nil
	}

	blockers, _ := freshReviewBlockers(latest, indexes)
	dispositions, err := s.Frontend.PromptFreshReviewBlockerDispositions(base, blockers)
	if err != nil {
		if errors.Is(err, ErrFreshReviewBlockerDispositionInterrupted) {
			return freshReviewBlockerGuardError{interrupted: true}
		}
		return fmt.Errorf("task run %s: prompt fresh review blocker dispositions: %w", base.TaskID(), err)
	}
	if err := validateFreshReviewBlockerDispositions(blockers, dispositions); err != nil {
		return fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}

	if err := recordFreshReviewDispositions(base, taskState, latest, dispositions); err != nil {
		return err
	}

	updated, err := base.store.Load(base.RepoID(), base.TaskID())
	if err != nil {
		return fmt.Errorf("task run %s: reload prior review blockers: %w", base.TaskID(), err)
	}
	latest, _ = taskstate.LatestReview(updated)
	if !taskstate.ReviewHasOpenBlockers(latest) {
		return nil
	}
	return freshReviewBlockerGuardError{}
}

func recordFreshReviewDispositions(base ReviewContext, taskState taskstate.TaskState, latest taskstate.ReviewAttempt, dispositions []FreshReviewBlockerDisposition) error {
	actor := latest.Address
	if taskState.Work != nil {
		actor.Workflow = taskState.CurrentWorkflow
		actor.Phase = len(taskState.Work.Workflows[actor.Workflow].Phases) - 1
		actor.Step = nil
		actor.BranchSync = nil
	}
	for _, disposition := range dispositions {
		finding := latest.Findings[disposition.FindingIndex]
		if finding.Address == nil {
			return errors.New("finding has no workflow-owned address")
		}
		var choice work.FindingDisposition
		switch disposition.Action {
		case FreshReviewBlockerActionKeep:
			choice = &work.RepairRequired{}
		case FreshReviewBlockerActionAddressedManually:
			choice = &work.AddressedFinding{Waiver: work.Waiver{Reason: disposition.Reason}}
		case FreshReviewBlockerActionWaive:
			choice = &work.WaivedFinding{Waiver: work.Waiver{Reason: disposition.Reason}}
		}
		if _, err := base.store.RecordFindingDecision(actor, *finding.Address, choice); err != nil {
			return fmt.Errorf("record prior finding decision: %w", err)
		}
	}
	return nil
}

func freshReviewBlockers(reviewAttempt taskstate.ReviewAttempt, indexes []int) ([]FreshReviewBlocker, map[int]bool) {
	blockers := make([]FreshReviewBlocker, 0, len(indexes))
	automated := make(map[int]bool, len(indexes))
	for _, index := range taskstate.UntargetedAutomatedBlockingFindingIndexes(reviewAttempt) {
		automated[index] = true
	}
	for _, index := range indexes {
		blockers = append(blockers, FreshReviewBlocker{Index: index, Finding: reviewAttempt.Findings[index]})
	}
	return blockers, automated
}

func validateFreshReviewBlockerDispositions(blockers []FreshReviewBlocker, dispositions []FreshReviewBlockerDisposition) error {
	if len(dispositions) != len(blockers) {
		return fmt.Errorf("expected dispositions for %d blockers, got %d", len(blockers), len(dispositions))
	}
	allowed := make(map[int]bool, len(blockers))
	for _, blocker := range blockers {
		allowed[blocker.Index] = true
	}
	seen := make(map[int]bool, len(dispositions))
	for _, disposition := range dispositions {
		if !allowed[disposition.FindingIndex] || seen[disposition.FindingIndex] {
			return fmt.Errorf("disposition for finding %d is invalid", disposition.FindingIndex+1)
		}
		seen[disposition.FindingIndex] = true
		switch disposition.Action {
		case FreshReviewBlockerActionKeep:
		case FreshReviewBlockerActionAddressedManually, FreshReviewBlockerActionWaive:
			if strings.TrimSpace(disposition.Reason) == "" {
				return fmt.Errorf("disposition for finding %d requires a reason", disposition.FindingIndex+1)
			}
		default:
			return fmt.Errorf("disposition for finding %d has unsupported action %q", disposition.FindingIndex+1, disposition.Action)
		}
	}
	return nil
}

func (s ReviewLifecycleService) resumeReview(base ReviewContext, paused taskstate.ReviewAttempt, pipelineName string) (ReviewAttemptContext, error) {
	observedOwner := paused.Address
	if len(paused.Steps) > 0 {
		observedOwner = paused.Steps[len(paused.Steps)-1].Address
	}
	if base.owner != nil && !sameRouteOwner(base.owner, &observedOwner) {
		return ReviewAttemptContext{}, fmt.Errorf("review resumption is stale; current work changed")
	}
	pipeline, err := ResolvePausedTaskReviewPipeline(base.paths, base.Source.Repository, paused, pipelineName)
	if err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	prepared, err := s.preparePipeline(base, pipeline)
	if err != nil {
		return prepared, err
	}
	request := review.FreshExecution()
	if len(paused.Steps) > 0 {
		request, err = review.ResumeExecution(pipeline, paused)
		if paused.Status == taskstate.ReviewStatusRunning {
			request, err = review.ContinueExecution(pipeline, paused)
		}
	}
	if err != nil {
		return prepared, err
	}
	state, err := base.store.Load(base.RepoID(), base.TaskID())
	if err != nil {
		return prepared, fmt.Errorf("task run %s: load paused review: %w", base.TaskID(), err)
	}
	latest, ok := taskstate.LatestReview(state)
	if !ok {
		return prepared, fmt.Errorf("%w: no paused review attempt", review.ErrInvalidExecution)
	}
	if err := request.ValidateRunning(pipeline, latest); err != nil {
		return prepared, err
	}
	reviewAttempt, err := base.store.CurrentReviewAt(paused.Address)
	if err != nil {
		return prepared, fmt.Errorf("task run %s: resume review attempt: %w", base.TaskID(), err)
	}
	prepared.Review = reviewAttempt
	prepared.execution = request
	if err := s.Frontend.ReviewResumed(prepared); err != nil {
		return prepared, err
	}
	return prepared, nil
}

func (s ReviewLifecycleService) preparePipeline(base ReviewContext, pipeline review.Pipeline) (ReviewAttemptContext, error) {
	ctx := ReviewAttemptContext{ReviewContext: base, Pipeline: pipeline}
	agentConfig, err := ResolveReviewAgentConfig(base.paths, pipeline)
	if err != nil {
		return ctx, fmt.Errorf("task run %s: %w", ctx.TaskID(), err)
	}
	if pipelineUsesAgentReview(pipeline) && s.AgentLauncher == nil {
		return ctx, fmt.Errorf("task run %s: review agent launcher is required", ctx.TaskID())
	}
	ctx.AgentConfig = agentConfig
	return ctx, nil
}

// ResolveReviewAgentConfig validates and returns reviewer-agent configuration for a pipeline.
func ResolveReviewAgentConfig(paths state.Paths, pipeline review.Pipeline) (agent.Config, error) {
	var agentConfig agent.Config
	loaded := false
	for _, step := range pipeline.Steps {
		if step.Kind != review.KindAgentReview {
			continue
		}
		if !loaded {
			config, err := agent.LoadConfig(paths)
			if err != nil {
				return agent.Config{}, err
			}
			agentConfig = config
			loaded = true
		}
		if _, err := agentConfig.ResolveReviewerCommandWithValues(step.Agent, agent.InterpolationValues{}); err != nil {
			return agent.Config{}, fmt.Errorf("resolve agent_review step %q: %w", step.Name, err)
		}
	}
	return agentConfig, nil
}

func pipelineUsesAgentReview(pipeline review.Pipeline) bool {
	for _, step := range pipeline.Steps {
		if step.Kind == review.KindAgentReview {
			return true
		}
	}
	return false
}

func reviewMaxAutonomousReviewAttempts(paths state.Paths) (int, error) {
	config, err := review.LoadConfig(paths)
	if err != nil {
		return 0, err
	}
	return config.MaxAutonomousReviewAttempts, nil
}

// ResolveTaskReviewPipeline resolves the effective review pipeline for a repository.
func ResolveTaskReviewPipeline(paths state.Paths, repo task.Repository, pipelineName string) (review.Pipeline, error) {
	config, err := review.LoadConfig(paths)
	if err != nil {
		return review.Pipeline{}, err
	}
	if name := strings.TrimSpace(pipelineName); name != "" {
		if target, ok := repo.ReviewPipelineAliases[name]; ok {
			pipeline, err := review.ResolvePipeline(config, target, "")
			if err != nil {
				return review.Pipeline{}, fmt.Errorf("CLI --pipeline alias %q targets %q: %w", name, target, err)
			}
			return pipeline, nil
		}
		pipeline, err := review.ResolvePipeline(config, name, "")
		if err != nil {
			return review.Pipeline{}, appendRepoReviewPipelineAliases(err, repo)
		}
		return pipeline, nil
	}
	return review.ResolvePipeline(config, pipelineName, repo.ReviewPipeline)
}

// ResolvePausedTaskReviewPipeline prevents replacing a paused manual review pipeline.
func ResolvePausedTaskReviewPipeline(paths state.Paths, repo task.Repository, paused taskstate.ReviewAttempt, pipelineName string) (review.Pipeline, error) {
	storedPipeline := strings.TrimSpace(paused.Pipeline)
	if storedPipeline == "" {
		return review.Pipeline{}, fmt.Errorf("waiting review attempt %d has no recorded pipeline", paused.Attempt)
	}
	pipeline, err := resolveStoredTaskReviewPipeline(paths, storedPipeline)
	if err != nil {
		return review.Pipeline{}, fmt.Errorf("resolve paused review pipeline %q: %w", storedPipeline, err)
	}
	if strings.TrimSpace(pipelineName) == "" {
		return pipeline, nil
	}
	requested, err := ResolveTaskReviewPipeline(paths, repo, pipelineName)
	if err != nil {
		return review.Pipeline{}, err
	}
	if requested.Name != pipeline.Name {
		return review.Pipeline{}, fmt.Errorf("review attempt %d is waiting at step %q in pipeline %q; --pipeline %q resolves to %q and cannot replace a paused review", paused.Attempt, paused.Step, pipeline.Name, strings.TrimSpace(pipelineName), requested.Name)
	}
	return pipeline, nil
}

func resolveStoredTaskReviewPipeline(paths state.Paths, pipelineName string) (review.Pipeline, error) {
	config, err := review.LoadConfig(paths)
	if err != nil {
		return review.Pipeline{}, err
	}
	pipeline, err := review.ResolvePipeline(config, pipelineName, "")
	if err == nil {
		return pipeline, nil
	}
	builtin := review.BuiltinManualPipeline()
	if pipelineName == builtin.Name {
		return builtin, nil
	}
	return review.Pipeline{}, err
}

func appendRepoReviewPipelineAliases(err error, repo task.Repository) error {
	aliases := make([]string, 0, len(repo.ReviewPipelineAliases))
	for alias, target := range repo.ReviewPipelineAliases {
		aliases = append(aliases, fmt.Sprintf("%s=%s", alias, target))
	}
	sort.Strings(aliases)
	if len(aliases) == 0 {
		return err
	}
	return fmt.Errorf("%w; configured repo aliases: %s", err, strings.Join(aliases, ", "))
}

// ReviewTarget returns the taskstate-backed review target after mirror validation.
func ReviewTarget(store ReviewLifecycleStore, paths state.Paths, ctx ReviewContext) (tasktarget.Target, error) {
	repo := ctx.Source.Repository
	taskID := ctx.TaskID()
	taskState, err := store.Load(repo.ID, taskID)
	if err != nil {
		return tasktarget.Target{}, fmt.Errorf("load task state: %w", err)
	}
	taskTarget, ok := taskstate.GitFactsFor(taskState)
	if !ok {
		return tasktarget.Target{}, fmt.Errorf("task has no Orpheus target; run `orpheus task run %s` first", taskID)
	}
	targets, err := tasktarget.ExpectedTargetsForTaskOrRecordedBranch(repo, ctx.Task, taskTarget.Branch, paths)
	if err != nil {
		return tasktarget.Target{}, err
	}
	target, err := tasktarget.ClassifyGitFacts(taskTarget, targets)
	if err != nil {
		return tasktarget.Target{}, fmt.Errorf("task has inconsistent taskstate target: %w", err)
	}
	if err := ValidateTaskMetadataMirror(ctx.Task, targets, target); err != nil {
		return tasktarget.Target{}, err
	}
	return target, nil
}

// ValidateTaskMetadataMirror ensures backend metadata and taskstate target agree.
func ValidateTaskMetadataMirror(taskItem task.Task, targets tasktarget.ExpectedTargets, target tasktarget.Target) error {
	metadataTarget, err := tasktarget.ClassifyMetadataTarget(taskItem.OrpheusMetadata(), targets)
	if err != nil {
		return fmt.Errorf("task %s metadata target is invalid: %w", taskItem.ID, err)
	}
	if metadataTarget.Branch == target.Branch && metadataTarget.Worktree == target.Worktree {
		return nil
	}
	return fmt.Errorf("task %s metadata target %q/%q does not mirror taskstate target %q/%q", taskItem.ID, metadataTarget.Branch, metadataTarget.Worktree, target.Branch, target.Worktree)
}

func resumedReviewImplementerName(ctx ReviewAttemptContext) (string, error) {
	taskState, err := ctx.store.Load(ctx.RepoID(), ctx.TaskID())
	if err != nil {
		return "", fmt.Errorf("load task state for resumed review implementer: %w", err)
	}
	completions, err := manualReviewCompletions(taskState)
	if err != nil {
		return "", fmt.Errorf("resolve resumed review implementer: %w", err)
	}
	agentName := strings.TrimSpace(completions.Latest.Execution.Agent)
	if agentName == "" {
		agentName = strings.TrimSpace(completions.Latest.Execution.Profile)
	}
	if agentName == "" {
		return "", fmt.Errorf("resolve resumed review implementer: latest completed run attempt %d has no recorded agent profile", completions.Latest.Attempt)
	}
	return agentName, nil
}

type manualReviewCompletionContext = taskstate.CompletionRunHistory

func manualReviewCompletions(taskState taskstate.TaskState) (manualReviewCompletionContext, error) {
	return taskstate.CompletionRunsForReview(taskState)
}

func (s ReviewLifecycleService) processSeparateTaskReviewCandidates(runCtx context.Context, ctx ReviewAttemptContext) (bool, error) {
	candidates, err := PendingSeparateTaskCandidates(ctx.store, ctx.RepoID(), ctx.TaskID(), ctx.Review.Attempt)
	if err != nil {
		return false, fmt.Errorf("task run %s: load separate-task candidates: %w", ctx.TaskID(), err)
	}
	if len(candidates) == 0 {
		return true, nil
	}
	selected, err := s.Frontend.SelectSeparateTaskCandidates(ctx, candidates)
	if err != nil {
		return false, fmt.Errorf("%w: task run %s: %w", review.ErrPresentationUnavailable, ctx.TaskID(), err)
	}
	if len(selected) == 0 {
		return true, nil
	}
	return s.createSelectedSeparateTaskCandidates(runCtx, ctx, selected)
}

func (s ReviewLifecycleService) createSelectedSeparateTaskCandidates(ctx context.Context, reviewCtx ReviewAttemptContext, selected []SeparateTaskCandidate) (bool, error) {
	backend, err := s.BackendFactory(reviewCtx.Source)
	if err != nil {
		return false, fmt.Errorf("task run %s: create follow-up task backend: %w", reviewCtx.TaskID(), err)
	}
	for _, candidate := range selected {
		created, err := backend.Create(ctx, candidate.CreateOptions(reviewCtx))
		if err != nil {
			proceed, promptErr := s.Frontend.ContinueAfterFollowUpCreationFailure(reviewCtx, candidate, err)
			if promptErr != nil {
				return false, fmt.Errorf("%w: %w", review.ErrPresentationUnavailable, promptErr)
			}
			return proceed, nil
		}
		if _, err := reviewCtx.store.RecordFindingCreatedTask(reviewCtx.Review.Address, *candidate.Finding.Address, created.ID); err != nil {
			return false, fmt.Errorf("task run %s: record created follow-up task %s: %w", reviewCtx.TaskID(), created.ID, err)
		}
		if err := s.Frontend.SeparateTaskCreated(reviewCtx, candidate, created); err != nil {
			return false, fmt.Errorf("%w: %w", review.ErrPresentationUnavailable, err)
		}
	}
	return true, nil
}

// PendingSeparateTaskCandidates returns separate-task findings without created tasks.
func PendingSeparateTaskCandidates(store ReviewLifecycleStore, repoID string, taskID string, attempt int) ([]SeparateTaskCandidate, error) {
	state, err := store.Load(repoID, taskID)
	if err != nil {
		return nil, err
	}
	for _, reviewAttempt := range state.Reviews {
		if reviewAttempt.Attempt != attempt {
			continue
		}
		candidates := make([]SeparateTaskCandidate, 0)
		for index, finding := range reviewAttempt.Findings {
			if finding.Type != taskstate.FindingTypeSeparateTask || strings.TrimSpace(finding.CreatedTaskID) != "" {
				continue
			}
			candidates = append(candidates, SeparateTaskCandidate{Index: index, Finding: finding})
		}
		return candidates, nil
	}
	return nil, fmt.Errorf("review attempt %d was not found", attempt)
}

func formatLifecycleTaskField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

// FormatReviewFindingIndexes formats zero-based finding indexes for display.
func FormatReviewFindingIndexes(indexes []int) string {
	if len(indexes) == 0 {
		return "-"
	}
	labels := make([]string, 0, len(indexes))
	for _, index := range indexes {
		labels = append(labels, strconv.Itoa(index+1))
	}
	return strings.Join(labels, ", ")
}

// SelectSeparateTaskCandidates parses a frontend selection string.
func SelectSeparateTaskCandidates(candidates []SeparateTaskCandidate, input string) ([]SeparateTaskCandidate, error) {
	answer := strings.ToLower(strings.TrimSpace(input))
	switch answer {
	case "", "n", "no", "none", "skip":
		return nil, nil
	case "a", "all":
		return candidates, nil
	}
	fields := strings.FieldsFunc(answer, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	if len(fields) == 0 {
		return nil, nil
	}
	seen := map[int]bool{}
	selected := make([]SeparateTaskCandidate, 0, len(fields))
	for _, field := range fields {
		number, err := strconv.Atoi(field)
		if err != nil || number < 1 || number > len(candidates) {
			return nil, fmt.Errorf("invalid follow-up task selection %q", strings.TrimSpace(input))
		}
		if seen[number] {
			continue
		}
		seen[number] = true
		selected = append(selected, candidates[number-1])
	}
	return selected, nil
}

func validatedReviewOutcome(store review.PipelineStore, ref work.AttemptRef, status taskstate.ReviewStatus) (taskstate.ReviewAttempt, error) {
	current, err := store.CurrentReviewAt(ref)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	if status == taskstate.ReviewStatusPassed && current.Status != taskstate.ReviewStatusPassed {
		return taskstate.ReviewAttempt{}, errors.New("passing pipeline outcome cannot replace missing or failed recorded requirements")
	}
	return current, nil
}

func (s ReviewLifecycleService) manualStepRenderer(runCtx context.Context, ctx ReviewAttemptContext, render func(ReviewManualStepContext) error) func(review.Step) error {
	if render == nil {
		return nil
	}
	return func(step review.Step) error {
		manual, err := s.manualStepContext(runCtx, ctx, step)
		if err != nil {
			return err
		}
		return render(manual)
	}
}

func preservesReviewDecision(err error) bool {
	return errors.Is(err, review.ErrInteractionRequired) || errors.Is(err, review.ErrInvalidExecution) || errors.Is(err, ErrProgressUnavailable) || errors.Is(err, review.ErrPresentationUnavailable)
}
