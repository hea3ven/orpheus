package cli

import (
	"bufio"
	"context"
	"fmt"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/implementation"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workstate"
	"io"
	"log/slog"
	"strings"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/registry"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/spf13/cobra"
)

type taskReviewLifecycleFrontend struct {
	terminal TerminalCapabilities
	command  *cobra.Command
	logger   *slog.Logger
	reader   *bufio.Reader
}

func (f *taskReviewLifecycleFrontend) PipelinePresentation(ctx workflow.ReviewAttemptContext) (workflow.ReviewPipelinePresentation, error) {
	return taskReviewPipelinePresentation(
		f.command,
		taskReviewStartFromWorkflow(ctx),
		f.reader,
		f.logger,
		taskReviewExecutionOptions{interactiveManual: true, terminal: f.terminal},
	), nil
}

func (f taskReviewLifecycleFrontend) ReportReviewSelection(notice review.RevisionSelectionNotice) error {
	var text strings.Builder
	fmt.Fprintf(&text, "Implementation commit: %s\nSelected review commit: %s (%s)\n", notice.Implementation, notice.Selection.Revision.Commit, notice.Selection.Relationship)
	if len(notice.Selection.AddedCommits) > 0 {
		text.WriteString("Commits not in the implementation history:\n")
		for _, commit := range notice.Selection.AddedCommits {
			fmt.Fprintf(&text, "  %s\n", commit)
		}
	}
	_, err := io.WriteString(f.command.ErrOrStderr(), text.String())
	return err
}

