package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// PipelineStore persists and reads review pipeline state.
type PipelineStore interface {
	Load(repoID, taskID string) (taskstate.TaskState, error)
	RecordReviewFinding(repoID, taskID string, attempt int, finding taskstate.ReviewFinding) (taskstate.ReviewAttempt, error)
	PauseReviewForManual(repoID, taskID string, attempt int, step string) (taskstate.ReviewAttempt, error)
	ResumeReview(repoID, taskID string, attempt int) (taskstate.ReviewAttempt, error)
	FinishReviewStepExecution(repoID, taskID string, attempt int, stepName string, opts taskstate.FinishReviewStepExecutionOptions) (taskstate.ReviewAttempt, error)
	MarkReviewAutomatedBlockerDecisionInterrupted(repoID, taskID string, attempt int) (taskstate.ReviewAttempt, error)
	MarkReviewAutomatedBlockerDecisionKept(repoID, taskID string, attempt int) (taskstate.ReviewAttempt, error)
	DowngradeReviewBlockingFinding(repoID, taskID string, attempt int, findingIndex int, reason string) (taskstate.ReviewAttempt, error)
	WaiveReviewBlockingFinding(repoID, taskID string, attempt int, findingIndex int, reason string) (taskstate.ReviewAttempt, error)
	RecordReviewStep(repoID, taskID string, attempt int, opts taskstate.RecordReviewStepOptions) (taskstate.ReviewAttempt, error)
	StartReviewStepComparison(repoID, taskID string, attempt int, stepName string, execution taskstate.AgentExecution) (taskstate.ReviewAttempt, error)
	FinishReviewStepComparison(repoID, taskID string, attempt int, stepName string, opts taskstate.FinishReviewStepExecutionOptions) (taskstate.ReviewAttempt, error)
	RecordAlternateReviewFinding(repoID, taskID string, attempt int, stepName string, finding taskstate.ReviewFinding) (taskstate.ReviewAttempt, error)
	ClassifyAlternateReviewFindings(repoID, taskID string, attempt int, stepName string, decisions []taskstate.AlternateReviewFindingDecision) (taskstate.ReviewAttempt, error)
	MarkReviewComparisonInputInterrupted(repoID, taskID string, attempt int, stepName string) (taskstate.ReviewAttempt, error)
	RecordReviewComparisonFailure(repoID, taskID string, attempt int, stepName string, failure string) (taskstate.ReviewAttempt, error)
}

// PipelineRunOptions describes one local review pipeline execution.
type PipelineRunOptions struct {
	Context context.Context
	Store   PipelineStore
	Logger  *slog.Logger

	RepoID  string
	TaskID  string
	Branch  string
	Workdir string

	Attempt     taskstate.ReviewAttempt
	Pipeline    Pipeline
	SessionName string

	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	InteractiveOutput bool
	OutputWidth       int
	OutputWidthFunc   func() (int, bool)

	AgentConfig     agent.Config
	AgentLauncher   agentexec.Launcher
	Environment     []string
	UsageCaptureEnv map[string]string

	ResumeFromStep          bool
	PauseBeforeManual       bool
	RenderManualStep        func(step Step) error
	ConfirmManualCommand    func(step Step) (bool, error)
	PromptManualStep        func(step ManualStep) (ManualResult, error)
	PromptAutomatedBlockers func(review AutomatedBlockerReview) ([]AutomatedBlockerDecision, error)
	PromptAlternateFindings func(AlternateReviewComparison) ([]AlternateFindingDecision, error)
}

// PipelineOutcome records the terminal status from a pipeline execution.
type PipelineOutcome struct {
	Status taskstate.ReviewStatus
}

// ManualResult records the terminal status selected by an interactive manual step.
type ManualResult struct {
	Status taskstate.ReviewStatus
	Stop   bool
}

// ManualStep carries operator-facing manual step context after any configured
// manual command has finished.
type ManualStep struct {
	Step      Step
	HunkNotes []HunkNote
}

// AutomatedBlockerReview carries blocking findings recorded by one automated step.
type AutomatedBlockerReview struct {
	Step     Step
	Blockers []AutomatedBlocker
}

// AutomatedBlocker identifies one persisted review finding by index.
type AutomatedBlocker struct {
	Index   int
	Finding taskstate.ReviewFinding
}

// AutomatedBlockerAction records the operator decision for an automated blocker.
type AutomatedBlockerAction string

const (
	AutomatedBlockerActionKeep      AutomatedBlockerAction = "keep"
	AutomatedBlockerActionDowngrade AutomatedBlockerAction = "downgrade"
	AutomatedBlockerActionWaive     AutomatedBlockerAction = "waive"
)

// AutomatedBlockerDecision applies one operator decision to a persisted finding.
type AutomatedBlockerDecision struct {
	FindingIndex int
	Action       AutomatedBlockerAction
	Reason       string
}

// AlternateReviewComparison presents a primary/alternate finding comparison for classification.
type AlternateReviewComparison struct {
	Step               Step
	PrimaryExecution   *taskstate.AgentExecution
	AlternateExecution *taskstate.AgentExecution
	Primary            []AutomatedBlocker
	Alternate          []AlternateFinding
}

// AlternateFinding identifies a persisted raw alternate finding by its comparison-local index.
type AlternateFinding struct {
	Index   int
	Finding taskstate.ReviewFinding
}

// AlternateFindingDecision classifies one alternate finding.
type AlternateFindingDecision struct {
	FindingIndex   int
	Classification taskstate.AlternateFindingClassification
	DuplicateOf    int
}

