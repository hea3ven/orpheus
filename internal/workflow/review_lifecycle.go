package workflow

import (
	"context"
	"errors"
	"fmt"
	"github.com/hea3ven/orpheus/internal/workflow/delivery"
	"github.com/hea3ven/orpheus/internal/workflow/preparation"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/hea3ven/orpheus/internal/workflow/implementation"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"
)

// ReviewLifecycleBackend is the backend capability set needed by the review
// lifecycle. It intentionally describes backend semantics, not a CLI adapter.
type ReviewLifecycleBackend interface {
	implementation.DispatchBackend
	delivery.Backend
	task.CreateMutator
}

// ReviewLifecycleBackendFactory creates a review-lifecycle backend for one repository.
type ReviewLifecycleBackendFactory func(task.RepositorySource) (ReviewLifecycleBackend, error)

// ReviewLifecycleStore persists and reads lifecycle state used by review orchestration.
type ReviewLifecycleStore interface {
	implementation.RecoveryStore
	CurrentReviewAt(work.AttemptRef) (taskstate.ReviewAttempt, error)
	RecordRepairBudgetStop(work.AttemptRef, int, int) (taskstate.ReviewAttempt, error)
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
	RenderManualStep        func(review.ManualStepContext) error
	ConfirmManualCommand    func(step review.Step) (bool, error)
	PromptManualStep        func(review.ManualStepPrompt) (review.ManualResult, error)
	PromptAutomatedBlockers func(review.AutomatedBlockerReview) ([]review.AutomatedBlockerDecision, error)
	PromptCheckoutRecovery  func(review.CheckoutViolation) (bool, error)
}

// ReviewLifecycleFrontend contains all operator-facing review interactions.
// Implementations may render to terminals, prompt users, and choose output
// streams; workflow only consumes typed decisions and callbacks.
type ReviewLifecycleFrontend interface {
	PipelinePresentation(ReviewAttemptContext) (ReviewPipelinePresentation, error)
	ReportReviewSelection(review.RevisionSelectionNotice) error
	ReviewResumed(ReviewAttemptContext) error
	AutonomousFollowUp(ReviewAttemptContext, int, []int) error
	AutonomousBudgetExhausted(ReviewAttemptContext, int, int) error
	FollowUpRunIncomplete(ReviewAttemptContext, int) error
	SelectSeparateTaskCandidates(ReviewAttemptContext, []review.SeparateTaskCandidate) ([]review.SeparateTaskCandidate, error)
	SeparateTaskCreated(ReviewAttemptContext, review.SeparateTaskCandidate, task.Task) error
	ContinueAfterFollowUpCreationFailure(ReviewAttemptContext, review.SeparateTaskCandidate, error) (bool, error)
	PromptFreshReviewBlockerDispositions(ReviewContext, []review.FreshReviewBlocker) ([]review.FreshReviewBlockerDisposition, error)
}

// ReviewLifecycleService coordinates specialized review execution and its typed
// records. Phase progression and repair-loop policy belong to Controller.
type ReviewLifecycleService struct {
	Progress        func(Progress) error
	ReviewEffects   review.Effects
	ReviewStatus    func(context.Context, string) (string, error)
	Paths           state.Paths
	Sources         []task.RepositorySource
	BackendFactory  ReviewLifecycleBackendFactory
	RunStore        ReviewLifecycleStore
	CandidateGit    review.ReviewCandidateGit
	OutputGit       implementation.OutputGit
	DispatchGit     preparation.Git
	AgentRunner     implementation.AgentRunner
	AgentLauncher   agentexec.Launcher
	Environment     []string
	UsageCaptureEnv map[string]string
	Frontend        ReviewLifecycleFrontend
	PipelineRunner  func(review.PipelineRunOptions) (review.PipelineOutcome, error)
	Logger          *slog.Logger
	ProcessProbe    phaseexec.ProcessProbe
	Parents         task.Getter
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
	RecoveryInspection phaseexec.AttachedExecutionInspection
	Err                error
}

// ReviewContext contains inputs that can be reused across review attempts.
// It deliberately excludes pipeline execution and continuation state.
type ReviewContext struct {
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
	current     *work.State
}

// Resumed reports whether this execution continues a paused review.
func (c ReviewAttemptContext) Resumed() bool { return c.execution.Resumed() }

// ResumesAutomatedBlockerDecision reports which paused interaction to present.
func (c ReviewAttemptContext) ResumesAutomatedBlockerDecision() bool {
	return c.execution.ResumesAutomatedDecision()
}

// RepoID returns the registered repository id.
func (c ReviewContext) RepoID() string { return c.Source.Repository.ID }

// TaskID returns the resolved backend task id.
func (c ReviewContext) TaskID() string { return c.Task.ID }

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

