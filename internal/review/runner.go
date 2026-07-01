package review

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

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

	AgentConfig   agent.Config
	AgentLauncher agent.Launcher

	RenderManualStep func(step Step) error
	PromptManualStep func(step Step) (ManualResult, error)
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

type stepOutcome struct {
	status taskstate.ReviewStatus
	stop   bool
}

// RunPipeline executes a configured review pipeline.
func RunPipeline(opts PipelineRunOptions) (PipelineOutcome, error) {
	if opts.Context == nil {
		opts.Context = context.Background()
	}
	for _, step := range opts.Pipeline.Steps {
		stepEnv := stepEnvironment(opts, step.Name)
		outcome, err := runReadOnlyStep(opts.Context, opts.Workdir, func() (stepOutcome, error) {
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
	exitCode, err := runStepCommand(opts, step, env)
	if recordErr := recordStep(opts, step, exitCode); recordErr != nil {
		return stepOutcome{}, recordErr
	}
	if err == nil {
		return stepOutcome{}, nil
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return stepOutcome{}, fmt.Errorf("task review %s: start check step %q: %w", opts.TaskID, step.Name, err)
	}

	finding := taskstate.ReviewFinding{
		Type:            taskstate.FindingTypeBlocking,
		Title:           fmt.Sprintf("Check %q failed", step.Name),
		Description:     fmt.Sprintf("%s exited with status %d.", commandLine(step), exitErr.ExitCode()),
		Step:            step.Name,
		SuggestedAction: "Inspect the check output, fix the issue, then rerun task review.",
	}
	if _, err := opts.Store.RecordReviewFinding(opts.RepoID, opts.TaskID, opts.Attempt.Attempt, finding); err != nil {
		return stepOutcome{}, fmt.Errorf("task review %s: record check finding: %w", opts.TaskID, err)
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
	if step.Command != "" {
		var err error
		exitCode, err = runStepCommand(opts, step, env)
		if recordErr := recordStep(opts, step, exitCode); recordErr != nil {
			return stepOutcome{}, recordErr
		}
		if err != nil {
			return stepOutcome{}, fmt.Errorf("task review %s: run manual step %q: %w", opts.TaskID, step.Name, err)
		}
	} else if err := recordStep(opts, step, nil); err != nil {
		return stepOutcome{}, err
	}

	outcome, err := opts.PromptManualStep(step)
	if err != nil {
		return stepOutcome{}, err
	}
	return stepOutcome{status: outcome.Status, stop: outcome.Stop}, nil
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

	stepForState := step
	stepForState.Command = command.Command
	stepForState.Args = command.Args
	if err := recordStep(opts, stepForState, nil); err != nil {
		return stepOutcome{}, err
	}

	err = opts.AgentLauncher.Run(opts.Context, command, agent.LaunchOptions{
		Dir:    opts.Workdir,
		Env:    env,
		Stdin:  opts.Stdin,
		Stdout: opts.Stdout,
		Stderr: opts.Stderr,
	})
	if err != nil {
		return stepOutcome{}, fmt.Errorf("task review %s: run agent_review step %q: %w", opts.TaskID, step.Name, err)
	}

	reviewAttempt, err := opts.Store.Load(opts.RepoID, opts.TaskID)
	if err != nil {
		return stepOutcome{}, fmt.Errorf("task review %s: load agent_review findings: %w", opts.TaskID, err)
	}
	latest, ok := taskstate.LatestReview(reviewAttempt)
	if !ok || latest.Attempt != opts.Attempt.Attempt {
		return stepOutcome{}, fmt.Errorf("task review %s: latest review attempt no longer matches attempt %d", opts.TaskID, opts.Attempt.Attempt)
	}
	for _, finding := range latest.Findings {
		if finding.Step == step.Name && finding.Type == taskstate.FindingTypeBlocking {
			_, writeErr := fmt.Fprintf(opts.Stderr, "Review blocked for %s by agent_review %q.\n", opts.TaskID, step.Name)
			return stepOutcome{status: taskstate.ReviewStatusBlocked, stop: true}, writeErr
		}
	}
	return stepOutcome{}, nil
}

func runStepCommand(opts PipelineRunOptions, step Step, env []string) (*int, error) {
	process := exec.CommandContext(opts.Context, step.Command, step.Args...)
	process.Dir = opts.Workdir
	process.Env = append(os.Environ(), env...)
	process.Stdout = opts.Stdout
	process.Stderr = opts.Stderr

	err := process.Run()
	if process.ProcessState == nil {
		return nil, err
	}
	exitCode := process.ProcessState.ExitCode()
	return &exitCode, err
}

func recordStep(opts PipelineRunOptions, step Step, exitCode *int) error {
	_, err := opts.Store.RecordReviewStep(
		opts.RepoID,
		opts.TaskID,
		opts.Attempt.Attempt,
		taskstate.RecordReviewStepOptions{
			Kind:     step.Kind,
			Name:     step.Name,
			Command:  step.Command,
			Args:     step.Args,
			ExitCode: exitCode,
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
