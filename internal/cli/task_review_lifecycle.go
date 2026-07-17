package cli

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

type taskReviewLifecycleFrontend struct {
	command *cobra.Command
	logger  *slog.Logger
	reader  *bufio.Reader
}

func (f *taskReviewLifecycleFrontend) PipelinePresentation(ctx workflow.ReviewAttemptContext) (workflow.ReviewPipelinePresentation, error) {
	return taskReviewPipelinePresentation(
		f.command,
		taskReviewStartFromWorkflow(ctx),
		f.reader,
		f.logger,
	), nil
}

func (f taskReviewLifecycleFrontend) ReviewResumed(ctx workflow.ReviewAttemptContext) error {
	_, err := fmt.Fprintf(
		f.command.ErrOrStderr(),
		"Resuming review attempt %d at manual step %q.\n",
		ctx.Review.Attempt,
		ctx.Review.Step,
	)
	return err
}

func (f taskReviewLifecycleFrontend) AutonomousFollowUp(
	ctx workflow.ReviewAttemptContext,
	reviewAttempt int,
	findingIndexes []int,
) error {
	_, err := fmt.Fprintf(
		f.command.ErrOrStderr(),
		"Autonomous review follow-up for %s targets review attempt %d finding(s) %s.\n",
		ctx.TaskID(),
		reviewAttempt,
		workflow.FormatReviewFindingIndexes(findingIndexes),
	)
	return err
}

func (f taskReviewLifecycleFrontend) AutonomousBudgetExhausted(
	ctx workflow.ReviewAttemptContext,
	_ int,
	attemptsUsed int,
) error {
	_, err := fmt.Fprintf(
		f.command.ErrOrStderr(),
		"Autonomous review attempt budget exhausted for %s after %d review attempt(s). "+
			"Open blockers were preserved. Run `orpheus task run %s` to continue with a fresh budget.\n",
		ctx.TaskID(),
		attemptsUsed,
		ctx.TaskID(),
	)
	return err
}

func (f taskReviewLifecycleFrontend) FollowUpRunIncomplete(ctx workflow.ReviewAttemptContext, runAttempt int) error {
	_, err := fmt.Fprintf(
		f.command.ErrOrStderr(),
		"Autonomous follow-up run attempt %d exited without completion; run `orpheus agent done` or `orpheus task run %s` before reviewing again.\n",
		runAttempt,
		ctx.TaskID(),
	)
	return err
}

func (f taskReviewLifecycleFrontend) SelectSeparateTaskCandidates(
	_ workflow.ReviewAttemptContext,
	candidates []workflow.SeparateTaskCandidate,
) ([]workflow.SeparateTaskCandidate, error) {
	output := f.command.ErrOrStderr()
	if _, err := fmt.Fprintln(output, "\nSeparate-task review findings can be created as standalone Beads:"); err != nil {
		return nil, err
	}
	for displayIndex, candidate := range candidates {
		if _, err := fmt.Fprintf(
			output,
			"%d. %s (review finding %d)\n",
			displayIndex+1,
			candidate.Finding.TaskProposal.Title,
			candidate.Index+1,
		); err != nil {
			return nil, err
		}
	}
	if _, err := fmt.Fprint(output, "Create follow-up Beads [numbers, a=all, n=none]: "); err != nil {
		return nil, err
	}
	line, err := f.reader.ReadString('\n')
	if err != nil && line == "" {
		return nil, fmt.Errorf("read follow-up task selection: %w", err)
	}
	return workflow.SelectSeparateTaskCandidates(candidates, line)
}

func (f taskReviewLifecycleFrontend) SeparateTaskCreated(
	_ workflow.ReviewAttemptContext,
	candidate workflow.SeparateTaskCandidate,
	created taskmodel.Task,
) error {
	_, err := fmt.Fprintf(
		f.command.ErrOrStderr(),
		"Created follow-up Bead %s for review finding %d.\n",
		created.ID,
		candidate.Index+1,
	)
	return err
}