// HunkNote is a cached user-authored Hunk review note.
type HunkNote struct {
	NoteID    string `json:"noteId"`
	Source    string `json:"source"`
	FilePath  string `json:"filePath"`
	HunkIndex *int   `json:"hunkIndex,omitempty"`
	OldRange  []int  `json:"oldRange,omitempty"`
	NewRange  []int  `json:"newRange,omitempty"`
	Body      string `json:"body"`
	Title     string `json:"title,omitempty"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"createdAt,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type stepOutcome struct {
	status taskstate.ReviewStatus
	stop   bool
}

var hunkNotePollInterval = 250 * time.Millisecond

// ErrManualInputUnavailable reports that an attached manual step could not
// continue because operator input disappeared. The review attempt should remain
// paused for task review resumption instead of being marked failed.
var ErrManualInputUnavailable = errors.New("manual review input unavailable")

// ErrAutomatedBlockerInputUnavailable reports that automated blocker
// classification could not continue because operator input disappeared. The
// review attempt should finish blocked without launching a targeted fix.
var ErrAutomatedBlockerInputUnavailable = errors.New("automated blocker decision input unavailable")

// RunPipeline executes a configured review pipeline.
func RunPipeline(opts PipelineRunOptions) (PipelineOutcome, error) {
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	span := logging.Start(opts.Context, opts.Logger, "review pipeline",
		slog.String("component", "review"),
		slog.String("operation", "pipeline"),
		slog.String("repo_id", opts.RepoID),
		slog.String("task_id", opts.TaskID),
		slog.Int("attempt", opts.Attempt.Attempt),
		slog.String("pipeline", opts.Pipeline.Name),
		slog.String("branch", opts.Branch),
		slog.String("cwd", opts.Workdir),
	)
	var finalOutcome PipelineOutcome
	var finalErr error
	defer func() {
		attrs := []slog.Attr{}
		if finalOutcome.Status != "" {
			attrs = append(attrs, slog.String("review_status", string(finalOutcome.Status)))
		}
		span.Finish(opts.Context, reviewPipelineDiagnosticStatus(opts.Context, finalOutcome, finalErr), attrs...)
	}()

	startIndex := 0
	if opts.ResumeFromStep {
		var err error
		startIndex, err = pipelineStartIndex(opts.Pipeline, opts.Attempt.Step)
		if err != nil {
			finalErr = err
			return PipelineOutcome{}, err
		}
	}
	for _, step := range opts.Pipeline.Steps[startIndex:] {
		outcome, err := runReadOnlyStep(opts, step, func() (stepOutcome, error) {
			if step.Kind != KindManual {
				if err := writeStepHeader(opts.Stderr, step); err != nil {
					return stepOutcome{}, err
				}
			}
			return runStep(opts, step)
		})
		if err != nil {
			finalErr = err
			return PipelineOutcome{}, err
		}
		if outcome.stop {
			finalOutcome = PipelineOutcome{Status: outcome.status}
			return finalOutcome, nil
		}
	}
	finalOutcome = PipelineOutcome{Status: taskstate.ReviewStatusPassed}
	return finalOutcome, nil
}

func reviewPipelineDiagnosticStatus(ctx context.Context, outcome PipelineOutcome, err error) string {
	if err == nil {
		return logging.StatusSuccess
	}
	if ctx != nil && ctx.Err() != nil {
		return "canceled"
	}
	if outcome.Status != "" {
		return string(outcome.Status)
	}
	return logging.StatusFailure
}

func pipelineStartIndex(pipeline Pipeline, stepName string) (int, error) {
	stepName = strings.TrimSpace(stepName)
	if stepName == "" {
		return 0, nil
	}
	for index, step := range pipeline.Steps {
		if step.Name == stepName {
			return index, nil
		}
	}
	return 0, fmt.Errorf(
		"review pipeline %q does not contain pending step %q",
		pipeline.Name,
		stepName,
	)
}

func runReadOnlyStep(
	opts PipelineRunOptions,
	step Step,
	run func() (stepOutcome, error),
) (stepOutcome, error) {
	span := logging.Start(opts.Context, opts.Logger, "review step",
		slog.String("component", "review"),
		slog.String("operation", "step"),
		slog.String("repo_id", opts.RepoID),
		slog.String("task_id", opts.TaskID),
		slog.Int("attempt", opts.Attempt.Attempt),
		slog.String("pipeline", opts.Pipeline.Name),
		slog.String("step", step.Name),
		slog.String("kind", step.Kind),
		slog.String("cwd", opts.Workdir),
	)
	var outcome stepOutcome
	var finalErr error
	defer func() {
		attrs := []slog.Attr{}
		if outcome.status != "" {
			attrs = append(attrs, slog.String("review_status", string(outcome.status)))
		}
		span.Finish(opts.Context, reviewStepDiagnosticStatus(opts.Context, outcome, finalErr), attrs...)
	}()

	snapshot, err := captureCandidateSnapshot(opts.Context, opts.Workdir, opts.Logger, reviewStepAttrs(opts, step)...)
	if err != nil {
		finalErr = fmt.Errorf("snapshot candidate changes: %w", err)
		return stepOutcome{}, finalErr
	}

	outcome, stepErr := run()
	mutationErr := restoreCandidateIfMutated(opts.Context, snapshot, opts.Logger, reviewStepAttrs(opts, step)...)
	if mutationErr != nil {
		finalErr = mutationErr
		return stepOutcome{}, mutationErr
	}
	if stepErr != nil {
		finalErr = stepErr
		return stepOutcome{}, stepErr
	}
	return outcome, nil
}

func reviewStepDiagnosticStatus(ctx context.Context, outcome stepOutcome, err error) string {
	if err == nil {
		return logging.StatusSuccess
	}
	if ctx != nil && ctx.Err() != nil {
		return "canceled"
	}
	if outcome.status != "" {
		return string(outcome.status)
	}
	return logging.StatusFailure
}

func reviewStepAttrs(opts PipelineRunOptions, step Step) []slog.Attr {
	return []slog.Attr{
		slog.String("repo_id", opts.RepoID),
		slog.String("task_id", opts.TaskID),
		slog.Int("attempt", opts.Attempt.Attempt),
		slog.String("pipeline", opts.Pipeline.Name),
		slog.String("step", step.Name),
		slog.String("kind", step.Kind),
		slog.String("cwd", opts.Workdir),
	}
}

func runStep(opts PipelineRunOptions, step Step) (stepOutcome, error) {
	switch step.Kind {
	case KindCheck:
		env := stepEnvironment(opts, step.Name, agent.RenderBootstrapPrompt())
		return runCheckStep(opts, step, env)
	case KindManual:
		env := stepEnvironment(opts, step.Name, agent.RenderBootstrapPrompt())
		return runManualStep(opts, step, env)
	case KindAgentReview:
		return runAgentReviewStep(opts, step)
	default:
		return stepOutcome{}, fmt.Errorf(
			"task review %s: review step %q has unsupported kind %q",
			opts.TaskID,
			step.Name,
			step.Kind,
		)
	}
}

func runCheckStep(opts PipelineRunOptions, step Step, env []string) (stepOutcome, error) {
	output := newStepOutput(opts, true)
	exitCode, err := runStepCommandWithOutput(opts, step, env, output.stdout(), output.stderr())
	if recordErr := recordStep(opts, step, nil, exitCode); recordErr != nil {
		output.finishExpanded()
		return stepOutcome{}, recordErr
	}
	if err == nil {
		output.finishClear()
		return stepOutcome{}, nil
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		output.finishExpanded()
		return stepOutcome{}, fmt.Errorf("task review %s: start check step %q: %w", opts.TaskID, step.Name, err)
	}

	finding := taskstate.ReviewFinding{
		Type:            taskstate.FindingTypeBlocking,
		Title:           fmt.Sprintf("Check %q failed", step.Name),
		Description:     fmt.Sprintf("%s exited with status %d.", commandLine(step), exitErr.ExitCode()),
		Step:            step.Name,
		SuggestedAction: "Inspect the check output, fix the issue, then rerun task run.",
	}
	reviewAttempt, err := opts.Store.RecordReviewFinding(opts.RepoID, opts.TaskID, opts.Attempt.Attempt, finding)
	if err != nil {
		output.finishExpanded()
		return stepOutcome{}, fmt.Errorf("task review %s: record check finding: %w", opts.TaskID, err)
	}
	output.finishExpanded()
	findingIndex := len(reviewAttempt.Findings) - 1
	blocked, err := reviewAutomatedBlockers(opts, step, []AutomatedBlocker{{
		Index:   findingIndex,
		Finding: reviewAttempt.Findings[findingIndex],
	}})
	if err != nil {
		return stepOutcome{}, err
	}
	if !blocked {
		return stepOutcome{}, nil
	}
	_, writeErr := fmt.Fprintf(opts.Stderr, "Review blocked for %s by check %q.\n", opts.TaskID, step.Name)
	return stepOutcome{status: taskstate.ReviewStatusBlocked, stop: true}, writeErr
}

func runManualStep(opts PipelineRunOptions, step Step, env []string) (stepOutcome, error) {
	if _, err := opts.Store.PauseReviewForManual(opts.RepoID, opts.TaskID, opts.Attempt.Attempt, step.Name); err != nil {
		return stepOutcome{}, err
	}
	if opts.PauseBeforeManual {
		return pauseManualStep(opts, step)
	}
	if opts.RenderManualStep == nil || opts.PromptManualStep == nil {
		return failManualStep(opts, step, fmt.Errorf(
			"task review %s: manual step %q requires manual review hooks",
			opts.TaskID,
			step.Name,
		))
	}
	if err := opts.RenderManualStep(step); err != nil {
		return failManualStep(opts, step, fmt.Errorf("task review %s: %w", opts.TaskID, err))
	}

	prep, err := prepareManualStepPrompt(opts, step, env)
	if err != nil {
		return failManualStep(opts, step, err)
	}
	if prep.commandDeclined {
		if err := resumeManualReviewStep(opts, step); err != nil {
			return stepOutcome{}, err
		}
		return stepOutcome{status: taskstate.ReviewStatusAborted, stop: true}, nil
	}

	outcome, err := opts.PromptManualStep(ManualStep{
		Step:      step,
		HunkNotes: prep.hunkNotes,
	})
	if err != nil {
		return failManualStep(opts, step, err)
	}
	if err := resumeManualReviewStep(opts, step); err != nil {
		return stepOutcome{}, err
	}
	return stepOutcome{status: outcome.Status, stop: outcome.Stop}, nil
}

func pauseManualStep(opts PipelineRunOptions, step Step) (stepOutcome, error) {
	_, err := fmt.Fprintf(
		opts.Stderr,
		"Review for %s is waiting for manual step %q. Resume with `orpheus task run %s`.\n",
		opts.TaskID,
		step.Name,
		opts.TaskID,
	)
	if err != nil {
		return stepOutcome{}, err
	}
	return stepOutcome{status: taskstate.ReviewStatusWaitingForManual, stop: true}, nil
}

type manualStepPreparation struct {
	commandDeclined bool
	hunkNotes       []HunkNote
}

func prepareManualStepPrompt(
	opts PipelineRunOptions,
	step Step,
	env []string,
) (manualStepPreparation, error) {
	if step.Command == "" {
		return manualStepPreparation{}, recordStep(opts, step, nil, nil)
	}
	exitCode, hunkNotes, err := runConfirmedManualCommand(opts, step, env)
	if err != nil {
		return manualStepPreparation{}, err
	}
	return manualStepPreparation{
		commandDeclined: exitCode == nil,
		hunkNotes:       hunkNotes,
	}, nil
}

func failManualStep(opts PipelineRunOptions, step Step, err error) (stepOutcome, error) {
	if errors.Is(err, ErrManualInputUnavailable) || opts.Context.Err() != nil {
		return manualWaitingStepOutcome()
	}
	if resumeErr := resumeManualReviewStep(opts, step); resumeErr != nil {
		return stepOutcome{}, resumeErr
	}
	return stepOutcome{}, err
}

func manualWaitingStepOutcome() (stepOutcome, error) {
	return stepOutcome{status: taskstate.ReviewStatusWaitingForManual, stop: true}, nil
}

func resumeManualReviewStep(opts PipelineRunOptions, step Step) error {
	if _, err := opts.Store.ResumeReview(opts.RepoID, opts.TaskID, opts.Attempt.Attempt); err != nil {
		return fmt.Errorf("task review %s: resume manual step %q: %w", opts.TaskID, step.Name, err)
	}
	return nil
}

func runConfirmedManualCommand(opts PipelineRunOptions, step Step, env []string) (*int, []HunkNote, error) {
	if opts.ConfirmManualCommand == nil {
		return nil, nil, fmt.Errorf(
			"task review %s: manual step %q requires manual command confirmation hook",
			opts.TaskID,
			step.Name,
		)
	}
	confirmed, err := opts.ConfirmManualCommand(step)
	if err != nil {
		return nil, nil, err
	}
	if !confirmed {
		return nil, nil, nil
	}

	exitCode, hunkNotes, err := runManualStepCommand(opts, step, env)
	if recordErr := recordStep(opts, step, nil, exitCode); recordErr != nil {
		return nil, nil, recordErr
	}
	if err != nil {
		return nil, nil, fmt.Errorf("task review %s: run manual step %q: %w", opts.TaskID, step.Name, err)
	}
	return exitCode, hunkNotes, nil
}

func runManualStepCommand(opts PipelineRunOptions, step Step, env []string) (*int, []HunkNote, error) {
	if !step.HunkNotes {
		exitCode, err := runStepCommand(opts, step, env)
		return exitCode, nil, err
	}
	return runHunkBackedManualCommand(opts, step, env)
}

func runHunkBackedManualCommand(opts PipelineRunOptions, step Step, env []string) (*int, []HunkNote, error) {
	span := logging.Start(opts.Context, opts.Logger, "review command",
		reviewCommandAttrs(opts, step)...,
	)
	process := exec.CommandContext(opts.Context, executable(opts.Environment, step.Command), step.Args...)
	process.Dir = opts.Workdir
	process.Env = mergeEnvironment(opts.Environment, env)
	process.Stdout = opts.Stdout
	process.Stderr = opts.Stderr

	if err := process.Start(); err != nil {
		span.Finish(opts.Context, reviewCommandStatus(opts.Context, process, err), reviewCommandExitAttrs(process, err)...)
		return nil, nil, err
	}

	var latest []HunkNote
	if notes, err := captureHunkUserNotes(opts.Context, opts.Workdir, opts.Logger, opts.Environment, reviewStepAttrs(opts, step)...); err == nil {
		latest = notes
	}

	done := make(chan error, 1)
	go func() {
		done <- process.Wait()
	}()

	ticker := time.NewTicker(hunkNotePollInterval)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			latest = captureFinalHunkUserNotes(opts.Context, opts.Workdir, opts.Logger, opts.Environment, latest, reviewStepAttrs(opts, step)...)
			exitCode := process.ProcessState.ExitCode()
			span.Finish(opts.Context, reviewCommandStatus(opts.Context, process, err), reviewCommandExitAttrs(process, err)...)
			return &exitCode, latest, err
		case <-ticker.C:
			notes, err := captureHunkUserNotes(opts.Context, opts.Workdir, opts.Logger, opts.Environment, reviewStepAttrs(opts, step)...)
			if err == nil {
				latest = notes
			}
		case <-opts.Context.Done():
			err := <-done
			latest = captureFinalHunkUserNotes(opts.Context, opts.Workdir, opts.Logger, opts.Environment, latest, reviewStepAttrs(opts, step)...)
			exitCode := process.ProcessState.ExitCode()
			span.Finish(opts.Context, "canceled", reviewCommandExitAttrs(process, err)...)
			return &exitCode, latest, err
		}
	}
}

func captureFinalHunkUserNotes(
	ctx context.Context,
	workdir string,
	logger *slog.Logger,
	environment []string,
	fallback []HunkNote,
	attrs ...slog.Attr,
) []HunkNote {
	notes, err := captureHunkUserNotes(ctx, workdir, logger, environment, attrs...)
	if err != nil {
		return fallback
	}
	return notes
}

func captureHunkUserNotes(ctx context.Context, workdir string, logger *slog.Logger, environment []string, attrs ...slog.Attr) ([]HunkNote, error) {
	spanAttrs := []slog.Attr{
		slog.String("component", "review"),
		slog.String("operation", "hunk_notes_poll"),
		slog.String("cwd", workdir),
	}
	spanAttrs = append(spanAttrs, attrs...)
	span := logging.Start(ctx, logger, "hunk notes poll", spanAttrs...)
	command := exec.CommandContext(ctx, executable(environment, "hunk"), "session", "comment", "list", "--repo", workdir, "--type", "user", "--json")
	command.Dir = workdir
	command.Env = mergeEnvironment(environment, nil)
	output, err := command.Output()
	if err != nil {
		span.Finish(ctx, reviewCommandStatus(ctx, command, err), reviewCommandExitAttrs(command, err)...)
		return nil, err
	}

	var response struct {
		Comments []HunkNote `json:"comments"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		span.FinishError(ctx, err)
		return nil, fmt.Errorf("decode Hunk user notes: %w", err)
	}
	span.Finish(ctx, logging.StatusSuccess,
		slog.Int("note_count", len(response.Comments)),
		slog.Int("exit_code", command.ProcessState.ExitCode()),
	)
	return response.Comments, nil
}

