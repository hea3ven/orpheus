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
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// PipelineRunOptions describes one local review pipeline execution.
type PipelineRunOptions struct {
	Effects Effects
	Context context.Context
	Runner  phaseexec.Runner
	Initial *work.State
	current *work.State
	Logger  *slog.Logger

	RepoID       string
	TaskID       string
	Branch       string
	Workdir      string
	Source       task.RepositorySource
	Task         task.Task
	CandidateGit ReviewCandidateGit
	ReadStatus   func(context.Context, string) (string, error)

	Attempt     taskstate.ReviewAttempt
	Pipeline    Pipeline
	SessionName string

	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader

	OpenOutput     func(rolling bool) StepOutput
	StepStarted    func(Step) error
	ValidateAnswer func(context.Context, phaseexec.InputID) error
	Report         func(Notice, Step) error

	AgentConfig     agent.Config
	AgentLauncher   agentexec.Launcher
	Environment     []string
	UsageCaptureEnv map[string]string
	RecordChildPID  func(work.AttemptRef, int) error
	AgentResults    phaseexec.AgentResults

	Execution               ExecutionRequest
	PauseBeforeManual       bool
	RenderManualStep        func(ManualStepContext) error
	ConfirmManualCommand    func(step Step) (bool, error)
	PromptManualStep        func(ManualStepPrompt) (ManualResult, error)
	PromptAutomatedBlockers func(review AutomatedBlockerReview) ([]AutomatedBlockerDecision, error)
	PromptCheckoutRecovery  func(CheckoutViolation) (bool, error)
}

// PipelineOutcome records the terminal status from a pipeline execution.
type PipelineOutcome struct {
	Status taskstate.ReviewStatus
}

// ManualResult records the terminal status selected by an interactive manual step.
type ManualResult struct {
	Status     taskstate.ReviewStatus
	Stop       bool
	Submission *ManualSubmission
	InputID    *phaseexec.InputID
}

// AutomatedBlockerReview carries blocking findings recorded by one automated step.
type AutomatedBlockerReview struct {
	Step     Step
	Blockers []AutomatedBlocker
}

// AutomatedBlocker identifies one persisted review finding and its
// authoritative display number.
type AutomatedBlocker struct {
	Index   int
	Number  int
	Finding taskstate.ReviewFinding
}

// AutomatedBlockerAction records the operator decision for an automated blocker.
type AutomatedBlockerAction string

const (
	AutomatedBlockerActionKeep      AutomatedBlockerAction = "keep"
	AutomatedBlockerActionDowngrade AutomatedBlockerAction = "downgrade"
	AutomatedBlockerActionWaive     AutomatedBlockerAction = "waive"
	AutomatedBlockerActionRestart   AutomatedBlockerAction = "restart"
	AutomatedBlockerActionPause     AutomatedBlockerAction = "pause"
)

// AutomatedBlockerDecision applies one operator decision to a persisted finding.
type AutomatedBlockerDecision struct {
	FindingIndex int
	Action       AutomatedBlockerAction
	Reason       string
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
	status  taskstate.ReviewStatus
	stop    bool
	restart bool
}

var hunkNotePollInterval = 250 * time.Millisecond

// ErrManualInputUnavailable reports that an attached manual step could not
// continue because operator input disappeared. The review attempt should remain
// paused for task run resumption instead of being marked failed.
var ErrManualInputUnavailable = errors.New("manual review input unavailable")

// ErrInteractionRequired means the frontend cannot obtain an interactive operator
// decision. Executors must preserve pending work and propagate this error.
var ErrInteractionRequired = errors.New("operator interaction required")

// ErrAutomatedBlockerInputUnavailable reports that automated blocker
// classification could not continue because operator input disappeared. The
// decision remains pending without launching a targeted fix.
var ErrAutomatedBlockerInputUnavailable = errors.New("automated blocker decision input unavailable")