func (f taskReviewLifecycleFrontend) ReviewResumed(ctx workflow.ReviewAttemptContext) error {
	message := "Resuming review attempt %d after answered step %q.\n"
	if len(ctx.Review.Steps) == 0 {
		message = "Starting reserved review attempt %d at step %q.\n"
	}
	if ctx.Review.Status == taskstate.ReviewStatusWaitingForManual {
		message = "Resuming review attempt %d at manual step %q.\n"
	}
	if ctx.ResumesAutomatedBlockerDecision() {
		message = "Resuming review attempt %d at automated blocker decision for step %q.\n"
	}
	_, err := fmt.Fprintf(f.command.ErrOrStderr(), message, ctx.Review.Attempt, ctx.Review.Step)
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
	ctx workflow.ReviewAttemptContext,
	candidates []review.SeparateTaskCandidate,
) ([]review.SeparateTaskCandidate, error) {
	if err := requireOperatorInput(f.command, f.terminal, ctx.TaskID()); err != nil {
		return nil, err
	}
	output := f.command.ErrOrStderr()
	if _, err := fmt.Fprintln(output, "\nSeparate-task review findings can be created as standalone tasks:"); err != nil {
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
	if _, err := fmt.Fprint(output, "Create follow-up tasks [numbers, a=all, n=none]: "); err != nil {
		return nil, err
	}
	line, err := f.reader.ReadString('\n')
	if err != nil {
		return nil, operatorInteractionError(ctx.TaskID(), err)
	}
	if answer := strings.ToLower(strings.TrimSpace(line)); answer == "q" || answer == "pause" {
		return nil, operatorInteractionError(ctx.TaskID(), nil)
	}
	return review.SelectSeparateTaskCandidates(candidates, line)
}

func (f taskReviewLifecycleFrontend) SeparateTaskCreated(
	_ workflow.ReviewAttemptContext,
	candidate review.SeparateTaskCandidate,
	created taskmodel.Task,
) error {
	_, err := fmt.Fprintf(
		f.command.ErrOrStderr(),
		"Created follow-up task %s for review finding %d.\n",
		created.ID,
		candidate.Index+1,
	)
	return err
}

func (f taskReviewLifecycleFrontend) ContinueAfterFollowUpCreationFailure(
	ctx workflow.ReviewAttemptContext,
	candidate review.SeparateTaskCandidate,
	cause error,
) (bool, error) {
	if err := requireOperatorInput(f.command, f.terminal, ctx.TaskID()); err != nil {
		return false, err
	}
	output := f.command.ErrOrStderr()
	if _, err := fmt.Fprintf(
		output,
		"Failed to create follow-up task for review finding %d (%s): %v\n",
		candidate.Index+1,
		candidate.Finding.TaskProposal.Title,
		cause,
	); err != nil {
		return false, err
	}
	if _, err := fmt.Fprint(output, "Continue publication without creating this follow-up task? [y/N]: "); err != nil {
		return false, err
	}
	line, err := f.reader.ReadString('\n')
	if err != nil {
		return false, operatorInteractionError(ctx.TaskID(), err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "q" || answer == "pause" {
		return false, operatorInteractionError(ctx.TaskID(), nil)
	}
	return answer == "y" || answer == "yes", nil
}

func (f taskReviewLifecycleFrontend) PromptFreshReviewBlockerDispositions(
	ctx workflow.ReviewContext,
	blockers []review.FreshReviewBlocker,
) ([]review.FreshReviewBlockerDisposition, error) {
	if err := requireOperatorInput(f.command, f.terminal, ctx.TaskID()); err != nil {
		return nil, err
	}
	return promptFreshReviewBlockerDispositions(f.command, f.reader, ctx.TaskID(), blockers)
}

type taskImplementationRunner struct {
	command *cobra.Command
	deps    *invocationDependencies
	logger  *slog.Logger
}

func (r taskImplementationRunner) RunImplementation(
	ctx context.Context,
	run implementation.ImplementationRun,
) agent.ExecutionResult {
	logTaskRunLaunch(r.command, r.logger, run.RepoID, run.TaskID, run.Start)
	span := logging.Start(ctx, r.logger, "attached agent process", taskRunProcessAttrs(run.RepoID, run.TaskID, run.Start)...)
	runner := phaseexec.Runner{Store: workstate.NewStore(r.deps.paths), Paths: r.deps.paths, RepositoryID: run.RepoID, TaskID: run.TaskID, Logger: r.logger}
	attached := phaseexec.AttachedAgents{Launcher: r.deps.agentLauncher, RecordPID: func(owner work.AttemptRef, pid int) error {
		return implementation.RecordChildPID(runner, owner, pid)
	}}
	result := attached.Run(ctx, run.Start.Attempt.Address, phaseexec.AgentRun{
		Prepared: run.Start.Prepared, Execution: run.Start.Attempt.Execution, Workdir: run.Start.ExecutionDir, Branch: run.Start.Setup.Branch,
		Environment: stateEnvironmentEntries(r.deps.paths),
		Stdin:       r.command.InOrStdin(), Stdout: r.command.OutOrStdout(), Stderr: r.command.ErrOrStderr(),
		UsageEnvironment: r.deps.usageCaptureEnvironment(), CaptureUsage: r.deps.captureUsage,
		Logger: r.logger, Attempt: run.Start.Attempt.Attempt,
	})
	if result.Err == nil {
		span.Finish(ctx, logging.StatusSuccess, slog.String("lifecycle_outcome", "success"), slog.Int("exit_code", 0))
	} else {
		span.Finish(ctx, taskRunProcessStatus(ctx, result.Err), taskRunProcessOutcomeAttrs(ctx, result.Err)...)
	}
	return result
}

func taskReviewStartFromWorkflow(ctx workflow.ReviewAttemptContext) taskReviewStart {
	return taskReviewStart{
		workdir:  ctx.Workdir,
		target:   ctx.Target,
		review:   ctx.Review,
		pipeline: ctx.Pipeline,
		resumed:  ctx.Resumed(),
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
func newTaskWorkflowController(
	command *cobra.Command,
	deps *invocationDependencies,
	taskCtx taskContext,
	logger *slog.Logger,
	reader *bufio.Reader,
) workflow.Controller {
	effects := deps.reviewEffects
	if effects.CaptureUsage == nil {
		effects.CaptureUsage = deps.captureUsage
	}
	service := workflow.ReviewLifecycleService{
		Progress:      newWorkflowHeaders(command, deps.terminal).Render,
		ReviewEffects: effects,
		ReviewStatus:  deps.reviewStatus,
		Paths:         deps.paths,
		Sources:       taskCtx.Sources,
		RunStore:      deps.taskStateStore,
		BackendFactory: func(source taskmodel.RepositorySource) (workflow.ReviewLifecycleBackend, error) {
			return invocationTaskBackend[workflow.ReviewLifecycleBackend](deps, source)
		},
		CandidateGit:    deps.reviewCandidateGit,
		DispatchGit:     deps.dispatchGit,
		OutputGit:       deps.outputGit,
		AgentLauncher:   deps.agentLauncher,
		Environment:     environmentEntries(deps.environment),
		UsageCaptureEnv: deps.usageCaptureEnvironment(),
		Logger:          logger,
		ProcessProbe:    deps.processProbe,
		Parents: taskmodel.RelationshipReader{Sources: taskCtx.Sources, Factory: func(source taskmodel.RepositorySource) (taskmodel.Getter, error) {
			return deps.taskBackendFactory(source)
		}},
		PipelineRunner: deps.reviewPipeline,
		Frontend: &taskReviewLifecycleFrontend{
			terminal: deps.terminal,
			command:  command,
			logger:   logger,
			reader:   reader,
		},
	}
	service.AgentRunner = taskImplementationRunner{command: command, deps: deps, logger: logger}
	return workflow.Controller{ReviewLifecycleService: service, Delivery: newTaskDeliveryService(command, deps, taskCtx, logger)}
}