func writeStepHeader(output io.Writer, step Step) error {
	if output == nil {
		return nil
	}
	_, err := fmt.Fprintf(output, "\n== Review step: %s (%s) ==\n", step.Name, step.Kind)
	return err
}

func runAgentReviewStep(opts PipelineRunOptions, step Step) (stepOutcome, error) {
	if opts.AgentLauncher == nil {
		return stepOutcome{}, fmt.Errorf("task review %s: agent_review step %q requires an agent launcher", opts.TaskID, step.Name)
	}
	command, err := opts.AgentConfig.ResolveReviewerCommandWithValues(step.Agent, agent.InterpolationValues{SessionName: opts.SessionName})
	if err != nil {
		return stepOutcome{}, fmt.Errorf("task review %s: resolve agent_review step %q: %w", opts.TaskID, step.Name, err)
	}
	_, profile, err := opts.AgentConfig.ResolveReviewerProfile(step.Agent)
	if err != nil {
		return stepOutcome{}, fmt.Errorf("task review %s: resolve agent_review step %q: %w", opts.TaskID, step.Name, err)
	}
	initialFindingCount, err := currentReviewFindingCount(opts)
	if err != nil {
		return stepOutcome{}, err
	}
	execution, err := recordAgentReviewStep(opts, step, command)
	if err != nil {
		return stepOutcome{}, err
	}
	output := newStepOutput(opts, !profile.Interactive)
	alternate := strings.TrimSpace(os.Getenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE"))
	primaryEnvironment := stepEnvironment(opts, step.Name, command.Prompt)
	if alternate != "" {
		primaryEnvironment = reviewerEnvironment(opts, step.Name, command.Prompt, "primary")
	}
	primaryErr := runReviewerExecution(opts, step, profile, command, primaryEnvironment, output)
	if err := finishAgentReviewExecution(opts, step, command, execution, statusForError(primaryErr), time.Now().UTC(), primaryErr); err != nil {
		output.finishExpanded()
		return stepOutcome{}, err
	}
	if primaryErr != nil {
		output.finishExpanded()
		return stepOutcome{}, fmt.Errorf("task review %s: run agent_review step %q: %w", opts.TaskID, step.Name, primaryErr)
	}
	if alternate != "" {
		if interrupted, err := runAlternateReviewerComparison(opts, step, alternate, output); err != nil {
			return stepOutcome{}, err
		} else if interrupted {
			return stepOutcome{status: taskstate.ReviewStatusBlocked, stop: true}, nil
		}
	}
	return finishAgentReviewStep(opts, step, output, initialFindingCount)
}

func statusForError(err error) taskstate.RunStatus {
	if err != nil {
		return taskstate.RunStatusFailed
	}
	return taskstate.RunStatusSucceeded
}

func runReviewerExecution(opts PipelineRunOptions, step Step, profile agent.Profile, command agent.CommandSnapshot, env []string, output stepOutput) error {
	_, err := runReadOnlyStep(opts, step, func() (stepOutcome, error) {
		return stepOutcome{}, launchAgentReview(opts, step, profile, command, env, output)
	})
	return err
}

func runAlternateReviewerComparison(opts PipelineRunOptions, step Step, alternate string, output stepOutput) (bool, error) {
	command, commandErr := opts.AgentConfig.ResolveReviewerCommandWithValues(alternate, agent.InterpolationValues{SessionName: opts.SessionName})
	_, profile, profileErr := opts.AgentConfig.ResolveReviewerProfile(alternate)
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: alternate, Profile: alternate, SessionName: opts.SessionName, StartedAt: time.Now().UTC()}
	if commandErr == nil {
		selection := command.AgentSelection()
		execution.Harness, execution.Model, execution.Thinking, execution.Command, execution.Args = selection.Harness, selection.Model, selection.Thinking, command.Command, command.Args
	}
	if _, err := opts.Store.StartReviewStepComparison(opts.RepoID, opts.TaskID, opts.Attempt.Attempt, step.Name, execution); err != nil {
		return false, err
	}
	if commandErr != nil || profileErr != nil {
		err := commandErr
		if err == nil {
			err = profileErr
		}
		if finishErr := finishAlternateReviewExecution(opts, step, command, execution, taskstate.RunStatusFailed, err); finishErr != nil {
			return false, finishErr
		}
		if _, recordErr := opts.Store.RecordReviewComparisonFailure(opts.RepoID, opts.TaskID, opts.Attempt.Attempt, step.Name, err.Error()); recordErr != nil {
			return false, recordErr
		}
		_, writeErr := fmt.Fprintf(opts.Stderr, "Alternate reviewer %q could not start: %v\n", alternate, err)
		return false, writeErr
	}
	alternateOutput := newStepOutput(opts, !profile.Interactive)
	runErr := runReviewerExecution(opts, step, profile, command, reviewerEnvironment(opts, step.Name, command.Prompt, "alternate"), alternateOutput)
	if err := finishAlternateReviewExecution(opts, step, command, execution, statusForError(runErr), runErr); err != nil {
		return false, err
	}
	if runErr != nil {
		alternateOutput.finishExpanded()
		if _, recordErr := opts.Store.RecordReviewComparisonFailure(opts.RepoID, opts.TaskID, opts.Attempt.Attempt, step.Name, runErr.Error()); recordErr != nil {
			return false, recordErr
		}
		_, writeErr := fmt.Fprintf(opts.Stderr, "Alternate reviewer %q failed; primary review remains authoritative: %v\n", alternate, runErr)
		return false, writeErr
	}
	alternateOutput.finishTail()
	interrupted, err := classifyAlternateReviewFindings(opts, step)
	if err != nil {
		return false, err
	}
	return interrupted, nil
}