func (f taskReviewLifecycleFrontend) ContinueAfterFollowUpCreationFailure(
	_ workflow.ReviewAttemptContext,
	candidate workflow.SeparateTaskCandidate,
	cause error,
) (bool, error) {
	output := f.command.ErrOrStderr()
	if _, err := fmt.Fprintf(
		output,
		"Failed to create follow-up Bead for review finding %d (%s): %v\n",
		candidate.Index+1,
		candidate.Finding.TaskProposal.Title,
		cause,
	); err != nil {
		return false, err
	}
	if _, err := fmt.Fprint(output, "Continue publication without creating this follow-up Bead? [y/N]: "); err != nil {
		return false, err
	}
	line, err := f.reader.ReadString('\n')
	if err != nil && line == "" {
		return false, fmt.Errorf("read follow-up task failure confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func (f taskReviewLifecycleFrontend) ConfirmRunningCompletionFinalization(
	_ workflow.ReviewAttemptContext,
	confirmation workflow.RunningCompletionConfirmation,
) (bool, error) {
	return confirmRunningCompletionFinalizationWithReader(f.command, confirmation, f.reader)
}

type taskReviewLifecycleAgentRunner struct {
	command *cobra.Command
	service workflow.DispatchService
}

func (r taskReviewLifecycleAgentRunner) IsStartError(err error) bool {
	return agentexec.IsStartError(err)
}

func (r taskReviewLifecycleAgentRunner) RunReviewLifecycleAgent(
	ctx context.Context,
	run workflow.ReviewLifecycleAgentRun,
) (workflow.ReviewLifecycleAgentRunResult, error) {
	if err := attachedAgentLauncher.Run(ctx, agentexec.Command{
		Name:    run.Start.Command.AgentName,
		Command: run.Start.Command.Command,
		Args:    append([]string{}, run.Start.Command.Args...),
	}, agentexec.LaunchOptions{
		Dir: run.Start.ExecutionDir,
		Env: taskRunEnvironment(
			run.RepoID,
			run.Start.Task.ID,
			run.Start.Setup.WorktreePath,
			run.Start.Setup.Branch,
			run.Prompt,
		),
		Stdin:  r.command.InOrStdin(),
		Stdout: r.command.OutOrStdout(),
		Stderr: r.command.ErrOrStderr(),
	}); err != nil {
		return workflow.ReviewLifecycleAgentRunResult{}, err
	}
	_, err := r.service.RunStore.RecordRunUsage(
		run.RepoID,
		run.TaskID,
		run.Start.Attempt.Attempt,
		agent.CaptureUsage(agent.UsageCaptureOptions{
			Harness:      run.Start.Attempt.Execution.Harness,
			ExecutionDir: run.Start.ExecutionDir,
			SessionName:  run.Start.Attempt.Execution.SessionName,
			StartedAt:    run.Start.Attempt.Execution.StartedAt,
			Env:          agent.UsageCaptureEnvironment(),
		}),
	)
	return workflow.ReviewLifecycleAgentRunResult{UsageError: err}, nil
}

func taskReviewStartFromWorkflow(ctx workflow.ReviewAttemptContext) taskReviewStart {
	return taskReviewStart{
		workdir:  ctx.Workdir,
		target:   ctx.Target,
		review:   ctx.Review,
		pipeline: ctx.Pipeline,
		resumed:  ctx.Resumed,
		resolvedCtx: resolvedTaskContext{
			Resolved: taskmodel.ResolvedTaskSource{
				TaskID: ctx.TaskID(),
				Source: ctx.Source,
			},
			Task:           ctx.Task,
			RegisteredRepo: registry.Repo{ID: ctx.Source.Repository.ID},
		},
	}
}

func newTaskReviewLifecycleService(
	command *cobra.Command,
	paths state.Paths,
	taskCtx taskContext,
	logger *slog.Logger,
	reader *bufio.Reader,
) workflow.ReviewLifecycleService {
	store := taskstate.NewStore(paths)
	service := workflow.ReviewLifecycleService{
		Paths:    paths,
		Sources:  taskCtx.Sources,
		RunStore: store,
		BackendFactory: func(source taskmodel.RepositorySource) (workflow.ReviewLifecycleBackend, error) {
			return newBeadsTaskBackend(source.BackendDir)
		},
		PRProvider:    pullrequest.GHProvider{},
		AgentLauncher: attachedAgentLauncher,
		Frontend: &taskReviewLifecycleFrontend{
			command: command,
			logger:  logger,
			reader:  reader,
		},
	}
	service.AgentRunner = taskReviewLifecycleAgentRunner{command: command, service: workflow.DispatchService{Paths: paths, RunStore: store}}
	service.ResolveCommand = func(commandContext workflow.DispatchCommandContext, agentName string) (workflow.DispatchCommand, string, error) {
		prompt, commandSnapshot, err := resolveTaskRunAgentCommand(paths, agentName, commandContext.SessionName)
		if err != nil {
			return workflow.DispatchCommand{}, "", err
		}
		return workflow.DispatchCommand{
			AgentName: commandSnapshot.AgentName,
			Command:   commandSnapshot.Command,
			Args:      commandSnapshot.Args,
			Harness:   commandSnapshot.Harness,
			Model:     commandSnapshot.Model,
		}, prompt, nil
	}
	service.ResolveFollowUpCommand = func(commandContext workflow.DispatchCommandContext, agentName string) (workflow.DispatchCommand, string, error) {
		prompt, commandSnapshot, err := resolveTaskRunFollowUpAgentCommand(paths, agentName, commandContext.SessionName)
		if err != nil {
			return workflow.DispatchCommand{}, "", err
		}
		return workflow.DispatchCommand{
			AgentName: commandSnapshot.AgentName,
			Command:   commandSnapshot.Command,
			Args:      commandSnapshot.Args,
			Harness:   commandSnapshot.Harness,
			Model:     commandSnapshot.Model,
		}, prompt, nil
	}
	return service
}
