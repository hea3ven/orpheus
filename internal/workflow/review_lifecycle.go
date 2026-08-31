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
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
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
	DispatchRunStore
	FinalizationRunStore
	review.PipelineStore
	StartReviewWithOptions(repoID, taskID string, opts taskstate.StartReviewOptions) (taskstate.ReviewAttempt, error)
	FinishReview(repoID, taskID string, attempt int, status taskstate.ReviewStatus) (taskstate.ReviewAttempt, error)
	MarkReviewAutonomousBudgetExhausted(repoID, taskID string, attempt int) (taskstate.ReviewAttempt, error)
	PromoteReviewAdvisoryFinding(repoID, taskID string, attempt int, findingIndex int) (taskstate.ReviewAttempt, error)
	RecordReviewFindingCreatedTask(repoID, taskID string, attempt int, findingIndex int, createdTaskID string) (taskstate.ReviewAttempt, error)
	AddressReviewBlockingFindingManually(repoID, taskID string, attempt int, findingIndex int, reason string) (taskstate.ReviewAttempt, error)
	WaiveReviewBlockingFinding(repoID, taskID string, attempt int, findingIndex int, reason string) (taskstate.ReviewAttempt, error)
	MarkReviewAutomatedBlockerDecisionKept(repoID, taskID string, attempt int) (taskstate.ReviewAttempt, error)
	PrepareReviewForTargetedFollowUp(repoID, taskID string, attempt int) (taskstate.ReviewAttempt, error)
	InterruptPrimaryReviewExecution(repoID, taskID string, attempt int, stepName string, opts taskstate.InterruptPrimaryReviewExecutionOptions) (taskstate.ReviewAttempt, error)
}

// ReviewLifecycleAgentRun describes an attached implementation/fix run that the
// frontend must execute using its chosen process launcher and streams.
type ReviewLifecycleAgentRun struct {
	RepoID       string
	TaskID       string
	Start        DispatchStartResult
	Prompt       string
	Environment  []string
	ExecutionDir string
}

// ReviewLifecycleAgentRunResult describes post-execution facts from an attached
// implementation/fix agent run. UsageError is separate from the execution error
// path so successful runs are not recorded as failed when telemetry capture fails.
type ReviewLifecycleAgentRunResult struct {
	UsageError error
}

// ReviewLifecycleAgentRunner executes an attached implementation/fix agent.
type ReviewLifecycleAgentRunner interface {
	RunReviewLifecycleAgent(ctx context.Context, run ReviewLifecycleAgentRun) (ReviewLifecycleAgentRunResult, error)
	IsStartError(error) bool
}

// ReviewPipelinePresentation contains frontend-owned review presentation hooks.
// Core pipeline execution facts are assembled by workflow from ReviewAttemptContext.
type ReviewPipelinePresentation struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	InteractiveOutput bool
	OutputWidth       int
	OutputWidthFunc   func() (int, bool)

	PauseBeforeManual       bool
	RenderManualStep        func(ReviewManualStepContext) error
	ConfirmManualCommand    func(step review.Step) (bool, error)
	PromptManualStep        func(ReviewManualStepPrompt) (review.ManualResult, error)
	PromptAutomatedBlockers func(review.AutomatedBlockerReview) ([]review.AutomatedBlockerDecision, error)
	PromptAlternateFindings func(review.AlternateReviewComparison) ([]review.AlternateFindingDecision, error)
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
	ConfirmRunningCompletionFinalization(ReviewAttemptContext, RunningCompletionConfirmation) (bool, error)
	PromptFreshReviewBlockerDispositions(ReviewAttemptContext, []FreshReviewBlocker) ([]FreshReviewBlockerDisposition, error)
}

// ReviewLifecycleService owns complete task-review lifecycle orchestration.
type ReviewLifecycleService struct {
	Paths                  state.Paths
	Sources                []task.RepositorySource
	BackendFactory         ReviewLifecycleBackendFactory
	RunStore               ReviewLifecycleStore
	PRProvider             pullrequest.Provider
	AgentRunner            ReviewLifecycleAgentRunner
	AgentLauncher          agentexec.Launcher
	Environment            []string
	UsageCaptureEnv        map[string]string
	ResumeSessionsEnabled  *bool
	Frontend               ReviewLifecycleFrontend
	PipelineRunner         func(review.PipelineRunOptions) (review.PipelineOutcome, error)
	ResolveCommand         func(DispatchCommandContext, string) (DispatchCommand, string, error)
	ResolveFollowUpCommand func(DispatchCommandContext, string) (DispatchCommand, string, error)
	Logger                 *slog.Logger
	ProcessProbe           ProcessProbe
}

// ReviewLifecycleOptions describes a task review invocation.
type ReviewLifecycleOptions struct {
	TaskID            string
	PipelineName      string
	DispatchAgentName string
}

// ReviewAfterRunCompletionOptions describes a just-finished task run that may
// continue into review.
type ReviewAfterRunCompletionOptions struct {
	RepoID                    string
	TaskID                    string
	RunAttempt                int
	SelectedDispatchAgentName string
	FallbackDispatchAgentName string
	PipelineName              string
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
	ReviewLifecycleOutcomePublicationRetry            ReviewLifecycleOutcomeKind = "publication_retry"
	ReviewLifecycleOutcomeAborted                     ReviewLifecycleOutcomeKind = "aborted"
	ReviewLifecycleOutcomePrimaryRecovered            ReviewLifecycleOutcomeKind = "primary_review_recovered"
	ReviewLifecycleOutcomePrimaryLive                 ReviewLifecycleOutcomeKind = "primary_review_live"
	ReviewLifecycleOutcomePrimaryUnknown              ReviewLifecycleOutcomeKind = "primary_review_unverifiable"
)

// ReviewLifecycleOutcome is the typed result returned to frontends.
type ReviewLifecycleOutcome struct {
	Kind               ReviewLifecycleOutcomeKind
	Context            ReviewAttemptContext
	Finalization       FinalizationResult
	RecoveryInspection AttachedExecutionInspection
	Err                error
}

// ReviewAttemptContext carries non-CLI facts for one review attempt.
type ReviewAttemptContext struct {
	paths                          state.Paths
	store                          ReviewLifecycleStore
	Source                         task.RepositorySource
	Task                           task.Task
	Workdir                        string
	Target                         tasktarget.Target
	Review                         taskstate.ReviewAttempt
	Pipeline                       review.Pipeline
	AgentConfig                    agent.Config
	Resumed                        bool
	ResumeAutomatedBlockerDecision bool
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
	Source            task.RepositorySource
	Task              task.Task
	Review            taskstate.ReviewAttempt
	Step              review.Step
	HunkNotes         []review.HunkNote
	IntegrationFlow   publication.IntegrationFlow
	DestinationBranch string
	Recorder          ReviewManualStepRecorder
}