func finishAlternateReviewExecution(opts PipelineRunOptions, step Step, command agent.CommandSnapshot, execution taskstate.AgentExecution, status taskstate.RunStatus, runErr error) error {
	usageOpts := agentReviewUsageOptions(command, opts.Workdir, execution, runErr, usageCaptureEnvironment(opts))
	_, err := opts.Store.FinishReviewStepComparison(opts.RepoID, opts.TaskID, opts.Attempt.Attempt, step.Name, taskstate.FinishReviewStepExecutionOptions{Status: status, FinishedAt: time.Now().UTC(), Session: usageOpts.Session, Usage: usageOpts.Usage, UsageCost: usageOpts.UsageCost, UsageCapture: usageOpts.UsageCapture, Model: usageOpts.Model})
	return err
}

func classifyAlternateReviewFindings(opts PipelineRunOptions, step Step) (bool, error) {
	state, err := opts.Store.Load(opts.RepoID, opts.TaskID)
	if err != nil {
		return false, err
	}
	latest, ok := taskstate.LatestReview(state)
	if !ok || latest.Attempt != opts.Attempt.Attempt {
		return false, fmt.Errorf("task review %s: latest review attempt no longer matches attempt %d", opts.TaskID, opts.Attempt.Attempt)
	}
	stepIndex := reviewStepExecutionIndex(latest, step.Name)
	if stepIndex < 0 {
		return false, fmt.Errorf("task review %s: agent_review step %q was not found", opts.TaskID, step.Name)
	}
	reviewStep := latest.Steps[stepIndex]
	comparison := reviewStep.Comparison
	if comparison == nil || len(comparison.AlternateFindings) == 0 {
		return false, nil
	}
	if opts.PromptAlternateFindings == nil {
		return interruptAlternateComparison(opts, step)
	}
	primary := make([]AutomatedBlocker, 0)
	for index, finding := range latest.Findings {
		if finding.Step == step.Name && finding.Reviewer != "alternate" {
			primary = append(primary, AutomatedBlocker{Index: index, Finding: finding})
		}
	}
	alternate := make([]AlternateFinding, len(comparison.AlternateFindings))
	for index, finding := range comparison.AlternateFindings {
		alternate[index] = AlternateFinding{Index: index, Finding: finding.Finding}
	}
	decisions, err := opts.PromptAlternateFindings(AlternateReviewComparison{Step: step, PrimaryExecution: reviewStep.Execution, AlternateExecution: comparison.AlternateExecution, Primary: primary, Alternate: alternate})
	if err != nil {
		if errors.Is(err, ErrAutomatedBlockerInputUnavailable) {
			return interruptAlternateComparison(opts, step)
		}
		return false, err
	}
	persisted := make([]taskstate.AlternateReviewFindingDecision, len(decisions))
	for i, decision := range decisions {
		persisted[i] = taskstate.AlternateReviewFindingDecision{FindingIndex: decision.FindingIndex, Classification: decision.Classification, DuplicateOf: decision.DuplicateOf}
	}
	if _, err := opts.Store.ClassifyAlternateReviewFindings(opts.RepoID, opts.TaskID, opts.Attempt.Attempt, step.Name, persisted); err != nil {
		return false, err
	}
	return false, nil
}