func reviewLifecycleOutcome(ctx ReviewAttemptContext, inspection phaseexec.AttachedExecutionInspection) ReviewLifecycleOutcome {
	switch inspection.Condition {
	case phaseexec.AttachedExecutionRecoverable, phaseexec.AttachedExecutionAlreadyRecovered:
		return ReviewLifecycleOutcome{Kind: ReviewLifecycleOutcomeExecutionRecovered, Context: ctx, RecoveryInspection: inspection}
	case phaseexec.AttachedExecutionLive:
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

func reviewStartupStopsForInspection(inspection phaseexec.AttachedExecutionInspection) bool {
	switch inspection.Condition {
	case phaseexec.AttachedExecutionRecoverable, phaseexec.AttachedExecutionAlreadyRecovered,
		phaseexec.AttachedExecutionLive, phaseexec.AttachedExecutionUnverifiable:
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
	presentation, err := s.Frontend.PipelinePresentation(ctx)
	if err != nil {
		return review.PipelineRunOptions{}, fmt.Errorf("task run %s: %w", ctx.TaskID(), err)
	}

	return review.PipelineRunOptions{
		Effects: s.ReviewEffects,

		ValidateAnswer: func(responseCtx context.Context, id phaseexec.InputID) error {
			_, _, err := (Controller{ReviewLifecycleService: s}).inspectReviewResponse(responseCtx, InteractionID{Owner: id.Owner, Version: id.Version, Kind: InteractionKind(id.Kind)})
			return err
		},
		Source:                  ctx.Source,
		Task:                    ctx.Task,
		CandidateGit:            s.CandidateGit,
		ReadStatus:              s.ReviewStatus,
		Initial:                 ctx.current,
		Context:                 runCtx,
		Runner:                  s.reviewRunner(ctx.RepoID(), ctx.TaskID()),
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
		AgentResults:            phaseexec.AgentResults{Commit: s.reviewRunner(ctx.RepoID(), ctx.TaskID()).Commit},
		Execution:               ctx.execution,
		PauseBeforeManual:       presentation.PauseBeforeManual,
		RenderManualStep:        presentation.RenderManualStep,
		ConfirmManualCommand:    presentation.ConfirmManualCommand,
		PromptManualStep:        presentation.PromptManualStep,
		PromptAutomatedBlockers: presentation.PromptAutomatedBlockers,
		PromptCheckoutRecovery:  presentation.PromptCheckoutRecovery,
	}, nil
}

// RecordReviewChildPID persists a newly started reviewer's
// direct PID under the same mutation protection used for implementation runs.
func (s ReviewLifecycleService) RecordReviewChildPID(ref work.AttemptRef, pid int) error {
	return review.RecordChildPID(s.reviewRunner(ref.RepositoryID, ref.TaskID), ref, pid)
}

func (s ReviewLifecycleService) reviewRunner(repoID, taskID string) phaseexec.Runner {
	return phaseexec.Runner{Paths: s.Paths, Store: workstate.NewStore(s.Paths), RepositoryID: repoID, TaskID: taskID, Logger: s.Logger}
}

func (s ReviewLifecycleService) executeReviewAttempt(runCtx context.Context, ctx ReviewAttemptContext) (taskstate.ReviewStatus, error) {
	runner := s.PipelineRunner
	if runner == nil {
		runner = review.RunPipeline
	}
	opts, err := s.pipelineRunOptions(runCtx, ctx)
	if err != nil {
		if !preservesReviewDecision(err) {
			_, _ = s.reviewRunner(ctx.RepoID(), ctx.TaskID()).Commit(context.WithoutCancel(runCtx), review.FailureChange(ctx.Review.Address, err))
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
			if _, recordErr := s.reviewRunner(ctx.RepoID(), ctx.TaskID()).Commit(context.WithoutCancel(runCtx), review.FailureChange(ctx.Review.Address, err)); recordErr != nil {
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
		_, _ = s.reviewRunner(ctx.RepoID(), ctx.TaskID()).Commit(context.WithoutCancel(runCtx), review.FailureChange(ctx.Review.Address, err))
		return "", fmt.Errorf("task run %s: record %s review: %w", ctx.TaskID(), outcome.Status, err)
	}
	return outcome.Status, nil
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

func validatedReviewOutcome(store interface {
	CurrentReviewAt(work.AttemptRef) (taskstate.ReviewAttempt, error)
}, ref work.AttemptRef, status taskstate.ReviewStatus) (taskstate.ReviewAttempt, error) {
	current, err := store.CurrentReviewAt(ref)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	if status == taskstate.ReviewStatusPassed && current.Status != taskstate.ReviewStatusPassed {
		return taskstate.ReviewAttempt{}, errors.New("passing pipeline outcome cannot replace missing or failed recorded requirements")
	}
	return current, nil
}

func preservesReviewDecision(err error) bool {
	return errors.Is(err, review.ErrInteractionRequired) || errors.Is(err, review.ErrInvalidExecution) || errors.Is(err, ErrProgressUnavailable) || errors.Is(err, review.ErrPresentationUnavailable)
}
