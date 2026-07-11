package review

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// PipelineRunOptions describes one local review pipeline execution.
type PipelineRunOptions struct {
	Context context.Context
	Store   taskstate.Store

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

	AgentConfig   agent.Config
	AgentLauncher agent.Launcher

	RenderManualStep        func(step Step) error
	ConfirmManualCommand    func(step Step) (bool, error)
	PromptManualStep        func(step ManualStep) (ManualResult, error)
	PromptAutomatedBlockers func(review AutomatedBlockerReview) ([]AutomatedBlockerDecision, error)
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

// RunPipeline executes a configured review pipeline.
func RunPipeline(opts PipelineRunOptions) (PipelineOutcome, error) {
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	for _, step := range opts.Pipeline.Steps {
		stepEnv := stepEnvironment(opts, step.Name)
		outcome, err := runReadOnlyStep(opts.Context, opts.Workdir, func() (stepOutcome, error) {
			if err := writeStepHeader(opts.Stderr, step); err != nil {
				return stepOutcome{}, err
			}
			return runStep(opts, step, stepEnv)
		})
		if err != nil {
			return PipelineOutcome{}, err
		}
		if outcome.stop {
			return PipelineOutcome{Status: outcome.status}, nil
		}
	}
	return PipelineOutcome{Status: taskstate.ReviewStatusPassed}, nil
}

func runReadOnlyStep(
	ctx context.Context,
	workdir string,
	run func() (stepOutcome, error),
) (stepOutcome, error) {
	snapshot, err := captureCandidateSnapshot(ctx, workdir)
	if err != nil {
		return stepOutcome{}, fmt.Errorf("snapshot candidate changes: %w", err)
	}

	outcome, stepErr := run()
	mutationErr := restoreCandidateIfMutated(ctx, snapshot)
	if mutationErr != nil {
		return stepOutcome{}, mutationErr
	}
	if stepErr != nil {
		return stepOutcome{}, stepErr
	}
	return outcome, nil
}

func runStep(opts PipelineRunOptions, step Step, env []string) (stepOutcome, error) {
	switch step.Kind {
	case KindCheck:
		return runCheckStep(opts, step, env)
	case KindManual:
		return runManualStep(opts, step, env)
	case KindAgentReview:
		return runAgentReviewStep(opts, step, env)
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
		SuggestedAction: "Inspect the check output, fix the issue, then rerun task review.",
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
	if opts.RenderManualStep == nil || opts.PromptManualStep == nil {
		return stepOutcome{}, fmt.Errorf(
			"task review %s: manual step %q requires manual review hooks",
			opts.TaskID,
			step.Name,
		)
	}
	if err := opts.RenderManualStep(step); err != nil {
		return stepOutcome{}, fmt.Errorf("task review %s: %w", opts.TaskID, err)
	}

	var exitCode *int
	var hunkNotes []HunkNote
	if step.Command != "" {
		var err error
		exitCode, hunkNotes, err = runConfirmedManualCommand(opts, step, env)
		if err != nil {
			return stepOutcome{}, err
		}
		if exitCode == nil {
			return stepOutcome{status: taskstate.ReviewStatusAborted, stop: true}, nil
		}
	} else if err := recordStep(opts, step, nil, nil); err != nil {
		return stepOutcome{}, err
	}

	outcome, err := opts.PromptManualStep(ManualStep{
		Step:      step,
		HunkNotes: hunkNotes,
	})
	if err != nil {
		return stepOutcome{}, err
	}
	return stepOutcome{status: outcome.Status, stop: outcome.Stop}, nil
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
	process := exec.CommandContext(opts.Context, step.Command, step.Args...)
	process.Dir = opts.Workdir
	process.Env = append(os.Environ(), env...)
	process.Stdout = opts.Stdout
	process.Stderr = opts.Stderr

	if err := process.Start(); err != nil {
		return nil, nil, err
	}

	var latest []HunkNote
	if notes, err := captureHunkUserNotes(opts.Context, opts.Workdir); err == nil {
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
			latest = captureFinalHunkUserNotes(opts.Context, opts.Workdir, latest)
			exitCode := process.ProcessState.ExitCode()
			return &exitCode, latest, err
		case <-ticker.C:
			notes, err := captureHunkUserNotes(opts.Context, opts.Workdir)
			if err == nil {
				latest = notes
			}
		case <-opts.Context.Done():
			err := <-done
			latest = captureFinalHunkUserNotes(opts.Context, opts.Workdir, latest)
			exitCode := process.ProcessState.ExitCode()
			return &exitCode, latest, err
		}
	}
}

func captureFinalHunkUserNotes(ctx context.Context, workdir string, fallback []HunkNote) []HunkNote {
	notes, err := captureHunkUserNotes(ctx, workdir)
	if err != nil {
		return fallback
	}
	return notes
}

func captureHunkUserNotes(ctx context.Context, workdir string) ([]HunkNote, error) {
	command := exec.CommandContext(ctx, "hunk", "session", "comment", "list", "--repo", workdir, "--type", "user", "--json")
	command.Dir = workdir
	output, err := command.Output()
	if err != nil {
		return nil, err
	}

	var response struct {
		Comments []HunkNote `json:"comments"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("decode Hunk user notes: %w", err)
	}
	return response.Comments, nil
}

func writeStepHeader(output io.Writer, step Step) error {
	if output == nil {
		return nil
	}
	_, err := fmt.Fprintf(output, "\n== Review step: %s (%s) ==\n", step.Name, step.Kind)
	return err
}

func runAgentReviewStep(opts PipelineRunOptions, step Step, env []string) (stepOutcome, error) {
	if opts.AgentLauncher == nil {
		return stepOutcome{}, fmt.Errorf(
			"task review %s: agent_review step %q requires an agent launcher",
			opts.TaskID,
			step.Name,
		)
	}

	prompt := agent.RenderBootstrapPrompt()
	command, err := opts.AgentConfig.ResolveReviewerCommandWithValues(step.Agent, agent.InterpolationValues{
		Prompt:      prompt,
		SessionName: opts.SessionName,
	})
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
	runErr := launchAgentReview(opts, profile, command, env, output)
	finishedAt := time.Now().UTC()
	status := taskstate.RunStatusSucceeded
	if runErr != nil {
		status = taskstate.RunStatusFailed
	}
	if err := finishAgentReviewExecution(opts, step, command, execution, status, finishedAt, runErr); err != nil {
		output.finishExpanded()
		if runErr != nil {
			return stepOutcome{}, fmt.Errorf(
				"task review %s: run agent_review step %q: %w; additionally failed to record agent execution: %w",
				opts.TaskID,
				step.Name,
				runErr,
				err,
			)
		}
		return stepOutcome{}, err
	}
	if runErr != nil {
		output.finishExpanded()
		return stepOutcome{}, fmt.Errorf("task review %s: run agent_review step %q: %w", opts.TaskID, step.Name, runErr)
	}

	return finishAgentReviewStep(opts, step, output, initialFindingCount)
}

func launchAgentReview(
	opts PipelineRunOptions,
	profile agent.Profile,
	command agent.CommandSnapshot,
	env []string,
	output stepOutput,
) error {
	reviewerStdin := opts.Stdin
	if !profile.Interactive {
		reviewerStdin = nil
	}
	return opts.AgentLauncher.Run(opts.Context, command, agent.LaunchOptions{
		Dir:    opts.Workdir,
		Env:    env,
		Stdin:  reviewerStdin,
		Stdout: output.stdout(),
		Stderr: output.stderr(),
	})
}

func recordAgentReviewStep(opts PipelineRunOptions, step Step, command agent.CommandSnapshot) (taskstate.AgentExecution, error) {
	execution := taskstate.AgentExecution{
		Purpose:     taskstate.AgentExecutionPurposeReview,
		Status:      taskstate.RunStatusRunning,
		Agent:       command.AgentName,
		Profile:     command.AgentName,
		Harness:     command.Harness,
		Model:       command.Model,
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
	usageOpts := agentReviewUsageOptions(command, opts.Workdir, execution, finishedAt, runErr)
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
	finishedAt time.Time,
	runErr error,
) taskstate.RecordRunUsageOptions {
	if agent.IsStartError(runErr) {
		return taskstate.RecordRunUsageOptions{
			UsageCapture: taskstate.AgentUsageCapture{
				Status: taskstate.UsageCaptureUnknown,
				Reason: "agent process failed before usage capture",
			},
		}
	}
	if command.Harness != "codex" {
		return taskstate.RecordRunUsageOptions{
			UsageCapture: taskstate.AgentUsageCapture{
				Status: taskstate.UsageCaptureUnknown,
				Reason: "usage capture is not supported for harness " +
					formatUsageHarness(command.Harness),
			},
		}
	}
	return agent.CaptureCodexUsage(agent.CodexUsageCaptureOptions{
		ExecutionDir: workdir,
		StartedAt:    execution.StartedAt,
		FinishedAt:   finishedAt,
		Env:          agent.CodexUsageCaptureEnvironment(),
	})
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
	decisions := keepAutomatedBlockerDecisions(blockers)
	if opts.PromptAutomatedBlockers != nil {
		prompted, err := opts.PromptAutomatedBlockers(AutomatedBlockerReview{
			Step:     step,
			Blockers: blockers,
		})
		if err != nil {
			return false, fmt.Errorf("task review %s: review automated blockers: %w", opts.TaskID, err)
		}
		decisions = mergeAutomatedBlockerDecisions(decisions, prompted)
	}
	if err := applyAutomatedBlockerDecisions(opts, decisions); err != nil {
		return false, err
	}
	return currentReviewHasOpenBlockers(opts)
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
			continue
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
	process := exec.CommandContext(opts.Context, step.Command, step.Args...)
	process.Dir = opts.Workdir
	process.Env = append(os.Environ(), env...)
	process.Stdout = stdout
	process.Stderr = stderr

	err := process.Run()
	if process.ProcessState == nil {
		return nil, err
	}
	exitCode := process.ProcessState.ExitCode()
	return &exitCode, err
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

func stepEnvironment(opts PipelineRunOptions, stepName string) []string {
	prompt := agent.RenderBootstrapPrompt()
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