func reviewStepExecutionIndex(reviewAttempt taskstate.ReviewAttempt, stepName string) int {
	for index := len(reviewAttempt.Steps) - 1; index >= 0; index-- {
		if reviewAttempt.Steps[index].Name == stepName && reviewAttempt.Steps[index].Execution != nil {
			return index
		}
	}
	return -1
}

func interruptAlternateComparison(opts PipelineRunOptions, step Step) (bool, error) {
	if _, err := opts.Store.MarkReviewComparisonInputInterrupted(opts.RepoID, opts.TaskID, opts.Attempt.Attempt, step.Name); err != nil {
		return false, err
	}
	_, err := fmt.Fprintf(opts.Stderr, "Alternate reviewer comparison for %s was interrupted; run `orpheus task run %s` to start a fresh review.\n", opts.TaskID, opts.TaskID)
	return true, err
}

func launchAgentReview(
	opts PipelineRunOptions,
	step Step,
	profile agent.Profile,
	command agent.CommandSnapshot,
	env []string,
	output stepOutput,
) error {
	attrs := append(reviewCommandAttrs(opts, step),
		slog.String("agent", command.AgentName),
		slog.String("harness", command.Harness),
	)
	span := logging.Start(opts.Context, opts.Logger, "review command", attrs...)
	reviewerStdin := opts.Stdin
	if !profile.Interactive {
		reviewerStdin = nil
	}
	err := opts.AgentLauncher.Run(opts.Context, command.ExecCommand(), agentexec.LaunchOptions{
		Dir:    opts.Workdir,
		Env:    env,
		Stdin:  reviewerStdin,
		Stdout: output.stdout(),
		Stderr: output.stderr(),
	})
	span.Finish(opts.Context, agentReviewCommandStatus(opts.Context, err), agentReviewCommandExitAttrs(err)...)
	return err
}