// RunPipeline executes a configured review pipeline.
func RunPipeline(opts PipelineRunOptions) (outcome PipelineOutcome, err error) {
	opts = normalizePipelineRunOptions(opts)
	if opts.Runner.Store == nil {
		return PipelineOutcome{}, fmt.Errorf("%w: review runner is required", ErrInvalidExecution)
	}

	var current work.State
	if opts.Initial != nil {
		current = *opts.Initial
	} else {
		current, err = opts.Runner.Load(opts.Context)
		if err != nil {
			return PipelineOutcome{}, fmt.Errorf("%w: load current review: %w", ErrInvalidExecution, err)
		}
	}

	if err := validatePipelineExecution(opts, current); err != nil {
		return PipelineOutcome{}, err
	}
	opts.current = &current
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
	defer func() {
		attrs := []slog.Attr{}
		if outcome.Status != "" {
			attrs = append(attrs, slog.String("review_status", string(outcome.Status)))
		}
		span.Finish(opts.Context, reviewPipelineDiagnosticStatus(opts.Context, outcome, err), attrs...)
	}()

	return executePipeline(opts)
}

func normalizePipelineRunOptions(opts PipelineRunOptions) PipelineRunOptions {
	if opts.Stdout == nil {
		opts.Stdout = io.Discard
	}
	if opts.Stderr == nil {
		opts.Stderr = io.Discard
	}
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	return opts
}

func executePipeline(opts PipelineRunOptions) (PipelineOutcome, error) {
	phase := NewPipelineExecution(opts)
	current := *opts.current
	for {
		selected, err := phase.Next(current)
		if err != nil {
			return phase.finishError(current, err)
		}
		op, executing := selected.Operation()
		if request, ok := selected.Input(); ok {
			id, answer := pipelineInteraction(opts, request)
			op, err = phase.Respond(current, id, answer)
			if err != nil {
				return phase.finishError(current, err)
			}
			executing = true
		}
		if executing {
			current, err = opts.Runner.Execute(opts.Context, current, op)
			if err != nil {
				return phase.finishError(current, err)
			}
			continue
		}
		outcome, _ := selected.Outcome()
		return outcome, nil
	}
}

func pipelineInteraction(opts PipelineRunOptions, request PipelineRequest) (phaseexec.InputID, PipelineAnswer) {
	switch request := request.(type) {
	case ManualCommandRequest:
		if opts.ConfirmManualCommand == nil {
			return request.ID, ManualCommandAnswer{Err: fmt.Errorf("task run %s: manual step %q requires manual command confirmation hook", opts.TaskID, request.Step.Name)}
		}
		confirmed, err := opts.ConfirmManualCommand(request.Step)
		if err != nil {
			err = fmt.Errorf("%w: %w", ErrPresentationUnavailable, err)
		}
		return request.ID, ManualCommandAnswer{Confirmed: confirmed, Err: err}
	case ManualAssessmentRequest:
		result, err := opts.PromptManualStep(request.Prompt)
		return request.ID, ManualAssessmentAnswer{Result: result, Err: err}
	case AutomatedAssessmentRequest:
		if opts.PromptAutomatedBlockers == nil {
			return request.ID, AutomatedAssessmentAnswer{Unavailable: true}
		}
		decisions, err := opts.PromptAutomatedBlockers(request.Review)
		return request.ID, AutomatedAssessmentAnswer{Decisions: decisions, Err: err}
	default:
		panic("unknown review pipeline request")
	}
}

// A failed operation still checks the checkout after the whole step, including
// a failed response or presentation. Safety receipts and explicit restoration
// keep their independent boundaries and never authorize continued execution.
func (s *PipelineExecution) finishError(current work.State, cause error) (PipelineOutcome, error) {
	a := s.active
	if a == nil {
		return s.interruptedManualOutcome(current, cause)
	}
	if a.checkCommand != nil {
		a.checkCommand.output.finish(OutputExpanded)
	}
	if a.reviewer != nil {
		a.reviewer.output.finish(OutputExpanded)
	}
	if a.check == nil {
		return s.interruptedManualOutcome(current, cause)
	}
	s.opts.current = &current
	if a.safetyVersion != nil {
		// A final safety update has already been attempted. A lost receipt stops
		// here; neither persistence nor restoration is retried generically.
		if current.Version == *a.safetyVersion {
			return PipelineOutcome{}, cause
		}
		return PipelineOutcome{}, errors.Join(cause, s.checkoutRecovery())
	}
	saved, err := s.opts.Runner.Execute(context.WithoutCancel(s.opts.Context), current, s.verifyOperation(nil, true))
	if err == nil {
		return s.interruptedManualOutcome(current, cause)
	}
	if saved.Version == current.Version {
		return PipelineOutcome{}, errors.Join(cause, err)
	}
	return PipelineOutcome{}, errors.Join(cause, err, s.checkoutRecovery())
}