// TaskID returns the resolved backend task id.
func (p ReviewManualStepPrompt) TaskID() string { return p.Task.ID }

// ReviewManualStepRecorder is the narrow workflow-owned persistence port for manual review input.
type ReviewManualStepRecorder interface {
	LatestReview() (taskstate.ReviewAttempt, error)
	SetIntegrationFlow(publication.IntegrationFlow) (taskstate.Finalization, error)
	SetIntegrationDestination(string) (taskstate.Finalization, error)
	RecordFinding(taskstate.ReviewFinding) (taskstate.ReviewAttempt, error)
	PromoteAdvisoryFinding(index int) (taskstate.ReviewAttempt, error)
}

// RepoID returns the registered repository id.
func (c ReviewAttemptContext) RepoID() string { return c.Source.Repository.ID }

// TaskID returns the resolved backend task id.
func (c ReviewAttemptContext) TaskID() string { return c.Task.ID }

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

// Run executes or resumes a task review, including autonomous follow-up loops
// and finalization after approval.
func (s ReviewLifecycleService) Run(
	ctx context.Context,
	opts ReviewLifecycleOptions,
) (outcome ReviewLifecycleOutcome, err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = gitmeta.ContextWithLogger(ctx, s.Logger)
	span := logging.Start(ctx, s.Logger, "task review workflow",
		slog.String("component", "workflow"),
		slog.String("operation", "task_review"),
		slog.String("task_id", opts.TaskID),
		slog.String("pipeline", strings.TrimSpace(opts.PipelineName)),
	)
	defer func() {
		attrs := []slog.Attr{}
		if outcome.Kind != "" {
			attrs = append(attrs, slog.String("outcome", string(outcome.Kind)))
		}
		if outcome.Context.Source.Repository.ID != "" {
			attrs = append(attrs, slog.String("repo_id", outcome.Context.RepoID()))
		}
		span.FinishError(ctx, err, attrs...)
	}()

	return s.runReviewLifecycle(ctx, opts)
}

func (s ReviewLifecycleService) runReviewLifecycle(
	ctx context.Context,
	opts ReviewLifecycleOptions,
) (ReviewLifecycleOutcome, error) {
	if err := s.validate(); err != nil {
		return reviewLifecycleOperationalFailure(ReviewAttemptContext{}, err), err
	}
	preflight, inspection, handled, err := s.recoverPrimaryReviewBeforeRouting(ctx, opts.TaskID)
	if err != nil {
		return reviewLifecycleOperationalFailure(preflight, err), err
	}
	if handled {
		return primaryReviewLifecycleOutcome(preflight, inspection), nil
	}
	maxAttempts, err := reviewMaxAutonomousReviewAttempts(s.Paths)
	if err != nil {
		err = fmt.Errorf("task run %s: %w", opts.TaskID, err)
		return reviewLifecycleOperationalFailure(preflight, err), err
	}
	start, err := s.startReview(ctx, opts.TaskID, opts.PipelineName)
	if err != nil {
		return reviewLifecycleStartError(start, err)
	}
	dispatchAgentName := opts.DispatchAgentName
	if start.Resumed && strings.TrimSpace(dispatchAgentName) == "" {
		dispatchAgentName, err = resumedReviewImplementerName(start)
		if err != nil {
			err = fmt.Errorf("task run %s: %w", start.TaskID(), err)
			return reviewLifecycleOperationalFailure(start, err), err
		}
	}
	return s.executeAutonomousReviewLoop(ctx, start, autonomousReviewLoopOptions{
		maxAttempts:       maxAttempts,
		dispatchAgentName: dispatchAgentName,
	})
}

func reviewLifecycleStartError(start ReviewAttemptContext, err error) (ReviewLifecycleOutcome, error) {
	var recovered primaryReviewRecoveredError
	if errors.As(err, &recovered) {
		return ReviewLifecycleOutcome{
			Kind: ReviewLifecycleOutcomePrimaryRecovered, Context: start, RecoveryInspection: recovered.inspection,
		}, nil
	}
	var notRecoverable primaryReviewNotRecoverableError
	if errors.As(err, &notRecoverable) {
		return primaryReviewLifecycleOutcome(start, notRecoverable.inspection), nil
	}
	return reviewLifecycleStartOutcome(start, err)
}

// RunAfterCompletedRun inspects a completed task run and starts review when the
// run produced an agent completion. It centralizes task-run-to-review lifecycle
// continuation below frontends.
func (s ReviewLifecycleService) RunAfterCompletedRun(
	ctx context.Context,
	opts ReviewAfterRunCompletionOptions,
) (ReviewLifecycleOutcome, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.RunStore == nil {
		err := errors.New("review lifecycle run store is required")
		return reviewLifecycleOperationalFailure(ReviewAttemptContext{}, err), false, err
	}
	shouldReview, err := CompletedTaskRunReadyForReview(s.RunStore, opts.RepoID, opts.TaskID, opts.RunAttempt)
	if err != nil {
		err = fmt.Errorf("task run %s: inspect completion before review: %w", opts.TaskID, err)
		return reviewLifecycleOperationalFailure(ReviewAttemptContext{}, err), false, err
	}
	if !shouldReview {
		return ReviewLifecycleOutcome{}, false, nil
	}
	dispatchAgentName := strings.TrimSpace(opts.SelectedDispatchAgentName)
	if dispatchAgentName == "" {
		dispatchAgentName = strings.TrimSpace(opts.FallbackDispatchAgentName)
	}
	outcome, err := s.Run(ctx, ReviewLifecycleOptions{
		TaskID:            opts.TaskID,
		PipelineName:      opts.PipelineName,
		DispatchAgentName: dispatchAgentName,
	})
	return outcome, true, err
}

func reviewLifecycleOperationalFailure(ctx ReviewAttemptContext, err error) ReviewLifecycleOutcome {
	return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeOperationalFail, Context: ctx, Err: err}
}

func primaryReviewLifecycleOutcome(ctx ReviewAttemptContext, inspection AttachedExecutionInspection) ReviewLifecycleOutcome {
	switch inspection.Condition {
	case AttachedExecutionRecoverable, AttachedExecutionAlreadyRecovered:
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomePrimaryRecovered, Context: ctx, RecoveryInspection: inspection}
	case AttachedExecutionLive:
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomePrimaryLive, Context: ctx, RecoveryInspection: inspection}
	default:
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomePrimaryUnknown, Context: ctx, RecoveryInspection: inspection}
	}
}