func agentReviewCommandStatus(ctx context.Context, err error) string {
	if err == nil {
		return logging.StatusSuccess
	}
	if ctx != nil && ctx.Err() != nil {
		return "canceled"
	}
	if agentexec.IsStartError(err) {
		return "start_failure"
	}
	if _, ok := logging.ExitCode(err); ok {
		return "nonzero_exit"
	}
	return logging.StatusFailure
}

func agentReviewCommandExitAttrs(err error) []slog.Attr {
	if err == nil {
		return []slog.Attr{slog.Int("exit_code", 0)}
	}
	if exitCode, ok := logging.ExitCode(err); ok {
		return []slog.Attr{slog.Int("exit_code", exitCode)}
	}
	return nil
}

func recordAgentReviewStep(opts PipelineRunOptions, step Step, command agent.CommandSnapshot) (taskstate.AgentExecution, error) {
	selection := command.AgentSelection()
	execution := taskstate.AgentExecution{
		Purpose:     taskstate.AgentExecutionPurposeReview,
		Status:      taskstate.RunStatusRunning,
		Agent:       command.AgentName,
		Profile:     command.AgentName,
		Harness:     selection.Harness,
		Model:       selection.Model,
		Thinking:    selection.Thinking,
		Command:     command.Command,
		Args:        command.Args,
		SessionName: opts.SessionName,
		StartedAt:   time.Now().UTC(),
	}
	return execution, recordStep(opts, step, &execution, nil)
}

func finishAgentReviewExecution(
	opts PipelineRunOptions,
	step Step,
	command agent.CommandSnapshot,
	execution taskstate.AgentExecution,
	status taskstate.RunStatus,
	finishedAt time.Time,
	runErr error,
) error {
	usageOpts := agentReviewUsageOptions(command, opts.Workdir, execution, runErr, usageCaptureEnvironment(opts))
	_, err := opts.Store.FinishReviewStepExecution(
		opts.RepoID,
		opts.TaskID,
		opts.Attempt.Attempt,
		step.Name,
		taskstate.FinishReviewStepExecutionOptions{
			Status:       status,
			FinishedAt:   finishedAt,
			Session:      usageOpts.Session,
			Usage:        usageOpts.Usage,
			UsageCost:    usageOpts.UsageCost,
			UsageCapture: usageOpts.UsageCapture,
			Model:        usageOpts.Model,
		},
	)
	if err != nil {
		return fmt.Errorf("task review %s: record agent_review step %q execution: %w", opts.TaskID, step.Name, err)
	}
	return nil
}

func agentReviewUsageOptions(
	command agent.CommandSnapshot,
	workdir string,
	execution taskstate.AgentExecution,
	runErr error,
	environment map[string]string,
) taskstate.RecordRunUsageOptions {
	if agentexec.IsStartError(runErr) {
		return taskstate.RecordRunUsageOptions{
			UsageCapture: taskstate.AgentUsageCapture{
				Status: taskstate.UsageCaptureUnknown,
				Reason: "agent process failed before usage capture",
			},
		}
	}
	if command.Harness != "codex" && command.Harness != "pi" {
		return taskstate.RecordRunUsageOptions{
			UsageCapture: taskstate.AgentUsageCapture{
				Status: taskstate.UsageCaptureUnknown,
				Reason: "usage capture is not supported for harness " +
					formatUsageHarness(command.Harness),
			},
		}
	}
	return agent.CaptureUsage(agent.UsageCaptureOptions{
		Harness:      command.Harness,
		ExecutionDir: workdir,
		SessionName:  execution.SessionName,
		StartedAt:    execution.StartedAt,
		Env:          environment,
	})
}

func usageCaptureEnvironment(opts PipelineRunOptions) map[string]string {
	if opts.UsageCaptureEnv != nil {
		return opts.UsageCaptureEnv
	}
	return agent.UsageCaptureEnvironment()
}

func formatUsageHarness(harness string) string {
	harness = strings.TrimSpace(harness)
	if harness == "" {
		return "-"
	}
	return harness
}