func (s *PipelineExecution) checkoutRecovery() error {
	return ResolveCheckoutRecovery(CheckoutRecoveryOptions{Context: s.opts.Context, Owner: s.opts.Attempt.Address, Runner: s.opts.Runner, Prompt: s.opts.PromptCheckoutRecovery, Restore: s.opts.Effects.RestoreCandidate, Output: s.opts.Stderr})
}
func (s *PipelineExecution) interruptedManualOutcome(current work.State, cause error) (PipelineOutcome, error) {
	if s.active != nil && s.active.step.Kind == KindManual && !errors.Is(cause, ErrInteractionRequired) && (s.opts.Context.Err() != nil || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, ErrManualInputUnavailable)) {
		s.opts.current = &current
		latest, err := pipelineReview(s.opts, s.opts.Attempt.Address)
		if err != nil {
			return PipelineOutcome{}, errors.Join(cause, err)
		}
		if latest.Status != taskstate.ReviewStatusFailed {
			return PipelineOutcome{Status: taskstate.ReviewStatusWaitingForManual}, nil
		}
	}
	return PipelineOutcome{}, cause
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
	for index, step := range pipeline.Steps {
		if step.Name == stepName {
			return index, nil
		}
	}
	return 0, fmt.Errorf(
		"review pipeline %q does not contain current step %q; restore that step in config.yaml",
		pipeline.Name,
		stepName,
	)
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