// recoverPrimaryReviewBeforeRouting performs task resolution and stale-primary
// reconciliation before loading replacement-review configuration.
func (s ReviewLifecycleService) recoverPrimaryReviewBeforeRouting(
	ctx context.Context,
	taskID string,
) (ReviewAttemptContext, AttachedExecutionInspection, bool, error) {
	base, err := s.resolveReviewTask(ctx, taskID)
	if err != nil {
		return ReviewAttemptContext{}, AttachedExecutionInspection{}, false, err
	}
	inspection, handled, err := s.reconcileActiveReviewExecution(ctx, base)
	return base, inspection, handled, err
}

func (s ReviewLifecycleService) resolveReviewTask(ctx context.Context, taskID string) (ReviewAttemptContext, error) {
	resolved, err := task.ResolveTaskSource(s.Sources, strings.TrimSpace(taskID))
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	backend, err := s.BackendFactory(resolved.Source)
	if err != nil {
		return ReviewAttemptContext{}, fmt.Errorf(
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
		return ReviewAttemptContext{}, err
	}
	return ReviewAttemptContext{
		paths: s.Paths, store: s.RunStore, Source: resolved.Source, Task: taskItem,
	}, nil
}

func (s ReviewLifecycleService) reconcileActiveReviewExecution(
	ctx context.Context,
	base ReviewAttemptContext,
) (AttachedExecutionInspection, bool, error) {
	primary, ok, err := latestPrimaryReviewExecution(s.RunStore, base)
	if err != nil {
		return AttachedExecutionInspection{}, false, fmt.Errorf(
			"task run %s: inspect primary reviewer: %w", base.TaskID(), err,
		)
	}
	if !ok {
		return AttachedExecutionInspection{}, false, nil
	}
	inspection, err := ReconcilePrimaryReviewExecution(
		ctx, s.Paths, s.RunStore, base.RepoID(), base.TaskID(),
		primary.Attempt, "task_review", s.ProcessProbe,
	)
	if err != nil {
		return inspection, false, fmt.Errorf("task run %s: recover primary reviewer: %w", base.TaskID(), err)
	}
	return inspection, reviewStartupStopsForInspection(inspection), nil
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

func reviewLifecycleStartOutcome(ctx ReviewAttemptContext, err error) (ReviewLifecycleOutcome, error) {
	if outcome, handled := freshReviewBlockerGuardOutcome(ctx, err); handled {
		return outcome, nil
	}
	return reviewLifecycleOperationalFailure(ctx, err), err
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

type autonomousReviewLoopOptions struct {
	maxAttempts       int
	dispatchAgentName string
}

var errReviewFollowUpIncomplete = errors.New("autonomous review follow-up completed without agent completion")

//nolint:funlen // The lifecycle loop keeps terminal outcomes and retry transitions together.
func (s ReviewLifecycleService) executeAutonomousReviewLoop(
	ctx context.Context,
	start ReviewAttemptContext,
	opts autonomousReviewLoopOptions,
) (ReviewLifecycleOutcome, error) {
	if opts.maxAttempts <= 0 {
		err := fmt.Errorf("task run %s: autonomous review attempt budget must be positive", start.TaskID())
		return reviewLifecycleOperationalFailure(start, err), err
	}

	attemptsUsed := 0
	current := start
	for {
		var result taskstate.ReviewStatus
		if current.Review.Status == taskstate.ReviewStatusBlocked {
			// A fresh invocation can continue an eligible blocked review without
			// asking the manual reviewer to keep the same findings again. That
			// blocked review is the first attempt in its fresh repair budget.
			attemptsUsed++
			result = taskstate.ReviewStatusBlocked
		} else {
			attemptsUsed++
			var err error
			result, err = s.executeReviewAttempt(ctx, current)
			if err != nil {
				return reviewLifecycleOperationalFailure(current, err), err
			}
		}

		switch result {
		case taskstate.ReviewStatusPassed:
			finalized, err := s.finalizeApprovedReview(ctx, current)
			if err != nil {
				return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomePublicationRetry, Context: current, Err: err}, err
			}
			return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomePassed, Context: current, Finalization: finalized}, nil
		case taskstate.ReviewStatusBlocked:
		case taskstate.ReviewStatusWaitingForManual:
			return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeWaitingForManual, Context: current}, nil
		case taskstate.ReviewStatusWaitingForAutomatedDecision:
			return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeWaitingForAutomatedDecision, Context: current}, nil
		case taskstate.ReviewStatusAborted:
			return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeAborted, Context: current}, nil
		case taskstate.ReviewStatusFailed:
			return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeOperationalFail, Context: current}, nil
		default:
			return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeBlocked, Context: current}, nil
		}

		latest, indexes, ok, err := latestAutonomousReviewBlockers(current.store, current.RepoID(), current.TaskID())
		if err != nil {
			return reviewLifecycleOperationalFailure(current, err), err
		}
		if !ok {
			return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeBlocked, Context: current}, nil
		}
		if attemptsUsed >= opts.maxAttempts {
			if _, err := current.store.MarkReviewAutonomousBudgetExhausted(current.RepoID(), current.TaskID(), latest.Attempt); err != nil {
				err = fmt.Errorf("task run %s: mark autonomous review budget exhausted: %w", current.TaskID(), err)
				return reviewLifecycleOperationalFailure(current, err), err
			}
			if err := s.Frontend.AutonomousBudgetExhausted(current, latest.Attempt, attemptsUsed); err != nil {
				return reviewLifecycleOperationalFailure(current, err), err
			}
			return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeExhausted, Context: current}, nil
		}

		if err := s.runAutonomousReviewFollowUp(ctx, current, opts.dispatchAgentName, latest.Attempt, indexes); err != nil {
			return autonomousFollowUpFailure(current, err)
		}
		next, err := s.startFreshAutonomousReview(ctx, current)
		if err != nil {
			return reviewLifecycleOperationalFailure(current, err), err
		}
		current = next
	}
}

func autonomousFollowUpFailure(current ReviewAttemptContext, err error) (ReviewLifecycleOutcome, error) {
	if errors.Is(err, errReviewFollowUpIncomplete) {
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeBlocked, Context: current}, nil
	}
	return reviewLifecycleOperationalFailure(current, err), err
}