func finishAgentReviewStep(
	opts PipelineRunOptions,
	step Step,
	output stepOutput,
	initialFindingCount int,
) (stepOutcome, error) {
	reviewAttempt, err := opts.Store.Load(opts.RepoID, opts.TaskID)
	if err != nil {
		output.finishExpanded()
		return stepOutcome{}, fmt.Errorf("task review %s: load agent_review findings: %w", opts.TaskID, err)
	}
	latest, ok := taskstate.LatestReview(reviewAttempt)
	if !ok || latest.Attempt != opts.Attempt.Attempt {
		output.finishExpanded()
		return stepOutcome{}, fmt.Errorf("task review %s: latest review attempt no longer matches attempt %d", opts.TaskID, opts.Attempt.Attempt)
	}
	blockers := make([]AutomatedBlocker, 0)
	hasStepFinding := false
	for index, finding := range latest.Findings {
		if finding.Step != step.Name {
			continue
		}
		hasStepFinding = true
		if index < initialFindingCount || !taskstate.IsOpenBlockingReviewFinding(finding) {
			continue
		}
		blockers = append(blockers, AutomatedBlocker{Index: index, Finding: finding})
	}
	if len(blockers) > 0 {
		output.finishExpanded()
		blocked, err := reviewAutomatedBlockers(opts, step, blockers)
		if err != nil {
			return stepOutcome{}, err
		}
		if blocked {
			_, writeErr := fmt.Fprintf(opts.Stderr, "Review blocked for %s by agent_review %q.\n", opts.TaskID, step.Name)
			return stepOutcome{status: taskstate.ReviewStatusBlocked, stop: true}, writeErr
		}
		return stepOutcome{}, nil
	}
	if hasStepFinding {
		output.finishTail()
	} else {
		output.finishClear()
	}
	return stepOutcome{}, nil
}

func currentReviewFindingCount(opts PipelineRunOptions) (int, error) {
	taskState, err := opts.Store.Load(opts.RepoID, opts.TaskID)
	if err != nil {
		return 0, fmt.Errorf("task review %s: load review findings: %w", opts.TaskID, err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Attempt != opts.Attempt.Attempt {
		return 0, fmt.Errorf("task review %s: latest review attempt no longer matches attempt %d", opts.TaskID, opts.Attempt.Attempt)
	}
	return len(latest.Findings), nil
}

func reviewAutomatedBlockers(
	opts PipelineRunOptions,
	step Step,
	blockers []AutomatedBlocker,
) (bool, error) {
	if len(blockers) == 0 {
		return currentReviewHasOpenBlockers(opts)
	}
	if opts.PromptAutomatedBlockers == nil {
		return interruptAutomatedBlockerDecision(opts)
	}
	decisions := keepAutomatedBlockerDecisions(blockers)
	prompted, err := opts.PromptAutomatedBlockers(AutomatedBlockerReview{
		Step:     step,
		Blockers: blockers,
	})
	if err != nil {
		if errors.Is(err, ErrAutomatedBlockerInputUnavailable) {
			return interruptAutomatedBlockerDecision(opts)
		}
		return false, fmt.Errorf("task review %s: review automated blockers: %w", opts.TaskID, err)
	}
	decisions = mergeAutomatedBlockerDecisions(decisions, prompted)
	if err := applyAutomatedBlockerDecisions(opts, decisions); err != nil {
		return false, err
	}
	return currentReviewHasOpenBlockers(opts)
}

func interruptAutomatedBlockerDecision(opts PipelineRunOptions) (bool, error) {
	if _, err := opts.Store.MarkReviewAutomatedBlockerDecisionInterrupted(
		opts.RepoID,
		opts.TaskID,
		opts.Attempt.Attempt,
	); err != nil {
		return false, fmt.Errorf("task review %s: record interrupted automated blocker decision: %w", opts.TaskID, err)
	}
	if _, err := fmt.Fprintf(
		opts.Stderr,
		"Automated blocker decisions for %s were interrupted; run `orpheus task run %s` to start a fresh review.\n",
		opts.TaskID,
		opts.TaskID,
	); err != nil {
		return false, err
	}
	return true, nil
}

func keepAutomatedBlockerDecisions(blockers []AutomatedBlocker) []AutomatedBlockerDecision {
	decisions := make([]AutomatedBlockerDecision, 0, len(blockers))
	for _, blocker := range blockers {
		decisions = append(decisions, AutomatedBlockerDecision{
			FindingIndex: blocker.Index,
			Action:       AutomatedBlockerActionKeep,
		})
	}
	return decisions
}

func mergeAutomatedBlockerDecisions(
	defaults []AutomatedBlockerDecision,
	overrides []AutomatedBlockerDecision,
) []AutomatedBlockerDecision {
	indexByFinding := make(map[int]int, len(defaults))
	for index, decision := range defaults {
		indexByFinding[decision.FindingIndex] = index
	}
	for _, override := range overrides {
		index, ok := indexByFinding[override.FindingIndex]
		if !ok {
			continue
		}
		defaults[index] = override
	}
	return defaults
}

func applyAutomatedBlockerDecisions(opts PipelineRunOptions, decisions []AutomatedBlockerDecision) error {
	for _, decision := range decisions {
		switch decision.Action {
		case AutomatedBlockerActionKeep:
			if _, err := opts.Store.MarkReviewAutomatedBlockerDecisionKept(
				opts.RepoID,
				opts.TaskID,
				opts.Attempt.Attempt,
			); err != nil {
				return fmt.Errorf(
					"task review %s: record kept automated blocker finding %d: %w",
					opts.TaskID,
					decision.FindingIndex+1,
					err,
				)
			}
		case AutomatedBlockerActionDowngrade:
			if _, err := opts.Store.DowngradeReviewBlockingFinding(
				opts.RepoID,
				opts.TaskID,
				opts.Attempt.Attempt,
				decision.FindingIndex,
				decision.Reason,
			); err != nil {
				return fmt.Errorf(
					"task review %s: downgrade automated blocker finding %d: %w",
					opts.TaskID,
					decision.FindingIndex+1,
					err,
				)
			}
		case AutomatedBlockerActionWaive:
			if _, err := opts.Store.WaiveReviewBlockingFinding(
				opts.RepoID,
				opts.TaskID,
				opts.Attempt.Attempt,
				decision.FindingIndex,
				decision.Reason,
			); err != nil {
				return fmt.Errorf(
					"task review %s: waive automated blocker finding %d: %w",
					opts.TaskID,
					decision.FindingIndex+1,
					err,
				)
			}
		default:
			return fmt.Errorf(
				"task review %s: automated blocker finding %d has unsupported action %q",
				opts.TaskID,
				decision.FindingIndex+1,
				decision.Action,
			)
		}
	}
	return nil
}

func currentReviewHasOpenBlockers(opts PipelineRunOptions) (bool, error) {
	taskState, err := opts.Store.Load(opts.RepoID, opts.TaskID)
	if err != nil {
		return false, fmt.Errorf("task review %s: load review blockers: %w", opts.TaskID, err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Attempt != opts.Attempt.Attempt {
		return false, fmt.Errorf("task review %s: latest review attempt no longer matches attempt %d", opts.TaskID, opts.Attempt.Attempt)
	}
	return taskstate.ReviewHasOpenBlockers(latest), nil
}

func runStepCommand(opts PipelineRunOptions, step Step, env []string) (*int, error) {
	return runStepCommandWithOutput(opts, step, env, opts.Stdout, opts.Stderr)
}

func runStepCommandWithOutput(
	opts PipelineRunOptions,
	step Step,
	env []string,
	stdout io.Writer,
	stderr io.Writer,
) (*int, error) {
	span := logging.Start(opts.Context, opts.Logger, "review command",
		reviewCommandAttrs(opts, step)...,
	)
	process := exec.CommandContext(opts.Context, executable(opts.Environment, step.Command), step.Args...)
	process.Dir = opts.Workdir
	process.Env = mergeEnvironment(opts.Environment, env)
	process.Stdout = stdout
	process.Stderr = stderr

	err := process.Run()
	finishAttrs := reviewCommandExitAttrs(process, err)
	span.Finish(opts.Context, reviewCommandStatus(opts.Context, process, err), finishAttrs...)
	if process.ProcessState == nil {
		return nil, err
	}
	exitCode := process.ProcessState.ExitCode()
	return &exitCode, err
}

func reviewCommandAttrs(opts PipelineRunOptions, step Step) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("component", "review"),
		slog.String("operation", reviewCommandOperation(step)),
	}
	return append(attrs, reviewStepAttrs(opts, step)...)
}

func reviewCommandOperation(step Step) string {
	if step.Kind == KindManual && step.HunkNotes {
		return "hunk_manual_command"
	}
	return step.Kind + "_command"
}

func reviewCommandStatus(ctx context.Context, process *exec.Cmd, err error) string {
	if err == nil {
		return logging.StatusSuccess
	}
	if ctx != nil && ctx.Err() != nil {
		return "canceled"
	}
	if process == nil || process.ProcessState == nil {
		return "start_failure"
	}
	if _, ok := logging.ExitCode(err); ok {
		return "nonzero_exit"
	}
	return logging.StatusFailure
}

func reviewCommandExitAttrs(process *exec.Cmd, err error) []slog.Attr {
	if process != nil && process.ProcessState != nil {
		return []slog.Attr{slog.Int("exit_code", process.ProcessState.ExitCode())}
	}
	if exitCode, ok := logging.ExitCode(err); ok {
		return []slog.Attr{slog.Int("exit_code", exitCode)}
	}
	return nil
}

func recordStep(opts PipelineRunOptions, step Step, execution *taskstate.AgentExecution, exitCode *int) error {
	_, err := opts.Store.RecordReviewStep(
		opts.RepoID,
		opts.TaskID,
		opts.Attempt.Attempt,
		taskstate.RecordReviewStepOptions{
			Kind:      step.Kind,
			Name:      step.Name,
			Execution: execution,
			ExitCode:  exitCode,
		},
	)
	if err != nil {
		return fmt.Errorf("task review %s: record review step %q: %w", opts.TaskID, step.Name, err)
	}
	return nil
}

func executable(environment []string, name string) string {
	if filepath.IsAbs(name) || strings.ContainsRune(name, os.PathSeparator) {
		return name
	}
	if environment == nil {
		environment = os.Environ()
	}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key != "PATH" {
			continue
		}
		for _, directory := range filepath.SplitList(value) {
			candidate := filepath.Join(directory, name)
			info, err := os.Stat(candidate)
			if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				absolute, absErr := filepath.Abs(candidate)
				if absErr == nil {
					return absolute
				}
			}
		}
	}
	return filepath.Join(os.TempDir(), "orpheus-missing-executable", filepath.Base(name))
}