func manualCommandCancellation(ctx context.Context, exitCode *int, err error) error {
	// A real nonnegative exit is independent failure/success evidence, even
	// when cancellation races with completion. A killed child has exit -1.
	if exitCode != nil && *exitCode >= 0 {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ctx.Err()
}

func runManualStepCommand(opts PipelineRunOptions, step Step, env []string) (*int, []HunkNote, error) {
	if !step.HunkNotes {
		exitCode, err := runStepCommand(opts, step, env)
		return exitCode, nil, err
	}
	return runHunkBackedManualCommand(opts, step, env)
}

func runHunkBackedManualCommand(opts PipelineRunOptions, step Step, env []string) (*int, []HunkNote, error) {
	mergedEnv := mergeEnvironment(opts.Environment, env)
	if opts.Effects.RunHunkCommand != nil {
		return opts.Effects.RunHunkCommand(HunkCommandOptions{
			Context:     opts.Context,
			Step:        step,
			Workdir:     opts.Workdir,
			Environment: mergedEnv,
			Stdout:      opts.Stdout,
			Stderr:      opts.Stderr,
		})
	}

	span := logging.Start(opts.Context, opts.Logger, "review command",
		reviewCommandAttrs(opts, step)...,
	)
	process := exec.CommandContext(opts.Context, executable(opts.Environment, step.Command), step.Args...)
	process.Dir = opts.Workdir
	process.Env = mergedEnv
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

func launchAgentReview(
	opts PipelineRunOptions,
	step Step,
	prepared agent.PreparedExecution,
	execution taskstate.AgentExecution,
	output StepOutput,
) agent.ExecutionResult {
	attrs := append(reviewCommandAttrs(opts, step),
		slog.String("agent", execution.Agent),
		slog.String("harness", execution.Harness),
	)
	span := logging.Start(opts.Context, opts.Logger, "review command", attrs...)
	attached := phaseexec.AttachedAgents{Launcher: opts.AgentLauncher, RecordPID: opts.RecordChildPID}
	result := attached.Run(opts.Context, execution.Address, phaseexec.AgentRun{
		Prepared: prepared, Execution: execution, Workdir: opts.Workdir, Branch: opts.Branch,
		Stdin: opts.Stdin, Stdout: output.Stdout, Stderr: output.Stderr,
		UsageEnvironment: usageCaptureEnvironment(opts), CaptureUsage: opts.Effects.CaptureUsage,
	})
	span.Finish(opts.Context, agentReviewCommandStatus(opts.Context, result.Err), agentReviewCommandExitAttrs(result.Err)...)
	return result
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

func finishAgentReviewExecution(opts PipelineRunOptions, step Step, address work.AttemptRef, result agent.ExecutionResult, mutationErr error) error {
	var process work.ProcessResult
	childPID := 0
	switch {
	case !result.Started:
		reason := "reviewer did not start"
		if result.Err != nil {
			reason = result.Err.Error()
		}
		process = &work.StartFailedProcess{At: time.Now().UTC(), Reason: reason}
	case result.Exit != nil:
		code := result.Exit.Code
		childPID = result.Exit.PID
		process = &work.ExitedProcess{At: result.Exit.At, Code: &code}
	default:
		// Leave the execution unresolved/running. Missing exit observations do
		// not prove absence of a child or authorize another reviewer/capture.
		return errors.Join(result.Err, errors.New("reviewer exit is unresolved; inspect execution recovery before retrying"))
	}
	observation := ProcessObservation{Address: address, Process: process, ChildPID: childPID}
	if mutationErr != nil {
		observation.ReadOnlyFailure = &work.ReadOnlyFailure{At: time.Now().UTC(), Reason: mutationErr.Error()}
	}
	if result.Err != nil && result.Exit != nil && result.Exit.Code == 0 {
		observation.ExecutionFailure = &work.ReviewExecutionFailure{At: time.Now().UTC(), Reason: result.Err.Error()}
	}
	_, terminalErr, usageErr := opts.AgentResults.ProcessThenUsage(opts.Context,
		ProcessChange(opts.Attempt.Attempt, step.Name, observation), UsageChange(address, result.Usage))
	if terminalErr != nil {
		terminalErr = fmt.Errorf("task run %s: record reviewer %q outcome: %w", opts.TaskID, step.Name, terminalErr)
	}
	if usageErr != nil {
		usageErr = fmt.Errorf("task run %s: record reviewer %q usage: %w", opts.TaskID, step.Name, usageErr)
	}
	return errors.Join(terminalErr, usageErr)
}

func usageCaptureEnvironment(opts PipelineRunOptions) map[string]string {
	if opts.UsageCaptureEnv != nil {
		return opts.UsageCaptureEnv
	}
	return agent.UsageCaptureEnvironment()
}

func automatedBlockersForStep(reviewAttempt taskstate.ReviewAttempt, stepName string) []AutomatedBlocker {
	return automatedBlockersForStepSince(reviewAttempt, stepName, 0)
}

func automatedBlockersForStepSince(reviewAttempt taskstate.ReviewAttempt, stepName string, firstIndex int) []AutomatedBlocker {
	blockers := make([]AutomatedBlocker, 0)
	authoritativeNumber := 0
	for index, finding := range reviewAttempt.Findings {
		if taskstate.AuditOnlyReviewFinding(reviewAttempt, finding) {
			continue
		}
		authoritativeNumber++
		if index < firstIndex || finding.Step != stepName || !taskstate.IsOpenBlockingReviewFinding(finding) {
			continue
		}
		blockers = append(blockers, AutomatedBlocker{
			Index:   index,
			Number:  authoritativeNumber,
			Finding: finding,
		})
	}
	return blockers
}

func automatedBlockerControlAction(decisions []AutomatedBlockerDecision) (AutomatedBlockerAction, bool, error) {
	var action AutomatedBlockerAction
	for _, decision := range decisions {
		if decision.Action != AutomatedBlockerActionRestart && decision.Action != AutomatedBlockerActionPause {
			continue
		}
		if action != "" && action != decision.Action {
			return "", false, errors.New("restart and pause cannot be selected together")
		}
		action = decision.Action
	}
	return action, action != "", nil
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
	if opts.Effects.RunCommand != nil {
		return opts.Effects.RunCommand(CommandOptions{Context: opts.Context, Step: step, Workdir: opts.Workdir, Environment: mergeEnvironment(opts.Environment, env), Stdout: stdout, Stderr: stderr})
	}
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
	return []string{
		"ORPHEUS_REPO_ID=" + opts.RepoID,
		"ORPHEUS_TASK_ID=" + opts.TaskID,
		"ORPHEUS_WORKTREE=" + opts.Workdir,
		"ORPHEUS_BRANCH=" + opts.Branch,
		"ORPHEUS_AGENT_PROMPT=" + prompt,
		"ORPHEUS_AGENT_PURPOSE=review",
		"ORPHEUS_REVIEW_ATTEMPT=" + strconv.Itoa(opts.Attempt.Attempt),
		"ORPHEUS_REVIEW_STEP=" + stepName,
	}
}

func commandLine(step Step) string {
	parts := make([]string, 0, len(step.Args)+1)
	parts = append(parts, strconv.Quote(step.Command))
	for _, arg := range step.Args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}