func (s ReviewLifecycleService) pipelineRunOptions(runCtx context.Context, ctx ReviewAttemptContext) (review.PipelineRunOptions, error) {
	presentation, err := s.Frontend.PipelinePresentation(ctx)
	if err != nil {
		return review.PipelineRunOptions{}, fmt.Errorf("task run %s: %w", ctx.TaskID(), err)
	}
	var renderManualStep func(review.Step) error
	if presentation.RenderManualStep != nil {
		renderManualStep = func(step review.Step) error {
			manualCtx, err := s.manualStepContext(runCtx, ctx, step)
			if err != nil {
				return err
			}
			return presentation.RenderManualStep(manualCtx)
		}
	}
	promptManualStep := s.manualStepPrompt(ctx, presentation.PromptManualStep)
	return review.PipelineRunOptions{
		Context:           runCtx,
		Store:             ctx.store,
		Logger:            s.Logger,
		RepoID:            ctx.RepoID(),
		TaskID:            ctx.TaskID(),
		Branch:            ctx.Target.Branch,
		Workdir:           ctx.Workdir,
		Attempt:           ctx.Review,
		Pipeline:          ctx.Pipeline,
		SessionName:       ctx.Task.ReviewSessionName(),
		Stdout:            presentation.Stdout,
		Stderr:            presentation.Stderr,
		Stdin:             presentation.Stdin,
		InteractiveOutput: presentation.InteractiveOutput,
		OutputWidth:       presentation.OutputWidth,
		OutputWidthFunc:   presentation.OutputWidthFunc,
		AgentConfig:       ctx.AgentConfig,
		AgentLauncher:     s.AgentLauncher,
		Environment:       s.Environment,
		UsageCaptureEnv:   s.UsageCaptureEnv,
		RecordPrimaryChildPID: func(stepName string, pid int) error {
			return s.RecordPrimaryReviewChildPID(ctx.RepoID(), ctx.TaskID(), ctx.Review.Attempt, stepName, pid)
		},
		ResumeFromStep:                 ctx.Resumed,
		ResumeAutomatedBlockerDecision: ctx.ResumeAutomatedBlockerDecision,
		PauseBeforeManual:              presentation.PauseBeforeManual,
		RenderManualStep:               renderManualStep,
		ConfirmManualCommand:           presentation.ConfirmManualCommand,
		PromptManualStep:               promptManualStep,
		PromptAutomatedBlockers:        presentation.PromptAutomatedBlockers,
		PromptAlternateFindings:        presentation.PromptAlternateFindings,
	}, nil
}

// RecordPrimaryReviewChildPID persists a newly started primary reviewer's
// direct PID under the same mutation protection used for implementation runs.
func (s ReviewLifecycleService) RecordPrimaryReviewChildPID(repoID string, taskID string, attempt int, stepName string, pid int) error {
	return RecordPrimaryReviewChildPID(s.Paths, s.Logger, s.RunStore, repoID, taskID, attempt, stepName, pid)
}