func mergeEnvironment(base []string, overrides []string) []string {
	if base == nil {
		base = os.Environ()
	}
	merged := append([]string{}, base...)
	indexes := make(map[string]int, len(merged))
	for index, entry := range merged {
		if key, _, ok := strings.Cut(entry, "="); ok {
			indexes[key] = index
		}
	}
	for _, entry := range overrides {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			merged = append(merged, entry)
			continue
		}
		if index, ok := indexes[key]; ok {
			merged[index] = entry
			continue
		}
		indexes[key] = len(merged)
		merged = append(merged, entry)
	}
	return merged
}

func stepEnvironment(opts PipelineRunOptions, stepName string, prompt string) []string {
	return reviewerEnvironment(opts, stepName, prompt, "")
}

func reviewerEnvironment(opts PipelineRunOptions, stepName string, prompt string, reviewerRole string) []string {
	env := []string{
		"ORPHEUS_REPO_ID=" + opts.RepoID,
		"ORPHEUS_TASK_ID=" + opts.TaskID,
		"ORPHEUS_WORKTREE=" + opts.Workdir,
		"ORPHEUS_BRANCH=" + opts.Branch,
		"ORPHEUS_AGENT_PROMPT=" + prompt,
		"ORPHEUS_AGENT_PURPOSE=review",
		"ORPHEUS_REVIEW_ATTEMPT=" + strconv.Itoa(opts.Attempt.Attempt),
		"ORPHEUS_REVIEW_STEP=" + stepName,
	}
	if reviewerRole != "" {
		env = append(env, "ORPHEUS_REVIEWER_ROLE="+reviewerRole)
	}
	return env
}

func commandLine(step Step) string {
	parts := make([]string, 0, len(step.Args)+1)
	parts = append(parts, strconv.Quote(step.Command))
	for _, arg := range step.Args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}