func (s ReviewLifecycleService) manualStepPrompt(
	ctx ReviewAttemptContext,
	prompt func(ReviewManualStepPrompt) (review.ManualResult, error),
) func(review.ManualStep) (review.ManualResult, error) {
	if prompt == nil {
		return nil
	}
	return func(step review.ManualStep) (review.ManualResult, error) {
		flow, err := s.effectiveIntegrationFlow(ctx)
		if err != nil {
			return review.ManualResult{}, err
		}
		destination, err := s.effectiveIntegrationDestination(ctx)
		if err != nil {
			return review.ManualResult{}, err
		}
		return prompt(ReviewManualStepPrompt{
			Source:            ctx.Source,
			Task:              ctx.Task,
			Review:            ctx.Review,
			Step:              step.Step,
			HunkNotes:         step.HunkNotes,
			IntegrationFlow:   flow,
			DestinationBranch: destination,
			Recorder: reviewManualStepRecorder{
				store: ctx.store, repoID: ctx.RepoID(), taskID: ctx.TaskID(),
				attempt: ctx.Review.Attempt, step: step.Step.Name,
			},
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
	status, err := gitmeta.ShortStatus(runCtx, ctx.Workdir)
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
	store   ReviewLifecycleStore
	repoID  string
	taskID  string
	attempt int
	step    string
}

func (s ReviewLifecycleService) effectiveIntegrationDestination(ctx ReviewAttemptContext) (string, error) {
	state, err := s.RunStore.Load(ctx.RepoID(), ctx.TaskID())
	if err != nil {
		return "", fmt.Errorf("load task state for integration destination: %w", err)
	}
	if destination := strings.TrimSpace(taskstate.FinalizationFacts(state).DestinationBranch); destination != "" {
		return destination, nil
	}
	destination := strings.TrimSpace(ctx.Source.Repository.DefaultBranch)
	if destination == "" {
		return "", fmt.Errorf("repository %s has no registered default branch", ctx.RepoID())
	}
	return destination, nil
}

func (s ReviewLifecycleService) effectiveIntegrationFlow(ctx ReviewAttemptContext) (publication.IntegrationFlow, error) {
	state, err := s.RunStore.Load(ctx.RepoID(), ctx.TaskID())
	if err != nil {
		return "", fmt.Errorf("load task state for integration flow: %w", err)
	}
	if locked := taskstate.FinalizationFacts(state).IntegrationFlow; strings.TrimSpace(string(locked)) != "" {
		return locked, nil
	}
	config, err := publication.LoadConfig(s.Paths)
	if err != nil {
		return "", err
	}
	return publication.ResolveIntegrationFlow("", publication.IntegrationFlow(ctx.Source.Repository.IntegrationFlow), config.IntegrationFlow), nil
}

func (r reviewManualStepRecorder) LatestReview() (taskstate.ReviewAttempt, error) {
	taskState, err := r.store.Load(r.repoID, r.taskID)
	if err != nil {
		return taskstate.ReviewAttempt{}, fmt.Errorf("load review state: %w", err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Attempt != r.attempt {
		return taskstate.ReviewAttempt{}, fmt.Errorf("latest review attempt no longer matches attempt %d", r.attempt)
	}
	return latest, nil
}

func (r reviewManualStepRecorder) SetIntegrationFlow(flow publication.IntegrationFlow) (taskstate.Finalization, error) {
	return r.store.SetFinalizationIntegrationFlow(r.repoID, r.taskID, flow)
}

func (r reviewManualStepRecorder) SetIntegrationDestination(destination string) (taskstate.Finalization, error) {
	return r.store.SetFinalizationDestination(r.repoID, r.taskID, destination)
}

func (r reviewManualStepRecorder) RecordFinding(finding taskstate.ReviewFinding) (taskstate.ReviewAttempt, error) {
	finding.Step = r.step
	return r.store.RecordReviewFinding(r.repoID, r.taskID, r.attempt, finding)
}

func (r reviewManualStepRecorder) PromoteAdvisoryFinding(index int) (taskstate.ReviewAttempt, error) {
	return r.store.PromoteReviewAdvisoryFinding(r.repoID, r.taskID, r.attempt, index)
}

func (s ReviewLifecycleService) executeReviewAttempt(runCtx context.Context, ctx ReviewAttemptContext) (taskstate.ReviewStatus, error) {
	runner := s.PipelineRunner
	if runner == nil {
		runner = review.RunPipeline
	}
	opts, err := s.pipelineRunOptions(runCtx, ctx)
	if err != nil {
		_, _ = ctx.store.FinishReview(ctx.RepoID(), ctx.TaskID(), ctx.Review.Attempt, taskstate.ReviewStatusFailed)
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
		_, _ = ctx.store.FinishReview(ctx.RepoID(), ctx.TaskID(), ctx.Review.Attempt, taskstate.ReviewStatusFailed)
		return "", err
	}
	span.Finish(runCtx, logging.StatusSuccess, slog.String("review_status", string(outcome.Status)))
	if outcome.Status == taskstate.ReviewStatusWaitingForManual || outcome.Status == taskstate.ReviewStatusWaitingForAutomatedDecision {
		return outcome.Status, nil
	}
	if outcome.Status == taskstate.ReviewStatusPassed {
		shouldPublish, err := s.processSeparateTaskReviewCandidates(runCtx, ctx)
		if err != nil {
			_, _ = ctx.store.FinishReview(ctx.RepoID(), ctx.TaskID(), ctx.Review.Attempt, taskstate.ReviewStatusFailed)
			return "", err
		}
		if !shouldPublish {
			if _, err := ctx.store.FinishReview(ctx.RepoID(), ctx.TaskID(), ctx.Review.Attempt, taskstate.ReviewStatusAborted); err != nil {
				_, _ = ctx.store.FinishReview(ctx.RepoID(), ctx.TaskID(), ctx.Review.Attempt, taskstate.ReviewStatusFailed)
				return "", fmt.Errorf("task run %s: record aborted review: %w", ctx.TaskID(), err)
			}
			return taskstate.ReviewStatusAborted, nil
		}
	}
	if _, err := ctx.store.FinishReview(ctx.RepoID(), ctx.TaskID(), ctx.Review.Attempt, outcome.Status); err != nil {
		_, _ = ctx.store.FinishReview(ctx.RepoID(), ctx.TaskID(), ctx.Review.Attempt, taskstate.ReviewStatusFailed)
		return "", fmt.Errorf("task run %s: record %s review: %w", ctx.TaskID(), outcome.Status, err)
	}
	return outcome.Status, nil
}

func (s ReviewLifecycleService) startReview(
	ctx context.Context,
	taskID string,
	pipelineName string,
) (ReviewAttemptContext, error) {
	base, err := s.resolveReviewTask(ctx, taskID)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	inspection, handled, err := s.reconcileActiveReviewExecution(ctx, base)
	if err != nil {
		return base, err
	}
	if handled {
		return base, reviewStartupInspectionError(inspection)
	}
	requestedPipeline, err := s.resolveRequestedReviewPipeline(base, pipelineName)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	base, err = s.validateReviewCandidate(ctx, base)
	if err != nil {
		return ReviewAttemptContext{}, err
	}
	if resumed, ok, err := s.routeReviewResumption(base, pipelineName); err != nil || ok {
		return resumed, err
	}
	return s.selectFreshReview(base, requestedPipeline, pipelineName)
}

func reviewStartupInspectionError(inspection AttachedExecutionInspection) error {
	switch inspection.Condition {
	case AttachedExecutionRecoverable, AttachedExecutionAlreadyRecovered:
		return primaryReviewRecoveredError{inspection: inspection}
	default:
		return primaryReviewNotRecoverableError{inspection: inspection}
	}
}

func (s ReviewLifecycleService) resolveRequestedReviewPipeline(
	base ReviewAttemptContext,
	pipelineName string,
) (*review.Pipeline, error) {
	if strings.TrimSpace(pipelineName) == "" {
		return nil, nil
	}
	pipeline, err := ResolveTaskReviewPipeline(s.Paths, base.Source.Repository, pipelineName)
	if err != nil {
		return nil, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	return &pipeline, nil
}

func (s ReviewLifecycleService) validateReviewCandidate(
	ctx context.Context,
	base ReviewAttemptContext,
) (ReviewAttemptContext, error) {
	target, err := ReviewTarget(s.RunStore, s.Paths, base)
	if err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	base.Target = target
	base.Workdir = target.Worktree
	if err := ValidateReviewCandidateReady(ctx, s.RunStore, base, target.Worktree); err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	return base, nil
}

func (s ReviewLifecycleService) routeReviewResumption(
	base ReviewAttemptContext,
	pipelineName string,
) (ReviewAttemptContext, bool, error) {
	paused, ok, err := latestAutomatedDecisionWaitingReview(s.RunStore, base)
	if err != nil {
		return ReviewAttemptContext{}, false, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	if ok {
		resumed, err := s.resumeReview(base, paused, pipelineName)
		return resumed, true, err
	}
	paused, ok, err = latestManualWaitingReview(s.RunStore, base)
	if err != nil {
		return ReviewAttemptContext{}, false, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	if ok {
		resumed, err := s.resumeReview(base, paused, pipelineName)
		return resumed, true, err
	}
	blocked, ok, err := latestEligibleBlockedReview(s.RunStore, base)
	if err != nil {
		return ReviewAttemptContext{}, false, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	if !ok {
		return ReviewAttemptContext{}, false, nil
	}
	resumed, err := s.continueBlockedReview(base, blocked, pipelineName)
	return resumed, true, err
}

func (s ReviewLifecycleService) selectFreshReview(
	base ReviewAttemptContext,
	requestedPipeline *review.Pipeline,
	pipelineName string,
) (ReviewAttemptContext, error) {
	interrupted, ok, err := latestInterruptedReviewComparison(s.RunStore, base)
	if err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	if ok {
		pipeline, err := resolveStoredTaskReviewPipeline(s.Paths, interrupted.Pipeline)
		if err != nil {
			return ReviewAttemptContext{}, fmt.Errorf(
				"task run %s: resolve interrupted comparison pipeline %q: %w",
				base.TaskID(), interrupted.Pipeline, err,
			)
		}
		return s.startFreshReviewAfterInterruptedComparison(base, pipeline)
	}
	if requestedPipeline != nil {
		return s.startFreshReview(base, *requestedPipeline)
	}
	interrupted, ok, err = latestInterruptedAutomatedBlockerReview(s.RunStore, base)
	if err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	if ok {
		pipelineName = interrupted.Pipeline
	}
	pipeline, err := ResolveTaskReviewPipeline(s.Paths, base.Source.Repository, pipelineName)
	if err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	return s.startFreshReview(base, pipeline)
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

type primaryReviewRecoveredError struct {
	inspection AttachedExecutionInspection
}

func (e primaryReviewRecoveredError) Error() string {
	return "primary reviewer execution recovered"
}

type primaryReviewNotRecoverableError struct {
	inspection AttachedExecutionInspection
}

func (e primaryReviewNotRecoverableError) Error() string {
	return "primary reviewer execution remains " + string(e.inspection.Condition) + " (" + e.inspection.Reason + ")"
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

func (s ReviewLifecycleService) startFreshReview(base ReviewAttemptContext, pipeline review.Pipeline) (ReviewAttemptContext, error) {
	base.Pipeline = pipeline
	base.Resumed = false
	if err := s.guardFreshReviewBlockers(base); err != nil {
		return base, err
	}
	prepared, err := s.preparePipeline(base)
	if err != nil {
		return base, err
	}
	base = prepared
	reviewAttempt, err := base.store.StartReviewWithOptions(base.RepoID(), base.TaskID(), taskstate.StartReviewOptions{Pipeline: pipeline.Name, Step: pipeline.Steps[0].Name})
	if err != nil {
		return base, fmt.Errorf("task run %s: start review attempt: %w", base.TaskID(), err)
	}
	base.Review = reviewAttempt
	return base, nil
}

// startFreshReviewAfterInterruptedComparison starts the recorded pipeline without treating
// incomplete-comparison findings as candidates for disposition or follow-up.
func (s ReviewLifecycleService) startFreshReviewAfterInterruptedComparison(base ReviewAttemptContext, pipeline review.Pipeline) (ReviewAttemptContext, error) {
	base.Pipeline = pipeline
	base.Resumed = false
	prepared, err := s.preparePipeline(base)
	if err != nil {
		return base, err
	}
	base = prepared
	reviewAttempt, err := base.store.StartReviewWithOptions(base.RepoID(), base.TaskID(), taskstate.StartReviewOptions{Pipeline: pipeline.Name, Step: pipeline.Steps[0].Name})
	if err != nil {
		return base, fmt.Errorf("task run %s: start review attempt: %w", base.TaskID(), err)
	}
	base.Review = reviewAttempt
	return base, nil
}

func (s ReviewLifecycleService) guardFreshReviewBlockers(base ReviewAttemptContext) error {
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

	blockers, automated := freshReviewBlockers(latest, indexes)
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

	for _, disposition := range dispositions {
		switch disposition.Action {
		case FreshReviewBlockerActionKeep:
			if automated[disposition.FindingIndex] {
				if _, err := base.store.MarkReviewAutomatedBlockerDecisionKept(base.RepoID(), base.TaskID(), latest.Attempt); err != nil {
					return fmt.Errorf("task run %s: record kept automated blocker finding %d: %w", base.TaskID(), disposition.FindingIndex+1, err)
				}
			}
		case FreshReviewBlockerActionAddressedManually:
			if _, err := base.store.AddressReviewBlockingFindingManually(base.RepoID(), base.TaskID(), latest.Attempt, disposition.FindingIndex, disposition.Reason); err != nil {
				return fmt.Errorf("task run %s: record manually addressed blocker finding %d: %w", base.TaskID(), disposition.FindingIndex+1, err)
			}
		case FreshReviewBlockerActionWaive:
			if _, err := base.store.WaiveReviewBlockingFinding(base.RepoID(), base.TaskID(), latest.Attempt, disposition.FindingIndex, disposition.Reason); err != nil {
				return fmt.Errorf("task run %s: waive prior blocker finding %d: %w", base.TaskID(), disposition.FindingIndex+1, err)
			}
		}
	}

	updated, err := base.store.Load(base.RepoID(), base.TaskID())
	if err != nil {
		return fmt.Errorf("task run %s: reload prior review blockers: %w", base.TaskID(), err)
	}
	latest, _ = taskstate.LatestReview(updated)
	if !taskstate.ReviewHasOpenBlockers(latest) {
		return nil
	}
	if _, err := base.store.PrepareReviewForTargetedFollowUp(base.RepoID(), base.TaskID(), latest.Attempt); err != nil {
		return fmt.Errorf("task run %s: preserve kept blockers for targeted follow-up: %w", base.TaskID(), err)
	}
	return freshReviewBlockerGuardError{}
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

func (s ReviewLifecycleService) resumeReview(base ReviewAttemptContext, paused taskstate.ReviewAttempt, pipelineName string) (ReviewAttemptContext, error) {
	pipeline, err := ResolvePausedTaskReviewPipeline(base.paths, base.Source.Repository, paused, pipelineName)
	if err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	base.Pipeline = pipeline
	base.Resumed = true
	base.ResumeAutomatedBlockerDecision = paused.Status == taskstate.ReviewStatusWaitingForAutomatedDecision
	prepared, err := s.preparePipeline(base)
	if err != nil {
		return base, err
	}
	base = prepared
	reviewAttempt, err := base.store.ResumeReview(base.RepoID(), base.TaskID(), paused.Attempt)
	if err != nil {
		return base, fmt.Errorf("task run %s: resume review attempt: %w", base.TaskID(), err)
	}
	base.Review = reviewAttempt
	if err := s.Frontend.ReviewResumed(base); err != nil {
		return base, err
	}
	return base, nil
}

func (s ReviewLifecycleService) preparePipeline(ctx ReviewAttemptContext) (ReviewAttemptContext, error) {
	agentConfig, err := ResolveReviewAgentConfig(ctx.paths, ctx.Pipeline)
	if err != nil {
		return ctx, fmt.Errorf("task run %s: %w", ctx.TaskID(), err)
	}
	if pipelineUsesAgentReview(ctx.Pipeline) && s.AgentLauncher == nil {
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

func latestPrimaryReviewExecution(store ReviewLifecycleStore, ctx ReviewAttemptContext) (PrimaryReviewExecution, bool, error) {
	taskState, err := store.Load(ctx.RepoID(), ctx.TaskID())
	if err != nil {
		return PrimaryReviewExecution{}, false, err
	}
	primary, ok := ActivePrimaryReviewExecution(taskState)
	return primary, ok, nil
}

func latestAutomatedDecisionWaitingReview(store ReviewLifecycleStore, ctx ReviewAttemptContext) (taskstate.ReviewAttempt, bool, error) {
	taskState, err := store.Load(ctx.RepoID(), ctx.TaskID())
	if err != nil {
		return taskstate.ReviewAttempt{}, false, fmt.Errorf("load task state: %w", err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Status != taskstate.ReviewStatusWaitingForAutomatedDecision {
		return taskstate.ReviewAttempt{}, false, nil
	}
	return latest, true, nil
}

func latestManualWaitingReview(store ReviewLifecycleStore, ctx ReviewAttemptContext) (taskstate.ReviewAttempt, bool, error) {
	taskState, err := store.Load(ctx.RepoID(), ctx.TaskID())
	if err != nil {
		return taskstate.ReviewAttempt{}, false, fmt.Errorf("load task state: %w", err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Status != taskstate.ReviewStatusWaitingForManual {
		return taskstate.ReviewAttempt{}, false, nil
	}
	return latest, true, nil
}

func latestEligibleBlockedReview(store ReviewLifecycleStore, ctx ReviewAttemptContext) (taskstate.ReviewAttempt, bool, error) {
	taskState, err := store.Load(ctx.RepoID(), ctx.TaskID())
	if err != nil {
		return taskstate.ReviewAttempt{}, false, fmt.Errorf("load task state: %w", err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Status != taskstate.ReviewStatusBlocked || latest.AutomatedBlockerDecisionInterrupted || taskstate.ReviewComparisonInputInterrupted(latest) {
		return taskstate.ReviewAttempt{}, false, nil
	}
	indexes, eligible := taskstate.UntargetedBlockingFindingIndexesForFollowUpInState(taskState, latest)
	return latest, eligible && len(indexes) > 0, nil
}

func (s ReviewLifecycleService) continueBlockedReview(base ReviewAttemptContext, blocked taskstate.ReviewAttempt, pipelineName string) (ReviewAttemptContext, error) {
	pipeline, err := ResolvePausedTaskReviewPipeline(base.paths, base.Source.Repository, blocked, pipelineName)
	if err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", base.TaskID(), err)
	}
	base.Pipeline = pipeline
	prepared, err := s.preparePipeline(base)
	if err != nil {
		return base, err
	}
	prepared.Review = blocked
	return prepared, nil
}

func latestInterruptedReviewComparison(store ReviewLifecycleStore, ctx ReviewAttemptContext) (taskstate.ReviewAttempt, bool, error) {
	taskState, err := store.Load(ctx.RepoID(), ctx.TaskID())
	if err != nil {
		return taskstate.ReviewAttempt{}, false, fmt.Errorf("load task state: %w", err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Status != taskstate.ReviewStatusBlocked || !taskstate.ReviewComparisonInputInterrupted(latest) || strings.TrimSpace(latest.Pipeline) == "" {
		return taskstate.ReviewAttempt{}, false, nil
	}
	return latest, true, nil
}

func latestInterruptedAutomatedBlockerReview(store ReviewLifecycleStore, ctx ReviewAttemptContext) (taskstate.ReviewAttempt, bool, error) {
	taskState, err := store.Load(ctx.RepoID(), ctx.TaskID())
	if err != nil {
		return taskstate.ReviewAttempt{}, false, fmt.Errorf("load task state: %w", err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Status != taskstate.ReviewStatusBlocked || !latest.AutomatedBlockerDecisionInterrupted {
		return taskstate.ReviewAttempt{}, false, nil
	}
	if strings.TrimSpace(latest.Pipeline) == "" {
		return taskstate.ReviewAttempt{}, false, nil
	}
	return latest, true, nil
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
func ReviewTarget(store ReviewLifecycleStore, paths state.Paths, ctx ReviewAttemptContext) (tasktarget.Target, error) {
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

// ValidateReviewCandidateReady ensures there is a read-only candidate to review.
func ValidateReviewCandidateReady(ctx context.Context, store ReviewLifecycleStore, reviewCtx ReviewAttemptContext, workdir string) error {
	if err := RequireCleanReviewIndex(ctx, workdir); err != nil {
		return err
	}
	hasCandidate, err := review.HasCandidateChanges(ctx, workdir)
	if err != nil {
		return err
	}
	if hasCandidate {
		return nil
	}
	taskState, err := store.Load(reviewCtx.RepoID(), reviewCtx.TaskID())
	if err != nil {
		return fmt.Errorf("load task state: %w", err)
	}
	if strings.TrimSpace(taskstate.FinalizationFacts(taskState).Commit) != "" {
		return nil
	}
	return fmt.Errorf("worktree %q has no candidate changes to review and task has no recorded finalization commit", workdir)
}

// RequireCleanReviewIndex rejects staged changes before read-only review.
func RequireCleanReviewIndex(ctx context.Context, workdir string) error {
	hasStagedChanges, err := gitmeta.HasStagedChanges(ctx, workdir)
	if err != nil {
		return err
	}
	if !hasStagedChanges {
		return nil
	}
	status, statusErr := gitmeta.ShortStatus(ctx, workdir)
	if statusErr != nil {
		status = "unable to read git status: " + statusErr.Error()
	}
	return fmt.Errorf("review requires a clean Git index, but staged changes are present in %q; unstage them, then rerun `orpheus task run <task-id>`\n%s", workdir, strings.TrimSpace(status))
}

func latestAutonomousReviewBlockers(store ReviewLifecycleStore, repoID string, taskID string) (taskstate.ReviewAttempt, []int, bool, error) {
	taskState, err := store.Load(repoID, taskID)
	if err != nil {
		return taskstate.ReviewAttempt{}, nil, false, fmt.Errorf("task run %s: load review blockers: %w", taskID, err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Status != taskstate.ReviewStatusBlocked || latest.AutomatedBlockerDecisionInterrupted || taskstate.ReviewComparisonInputInterrupted(latest) {
		return taskstate.ReviewAttempt{}, nil, false, nil
	}
	indexes, eligible := taskstate.UntargetedBlockingFindingIndexesForFollowUpInState(taskState, latest)
	return latest, indexes, eligible && len(indexes) > 0, nil
}

//nolint:funlen // Dispatch, execution, and completion checks are one lifecycle transaction.
func (s ReviewLifecycleService) runAutonomousReviewFollowUp(ctx context.Context, current ReviewAttemptContext, agentName string, reviewAttempt int, findingIndexes []int) error {
	if s.AgentRunner == nil {
		return errors.New("review lifecycle agent runner is required for autonomous follow-up")
	}
	if err := s.Frontend.AutonomousFollowUp(current, reviewAttempt, findingIndexes); err != nil {
		return err
	}
	backend, err := s.BackendFactory(current.Source)
	if err != nil {
		return fmt.Errorf("task run %s: create backend for autonomous follow-up: %w", current.TaskID(), err)
	}
	promptCapture := ""
	dispatch := DispatchService{
		Paths:                 current.paths,
		RunStore:              current.store,
		Logger:                s.Logger,
		UsageCaptureEnv:       s.UsageCaptureEnv,
		ResumeSessionsEnabled: s.ResumeSessionsEnabled,
	}
	start, err := dispatch.Start(ctx, DispatchStartOptions{
		TaskID:  current.TaskID(),
		Source:  current.Source,
		Backend: backend,
		ResolveCommand: func(commandContext DispatchCommandContext) (DispatchCommand, error) {
			if s.ResolveCommand == nil {
				return DispatchCommand{AgentName: agentName}, nil
			}
			command, prompt, err := s.ResolveCommand(commandContext, agentName)
			promptCapture = prompt
			return command, err
		},
		ResolveFollowUpCommand: func(commandContext DispatchCommandContext) (DispatchCommand, error) {
			if s.ResolveFollowUpCommand == nil {
				return DispatchCommand{AgentName: agentName}, nil
			}
			command, prompt, err := s.ResolveFollowUpCommand(commandContext, agentName)
			promptCapture = prompt
			return command, err
		},
	})
	if err != nil {
		return fmt.Errorf("task run %s: start autonomous follow-up: %w", current.TaskID(), err)
	}
	result, err := s.AgentRunner.RunReviewLifecycleAgent(ctx, ReviewLifecycleAgentRun{
		RepoID:       current.RepoID(),
		TaskID:       current.TaskID(),
		Start:        start,
		Prompt:       promptCapture,
		ExecutionDir: start.ExecutionDir,
	})
	if err != nil {
		recordErr := dispatch.Fail(DispatchFailureOptions{
			RepoID:      current.RepoID(),
			TaskID:      current.TaskID(),
			Attempt:     start.Attempt.Attempt,
			Cause:       err,
			StartFailed: s.AgentRunner.IsStartError(err),
		})
		if recordErr != nil {
			return fmt.Errorf("task run %s: %w; additionally failed to record run failure: %w", current.TaskID(), err, recordErr)
		}
		return fmt.Errorf("task run %s: %w", current.TaskID(), err)
	}
	if err := dispatch.Finish(current.RepoID(), current.TaskID(), start.Attempt.Attempt); err != nil {
		return fmt.Errorf("task run %s: record run finish: %w", current.TaskID(), err)
	}
	if result.UsageError != nil {
		return fmt.Errorf("task run %s: record run usage: %w", current.TaskID(), result.UsageError)
	}
	ready, err := CompletedTaskRunReadyForReview(current.store, current.RepoID(), current.TaskID(), start.Attempt.Attempt)
	if err != nil {
		return fmt.Errorf("task run %s: inspect autonomous follow-up completion: %w", current.TaskID(), err)
	}
	if !ready {
		if err := s.Frontend.FollowUpRunIncomplete(current, start.Attempt.Attempt); err != nil {
			return err
		}
		return errReviewFollowUpIncomplete
	}
	return nil
}

func (s ReviewLifecycleService) startFreshAutonomousReview(ctx context.Context, previous ReviewAttemptContext) (ReviewAttemptContext, error) {
	target, err := ReviewTarget(previous.store, previous.paths, previous)
	if err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", previous.TaskID(), err)
	}
	previous.Target = target
	previous.Workdir = target.Worktree
	if err := ValidateReviewCandidateReady(ctx, previous.store, previous, target.Worktree); err != nil {
		return ReviewAttemptContext{}, fmt.Errorf("task run %s: %w", previous.TaskID(), err)
	}
	return s.startFreshReview(previous, previous.Pipeline)
}

// CompletedTaskRunReadyForReview reports whether an attached run produced a completion.
func CompletedTaskRunReadyForReview(store DispatchRunStore, repoID string, taskID string, attempt int) (bool, error) {
	latest, ok, err := store.LatestRun(repoID, taskID)
	if err != nil || !ok {
		return false, err
	}
	return latest.Attempt == attempt && completedHandoffStatus(latest.Status) && latest.Completion != nil, nil
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
		return false, fmt.Errorf("task run %s: %w", ctx.TaskID(), err)
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
			return s.Frontend.ContinueAfterFollowUpCreationFailure(reviewCtx, candidate, err)
		}
		if _, err := reviewCtx.store.RecordReviewFindingCreatedTask(reviewCtx.RepoID(), reviewCtx.TaskID(), reviewCtx.Review.Attempt, candidate.Index, created.ID); err != nil {
			return false, fmt.Errorf("task run %s: record created follow-up task %s: %w", reviewCtx.TaskID(), created.ID, err)
		}
		if err := s.Frontend.SeparateTaskCreated(reviewCtx, candidate, created); err != nil {
			return false, err
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

func (s ReviewLifecycleService) finalizeApprovedReview(ctx context.Context, reviewCtx ReviewAttemptContext) (FinalizationResult, error) {
	service := FinalizationService{
		Paths:          s.Paths,
		Sources:        s.Sources,
		BackendFactory: func(source task.RepositorySource) (FinalizationBackend, error) { return s.BackendFactory(source) },
		RunStore:       s.RunStore,
		PRProvider:     s.PRProvider,
		Logger:         s.Logger,
	}
	opts := FinalizeOptions{TaskID: reviewCtx.TaskID(), RequirePassedReview: true}
	finalized, err := service.Finalize(ctx, opts)
	if err == nil {
		return finalized, nil
	}
	confirmation, ok := RunningCompletionConfirmationFromError(err)
	if !ok {
		return FinalizationResult{}, err
	}
	confirmed, confirmErr := s.Frontend.ConfirmRunningCompletionFinalization(reviewCtx, confirmation)
	if confirmErr != nil {
		return FinalizationResult{}, confirmErr
	}
	if !confirmed {
		return FinalizationResult{}, fmt.Errorf("finalization declined for running completion attempt %d", confirmation.Attempt)
	}
	opts.AllowRunningCompleted = true
	return service.Finalize(ctx, opts)
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
