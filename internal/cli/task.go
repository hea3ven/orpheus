package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/beads"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/taskstats"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

var (
	newBeadsTaskBackend                       = beads.NewTaskBackend
	attachedAgentLauncher      agent.Launcher = agent.AttachedLauncher{}
	taskDoneInputIsTerminal                   = readerIsTerminal
	taskReviewOutputIsTerminal                = writerIsTerminal
)

const taskStatsCostEstimateDisclaimer = "Estimated API-equivalent cost is calculated from recorded token usage " +
	"and public pricing metadata; it may not match subscription billing or vendor invoices."

func newTaskCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Inspect tasks across registered repositories",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(
		newTaskListCommand(opts),
		newTaskReadyCommand(opts),
		newTaskShowCommand(opts),
		newTaskStatsCommand(opts),
		newTaskDirCommand(opts),
		newTaskRunCommand(opts),
		newTaskReviewCommand(opts),
		newTaskDoneCommand(opts),
		newTaskSyncCommand(opts),
	)
	return cmd
}

func newTaskListCommand(opts *rootOptions) *cobra.Command {
	var detailed bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List active items across registered repositories",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskRows(command, opts, taskRowsOptions{
				operation:    "task list",
				logOperation: "task_list",
				loadingLog:   "loading registered repos for task query",
				queryingLog:  "querying active tasks",
				queriedLog:   "queried active tasks",
				detailed:     detailed,
				query: func(ctx context.Context, aggregator taskmodel.Aggregator) taskRowsResult {
					result := aggregator.List(ctx)
					return taskRowsResult{
						Rows:     result.Rows,
						Failures: result.Failures,
					}
				},
			})
		},
	}
	addTaskDetailFlags(cmd, &detailed)
	return cmd
}

func newTaskReadyCommand(opts *rootOptions) *cobra.Command {
	var detailed bool
	cmd := &cobra.Command{
		Use:   "ready",
		Short: "List tasks ready under Orpheus' local readiness policy",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskReady(command, opts, detailed)
		},
	}
	addTaskDetailFlags(cmd, &detailed)
	return cmd
}

func newTaskShowCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <task-id>",
		Short: "Show an item from its registered repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskShow(command, opts, args[0])
		},
	}
	return cmd
}

func newTaskStatsCommand(opts *rootOptions) *cobra.Command {
	var group string
	cmd := &cobra.Command{
		Use:   "stats [<task-id>]",
		Short: "Show implementation execution stats for one task or aggregate trends",
		Args: func(command *cobra.Command, args []string) error {
			if strings.TrimSpace(group) == "" {
				return cobra.ExactArgs(1)(command, args)
			}
			if len(args) != 0 {
				return fmt.Errorf("--group cannot be combined with a task id")
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			taskID := ""
			if len(args) == 1 {
				taskID = args[0]
			}
			return runTaskStats(command, opts, taskID, taskStatsOptions{group: group})
		},
	}
	cmd.Flags().StringVar(&group, "group", "", "aggregate resolved task stats by day or month")
	return cmd
}

func newTaskDirCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dir <task-id>",
		Short: "Print a task's working directory",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskDir(command, opts, args[0])
		},
	}
	return cmd
}

func newTaskRunCommand(opts *rootOptions) *cobra.Command {
	var agentName string
	var pipelineName string
	var mainMode bool
	var repoRootMode bool
	cmd := &cobra.Command{
		Use:   "run <task-id>",
		Short: "Run an attached agent for a task",
		Long: "Run an attached agent for a task.\n\n" +
			"By default, Orpheus prepares a deterministic task branch and worktree, " +
			"records the attached run attempt, then runs the configured agent there. " +
			"Use --main to run explicitly from the registered repo root on the " +
			"registered default branch for local/manual review workflows. " +
			"Use --repo-root to run from the registered repo root on the task branch.\n\n" +
			"When the latest review is blocked by open current-task findings, task run " +
			"automatically starts a review follow-up run and targets those findings. " +
			"After the agent records completion with agent done, task run continues into " +
			"the effective review pipeline. Automated review keeps automated blockers, " +
			"runs targeted fixes, and starts fresh review attempts until it passes, " +
			"fails operationally, exhausts reviews.max_autonomous_review_attempts, or " +
			"reaches a manual step. Manual steps are persisted for resume with task review.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskRun(command, opts, args[0], agentName, pipelineName, mainMode, repoRootMode)
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "agent profile name to use instead of agents.defaults.implementer")
	cmd.Flags().StringVar(&pipelineName, "pipeline", "", "review pipeline name to use after implementation instead of repo/global defaults")
	cmd.Flags().BoolVar(&mainMode, "main", false, "run from the registered repo root on the registered default branch")
	cmd.Flags().BoolVar(&repoRootMode, "repo-root", false, "run from the registered repo root on the task branch")
	return cmd
}

func newTaskDoneCommand(opts *rootOptions) *cobra.Command {
	var summary string
	var description string
	cmd := &cobra.Command{
		Use:   "done [<task-id>]",
		Short: "Finalize a reviewed task",
		Long: "Finalize a reviewed task.\n\n" +
			"task done is not the normal approval command after agent done. It refuses " +
			"publication until the latest local review attempt has passed; run task review " +
			"first to record approval.\n\n" +
			"On the registered default branch, commits reviewed repo-root changes, pushes the " +
			"default branch, and closes the backend task. On a repo-root task branch, publishes " +
			"the feature branch as a pull request. Without a task id, the command infers one " +
			"ready task only when the current directory is exactly a registered repo root or " +
			"deterministic task worktree and the task owns the current branch.\n\n" +
			"Use task done to retry publication or finalization after a review has passed " +
			"and the previous publication attempt failed.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			taskID := ""
			if len(args) == 1 {
				taskID = args[0]
			}
			return runTaskDone(command, opts, taskID, summary, description)
		},
	}
	cmd.Flags().StringVar(&summary, "summary", "", "override the final commit summary")
	cmd.Flags().StringVar(&description, "description", "", "override the final commit description")
	return cmd
}

func newTaskReviewCommand(opts *rootOptions) *cobra.Command {
	var pipelineName string
	cmd := &cobra.Command{
		Use:   "review <task-id>",
		Short: "Run the local review pipeline for completed task work",
		Long: "Run the selected local review gate for completed task work.\n\n" +
			"Pipeline selection uses --pipeline, then the repo registry review_pipeline, " +
			"then reviews.default_pipeline, then the built-in manual local-review step. " +
			"--pipeline accepts configured global pipeline names and repo-local aliases " +
			"from review-pipeline-alias.<alias>. Configured pipelines may include check, " +
			"manual, and agent_review steps. Approval records a passed review attempt and " +
			"then finalizes through the same path as task done. When task run has paused " +
			"at a manual step, task review resumes that same attempt; --pipeline may only " +
			"name the already selected pipeline and cannot replace it. If later automated " +
			"steps block after a resumed manual gate, task review runs bounded targeted " +
			"fixes and restarts the pipeline from step 1 so manual gates must pass again.\n\n" +
			"Blocking findings leave the task ready for task run follow-up. Operational " +
			"review failures require fixing the review command, environment, or process " +
			"and rerunning task review. Exhausted autonomous blockers stay blocked until " +
			"the operator explicitly continues. Use task review show to inspect persisted " +
			"findings and created follow-up tasks.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskReview(command, opts, args[0], pipelineName)
		},
	}
	cmd.Flags().StringVar(&pipelineName, "pipeline", "", "review pipeline name to use instead of repo/global defaults")
	cmd.AddCommand(newTaskReviewShowCommand(opts))
	return cmd
}

func newTaskSyncCommand(opts *rootOptions) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "sync [<task-id>]",
		Short: "Reconcile tasks from recorded pull request state",
		Long: "Reconcile tasks from recorded pull request state.\n\n" +
			"Tasks with a recorded PR URL are polled from the PR provider. Merged PRs close " +
			"the backend task and record a local audit event. Tasks without a PR URL are skipped.",
		Args: func(cmd *cobra.Command, args []string) error {
			if all {
				if len(args) != 0 {
					return fmt.Errorf("--all cannot be combined with a task id")
				}
				return nil
			}
			return cobra.ExactArgs(1)(cmd, args)
		},
		RunE: func(command *cobra.Command, args []string) error {
			if all {
				return runTaskSyncAll(command, opts)
			}
			return runTaskSync(command, opts, args[0])
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "sync all registered repositories at PR boundaries")
	return cmd
}

func addTaskDetailFlags(cmd *cobra.Command, detailed *bool) {
	cmd.Flags().BoolVar(detailed, "details", false, "show detailed table with repo ids, task prefixes, and Orpheus metadata")
	cmd.Flags().BoolVarP(detailed, "long", "l", false, "show detailed table with repo ids, task prefixes, and Orpheus metadata")
}

func runTaskReady(command *cobra.Command, opts *rootOptions, detailed bool) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_ready"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task ready")

	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}
	logger.DebugContext(command.Context(), "querying task snapshots", slog.Int("repo_count", len(taskCtx.Sources)))

	snapshot := taskCtx.Aggregator.Snapshot(command.Context())
	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	runStates, runStateFailures := taskRunStateIndex(paths, snapshot)
	if len(runStateFailures) > 0 {
		snapshot.Failures = append(snapshot.Failures, runStateFailures...)
	}
	rows := status.ReadyRowsWithLocalTaskStates(snapshot, runStates)
	logger.DebugContext(
		command.Context(),
		"projected ready tasks",
		slog.Int("row_count", len(rows)),
		slog.Int("failure_count", len(snapshot.Failures)),
		slog.Int("run_state_count", len(runStates)),
	)

	if err := renderTaskRows(command.OutOrStdout(), rows, detailed); err != nil {
		return err
	}
	if snapshot.HasFailures() {
		writeRepoFailures(command.ErrOrStderr(), "task ready", snapshot.Failures)
		return partialRepoFailureError{operation: "task ready", failures: snapshot.Failures}
	}
	return nil
}

func runTaskRows(command *cobra.Command, opts *rootOptions, rowOpts taskRowsOptions) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", rowOpts.logOperation),
	)
	logger.DebugContext(command.Context(), rowOpts.loadingLog)

	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}
	logger.DebugContext(command.Context(), rowOpts.queryingLog, slog.Int("repo_count", len(taskCtx.Sources)))

	result := rowOpts.query(command.Context(), taskCtx.Aggregator)
	logger.DebugContext(
		command.Context(),
		rowOpts.queriedLog,
		slog.Int("row_count", len(result.Rows)),
		slog.Int("failure_count", len(result.Failures)),
	)

	if err := renderTaskRows(command.OutOrStdout(), result.Rows, rowOpts.detailed); err != nil {
		return err
	}
	if result.HasFailures() {
		writeRepoFailures(command.ErrOrStderr(), rowOpts.operation, result.Failures)
		return partialRepoFailureError{operation: rowOpts.operation, failures: result.Failures}
	}
	return nil
}

func runTaskShow(command *cobra.Command, opts *rootOptions, taskID string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_show"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task show")

	resolvedCtx, err := resolveTaskShowContext(command, taskID)
	if err != nil {
		return err
	}
	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	taskState, err := taskstate.NewStore(paths).Load(
		resolvedCtx.Resolved.Source.Repository.ID,
		resolvedCtx.Resolved.TaskID,
	)
	if err != nil {
		return fmt.Errorf(
			"task show %s: load local task-state for repo %s: %w",
			resolvedCtx.Resolved.TaskID,
			resolvedCtx.Resolved.Source.Repository.ID,
			err,
		)
	}

	logger.DebugContext(
		command.Context(),
		"queried task from resolved repo",
		slog.String("repo_id", resolvedCtx.Resolved.Source.Repository.ID),
		slog.String("task_id", resolvedCtx.Resolved.TaskID),
		slog.Int("history_event_count", len(taskState.Events)),
	)
	return renderTaskDetails(command.OutOrStdout(), taskmodel.RepoTask{
		Repository: resolvedCtx.Resolved.Source.Repository,
		Task:       resolvedCtx.Task,
	}, taskState)
}

type taskStatsOptions struct {
	group string
}

func runTaskStats(command *cobra.Command, opts *rootOptions, taskID string, statsOpts taskStatsOptions) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_stats"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task stats")

	if strings.TrimSpace(statsOpts.group) != "" {
		return runAggregateTaskStats(command, opts, statsOpts.group)
	}

	resolvedCtx, err := resolveTaskShowContext(command, taskID)
	if err != nil {
		return err
	}
	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	taskState, err := taskstate.NewStore(paths).Load(
		resolvedCtx.Resolved.Source.Repository.ID,
		resolvedCtx.Resolved.TaskID,
	)
	if err != nil {
		return fmt.Errorf(
			"task stats %s: load local task-state for repo %s: %w",
			resolvedCtx.Resolved.TaskID,
			resolvedCtx.Resolved.Source.Repository.ID,
			err,
		)
	}
	return renderTaskStats(command.OutOrStdout(), taskState)
}

func runAggregateTaskStats(command *cobra.Command, opts *rootOptions, group string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_stats_aggregate"),
	)
	normalizedGroup, err := taskstats.ParseGroup(group)
	if err != nil {
		return err
	}

	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}
	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}

	logger.DebugContext(
		command.Context(),
		"querying task snapshots for aggregate stats",
		slog.String("group", string(normalizedGroup)),
		slog.Int("repo_count", len(taskCtx.Sources)),
	)
	snapshot := taskCtx.Aggregator.Snapshot(command.Context())
	report, stateFailures := taskstats.AggregateReportFromSnapshot(
		snapshot,
		taskstate.NewStore(paths),
		normalizedGroup,
	)
	if len(stateFailures) > 0 {
		snapshot.Failures = append(snapshot.Failures, stateFailures...)
	}
	if err := renderTaskStatsAggregate(command.OutOrStdout(), report); err != nil {
		return err
	}
	if snapshot.HasFailures() {
		writeRepoFailures(command.ErrOrStderr(), "task stats", snapshot.Failures)
		return partialRepoFailureError{operation: "task stats", failures: snapshot.Failures}
	}
	return nil
}

func runTaskDir(command *cobra.Command, opts *rootOptions, taskID string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_dir"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task dir")

	resolvedCtx, err := resolveTaskContext(command, "task dir", taskID)
	if err != nil {
		return err
	}

	dir, err := taskWorkingDirectory(resolvedCtx.Resolved.Source.Repository, resolvedCtx.Task)
	if err != nil {
		return fmt.Errorf("task dir %s: %w", resolvedCtx.Resolved.TaskID, err)
	}

	logger.DebugContext(
		command.Context(),
		"resolved task working directory",
		slog.String("repo_id", resolvedCtx.Resolved.Source.Repository.ID),
		slog.String("task_id", resolvedCtx.Resolved.TaskID),
		slog.String("dir", dir),
	)
	_, err = fmt.Fprintln(command.OutOrStdout(), dir)
	return err
}

func runTaskRun(
	command *cobra.Command,
	opts *rootOptions,
	taskID string,
	agentName string,
	pipelineName string,
	mainMode bool,
	repoRootMode bool,
) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_run"),
	)
	if mainMode && repoRootMode {
		return fmt.Errorf("task run %s: --main cannot be combined with --repo-root", taskID)
	}

	resolvedCtx, err := resolveTaskRunContext(taskID)
	if err != nil {
		return err
	}

	resolved := resolvedCtx.Resolved
	repo := resolvedCtx.RegisteredRepo

	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}

	taskBackend, err := newBeadsTaskBackend(resolved.Source.BackendDir)
	if err != nil {
		return fmt.Errorf("task run %s: create backend for repo %s (%s; prefix %s): %w",
			resolved.TaskID,
			resolved.Source.Repository.ID,
			resolved.Source.Repository.Name,
			resolved.Source.Repository.TaskIDPrefix,
			err,
		)
	}
	if err := validateTaskRunExternalRef(command, resolved, taskBackend); err != nil {
		return err
	}

	dispatch, err := startTaskRunDispatch(command, paths, resolved, taskBackend, agentName, mainMode, repoRootMode)
	if err != nil {
		return fmt.Errorf("task run %s: %w", resolved.TaskID, err)
	}

	logTaskRunLaunch(command, logger, repo.ID, resolved.TaskID, dispatch.start)

	if err := launchTaskRunAgent(command, dispatch.service, repo.ID, resolved.TaskID, dispatch.start, dispatch.prompt); err != nil {
		return err
	}

	return finishTaskRunAndReview(command, opts, dispatch, repo.ID, resolved.TaskID, agentName, pipelineName)
}

func finishTaskRunAndReview(
	command *cobra.Command,
	opts *rootOptions,
	dispatch taskRunDispatch,
	repoID string,
	taskID string,
	agentName string,
	pipelineName string,
) error {
	if err := finishTaskRun(command, dispatch.service, repoID, taskID, dispatch.start); err != nil {
		return err
	}
	shouldReview, err := completedTaskRunReadyForReview(
		dispatch.service.RunStore,
		repoID,
		taskID,
		dispatch.start.Attempt.Attempt,
	)
	if err != nil {
		return fmt.Errorf("task run %s: inspect completion before review: %w", taskID, err)
	}
	if !shouldReview {
		return nil
	}
	selectedAgentName := dispatch.start.Command.AgentName
	if strings.TrimSpace(selectedAgentName) == "" {
		selectedAgentName = agentName
	}
	return runTaskRunReview(command, opts, taskID, selectedAgentName, pipelineName)
}

func completedTaskRunReadyForReview(
	store workflow.DispatchRunStore,
	repoID string,
	taskID string,
	attempt int,
) (bool, error) {
	latest, ok, err := store.LatestRun(repoID, taskID)
	if err != nil || !ok {
		return false, err
	}
	return latest.Attempt == attempt &&
		latest.Status == taskstate.RunStatusSucceeded &&
		latest.Completion != nil, nil
}

func validateTaskRunExternalRef(
	command *cobra.Command,
	resolved taskmodel.ResolvedTaskSource,
	backend taskmodel.Getter,
) error {
	if !publication.RequiresExternalRef(resolved.Source.Repository.TitleTemplate) {
		return nil
	}
	taskItem, err := queryTaskFromBackend(command.Context(), "task run", resolved, backend)
	if err != nil {
		return err
	}
	if taskItem.Status == taskmodel.StatusClosed || strings.TrimSpace(taskItem.ExternalRef) != "" {
		return nil
	}
	return fmt.Errorf(
		"task run %s: publication title template requires a task external reference; set it with `bd update %s --external-ref <reference>`",
		resolved.TaskID,
		resolved.TaskID,
	)
}

type taskRunDispatch struct {
	service workflow.DispatchService
	start   workflow.DispatchStartResult
	prompt  string
}

func startTaskRunDispatch(
	command *cobra.Command,
	paths state.Paths,
	resolved taskmodel.ResolvedTaskSource,
	backend workflow.DispatchBackend,
	agentName string,
	mainMode bool,
	repoRootMode bool,
) (taskRunDispatch, error) {
	dispatch := taskRunDispatch{
		service: workflow.DispatchService{
			Paths:    paths,
			RunStore: taskstate.NewStore(paths),
		},
	}
	start, err := dispatch.service.Start(command.Context(), workflow.DispatchStartOptions{
		TaskID:  resolved.TaskID,
		Source:  resolved.Source,
		Backend: backend,
		ResolveCommand: func(commandContext workflow.DispatchCommandContext) (workflow.DispatchCommand, error) {
			prompt, commandSnapshot, err := resolveTaskRunAgentCommand(paths, agentName, commandContext.SessionName)
			if err != nil {
				return workflow.DispatchCommand{}, err
			}
			dispatch.prompt = prompt
			return workflow.DispatchCommand{
				AgentName: commandSnapshot.AgentName,
				Command:   commandSnapshot.Command,
				Args:      commandSnapshot.Args,
				Harness:   commandSnapshot.Harness,
				Model:     commandSnapshot.Model,
			}, nil
		},
		ResolveFollowUpCommand: func(commandContext workflow.DispatchCommandContext) (workflow.DispatchCommand, error) {
			prompt, commandSnapshot, err := resolveTaskRunFollowUpAgentCommand(paths, agentName, commandContext.SessionName)
			if err != nil {
				return workflow.DispatchCommand{}, err
			}
			dispatch.prompt = prompt
			return workflow.DispatchCommand{
				AgentName: commandSnapshot.AgentName,
				Command:   commandSnapshot.Command,
				Args:      commandSnapshot.Args,
				Harness:   commandSnapshot.Harness,
				Model:     commandSnapshot.Model,
			}, nil
		},
		MainMode:     mainMode,
		RepoRootMode: repoRootMode,
	})
	if err != nil {
		return taskRunDispatch{}, err
	}
	dispatch.start = start
	return dispatch, nil
}

func logTaskRunLaunch(
	command *cobra.Command,
	logger *slog.Logger,
	repoID string,
	taskID string,
	start workflow.DispatchStartResult,
) {
	logger.DebugContext(
		command.Context(),
		"launching attached agent",
		slog.String("repo_id", repoID),
		slog.String("task_id", taskID),
		slog.String("agent", start.Command.AgentName),
		slog.String("command", start.Command.Command),
		slog.Int("arg_count", len(start.Command.Args)),
		slog.String("execution_dir", start.ExecutionDir),
		slog.String("branch", start.Setup.Branch),
		slog.String("worktree_lifecycle", string(start.Setup.Lifecycle)),
	)
}

func launchTaskRunAgent(
	command *cobra.Command,
	service workflow.DispatchService,
	repoID string,
	taskID string,
	start workflow.DispatchStartResult,
	prompt string,
) error {
	err := attachedAgentLauncher.Run(command.Context(), agent.CommandSnapshot{
		AgentName: start.Command.AgentName,
		Command:   start.Command.Command,
		Args:      start.Command.Args,
	}, agent.LaunchOptions{
		Dir: start.ExecutionDir,
		Env: taskRunEnvironment(
			repoID,
			start.Task.ID,
			start.Setup.WorktreePath,
			start.Setup.Branch,
			prompt,
		),
		Stdin:  command.InOrStdin(),
		Stdout: command.OutOrStdout(),
		Stderr: command.ErrOrStderr(),
	})
	if err == nil {
		return nil
	}

	recordErr := service.Fail(workflow.DispatchFailureOptions{
		RepoID:      repoID,
		TaskID:      taskID,
		Attempt:     start.Attempt.Attempt,
		Cause:       err,
		StartFailed: agent.IsStartError(err),
	})
	if recordErr != nil {
		return fmt.Errorf("task run %s: %w; additionally failed to record run failure: %w", taskID, err, recordErr)
	}
	return fmt.Errorf("task run %s: %w", taskID, err)
}

func recordTaskRunUsage(
	command *cobra.Command,
	service workflow.DispatchService,
	repoID string,
	taskID string,
	start workflow.DispatchStartResult,
) error {
	usageOpts := taskRunUsageOptions(command, start)
	_, err := service.RunStore.RecordRunUsage(repoID, taskID, start.Attempt.Attempt, usageOpts)
	return err
}

func finishTaskRun(
	command *cobra.Command,
	service workflow.DispatchService,
	repoID string,
	taskID string,
	start workflow.DispatchStartResult,
) error {
	usageErr := recordTaskRunUsage(command, service, repoID, taskID, start)
	if err := service.Finish(repoID, taskID, start.Attempt.Attempt); err != nil {
		return fmt.Errorf("task run %s: record run finish: %w", taskID, err)
	}
	if usageErr != nil {
		return fmt.Errorf("task run %s: record run usage: %w", taskID, usageErr)
	}
	return nil
}

func taskRunUsageOptions(command *cobra.Command, start workflow.DispatchStartResult) taskstate.RecordRunUsageOptions {
	if start.Attempt.Execution.Harness != "codex" {
		return taskstate.RecordRunUsageOptions{
			UsageCapture: taskstate.AgentUsageCapture{
				Status: taskstate.UsageCaptureUnknown,
				Reason: "usage capture is not supported for harness " +
					formatTaskStatsField(start.Attempt.Execution.Harness),
			},
		}
	}
	return agent.CaptureCodexUsage(agent.CodexUsageCaptureOptions{
		ExecutionDir: start.ExecutionDir,
		StartedAt:    start.Attempt.Execution.StartedAt,
		Env:          agent.CodexUsageCaptureEnvironment(),
	})
}

func runTaskReview(command *cobra.Command, opts *rootOptions, taskID string, pipelineName string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_review"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task review")

	start, err := startTaskReview(command, taskID, pipelineName)
	if err != nil {
		return err
	}

	if start.resumed {
		maxAttempts, err := taskReviewMaxAutonomousReviewAttempts(start.paths)
		if err != nil {
			return fmt.Errorf("task review %s: %w", start.taskID(), err)
		}
		dispatchAgentName, err := resumedReviewImplementerName(start)
		if err != nil {
			return fmt.Errorf("task review %s: %w", start.taskID(), err)
		}
		return executeAutonomousReviewLoop(command, logger, start, autonomousReviewLoopOptions{
			maxAttempts:       maxAttempts,
			dispatchAgentName: dispatchAgentName,
			review: taskReviewExecutionOptions{
				interactiveManual:     true,
				keepAutomatedBlockers: true,
			},
		})
	}

	return executeTaskReview(command, logger, start, taskReviewExecutionOptions{
		interactiveManual: true,
	})
}

func runTaskRunReview(command *cobra.Command, opts *rootOptions, taskID string, agentName string, pipelineName string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_run_review"),
	)
	logger.DebugContext(command.Context(), "starting automatic review after task run")

	start, err := startTaskReview(command, taskID, pipelineName)
	if err != nil {
		return err
	}
	maxAttempts, err := taskReviewMaxAutonomousReviewAttempts(start.paths)
	if err != nil {
		return fmt.Errorf("task review %s: %w", start.taskID(), err)
	}
	return executeAutonomousReviewLoop(command, logger, start, autonomousReviewLoopOptions{
		maxAttempts:       maxAttempts,
		dispatchAgentName: agentName,
		review: taskReviewExecutionOptions{
			pauseBeforeManual:     true,
			autoSeparateTasks:     true,
			keepAutomatedBlockers: true,
		},
	})
}

type taskReviewExecutionOptions struct {
	pauseBeforeManual     bool
	autoSeparateTasks     bool
	interactiveManual     bool
	keepAutomatedBlockers bool
}

type taskReviewAttemptResult struct {
	status taskstate.ReviewStatus
}

type autonomousReviewLoopOptions struct {
	maxAttempts       int
	dispatchAgentName string
	review            taskReviewExecutionOptions
}

func executeTaskReview(
	command *cobra.Command,
	logger *slog.Logger,
	start taskReviewStart,
	opts taskReviewExecutionOptions,
) error {
	reviewInput := bufio.NewReader(command.InOrStdin())
	result, err := executeTaskReviewAttempt(command, logger, start, reviewInput, opts)
	if err != nil {
		return err
	}
	if result.status != taskstate.ReviewStatusPassed {
		return nil
	}

	return finalizeApprovedTaskReview(command, logger, start.paths, start.resolvedCtx)
}

func executeAutonomousReviewLoop(
	command *cobra.Command,
	logger *slog.Logger,
	start taskReviewStart,
	opts autonomousReviewLoopOptions,
) error {
	if opts.maxAttempts <= 0 {
		return fmt.Errorf("task review %s: autonomous review attempt budget must be positive", start.taskID())
	}

	attemptsUsed := 0
	current := start
	reviewInput := bufio.NewReader(command.InOrStdin())
	for {
		attemptsUsed++
		result, err := executeTaskReviewAttempt(command, logger, current, reviewInput, opts.review)
		if err != nil {
			return err
		}

		switch result.status {
		case taskstate.ReviewStatusPassed:
			return finalizeApprovedTaskReview(command, logger, current.paths, current.resolvedCtx)
		case taskstate.ReviewStatusBlocked:
		case taskstate.ReviewStatusWaitingForManual,
			taskstate.ReviewStatusAborted,
			taskstate.ReviewStatusFailed:
			return nil
		default:
			return nil
		}

		latest, indexes, ok, err := latestAutonomousReviewBlockers(current.store, current.repoID(), current.taskID())
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		if attemptsUsed >= opts.maxAttempts {
			return markAutonomousReviewBudgetExhausted(command, current, latest.Attempt, attemptsUsed)
		}

		if err := runAutonomousReviewFollowUp(command, logger, current, opts.dispatchAgentName, latest.Attempt, indexes); err != nil {
			return err
		}
		next, err := startFreshAutonomousReview(command, current)
		if err != nil {
			return err
		}
		current = next
	}
}

func markAutonomousReviewBudgetExhausted(
	command *cobra.Command,
	start taskReviewStart,
	reviewAttempt int,
	attemptsUsed int,
) error {
	if _, err := start.store.MarkReviewAutonomousBudgetExhausted(
		start.repoID(),
		start.taskID(),
		reviewAttempt,
	); err != nil {
		return fmt.Errorf("task review %s: mark autonomous review budget exhausted: %w", start.taskID(), err)
	}
	_, err := fmt.Fprintf(
		command.ErrOrStderr(),
		"Autonomous review attempt budget exhausted for %s after %d review attempt(s). "+
			"Open blockers were preserved. Run `orpheus task run %s` to continue with a fresh budget.\n",
		start.taskID(),
		attemptsUsed,
		start.taskID(),
	)
	return err
}

func executeTaskReviewAttempt(
	command *cobra.Command,
	logger *slog.Logger,
	start taskReviewStart,
	reviewInput *bufio.Reader,
	opts taskReviewExecutionOptions,
) (taskReviewAttemptResult, error) {
	outcome, err := review.RunPipeline(taskReviewPipelineOptions(command, start, reviewInput, logger, opts))
	if err != nil {
		_, _ = start.store.FinishReview(start.repoID(), start.taskID(), start.review.Attempt, taskstate.ReviewStatusFailed)
		return taskReviewAttemptResult{}, err
	}
	if outcome.Status == taskstate.ReviewStatusWaitingForManual {
		return taskReviewAttemptResult{status: outcome.Status}, nil
	}
	if outcome.Status == taskstate.ReviewStatusPassed {
		shouldPublish, err := processSeparateTaskReviewCandidates(command, start, reviewInput, opts.autoSeparateTasks)
		if err != nil {
			_, _ = start.store.FinishReview(start.repoID(), start.taskID(), start.review.Attempt, taskstate.ReviewStatusFailed)
			return taskReviewAttemptResult{}, err
		}
		if !shouldPublish {
			if _, err := start.store.FinishReview(
				start.repoID(),
				start.taskID(),
				start.review.Attempt,
				taskstate.ReviewStatusAborted,
			); err != nil {
				_, _ = start.store.FinishReview(start.repoID(), start.taskID(), start.review.Attempt, taskstate.ReviewStatusFailed)
				return taskReviewAttemptResult{}, fmt.Errorf("task review %s: record aborted review: %w", start.taskID(), err)
			}
			return taskReviewAttemptResult{status: taskstate.ReviewStatusAborted}, nil
		}
	}
	if _, err := start.store.FinishReview(
		start.repoID(),
		start.taskID(),
		start.review.Attempt,
		outcome.Status,
	); err != nil {
		_, _ = start.store.FinishReview(start.repoID(), start.taskID(), start.review.Attempt, taskstate.ReviewStatusFailed)
		return taskReviewAttemptResult{}, fmt.Errorf("task review %s: record %s review: %w", start.taskID(), outcome.Status, err)
	}
	return taskReviewAttemptResult{status: outcome.Status}, nil
}

func taskReviewMaxAutonomousReviewAttempts(paths state.Paths) (int, error) {
	config, err := review.LoadConfig(paths)
	if err != nil {
		return 0, err
	}
	return config.MaxAutonomousReviewAttempts, nil
}

func latestAutonomousReviewBlockers(
	store taskstate.Store,
	repoID string,
	taskID string,
) (taskstate.ReviewAttempt, []int, bool, error) {
	taskState, err := store.Load(repoID, taskID)
	if err != nil {
		return taskstate.ReviewAttempt{}, nil, false, fmt.Errorf("task review %s: load review blockers: %w", taskID, err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Status != taskstate.ReviewStatusBlocked {
		return taskstate.ReviewAttempt{}, nil, false, nil
	}

	indexes := automatedBlockingFindingIndexes(latest)
	if len(indexes) == 0 {
		return latest, nil, false, nil
	}
	if len(indexes) != len(taskstate.UntargetedBlockingFindingIndexes(latest)) {
		return latest, nil, false, nil
	}
	return latest, indexes, true, nil
}

func automatedBlockingFindingIndexes(reviewAttempt taskstate.ReviewAttempt) []int {
	automatedStepKinds := map[string]bool{}
	for _, step := range reviewAttempt.Steps {
		switch step.Kind {
		case review.KindCheck, review.KindAgentReview:
			automatedStepKinds[step.Name] = true
		}
	}

	indexes := make([]int, 0)
	for _, index := range taskstate.UntargetedBlockingFindingIndexes(reviewAttempt) {
		finding := reviewAttempt.Findings[index]
		if automatedStepKinds[finding.Step] {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func runAutonomousReviewFollowUp(
	command *cobra.Command,
	logger *slog.Logger,
	start taskReviewStart,
	agentName string,
	reviewAttempt int,
	findingIndexes []int,
) error {
	_, err := fmt.Fprintf(
		command.ErrOrStderr(),
		"Autonomous review follow-up for %s targets review attempt %d finding(s) %s.\n",
		start.taskID(),
		reviewAttempt,
		formatReviewFindingIndexes(findingIndexes),
	)
	if err != nil {
		return err
	}

	backend, err := newBeadsTaskBackend(start.resolvedCtx.Resolved.Source.BackendDir)
	if err != nil {
		return fmt.Errorf("task review %s: create backend for autonomous follow-up: %w", start.taskID(), err)
	}
	dispatch, err := startAutonomousReviewFollowUpDispatch(command, start, backend, agentName)
	if err != nil {
		return err
	}
	logTaskRunLaunch(command, logger, start.repoID(), start.taskID(), dispatch.start)
	if err := launchTaskRunAgent(
		command,
		dispatch.service,
		start.repoID(),
		start.taskID(),
		dispatch.start,
		dispatch.prompt,
	); err != nil {
		return err
	}
	if err := finishTaskRun(command, dispatch.service, start.repoID(), start.taskID(), dispatch.start); err != nil {
		return err
	}
	return requireAutonomousReviewFollowUpCompletion(command, start, dispatch)
}

func startAutonomousReviewFollowUpDispatch(
	command *cobra.Command,
	start taskReviewStart,
	backend workflow.DispatchBackend,
	agentName string,
) (taskRunDispatch, error) {
	dispatch, err := startTaskRunDispatch(
		command,
		start.paths,
		start.resolvedCtx.Resolved,
		backend,
		agentName,
		false,
		false,
	)
	if err != nil {
		return taskRunDispatch{}, fmt.Errorf("task review %s: start autonomous follow-up: %w", start.taskID(), err)
	}
	return dispatch, nil
}

func requireAutonomousReviewFollowUpCompletion(
	command *cobra.Command,
	start taskReviewStart,
	dispatch taskRunDispatch,
) error {
	ready, err := completedTaskRunReadyForReview(
		dispatch.service.RunStore,
		start.repoID(),
		start.taskID(),
		dispatch.start.Attempt.Attempt,
	)
	if err != nil {
		return fmt.Errorf("task review %s: inspect autonomous follow-up completion: %w", start.taskID(), err)
	}
	if !ready {
		_, err := fmt.Fprintf(
			command.ErrOrStderr(),
			"Autonomous follow-up run attempt %d exited without completion; run `orpheus agent done` or `orpheus task run %s` before reviewing again.\n",
			dispatch.start.Attempt.Attempt,
			start.taskID(),
		)
		return err
	}
	return nil
}

func startFreshAutonomousReview(command *cobra.Command, previous taskReviewStart) (taskReviewStart, error) {
	target, err := taskReviewTarget(previous.store, previous.paths, previous.resolvedCtx)
	if err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: %w", previous.taskID(), err)
	}
	if err := validateReviewCandidateReady(command.Context(), previous.store, previous.resolvedCtx, target.Worktree); err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: %w", previous.taskID(), err)
	}
	return startFreshTaskReview(
		previous.paths,
		previous.resolvedCtx,
		previous.store,
		target,
		target.Worktree,
		previous.pipeline,
	)
}

func formatReviewFindingIndexes(indexes []int) string {
	labels := make([]string, 0, len(indexes))
	for _, index := range indexes {
		labels = append(labels, strconv.Itoa(index+1))
	}
	return strings.Join(labels, ", ")
}

func taskReviewPipelineOptions(
	command *cobra.Command,
	start taskReviewStart,
	reviewInput *bufio.Reader,
	logger *slog.Logger,
	execOptions ...taskReviewExecutionOptions,
) review.PipelineRunOptions {
	execOpts := taskReviewExecutionOptions{interactiveManual: true}
	if len(execOptions) > 0 {
		execOpts = execOptions[0]
	}
	outputMode := taskReviewOutputMode(command, logger)
	opts := review.PipelineRunOptions{
		Context:           command.Context(),
		Store:             start.store,
		RepoID:            start.repoID(),
		TaskID:            start.taskID(),
		Branch:            start.target.Branch,
		Workdir:           start.workdir,
		Attempt:           start.review,
		Pipeline:          start.pipeline,
		SessionName:       start.resolvedCtx.Task.ReviewSessionName(),
		Stdout:            outputMode.stdout,
		Stderr:            outputMode.stderr,
		Stdin:             command.InOrStdin(),
		InteractiveOutput: outputMode.interactive,
		OutputWidth:       outputMode.width,
		AgentConfig:       start.agentConfig,
		AgentLauncher:     attachedAgentLauncher,
		ResumeFromStep:    start.resumed,
		PauseBeforeManual: execOpts.pauseBeforeManual,
	}
	if !execOpts.interactiveManual {
		return opts
	}
	return attachInteractiveReviewHooks(command, start, reviewInput, opts, execOpts)
}

func attachInteractiveReviewHooks(
	command *cobra.Command,
	start taskReviewStart,
	reviewInput *bufio.Reader,
	opts review.PipelineRunOptions,
	execOpts taskReviewExecutionOptions,
) review.PipelineRunOptions {
	opts.RenderManualStep = func(step review.Step) error {
		return renderManualReviewContext(command, start.store, start.resolvedCtx, start.workdir, start.review, step)
	}
	opts.ConfirmManualCommand = func(step review.Step) (bool, error) {
		confirmed, err := promptManualCommandConfirmation(command, reviewInput, step)
		if err != nil {
			return false, fmt.Errorf("task review %s: %w", start.taskID(), err)
		}
		if confirmed {
			return true, nil
		}
		_, err = fmt.Fprintf(command.ErrOrStderr(), "Review aborted for %s.\n", start.taskID())
		return false, err
	}
	opts.PromptManualStep = func(step review.ManualStep) (review.ManualResult, error) {
		outcome, err := runManualReviewPrompt(
			command,
			reviewInput,
			start.store,
			start.resolvedCtx,
			start.review,
			step.Step.Name,
			step.HunkNotes,
		)
		if err != nil {
			return review.ManualResult{}, err
		}
		if outcome.result == manualReviewApproved {
			return review.ManualResult{}, nil
		}
		return review.ManualResult{
			Status: outcome.status,
			Stop:   true,
		}, nil
	}
	if !execOpts.keepAutomatedBlockers {
		opts.PromptAutomatedBlockers = taskReviewAutomatedBlockerPrompt(command, reviewInput)
	}
	return opts
}

func taskReviewAutomatedBlockerPrompt(
	command *cobra.Command,
	reviewInput *bufio.Reader,
) func(review.AutomatedBlockerReview) ([]review.AutomatedBlockerDecision, error) {
	return func(blockerReview review.AutomatedBlockerReview) ([]review.AutomatedBlockerDecision, error) {
		return promptAutomatedBlockerDecisions(command, reviewInput, blockerReview)
	}
}

type taskReviewOutputModeResult struct {
	stdout      io.Writer
	stderr      io.Writer
	interactive bool
	width       int
}

func taskReviewOutputMode(command *cobra.Command, logger *slog.Logger) taskReviewOutputModeResult {
	stdout := command.OutOrStdout()
	stderr := command.ErrOrStderr()
	stdoutInspection := inspectWriterTerminal(stdout)
	stderrInspection := inspectWriterTerminal(stderr)
	stdoutInteractive := taskReviewOutputIsTerminal(stdout)
	stderrInteractive := taskReviewOutputIsTerminal(stderr)
	interactiveOutput := stdoutInteractive && stderrInteractive
	outputWidth, _ := interactiveTerminalWidth(stderr)
	logTaskReviewOutputDetection(
		command.Context(),
		logger,
		stdoutInspection,
		stderrInspection,
		interactiveOutput,
		outputWidth,
	)
	return taskReviewOutputModeResult{
		stdout:      stdout,
		stderr:      stderr,
		interactive: interactiveOutput,
		width:       outputWidth,
	}
}

func logTaskReviewOutputDetection(
	ctx context.Context,
	logger *slog.Logger,
	stdout writerTerminalInspection,
	stderr writerTerminalInspection,
	interactiveOutput bool,
	outputWidth int,
) {
	if logger == nil {
		return
	}
	logger.DebugContext(
		ctx,
		"resolved task review output mode",
		slog.Bool("interactive_output", interactiveOutput),
		slog.Int("output_width", outputWidth),
		slog.Bool("stdout_interactive", stdout.interactive),
		slog.String("stdout_writer_type", stdout.writerType),
		slog.Bool("stdout_is_file", stdout.isFile),
		slog.Uint64("stdout_fd", uint64(stdout.fd)),
		slog.String("stdout_name", stdout.name),
		slog.String("stdout_stat_mode", stdout.statMode),
		slog.String("stdout_stat_error", stdout.statError),
		slog.Bool("stderr_interactive", stderr.interactive),
		slog.String("stderr_writer_type", stderr.writerType),
		slog.Bool("stderr_is_file", stderr.isFile),
		slog.Uint64("stderr_fd", uint64(stderr.fd)),
		slog.String("stderr_name", stderr.name),
		slog.String("stderr_stat_mode", stderr.statMode),
		slog.String("stderr_stat_error", stderr.statError),
	)
}

type taskReviewStart struct {
	paths       state.Paths
	resolvedCtx resolvedTaskContext
	store       taskstate.Store
	workdir     string
	target      workflow.Target
	review      taskstate.ReviewAttempt
	pipeline    review.Pipeline
	agentConfig agent.Config
	resumed     bool
}

func (s taskReviewStart) repoID() string {
	return s.resolvedCtx.Resolved.Source.Repository.ID
}

func (s taskReviewStart) taskID() string {
	return s.resolvedCtx.Resolved.TaskID
}

func startTaskReview(command *cobra.Command, taskID string, pipelineName string) (taskReviewStart, error) {
	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return taskReviewStart{}, err
	}
	resolvedCtx, err := resolveTaskContext(command, "task review", taskID)
	if err != nil {
		return taskReviewStart{}, err
	}
	var requestedPipeline *review.Pipeline
	if strings.TrimSpace(pipelineName) != "" {
		pipeline, err := resolveTaskReviewPipeline(paths, resolvedCtx.Resolved.Source.Repository, pipelineName)
		if err != nil {
			return taskReviewStart{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
		}
		requestedPipeline = &pipeline
	}
	store := taskstate.NewStore(paths)
	target, err := taskReviewTarget(store, paths, resolvedCtx)
	if err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
	}
	workdir := target.Worktree
	if err := validateReviewCandidateReady(command.Context(), store, resolvedCtx, workdir); err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
	}

	if paused, ok, err := latestManualWaitingReview(store, resolvedCtx); err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
	} else if ok {
		return resumeTaskReview(command, paths, resolvedCtx, store, target, workdir, paused, pipelineName)
	}

	if requestedPipeline != nil {
		return startFreshTaskReview(paths, resolvedCtx, store, target, workdir, *requestedPipeline)
	}
	pipeline, err := resolveTaskReviewPipeline(paths, resolvedCtx.Resolved.Source.Repository, pipelineName)
	if err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
	}
	return startFreshTaskReview(paths, resolvedCtx, store, target, workdir, pipeline)
}

func startFreshTaskReview(
	paths state.Paths,
	resolvedCtx resolvedTaskContext,
	store taskstate.Store,
	target workflow.Target,
	workdir string,
	pipeline review.Pipeline,
) (taskReviewStart, error) {
	agentConfig, err := resolveTaskReviewAgentConfig(paths, pipeline)
	if err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
	}

	reviewAttempt, err := store.StartReviewWithOptions(
		resolvedCtx.Resolved.Source.Repository.ID,
		resolvedCtx.Resolved.TaskID,
		taskstate.StartReviewOptions{
			Pipeline: pipeline.Name,
			Step:     pipeline.Steps[0].Name,
		},
	)
	if err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: start review attempt: %w", resolvedCtx.Resolved.TaskID, err)
	}
	return taskReviewStart{
		paths:       paths,
		resolvedCtx: resolvedCtx,
		store:       store,
		workdir:     workdir,
		target:      target,
		review:      reviewAttempt,
		pipeline:    pipeline,
		agentConfig: agentConfig,
	}, nil
}

func latestManualWaitingReview(
	store taskstate.Store,
	resolvedCtx resolvedTaskContext,
) (taskstate.ReviewAttempt, bool, error) {
	taskState, err := store.Load(
		resolvedCtx.Resolved.Source.Repository.ID,
		resolvedCtx.Resolved.TaskID,
	)
	if err != nil {
		return taskstate.ReviewAttempt{}, false, fmt.Errorf("load task state: %w", err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Status != taskstate.ReviewStatusWaitingForManual {
		return taskstate.ReviewAttempt{}, false, nil
	}
	return latest, true, nil
}

func resumeTaskReview(
	command *cobra.Command,
	paths state.Paths,
	resolvedCtx resolvedTaskContext,
	store taskstate.Store,
	target workflow.Target,
	workdir string,
	paused taskstate.ReviewAttempt,
	pipelineName string,
) (taskReviewStart, error) {
	pipeline, err := resolvePausedTaskReviewPipeline(paths, resolvedCtx.Resolved.Source.Repository, paused, pipelineName)
	if err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
	}
	agentConfig, err := resolveTaskReviewAgentConfig(paths, pipeline)
	if err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
	}
	reviewAttempt, err := store.ResumeReview(
		resolvedCtx.Resolved.Source.Repository.ID,
		resolvedCtx.Resolved.TaskID,
		paused.Attempt,
	)
	if err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: resume review attempt: %w", resolvedCtx.Resolved.TaskID, err)
	}
	_, err = fmt.Fprintf(
		command.ErrOrStderr(),
		"Resuming review attempt %d at manual step %q.\n",
		reviewAttempt.Attempt,
		reviewAttempt.Step,
	)
	if err != nil {
		return taskReviewStart{}, err
	}
	return taskReviewStart{
		paths:       paths,
		resolvedCtx: resolvedCtx,
		store:       store,
		workdir:     workdir,
		target:      target,
		review:      reviewAttempt,
		pipeline:    pipeline,
		agentConfig: agentConfig,
		resumed:     true,
	}, nil
}

func taskReviewTarget(
	store taskstate.Store,
	paths state.Paths,
	resolvedCtx resolvedTaskContext,
) (workflow.Target, error) {
	repo := resolvedCtx.Resolved.Source.Repository
	taskID := resolvedCtx.Resolved.TaskID
	taskState, err := store.Load(repo.ID, taskID)
	if err != nil {
		return workflow.Target{}, fmt.Errorf("load task state: %w", err)
	}
	taskTarget, ok := taskstate.Target(taskState)
	if !ok {
		return workflow.Target{}, fmt.Errorf("task has no Orpheus target; run `orpheus task run %s` first", taskID)
	}
	targets, err := workflow.ExpectedTargetsForTask(repo, taskID, paths)
	if err != nil {
		return workflow.Target{}, err
	}
	target, err := workflow.ClassifyTaskStateTarget(taskTarget, targets)
	if err != nil {
		return workflow.Target{}, fmt.Errorf("task has inconsistent taskstate target: %w", err)
	}
	if err := validateTaskMetadataMirror(resolvedCtx.Task, targets, target); err != nil {
		return workflow.Target{}, err
	}
	return target, nil
}

func validateTaskMetadataMirror(
	taskItem taskmodel.Task,
	targets workflow.ExpectedTargets,
	target workflow.Target,
) error {
	metadataTarget, err := workflow.ClassifyMetadataTarget(taskItem.OrpheusMetadata(), targets)
	if err != nil {
		return fmt.Errorf("task %s metadata target is invalid: %w", taskItem.ID, err)
	}
	if metadataTarget.Branch == target.Branch && metadataTarget.Worktree == target.Worktree {
		return nil
	}
	return fmt.Errorf(
		"task %s metadata target %q/%q does not mirror taskstate target %q/%q",
		taskItem.ID,
		metadataTarget.Branch,
		metadataTarget.Worktree,
		target.Branch,
		target.Worktree,
	)
}

func resolveTaskReviewAgentConfig(paths state.Paths, pipeline review.Pipeline) (agent.Config, error) {
	for _, step := range pipeline.Steps {
		if step.Kind == review.KindAgentReview {
			config, err := agent.LoadConfig(paths)
			if err != nil {
				return agent.Config{}, err
			}
			return config, nil
		}
	}
	return agent.Config{}, nil
}

func resolveTaskReviewPipeline(
	paths state.Paths,
	repo taskmodel.Repository,
	pipelineName string,
) (review.Pipeline, error) {
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

func resolvePausedTaskReviewPipeline(
	paths state.Paths,
	repo taskmodel.Repository,
	paused taskstate.ReviewAttempt,
	pipelineName string,
) (review.Pipeline, error) {
	storedPipeline := strings.TrimSpace(paused.Pipeline)
	if storedPipeline == "" {
		return review.Pipeline{}, fmt.Errorf("paused review attempt %d has no recorded pipeline", paused.Attempt)
	}
	pipeline, err := resolveStoredTaskReviewPipeline(paths, storedPipeline)
	if err != nil {
		return review.Pipeline{}, fmt.Errorf("resolve paused review pipeline %q: %w", storedPipeline, err)
	}

	if strings.TrimSpace(pipelineName) == "" {
		return pipeline, nil
	}
	requested, err := resolveTaskReviewPipeline(paths, repo, pipelineName)
	if err != nil {
		return review.Pipeline{}, err
	}
	if requested.Name != pipeline.Name {
		return review.Pipeline{}, fmt.Errorf(
			"review attempt %d is waiting for manual step %q in pipeline %q; --pipeline %q resolves to %q and cannot replace a paused review",
			paused.Attempt,
			paused.Step,
			pipeline.Name,
			strings.TrimSpace(pipelineName),
			requested.Name,
		)
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

func appendRepoReviewPipelineAliases(err error, repo taskmodel.Repository) error {
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

func finalizeApprovedTaskReview(
	command *cobra.Command,
	logger *slog.Logger,
	paths state.Paths,
	resolvedCtx resolvedTaskContext,
) error {
	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}
	service := newTaskFinalizationService(paths, taskCtx)
	finalized, err := finalizeTaskWithConfirmation(command, service, workflow.FinalizeOptions{
		TaskID:              resolvedCtx.Resolved.TaskID,
		RequirePassedReview: true,
	})
	if err != nil {
		return err
	}

	logger.DebugContext(
		command.Context(),
		"review approved and finalized task",
		slog.String("repo_id", finalized.Repository.ID),
		slog.String("task_id", finalized.Task.ID),
		slog.String("commit", finalized.Finalization.Commit),
	)
	return renderTaskDoneResult(command, finalized)
}

type separateTaskCandidate struct {
	index   int
	finding taskstate.ReviewFinding
}

func processSeparateTaskReviewCandidates(
	command *cobra.Command,
	start taskReviewStart,
	reader *bufio.Reader,
	createAll bool,
) (bool, error) {
	candidates, err := pendingSeparateTaskCandidates(start.store, start.repoID(), start.taskID(), start.review.Attempt)
	if err != nil {
		return false, fmt.Errorf("task review %s: load separate-task candidates: %w", start.taskID(), err)
	}
	if len(candidates) == 0 {
		return true, nil
	}
	if createAll {
		return createSeparateTaskCandidates(command, start, candidates)
	}

	selected, err := promptSeparateTaskCandidateSelection(command, reader, candidates)
	if err != nil {
		return false, fmt.Errorf("task review %s: %w", start.taskID(), err)
	}
	if len(selected) == 0 {
		return true, nil
	}
	return createSelectedSeparateTaskCandidates(command, start, reader, selected)
}

func createSeparateTaskCandidates(
	command *cobra.Command,
	start taskReviewStart,
	candidates []separateTaskCandidate,
) (bool, error) {
	backend, err := newBeadsTaskBackend(start.resolvedCtx.Resolved.Source.BackendDir)
	if err != nil {
		return false, fmt.Errorf("task review %s: create follow-up task backend: %w", start.taskID(), err)
	}
	for _, candidate := range candidates {
		created, err := backend.Create(command.Context(), createOptionsForReviewCandidate(start, candidate))
		if err != nil {
			return false, fmt.Errorf(
				"task review %s: create follow-up Bead for review finding %d (%s): %w; fix the backend issue, then rerun task review",
				start.taskID(),
				candidate.index+1,
				candidate.finding.TaskProposal.Title,
				err,
			)
		}
		if _, err := start.store.RecordReviewFindingCreatedTask(
			start.repoID(),
			start.taskID(),
			start.review.Attempt,
			candidate.index,
			created.ID,
		); err != nil {
			return false, fmt.Errorf("task review %s: record created follow-up task %s: %w", start.taskID(), created.ID, err)
		}
		if _, err := fmt.Fprintf(
			command.ErrOrStderr(),
			"Created follow-up Bead %s for review finding %d.\n",
			created.ID,
			candidate.index+1,
		); err != nil {
			return false, err
		}
	}
	return true, nil
}

func createSelectedSeparateTaskCandidates(
	command *cobra.Command,
	start taskReviewStart,
	reader *bufio.Reader,
	selected []separateTaskCandidate,
) (bool, error) {
	backend, err := newBeadsTaskBackend(start.resolvedCtx.Resolved.Source.BackendDir)
	if err != nil {
		return false, fmt.Errorf("task review %s: create follow-up task backend: %w", start.taskID(), err)
	}
	for _, candidate := range selected {
		created, err := backend.Create(command.Context(), createOptionsForReviewCandidate(start, candidate))
		if err != nil {
			return promptContinueAfterFollowUpCreationFailure(command, reader, candidate, err)
		}
		if _, err := start.store.RecordReviewFindingCreatedTask(
			start.repoID(),
			start.taskID(),
			start.review.Attempt,
			candidate.index,
			created.ID,
		); err != nil {
			return false, fmt.Errorf("task review %s: record created follow-up task %s: %w", start.taskID(), created.ID, err)
		}
		if _, err := fmt.Fprintf(
			command.ErrOrStderr(),
			"Created follow-up Bead %s for review finding %d.\n",
			created.ID,
			candidate.index+1,
		); err != nil {
			return false, err
		}
	}
	return true, nil
}

func pendingSeparateTaskCandidates(
	store taskstate.Store,
	repoID string,
	taskID string,
	attempt int,
) ([]separateTaskCandidate, error) {
	state, err := store.Load(repoID, taskID)
	if err != nil {
		return nil, err
	}
	for _, reviewAttempt := range state.Reviews {
		if reviewAttempt.Attempt != attempt {
			continue
		}
		candidates := make([]separateTaskCandidate, 0)
		for index, finding := range reviewAttempt.Findings {
			if finding.Type != taskstate.FindingTypeSeparateTask || strings.TrimSpace(finding.CreatedTaskID) != "" {
				continue
			}
			candidates = append(candidates, separateTaskCandidate{index: index, finding: finding})
		}
		return candidates, nil
	}
	return nil, fmt.Errorf("review attempt %d was not found", attempt)
}

func promptSeparateTaskCandidateSelection(
	command *cobra.Command,
	reader *bufio.Reader,
	candidates []separateTaskCandidate,
) ([]separateTaskCandidate, error) {
	output := command.ErrOrStderr()
	if _, err := fmt.Fprintln(output, "\nSeparate-task review findings can be created as standalone Beads:"); err != nil {
		return nil, err
	}
	for displayIndex, candidate := range candidates {
		if _, err := fmt.Fprintf(
			output,
			"%d. %s (review finding %d)\n",
			displayIndex+1,
			candidate.finding.TaskProposal.Title,
			candidate.index+1,
		); err != nil {
			return nil, err
		}
	}
	if _, err := fmt.Fprint(output, "Create follow-up Beads [numbers, a=all, n=none]: "); err != nil {
		return nil, err
	}

	line, err := reader.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return nil, fmt.Errorf("read follow-up task selection: %w", err)
	}
	return selectSeparateTaskCandidates(candidates, line)
}

func selectSeparateTaskCandidates(candidates []separateTaskCandidate, input string) ([]separateTaskCandidate, error) {
	answer := strings.ToLower(strings.TrimSpace(input))
	switch answer {
	case "", "n", "no", "none", "skip":
		return nil, nil
	case "a", "all":
		return candidates, nil
	}

	fields := strings.FieldsFunc(answer, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	if len(fields) == 0 {
		return nil, nil
	}

	seen := map[int]bool{}
	selected := make([]separateTaskCandidate, 0, len(fields))
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

func createOptionsForReviewCandidate(start taskReviewStart, candidate separateTaskCandidate) taskmodel.CreateOptions {
	proposal := candidate.finding.TaskProposal
	return taskmodel.CreateOptions{
		Title:              proposal.Title,
		Description:        reviewFollowUpTaskDescription(start, candidate),
		AcceptanceCriteria: proposal.AcceptanceCriteria,
		IssueType:          taskmodel.IssueTypeTask,
	}
}

func reviewFollowUpTaskDescription(start taskReviewStart, candidate separateTaskCandidate) string {
	proposal := candidate.finding.TaskProposal
	provenance := fmt.Sprintf(
		"Discovered during review of %s in repository %s (review attempt %d, finding %d).",
		start.taskID(),
		start.repoID(),
		start.review.Attempt,
		candidate.index+1,
	)
	if strings.TrimSpace(candidate.finding.Step) != "" {
		provenance += " Review step: " + candidate.finding.Step + "."
	}
	return strings.TrimSpace(proposal.Description) + "\n\nProvenance:\n" + provenance
}

func promptContinueAfterFollowUpCreationFailure(
	command *cobra.Command,
	reader *bufio.Reader,
	candidate separateTaskCandidate,
	cause error,
) (bool, error) {
	output := command.ErrOrStderr()
	if _, err := fmt.Fprintf(
		output,
		"Failed to create follow-up Bead for review finding %d (%s): %v\n",
		candidate.index+1,
		candidate.finding.TaskProposal.Title,
		cause,
	); err != nil {
		return false, err
	}
	if _, err := fmt.Fprint(output, "Continue publication without creating this follow-up Bead? [y/N]: "); err != nil {
		return false, err
	}

	line, err := reader.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return false, fmt.Errorf("read follow-up task failure confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func validateReviewCandidateReady(
	ctx context.Context,
	store taskstate.Store,
	resolvedCtx resolvedTaskContext,
	workdir string,
) error {
	if err := requireCleanReviewIndex(ctx, workdir); err != nil {
		return err
	}

	hasCandidate, err := hasReviewCandidateChanges(ctx, workdir)
	if err != nil {
		return err
	}
	if hasCandidate {
		return nil
	}

	taskState, err := store.Load(resolvedCtx.Resolved.Source.Repository.ID, resolvedCtx.Resolved.TaskID)
	if err != nil {
		return fmt.Errorf("load task state: %w", err)
	}
	if strings.TrimSpace(taskstate.FinalizationFacts(taskState).Commit) != "" {
		return nil
	}
	return fmt.Errorf(
		"worktree %q has no candidate changes to review and task has no recorded finalization commit",
		workdir,
	)
}

func requireCleanReviewIndex(ctx context.Context, workdir string) error {
	output, err := gitCombinedOutput(ctx, workdir, "diff", "--cached", "--quiet", "--")
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		status, statusErr := gitOutput(ctx, workdir, "status", "--short")
		if statusErr != nil {
			status = "unable to read git status: " + statusErr.Error()
		}
		return fmt.Errorf(
			"review requires a clean Git index, but staged changes are present in %q; unstage them before running task review\n%s",
			workdir,
			strings.TrimSpace(status),
		)
	}
	return fmt.Errorf("inspect staged changes: git diff --cached --quiet: %w: %s", err, strings.TrimSpace(string(output)))
}

func hasReviewCandidateChanges(ctx context.Context, workdir string) (bool, error) {
	return review.HasCandidateChanges(ctx, workdir)
}

type manualReviewOutcome struct {
	result manualReviewResult
	status taskstate.ReviewStatus
}

type manualReviewResult int

const (
	manualReviewApproved manualReviewResult = iota
	manualReviewBlocked
	manualReviewAborted
)

func renderManualReviewContext(
	command *cobra.Command,
	store taskstate.Store,
	resolvedCtx resolvedTaskContext,
	workdir string,
	reviewAttempt taskstate.ReviewAttempt,
	step review.Step,
) error {
	output := command.ErrOrStderr()
	repo := resolvedCtx.Resolved.Source.Repository
	taskItem := resolvedCtx.Task

	taskState, err := store.Load(repo.ID, taskItem.ID)
	if err != nil {
		return fmt.Errorf("load task state: %w", err)
	}
	latest, ok := taskstate.LatestRun(taskState)
	if !ok {
		return fmt.Errorf("task has no Orpheus run attempts; run `orpheus task run %s` first", taskItem.ID)
	}
	if latest.Completion == nil {
		return fmt.Errorf("latest run attempt %d has no completion block; run `orpheus agent done` first", latest.Attempt)
	}
	completions, err := manualReviewCompletions(taskState)
	if err != nil {
		return err
	}

	status, err := gitOutput(command.Context(), workdir, "status", "--short")
	if err != nil {
		return fmt.Errorf("read git status: %w", err)
	}

	if _, err := fmt.Fprintf(output, "Task: %s - %s\n", taskItem.ID, taskItem.Title); err != nil {
		return err
	}
	if err := renderManualReviewCompletions(output, completions); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "git status --short:"); err != nil {
		return err
	}
	if strings.TrimSpace(status) == "" {
		if _, err := fmt.Fprintln(output, "(clean)"); err != nil {
			return err
		}
	} else if _, err := fmt.Fprint(output, status); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output); err != nil {
		return err
	}

	return renderPriorReviewAdvisories(output, taskState, reviewAttempt.Attempt, step.Name)
}

type manualReviewCompletionContext struct {
	original taskstate.RunAttempt
	latest   taskstate.RunAttempt
}

func manualReviewCompletions(taskState taskstate.TaskState) (manualReviewCompletionContext, error) {
	var original taskstate.RunAttempt
	var latest taskstate.RunAttempt
	for _, run := range taskState.Runs {
		if run.Completion == nil {
			continue
		}
		if original.Attempt == 0 && run.ReviewFollowUp == nil {
			original = run
		}
		if latest.Attempt == 0 || run.Attempt > latest.Attempt {
			latest = run
		}
	}
	if original.Attempt == 0 {
		return manualReviewCompletionContext{}, errors.New("original implementation completion is required")
	}
	if latest.Attempt == 0 {
		return manualReviewCompletionContext{}, errors.New("latest completion is required")
	}
	return manualReviewCompletionContext{
		original: original,
		latest:   latest,
	}, nil
}

func resumedReviewImplementerName(start taskReviewStart) (string, error) {
	taskState, err := start.store.Load(start.repoID(), start.taskID())
	if err != nil {
		return "", fmt.Errorf("load task state for resumed review implementer: %w", err)
	}
	completions, err := manualReviewCompletions(taskState)
	if err != nil {
		return "", fmt.Errorf("resolve resumed review implementer: %w", err)
	}
	agentName := strings.TrimSpace(completions.latest.Execution.Agent)
	if agentName == "" {
		agentName = strings.TrimSpace(completions.latest.Execution.Profile)
	}
	if agentName == "" {
		return "", fmt.Errorf(
			"resolve resumed review implementer: latest completed run attempt %d has no recorded agent profile",
			completions.latest.Attempt,
		)
	}
	return agentName, nil
}

func renderManualReviewCompletions(output io.Writer, ctx manualReviewCompletionContext) error {
	if ctx.latest.ReviewFollowUp == nil {
		return renderManualReviewCompletion(output, "Latest completion", "Completion description", ctx.latest.Completion)
	}
	if err := renderManualReviewCompletion(output, "Original completion", "Original completion description", ctx.original.Completion); err != nil {
		return err
	}
	return renderManualReviewCompletion(output, "Latest fix completion", "Latest fix completion description", ctx.latest.Completion)
}

func renderManualReviewCompletion(output io.Writer, summaryLabel string, descriptionLabel string, completion *taskstate.Completion) error {
	if completion == nil {
		return errors.New("completion is required")
	}
	if _, err := fmt.Fprintf(output, "%s: %s\n", summaryLabel, strings.TrimSpace(completion.Summary)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "%s: %s\n\n", descriptionLabel, strings.TrimSpace(completion.Description)); err != nil {
		return err
	}
	return nil
}

func runManualReviewPrompt(
	command *cobra.Command,
	reader *bufio.Reader,
	store taskstate.Store,
	resolvedCtx resolvedTaskContext,
	review taskstate.ReviewAttempt,
	stepName string,
	hunkNotes []review.HunkNote,
) (manualReviewOutcome, error) {
	session := manualReviewSession{
		command:     command,
		store:       store,
		resolvedCtx: resolvedCtx,
		review:      review,
		stepName:    stepName,
	}
	if err := session.importHunkNotes(reader, hunkNotes); err != nil {
		return manualReviewOutcome{}, err
	}
	for {
		actions, err := session.availableActions()
		if err != nil {
			return manualReviewOutcome{}, err
		}
		action, err := promptManualReviewAction(command, reader, actions)
		if err != nil {
			return manualReviewOutcome{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
		}

		result, done, err := session.handleManualReviewAction(action, reader, actions)
		if err != nil || done {
			return result, err
		}
	}
}

type manualReviewSession struct {
	command     *cobra.Command
	store       taskstate.Store
	resolvedCtx resolvedTaskContext
	review      taskstate.ReviewAttempt
	stepName    string
}

func (s manualReviewSession) importHunkNotes(reader *bufio.Reader, notes []review.HunkNote) error {
	if len(notes) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(s.command.ErrOrStderr(), "\nCaptured %d Hunk note(s). Classify notes to import them as review findings.\n", len(notes)); err != nil {
		return err
	}
	for index, note := range notes {
		if err := renderHunkNoteForImport(s.command.ErrOrStderr(), index, note); err != nil {
			return err
		}
		finding, importNote, err := promptHunkNoteFinding(s.command, reader, note)
		if err != nil {
			return fmt.Errorf("task review %s: import Hunk note %s: %w", s.resolvedCtx.Resolved.TaskID, hunkNoteID(note), err)
		}
		if !importNote {
			continue
		}
		finding.Step = s.stepName
		if _, err := s.store.RecordReviewFinding(
			s.resolvedCtx.Resolved.Source.Repository.ID,
			s.resolvedCtx.Resolved.TaskID,
			s.review.Attempt,
			finding,
		); err != nil {
			return fmt.Errorf("task review %s: record Hunk note finding: %w", s.resolvedCtx.Resolved.TaskID, err)
		}
		if _, err := fmt.Fprintf(s.command.ErrOrStderr(), "Imported Hunk note %s as %s finding.\n", hunkNoteID(note), hunkNoteFindingTypeLabel(finding.Type)); err != nil {
			return err
		}
	}
	return nil
}

func (s manualReviewSession) handleManualReviewAction(
	action string,
	reader *bufio.Reader,
	actions manualReviewActions,
) (manualReviewOutcome, bool, error) {
	switch action {
	case "a", "approve":
		if !actions.hasOpenBlockers {
			return s.approve()
		}
	case "f", "finish", "finish/block":
		if actions.hasOpenBlockers {
			return s.approve()
		}
	case "b", "block":
		return s.block(reader)
	case "p", "promote":
		if actions.hasPromotableAdvisories {
			return s.promotePriorAdvisories(reader)
		}
	case "v", "advisory":
		return s.recordFinding(reader, taskstate.FindingTypeAdvisory, "advisory")
	case "t", "task":
		return s.recordFinding(reader, taskstate.FindingTypeSeparateTask, "separate-task")
	case "q", "abort":
		return s.abort()
	}
	err := s.writeInvalidAction(actions)
	return manualReviewOutcome{}, false, err
}

func (s manualReviewSession) availableActions() (manualReviewActions, error) {
	latest, err := s.loadLatestReview()
	if err != nil {
		return manualReviewActions{}, err
	}
	return manualReviewActions{
		hasOpenBlockers:         taskstate.ReviewHasOpenBlockers(latest),
		hasPromotableAdvisories: len(unresolvedPriorReviewAdvisories(latest, s.stepName)) > 0,
	}, nil
}

type manualReviewActions struct {
	hasOpenBlockers         bool
	hasPromotableAdvisories bool
}

func (s manualReviewSession) approve() (manualReviewOutcome, bool, error) {
	latest, err := s.loadLatestReview()
	if err != nil {
		return manualReviewOutcome{}, true, err
	}
	if taskstate.ReviewHasOpenBlockers(latest) {
		_, err := fmt.Fprintf(s.command.ErrOrStderr(), "Review blocked for %s.\n", s.resolvedCtx.Resolved.TaskID)
		return manualReviewOutcome{
			result: manualReviewBlocked,
			status: taskstate.ReviewStatusBlocked,
		}, true, err
	}
	return manualReviewOutcome{
		result: manualReviewApproved,
		status: taskstate.ReviewStatusPassed,
	}, true, nil
}

func (s manualReviewSession) block(reader *bufio.Reader) (manualReviewOutcome, bool, error) {
	if _, _, err := s.recordFinding(reader, taskstate.FindingTypeBlocking, "blocking"); err != nil {
		return manualReviewOutcome{}, true, err
	}
	_, err := fmt.Fprintf(s.command.ErrOrStderr(), "Review blocked for %s.\n", s.resolvedCtx.Resolved.TaskID)
	return manualReviewOutcome{
		result: manualReviewBlocked,
		status: taskstate.ReviewStatusBlocked,
	}, true, err
}

func (s manualReviewSession) recordFinding(
	reader *bufio.Reader,
	findingType taskstate.FindingType,
	label string,
) (manualReviewOutcome, bool, error) {
	finding, err := promptReviewFinding(s.command, reader, findingType)
	if err != nil {
		return manualReviewOutcome{}, true, fmt.Errorf("task review %s: %w", s.resolvedCtx.Resolved.TaskID, err)
	}
	finding.Step = s.stepName
	if _, err := s.store.RecordReviewFinding(
		s.resolvedCtx.Resolved.Source.Repository.ID,
		s.resolvedCtx.Resolved.TaskID,
		s.review.Attempt,
		finding,
	); err != nil {
		return manualReviewOutcome{}, true, fmt.Errorf("task review %s: record %s finding: %w", s.resolvedCtx.Resolved.TaskID, label, err)
	}
	return manualReviewOutcome{}, false, nil
}

func (s manualReviewSession) promotePriorAdvisories(reader *bufio.Reader) (manualReviewOutcome, bool, error) {
	latest, err := s.loadLatestReview()
	if err != nil {
		return manualReviewOutcome{}, true, err
	}
	advisories := unresolvedPriorReviewAdvisories(latest, s.stepName)
	if len(advisories) == 0 {
		_, err := fmt.Fprintln(s.command.ErrOrStderr(), "No unresolved prior advisories to promote.")
		return manualReviewOutcome{}, false, err
	}
	if err := renderPriorReviewAdvisoryList(s.command.ErrOrStderr(), advisories); err != nil {
		return manualReviewOutcome{}, true, err
	}

	indexes, err := promptReviewAdvisoryPromotionSelection(s.command, reader, advisories)
	if err != nil {
		return manualReviewOutcome{}, true, fmt.Errorf("task review %s: %w", s.resolvedCtx.Resolved.TaskID, err)
	}
	if len(indexes) == 0 {
		return manualReviewOutcome{}, false, nil
	}
	for _, index := range indexes {
		if _, err := s.store.PromoteReviewAdvisoryFinding(
			s.resolvedCtx.Resolved.Source.Repository.ID,
			s.resolvedCtx.Resolved.TaskID,
			s.review.Attempt,
			index,
		); err != nil {
			return manualReviewOutcome{}, true, fmt.Errorf("task review %s: promote advisory finding %d: %w", s.resolvedCtx.Resolved.TaskID, index+1, err)
		}
		if _, err := fmt.Fprintf(s.command.ErrOrStderr(), "Promoted advisory finding %d to blocking.\n", index+1); err != nil {
			return manualReviewOutcome{}, true, err
		}
	}
	return manualReviewOutcome{}, false, nil
}

func (s manualReviewSession) loadLatestReview() (taskstate.ReviewAttempt, error) {
	taskState, err := s.store.Load(s.resolvedCtx.Resolved.Source.Repository.ID, s.resolvedCtx.Resolved.TaskID)
	if err != nil {
		return taskstate.ReviewAttempt{}, fmt.Errorf("task review %s: load review state: %w", s.resolvedCtx.Resolved.TaskID, err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Attempt != s.review.Attempt {
		return taskstate.ReviewAttempt{}, fmt.Errorf("task review %s: latest review attempt no longer matches attempt %d", s.resolvedCtx.Resolved.TaskID, s.review.Attempt)
	}
	return latest, nil
}

func (s manualReviewSession) abort() (manualReviewOutcome, bool, error) {
	_, err := fmt.Fprintf(s.command.ErrOrStderr(), "Review aborted for %s.\n", s.resolvedCtx.Resolved.TaskID)
	return manualReviewOutcome{
		result: manualReviewAborted,
		status: taskstate.ReviewStatusAborted,
	}, true, err
}

func (s manualReviewSession) writeInvalidAction(actions manualReviewActions) error {
	_, err := fmt.Fprintf(s.command.ErrOrStderr(), "Choose %s.\n", actions.invalidActionChoices())
	return err
}

func (a manualReviewActions) promptChoices() string {
	choices := make([]string, 0, 6)
	if a.hasOpenBlockers {
		choices = append(choices, "f=finish/block")
	} else {
		choices = append(choices, "a=approve")
	}
	choices = append(choices, "b=block")
	if a.hasPromotableAdvisories {
		choices = append(choices, "p=promote advisory")
	}
	choices = append(choices, "v=advisory", "t=task", "q=abort")
	return strings.Join(choices, ", ")
}

func (a manualReviewActions) invalidActionChoices() string {
	choices := make([]string, 0, 6)
	if a.hasOpenBlockers {
		choices = append(choices, "finish/block")
	} else {
		choices = append(choices, "approve")
	}
	choices = append(choices, "block")
	if a.hasPromotableAdvisories {
		choices = append(choices, "promote")
	}
	choices = append(choices, "advisory", "task", "abort")
	return joinReviewChoiceLabels(choices)
}

func joinReviewChoiceLabels(choices []string) string {
	switch len(choices) {
	case 0:
		return ""
	case 1:
		return choices[0]
	case 2:
		return choices[0] + " or " + choices[1]
	default:
		return strings.Join(choices[:len(choices)-1], ", ") + ", or " + choices[len(choices)-1]
	}
}

func renderHunkNoteForImport(output io.Writer, index int, note review.HunkNote) error {
	lines := []string{
		fmt.Sprintf("\nHunk note %d: %s", index+1, hunkNoteID(note)),
		fmt.Sprintf("  File: %s", formatReviewValue(strings.TrimSpace(note.FilePath))),
		fmt.Sprintf("  Location: %s", formatReviewValue(formatHunkNoteLocation(note))),
	}
	if strings.TrimSpace(note.Title) != "" {
		lines = append(lines, fmt.Sprintf("  Title: %s", strings.TrimSpace(note.Title)))
	}
	lines = append(lines, fmt.Sprintf("  Body: %s", formatReviewValue(strings.TrimSpace(note.Body))))
	for _, line := range lines {
		if _, err := fmt.Fprintln(output, line); err != nil {
			return err
		}
	}
	return nil
}

func promptHunkNoteFinding(
	command *cobra.Command,
	reader *bufio.Reader,
	note review.HunkNote,
) (taskstate.ReviewFinding, bool, error) {
	for {
		if _, err := fmt.Fprintf(command.ErrOrStderr(), "Classify Hunk note %s [b=blocking, v=advisory, t=task, s=skip]: ", hunkNoteID(note)); err != nil {
			return taskstate.ReviewFinding{}, false, err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return taskstate.ReviewFinding{}, false, fmt.Errorf("read Hunk note classification: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "b", "blocking":
			return buildHunkNoteFinding(note, taskstate.FindingTypeBlocking, taskstate.ReviewTaskProposal{}), true, nil
		case "v", "advisory":
			return buildHunkNoteFinding(note, taskstate.FindingTypeAdvisory, taskstate.ReviewTaskProposal{}), true, nil
		case "t", "task", "separate-task":
			proposal, err := promptHunkNoteTaskProposal(command, reader)
			if err != nil {
				return taskstate.ReviewFinding{}, false, err
			}
			return buildHunkNoteFinding(note, taskstate.FindingTypeSeparateTask, proposal), true, nil
		case "s", "skip":
			return taskstate.ReviewFinding{}, false, nil
		default:
			if _, err := fmt.Fprintln(command.ErrOrStderr(), "Choose blocking, advisory, task, or skip."); err != nil {
				return taskstate.ReviewFinding{}, false, err
			}
		}
	}
}

func promptHunkNoteTaskProposal(command *cobra.Command, reader *bufio.Reader) (taskstate.ReviewTaskProposal, error) {
	taskTitle, err := promptReviewLine(command, reader, "Separate task title")
	if err != nil {
		return taskstate.ReviewTaskProposal{}, err
	}
	taskDescription, err := promptReviewLine(command, reader, "Separate task description")
	if err != nil {
		return taskstate.ReviewTaskProposal{}, err
	}
	taskAcceptanceCriteria, err := promptReviewLine(command, reader, "Separate task acceptance criteria")
	if err != nil {
		return taskstate.ReviewTaskProposal{}, err
	}
	return taskstate.ReviewTaskProposal{
		Title:              taskTitle,
		Description:        taskDescription,
		AcceptanceCriteria: taskAcceptanceCriteria,
	}, nil
}

func buildHunkNoteFinding(
	note review.HunkNote,
	findingType taskstate.FindingType,
	proposal taskstate.ReviewTaskProposal,
) taskstate.ReviewFinding {
	return taskstate.ReviewFinding{
		Type:         findingType,
		Title:        hunkNoteFindingTitle(note),
		Description:  hunkNoteFindingDescription(note),
		TaskProposal: proposal,
	}
}

func hunkNoteFindingTypeLabel(findingType taskstate.FindingType) string {
	if findingType == taskstate.FindingTypeSeparateTask {
		return "separate-task"
	}
	return string(findingType)
}

func hunkNoteFindingTitle(note review.HunkNote) string {
	if title := strings.TrimSpace(note.Title); title != "" {
		return "Hunk note: " + abbreviateReviewTitle(title)
	}
	for _, line := range strings.Split(strings.TrimSpace(note.Body), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return "Hunk note: " + abbreviateReviewTitle(line)
		}
	}
	if filePath := strings.TrimSpace(note.FilePath); filePath != "" {
		return "Hunk note on " + filePath
	}
	return "Hunk note " + hunkNoteID(note)
}

func abbreviateReviewTitle(value string) string {
	value = strings.TrimSpace(value)
	const maxTitleLength = 72
	runes := []rune(value)
	if len(runes) <= maxTitleLength {
		return value
	}
	return strings.TrimSpace(string(runes[:maxTitleLength-3])) + "..."
}

func hunkNoteFindingDescription(note review.HunkNote) string {
	lines := []string{
		"Hunk provenance:",
		"Note ID: " + hunkNoteID(note),
		"File: " + formatReviewValue(strings.TrimSpace(note.FilePath)),
		"Location: " + formatReviewValue(formatHunkNoteLocation(note)),
	}
	if note.HunkIndex != nil {
		lines = append(lines, fmt.Sprintf("Hunk index: %d", *note.HunkIndex))
	}
	if strings.TrimSpace(note.Source) != "" {
		lines = append(lines, "Source: "+strings.TrimSpace(note.Source))
	}
	if strings.TrimSpace(note.Author) != "" {
		lines = append(lines, "Author: "+strings.TrimSpace(note.Author))
	}
	lines = append(lines, "", "Note body:", strings.TrimSpace(note.Body))
	return strings.Join(lines, "\n")
}

func hunkNoteID(note review.HunkNote) string {
	if id := strings.TrimSpace(note.NoteID); id != "" {
		return id
	}
	return "(unknown)"
}

func formatHunkNoteLocation(note review.HunkNote) string {
	locations := make([]string, 0, 2)
	if location := formatHunkNoteRange("old", note.OldRange); location != "" {
		locations = append(locations, location)
	}
	if location := formatHunkNoteRange("new", note.NewRange); location != "" {
		locations = append(locations, location)
	}
	return strings.Join(locations, "; ")
}

func formatHunkNoteRange(side string, lineRange []int) string {
	if len(lineRange) == 0 {
		return ""
	}
	start := lineRange[0]
	end := start
	if len(lineRange) > 1 && lineRange[1] > 0 {
		end = lineRange[1]
	}
	if start <= 0 {
		return ""
	}
	if end <= start {
		return fmt.Sprintf("%s line %d", side, start)
	}
	return fmt.Sprintf("%s lines %d-%d", side, start, end)
}

func promptManualReviewAction(
	command *cobra.Command,
	reader *bufio.Reader,
	actions manualReviewActions,
) (string, error) {
	if _, err := fmt.Fprintf(command.ErrOrStderr(), "\nReview action [%s]: ", actions.promptChoices()); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read review action: %w", err)
	}
	return strings.ToLower(strings.TrimSpace(line)), nil
}

func promptAutomatedBlockerDecisions(
	command *cobra.Command,
	reader *bufio.Reader,
	blockerReview review.AutomatedBlockerReview,
) ([]review.AutomatedBlockerDecision, error) {
	if len(blockerReview.Blockers) == 0 {
		return nil, nil
	}
	output := command.ErrOrStderr()
	if _, err := fmt.Fprintf(
		output,
		"\nAutomated blocking findings from step %q can be kept, downgraded, or waived/canceled.\n",
		blockerReview.Step.Name,
	); err != nil {
		return nil, err
	}

	decisions := make([]review.AutomatedBlockerDecision, 0, len(blockerReview.Blockers))
	for _, blocker := range blockerReview.Blockers {
		if err := renderAutomatedBlocker(output, blocker); err != nil {
			return nil, err
		}
		decision, err := promptAutomatedBlockerDecision(command, reader, blocker)
		if err != nil {
			return nil, err
		}
		decisions = append(decisions, decision)
	}
	return decisions, nil
}

func renderAutomatedBlocker(output io.Writer, blocker review.AutomatedBlocker) error {
	finding := blocker.Finding
	lines := []string{
		fmt.Sprintf("  Finding %d:", blocker.Index+1),
		fmt.Sprintf("    Title: %s", formatReviewValue(finding.Title)),
		fmt.Sprintf("    Description: %s", formatReviewValue(finding.Description)),
	}
	if strings.TrimSpace(finding.SuggestedAction) != "" {
		lines = append(lines, fmt.Sprintf("    Suggested action: %s", finding.SuggestedAction))
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(output, line); err != nil {
			return err
		}
	}
	return nil
}

func promptAutomatedBlockerDecision(
	command *cobra.Command,
	reader *bufio.Reader,
	blocker review.AutomatedBlocker,
) (review.AutomatedBlockerDecision, error) {
	for {
		if _, err := fmt.Fprintf(
			command.ErrOrStderr(),
			"Decision for finding %d [k=keep, d=downgrade advisory, w=waive/cancel]: ",
			blocker.Index+1,
		); err != nil {
			return review.AutomatedBlockerDecision{}, err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return keepAutomatedBlockerDecision(blocker), nil
			}
			return review.AutomatedBlockerDecision{}, fmt.Errorf("read automated blocker decision: %w", err)
		}

		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "k", "keep":
			return keepAutomatedBlockerDecision(blocker), nil
		case "d", "downgrade", "advisory":
			reason, err := promptRequiredReviewReason(command, reader, "Downgrade reason")
			if err != nil {
				return review.AutomatedBlockerDecision{}, err
			}
			return review.AutomatedBlockerDecision{
				FindingIndex: blocker.Index,
				Action:       review.AutomatedBlockerActionDowngrade,
				Reason:       reason,
			}, nil
		case "w", "waive", "c", "cancel":
			reason, err := promptRequiredReviewReason(command, reader, "Waiver reason")
			if err != nil {
				return review.AutomatedBlockerDecision{}, err
			}
			return review.AutomatedBlockerDecision{
				FindingIndex: blocker.Index,
				Action:       review.AutomatedBlockerActionWaive,
				Reason:       reason,
			}, nil
		default:
			if _, err := fmt.Fprintln(command.ErrOrStderr(), "Choose keep, downgrade, waive, or cancel."); err != nil {
				return review.AutomatedBlockerDecision{}, err
			}
		}
	}
}

func keepAutomatedBlockerDecision(blocker review.AutomatedBlocker) review.AutomatedBlockerDecision {
	return review.AutomatedBlockerDecision{
		FindingIndex: blocker.Index,
		Action:       review.AutomatedBlockerActionKeep,
	}
}

func promptRequiredReviewReason(command *cobra.Command, reader *bufio.Reader, label string) (string, error) {
	for {
		reason, err := promptReviewLine(command, reader, label)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(reason) != "" {
			return reason, nil
		}
		if _, err := fmt.Fprintln(command.ErrOrStderr(), "A reason is required."); err != nil {
			return "", err
		}
	}
}

type priorReviewAdvisory struct {
	index   int
	finding taskstate.ReviewFinding
}

func renderPriorReviewAdvisories(
	output io.Writer,
	taskState taskstate.TaskState,
	reviewAttempt int,
	currentStep string,
) error {
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Attempt != reviewAttempt {
		return nil
	}
	advisories := unresolvedPriorReviewAdvisories(latest, currentStep)
	if len(advisories) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(output, "Prior unresolved advisories:"); err != nil {
		return err
	}
	return renderPriorReviewAdvisoryList(output, advisories)
}

func renderPriorReviewAdvisoryList(output io.Writer, advisories []priorReviewAdvisory) error {
	for _, advisory := range advisories {
		finding := advisory.finding
		step := strings.TrimSpace(finding.Step)
		if step == "" {
			step = "(unspecified)"
		}
		if _, err := fmt.Fprintf(output, "  Finding %d (%s): %s\n", advisory.index+1, step, finding.Title); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "    Description: %s\n", finding.Description); err != nil {
			return err
		}
		if strings.TrimSpace(finding.SuggestedAction) != "" {
			if _, err := fmt.Fprintf(output, "    Suggested action: %s\n", finding.SuggestedAction); err != nil {
				return err
			}
		}
	}
	return nil
}

func unresolvedPriorReviewAdvisories(review taskstate.ReviewAttempt, currentStep string) []priorReviewAdvisory {
	advisories := make([]priorReviewAdvisory, 0)
	currentStep = strings.TrimSpace(currentStep)
	for index, finding := range review.Findings {
		if !taskstate.IsOpenAdvisoryReviewFinding(finding) {
			continue
		}
		if currentStep != "" && strings.TrimSpace(finding.Step) == currentStep {
			continue
		}
		advisories = append(advisories, priorReviewAdvisory{index: index, finding: finding})
	}
	return advisories
}

func promptReviewAdvisoryPromotionSelection(
	command *cobra.Command,
	reader *bufio.Reader,
	advisories []priorReviewAdvisory,
) ([]int, error) {
	if _, err := fmt.Fprint(command.ErrOrStderr(), "Promote advisory finding numbers (blank to cancel): "); err != nil {
		return nil, err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("read advisory promotion selection: %w", err)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	return parseReviewAdvisoryPromotionSelection(line, advisories)
}

func parseReviewAdvisoryPromotionSelection(input string, advisories []priorReviewAdvisory) ([]int, error) {
	allowed := make(map[int]struct{}, len(advisories))
	for _, advisory := range advisories {
		allowed[advisory.index] = struct{}{}
	}

	seen := map[int]struct{}{}
	indexes := make([]int, 0)
	for _, field := range strings.FieldsFunc(input, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	}) {
		number, err := strconv.Atoi(field)
		if err != nil {
			return nil, fmt.Errorf("invalid advisory finding number %q", field)
		}
		index := number - 1
		if _, ok := allowed[index]; !ok {
			return nil, fmt.Errorf("finding %d is not an unresolved prior advisory", number)
		}
		if _, ok := seen[index]; ok {
			continue
		}
		seen[index] = struct{}{}
		indexes = append(indexes, index)
	}
	if len(indexes) == 0 {
		return nil, errors.New("at least one advisory finding number is required")
	}
	return indexes, nil
}

func promptManualCommandConfirmation(
	command *cobra.Command,
	reader *bufio.Reader,
	step review.Step,
) (bool, error) {
	if _, err := fmt.Fprintf(
		command.ErrOrStderr(),
		"\nRun manual command for step %q (%s)? [Y/n]: ",
		step.Name,
		commandLineForReviewStep(step),
	); err != nil {
		return false, err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && line == "" {
			return false, nil
		}
		if !errors.Is(err, io.EOF) {
			return false, fmt.Errorf("read manual command confirmation: %w", err)
		}
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	if answer == "" {
		return true, nil
	}
	return answer == "y" || answer == "yes", nil
}

func commandLineForReviewStep(step review.Step) string {
	parts := make([]string, 0, len(step.Args)+1)
	parts = append(parts, strconv.Quote(step.Command))
	for _, arg := range step.Args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

func promptReviewFinding(
	command *cobra.Command,
	reader *bufio.Reader,
	findingType taskstate.FindingType,
) (taskstate.ReviewFinding, error) {
	title, err := promptReviewLine(command, reader, "Finding title")
	if err != nil {
		return taskstate.ReviewFinding{}, err
	}
	description, err := promptReviewLine(command, reader, "Finding description")
	if err != nil {
		return taskstate.ReviewFinding{}, err
	}
	suggestedAction, err := promptReviewLine(command, reader, "Suggested action (optional)")
	if err != nil {
		return taskstate.ReviewFinding{}, err
	}

	finding := taskstate.ReviewFinding{
		Type:            findingType,
		Title:           title,
		Description:     description,
		SuggestedAction: suggestedAction,
	}
	if findingType == taskstate.FindingTypeSeparateTask {
		taskTitle, err := promptReviewLine(command, reader, "Separate task title")
		if err != nil {
			return taskstate.ReviewFinding{}, err
		}
		taskDescription, err := promptReviewLine(command, reader, "Separate task description")
		if err != nil {
			return taskstate.ReviewFinding{}, err
		}
		taskAcceptanceCriteria, err := promptReviewLine(command, reader, "Separate task acceptance criteria")
		if err != nil {
			return taskstate.ReviewFinding{}, err
		}
		finding.TaskProposal = taskstate.ReviewTaskProposal{
			Title:              taskTitle,
			Description:        taskDescription,
			AcceptanceCriteria: taskAcceptanceCriteria,
		}
	}
	return finding, nil
}

func promptReviewLine(command *cobra.Command, reader *bufio.Reader, label string) (string, error) {
	if _, err := fmt.Fprintf(command.ErrOrStderr(), "%s: ", label); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read %s: %w", strings.ToLower(label), err)
	}
	return strings.TrimSpace(line), nil
}

func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	output, err := gitCombinedOutput(ctx, dir, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func gitCombinedOutput(ctx context.Context, dir string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	return command.CombinedOutput()
}

func runTaskDone(command *cobra.Command, opts *rootOptions, taskID string, summary string, description string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_done"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task finalization")

	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}

	service := newTaskFinalizationService(paths, taskCtx)
	finalized, err := finalizeTaskWithConfirmation(command, service, workflow.FinalizeOptions{
		TaskID:              taskID,
		Summary:             summary,
		Description:         description,
		RequirePassedReview: true,
	})
	if err != nil {
		return err
	}

	logger.DebugContext(
		command.Context(),
		"finalized task",
		slog.String("repo_id", finalized.Repository.ID),
		slog.String("task_id", finalized.Task.ID),
		slog.String("commit", finalized.Finalization.Commit),
	)
	return renderTaskDoneResult(command, finalized)
}

func newTaskFinalizationService(paths state.Paths, taskCtx taskContext) workflow.FinalizationService {
	return workflow.FinalizationService{
		Paths:   paths,
		Sources: taskCtx.Sources,
		BackendFactory: func(source taskmodel.RepositorySource) (workflow.FinalizationBackend, error) {
			return newBeadsTaskBackend(source.BackendDir)
		},
		RunStore:   taskstate.NewStore(paths),
		PRProvider: pullrequest.GHProvider{},
	}
}

func finalizeTaskWithConfirmation(
	command *cobra.Command,
	service workflow.FinalizationService,
	finalizeOpts workflow.FinalizeOptions,
) (workflow.FinalizationResult, error) {
	finalized, err := service.Finalize(command.Context(), finalizeOpts)
	if err == nil {
		return finalized, nil
	}

	confirmation, ok := workflow.RunningCompletionConfirmationFromError(err)
	if !ok {
		return workflow.FinalizationResult{}, fmt.Errorf("task done: %w", err)
	}
	confirmed, confirmErr := confirmRunningCompletionFinalization(command, confirmation)
	if confirmErr != nil {
		return workflow.FinalizationResult{}, fmt.Errorf("task done: %w", confirmErr)
	}
	if !confirmed {
		return workflow.FinalizationResult{}, fmt.Errorf("task done: %w", err)
	}
	finalizeOpts.AllowRunningCompleted = true
	finalized, err = service.Finalize(command.Context(), finalizeOpts)
	if err != nil {
		return workflow.FinalizationResult{}, fmt.Errorf("task done: %w", err)
	}
	return finalized, nil
}

func renderTaskDoneResult(command *cobra.Command, finalized workflow.FinalizationResult) error {
	if finalized.PRURL != "" {
		action := "created"
		if finalized.PRRecovered {
			action = "recovered existing"
		}
		_, err := fmt.Fprintf(
			command.OutOrStdout(),
			"Published %s: committed %s, pushed %s, and %s PR %s. Backend task remains open for PR review.\n",
			finalized.Task.ID,
			finalized.Finalization.Commit,
			finalized.Branch,
			action,
			finalized.PRURL,
		)
		return err
	}
	_, err := fmt.Fprintf(
		command.OutOrStdout(),
		"Finalized %s: committed %s, pushed %s, and closed the backend task.\n",
		finalized.Task.ID,
		finalized.Finalization.Commit,
		finalized.Repository.DefaultBranch,
	)
	return err
}

func confirmRunningCompletionFinalization(
	command *cobra.Command,
	confirmation workflow.RunningCompletionConfirmation,
) (bool, error) {
	input := command.InOrStdin()
	if !taskDoneInputIsTerminal(input) {
		return false, nil
	}

	output := command.ErrOrStderr()
	if _, err := fmt.Fprintf(
		output,
		"Warning: latest run attempt %d for task %s is still recorded as running, but it has a completion block.\n",
		confirmation.Attempt,
		confirmation.TaskID,
	); err != nil {
		return false, err
	}
	if confirmation.Summary != "" {
		if _, err := fmt.Fprintf(output, "Recorded completion summary: %s\n", confirmation.Summary); err != nil {
			return false, err
		}
	}
	if _, err := fmt.Fprintln(
		output,
		"Continuing will finalize the reviewed main/solo work without changing the recorded run status.",
	); err != nil {
		return false, err
	}
	if _, err := fmt.Fprint(output, "Finalize anyway? [y/N]: "); err != nil {
		return false, err
	}

	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return false, fmt.Errorf("read finalization confirmation: %w", err)
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes", nil
}

func runTaskSync(command *cobra.Command, opts *rootOptions, taskID string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_sync"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task sync")

	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}

	service := workflow.SyncService{
		Paths:   paths,
		Sources: taskCtx.Sources,
		BackendFactory: func(source taskmodel.RepositorySource) (taskmodel.SyncBackend, error) {
			return newBeadsTaskBackend(source.BackendDir)
		},
		RunStore: taskstate.NewStore(paths),
		ConflictResolver: syncConflictAgentResolver{
			paths:    paths,
			stdout:   command.OutOrStdout(),
			stderr:   command.ErrOrStderr(),
			launcher: attachedAgentLauncher,
		},
		PRProvider: pullrequest.GHProvider{},
	}
	result, err := service.Sync(command.Context(), workflow.SyncOptions{TaskID: taskID})
	if err != nil {
		return fmt.Errorf("task sync: %w", err)
	}

	logger.DebugContext(
		command.Context(),
		"synced task",
		slog.String("repo_id", result.Repository.ID),
		slog.String("task_id", result.Task.ID),
		slog.String("status", string(result.Status)),
		slog.String("branch", result.Branch),
	)
	return renderTaskSyncResult(command.OutOrStdout(), result)
}

func runTaskSyncAll(command *cobra.Command, opts *rootOptions) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_sync_all"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task sync all")

	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}

	service := workflow.SyncService{
		Paths:   paths,
		Sources: taskCtx.Sources,
		BackendFactory: func(source taskmodel.RepositorySource) (taskmodel.SyncBackend, error) {
			return newBeadsTaskBackend(source.BackendDir)
		},
		ScanFactory: func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
			return newBeadsTaskBackend(source.BackendDir)
		},
		RunStore: taskstate.NewStore(paths),
		ConflictResolver: syncConflictAgentResolver{
			paths:    paths,
			stdout:   command.OutOrStdout(),
			stderr:   command.ErrOrStderr(),
			launcher: attachedAgentLauncher,
		},
		PRProvider: pullrequest.GHProvider{},
	}
	result, err := service.SyncAll(command.Context())
	if err != nil {
		return fmt.Errorf("task sync --all: %w", err)
	}

	logger.DebugContext(
		command.Context(),
		"synced all PR-boundary tasks",
		slog.Int("result_count", len(result.Results)),
		slog.Int("failure_count", len(result.Failures)),
	)
	if err := renderTaskSyncAllResult(command.OutOrStdout(), result); err != nil {
		return err
	}
	if result.HasFailures() {
		return taskSyncAllFailureError{failures: result.Failures}
	}
	return nil
}

func resolveTaskRunAgentCommand(paths state.Paths, agentName string, sessionName string) (string, agent.CommandSnapshot, error) {
	prompt := agent.RenderBootstrapPrompt()
	agentConfig, err := agent.LoadConfig(paths)
	if err != nil {
		return "", agent.CommandSnapshot{}, err
	}
	commandSnapshot, err := agentConfig.ResolveCommandWithValues(agentName, agent.InterpolationValues{
		SessionName: sessionName,
	})
	if err != nil {
		return "", agent.CommandSnapshot{}, fmt.Errorf("resolve agent profile: %w", err)
	}
	return prompt, commandSnapshot, nil
}

func resolveTaskRunFollowUpAgentCommand(paths state.Paths, agentName string, sessionName string) (string, agent.CommandSnapshot, error) {
	prompt := agent.RenderBootstrapPrompt()
	agentConfig, err := agent.LoadConfig(paths)
	if err != nil {
		return "", agent.CommandSnapshot{}, err
	}
	commandSnapshot, err := agentConfig.ResolveImplementerCommandWithValues(agentName, agent.InterpolationValues{
		SessionName: sessionName,
	})
	if err != nil {
		return "", agent.CommandSnapshot{}, fmt.Errorf("resolve agent profile: %w", err)
	}
	return prompt, commandSnapshot, nil
}

type syncConflictAgentResolver struct {
	paths    state.Paths
	stdout   io.Writer
	stderr   io.Writer
	launcher agent.Launcher
}

func (r syncConflictAgentResolver) PrepareSyncConflictResolution(
	_ context.Context,
	opts workflow.SyncConflictResolutionOptions,
) (workflow.PreparedSyncConflictResolution, error) {
	prompt := agent.RenderBootstrapPrompt()
	agentConfig, err := agent.LoadConfig(r.paths)
	if err != nil {
		return workflow.PreparedSyncConflictResolution{}, err
	}
	sessionName := syncConflictAgentSessionName(opts.Task.ID)
	commandSnapshot, err := agentConfig.ResolveImplementerCommandWithValues("", agent.InterpolationValues{
		SessionName: sessionName,
	})
	if err != nil {
		return workflow.PreparedSyncConflictResolution{}, fmt.Errorf("resolve conflict agent profile: %w", err)
	}

	launcher := r.launcher
	if launcher == nil {
		launcher = agent.AttachedLauncher{}
	}
	return workflow.PreparedSyncConflictResolution{
		Execution: taskstate.AgentExecution{
			Purpose:     taskstate.AgentExecutionPurposeSyncConflictResolution,
			Status:      taskstate.RunStatusRunning,
			Agent:       commandSnapshot.AgentName,
			Profile:     commandSnapshot.AgentName,
			Harness:     commandSnapshot.Harness,
			Model:       commandSnapshot.Model,
			Command:     commandSnapshot.Command,
			Args:        append([]string{}, commandSnapshot.Args...),
			SessionName: sessionName,
		},
		Resolve: func(ctx context.Context) error {
			return launcher.Run(ctx, commandSnapshot, agent.LaunchOptions{
				Dir: opts.Worktree,
				Env: syncConflictAgentEnvironment(
					opts.Repository.ID,
					opts.Task.ID,
					opts.Worktree,
					opts.Branch,
					prompt,
					opts.ConflictFiles,
				),
				Stdin:  strings.NewReader(""),
				Stdout: r.stdout,
				Stderr: r.stderr,
			})
		},
	}, nil
}

func syncConflictAgentSessionName(taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return "sync-conflict"
	}
	return "sync-conflict-" + taskID
}

func syncConflictAgentEnvironment(
	repoID string,
	taskID string,
	worktree string,
	branch string,
	prompt string,
	conflictFiles []string,
) []string {
	env := taskRunEnvironment(repoID, taskID, worktree, branch, prompt)
	env = append(env,
		"ORPHEUS_AGENT_PURPOSE=conflict_resolution",
		"ORPHEUS_CONFLICT_FILES="+strings.Join(conflictFiles, "\n"),
	)
	return env
}

func taskWorkingDirectory(repo taskmodel.Repository, taskItem taskmodel.Task) (string, error) {
	metadata := taskItem.OrpheusMetadata()
	if !metadata.HasWorktree || strings.TrimSpace(metadata.Worktree) == "" {
		return "", fmt.Errorf(
			"task has no Orpheus working directory metadata; run `orpheus task run %s` first",
			taskItem.ID,
		)
	}
	if !metadata.HasBranch || strings.TrimSpace(metadata.Branch) == "" {
		return "", fmt.Errorf(
			"task has incomplete Orpheus target metadata: %s is missing; run `orpheus task run %s` first",
			taskmodel.MetadataBranch,
			taskItem.ID,
		)
	}

	worktree := cleanTaskRunPath(metadata.Worktree)
	if !filepath.IsAbs(worktree) {
		return "", fmt.Errorf("%s=%q is not an absolute path", taskmodel.MetadataWorktree, metadata.Worktree)
	}

	switch workflow.ClassifyRunTarget(repo, metadata.Branch, worktree) {
	case workflow.TargetMainSolo:
		return cleanTaskRunPath(repo.Path), nil
	case workflow.TargetWorktreeTeam:
		return worktree, nil
	case workflow.TargetRepoRootTeam:
		return cleanTaskRunPath(repo.Path), nil
	default:
		return "", fmt.Errorf(
			"task Orpheus target metadata is inconsistent: %s=%q, %s=%q do not identify a main/solo, worktree/team, or repo-root/team target for repo %s",
			taskmodel.MetadataBranch,
			metadata.Branch,
			taskmodel.MetadataWorktree,
			metadata.Worktree,
			repo.ID,
		)
	}
}

func cleanTaskRunPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func renderTaskSyncResult(output interface{ Write([]byte) (int, error) }, result workflow.SyncResult) error {
	switch result.Status {
	case workflow.SyncStatusAlreadyInReview:
		_, err := fmt.Fprintf(
			output,
			"Synced %s: PR %s is still open for review. %s. No backend changes were made.\n",
			result.Task.ID,
			result.PRURL,
			result.Reason,
		)
		return err
	case workflow.SyncStatusBranchUpdated:
		_, err := fmt.Fprintf(
			output,
			"Synced %s: PR %s is still open for review. %s.\n",
			result.Task.ID,
			result.PRURL,
			result.Reason,
		)
		return err
	case workflow.SyncStatusPRMerged:
		_, err := fmt.Fprintf(
			output,
			"Synced %s: PR %s is merged. Backend task was closed.\n",
			result.Task.ID,
			result.PRURL,
		)
		return err
	case workflow.SyncStatusSkipped:
		_, err := fmt.Fprintf(
			output,
			"Skipped %s: %s. No backend changes were made.\n",
			result.Task.ID,
			result.Reason,
		)
		return err
	default:
		return fmt.Errorf("unknown task sync result status %q", result.Status)
	}
}

func renderTaskSyncAllResult(output interface{ Write([]byte) (int, error) }, result workflow.SyncAllResult) error {
	if len(result.Results) == 0 && len(result.Failures) == 0 {
		_, err := fmt.Fprintln(output, "No PR-boundary tasks found across registered repositories.")
		return err
	}

	if err := renderTaskSyncAllGroup(output, "Open/in-review PRs", result.Results, func(syncResult workflow.SyncResult) bool {
		return syncResult.Status == workflow.SyncStatusAlreadyInReview
	}); err != nil {
		return err
	}
	if err := renderTaskSyncAllGroup(output, "Updated open PR branches", result.Results, func(syncResult workflow.SyncResult) bool {
		return syncResult.Status == workflow.SyncStatusBranchUpdated
	}); err != nil {
		return err
	}
	if err := renderTaskSyncAllGroup(output, "Merged/closed tasks", result.Results, func(syncResult workflow.SyncResult) bool {
		return syncResult.Status == workflow.SyncStatusPRMerged
	}); err != nil {
		return err
	}
	if err := renderTaskSyncAllGroup(output, "Skipped", result.Results, func(syncResult workflow.SyncResult) bool {
		return syncResult.Status == workflow.SyncStatusSkipped
	}); err != nil {
		return err
	}
	return renderTaskSyncAllFailures(output, result.Failures)
}

func renderTaskSyncAllGroup(
	output interface{ Write([]byte) (int, error) },
	title string,
	results []workflow.SyncResult,
	matches func(workflow.SyncResult) bool,
) error {
	matched := make([]workflow.SyncResult, 0)
	for _, result := range results {
		if matches(result) {
			matched = append(matched, result)
		}
	}
	if len(matched) == 0 {
		return nil
	}

	if _, err := fmt.Fprintf(output, "%s (%d):\n", title, len(matched)); err != nil {
		return err
	}
	for _, result := range matched {
		if err := renderTaskSyncAllResultLine(output, result); err != nil {
			return err
		}
	}
	return nil
}

func renderTaskSyncAllResultLine(output interface{ Write([]byte) (int, error) }, result workflow.SyncResult) error {
	prefix := fmt.Sprintf("  - %s (%s): ", result.Task.ID, result.Repository.ID)
	switch result.Status {
	case workflow.SyncStatusAlreadyInReview:
		_, err := fmt.Fprintf(output, "%sPR %s is still open for review; %s\n", prefix, result.PRURL, result.Reason)
		return err
	case workflow.SyncStatusBranchUpdated:
		_, err := fmt.Fprintf(output, "%sPR %s branch updated; %s\n", prefix, result.PRURL, result.Reason)
		return err
	case workflow.SyncStatusPRMerged:
		_, err := fmt.Fprintf(output, "%sPR %s is merged; backend task was closed\n", prefix, result.PRURL)
		return err
	case workflow.SyncStatusSkipped:
		_, err := fmt.Fprintf(output, "%s%s\n", prefix, result.Reason)
		return err
	default:
		return fmt.Errorf("unknown task sync result status %q", result.Status)
	}
}

func renderTaskSyncAllFailures(output interface{ Write([]byte) (int, error) }, failures []workflow.SyncAllFailure) error {
	if len(failures) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(output, "Errors (%d):\n", len(failures)); err != nil {
		return err
	}
	for _, failure := range failures {
		taskID := strings.TrimSpace(failure.TaskID)
		if taskID == "" {
			if _, err := fmt.Fprintf(
				output,
				"  - repo %s: %s: %v\n",
				failure.Repository.ID,
				failure.Operation,
				failure.Err,
			); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(
			output,
			"  - %s (%s): %s: %v\n",
			taskID,
			failure.Repository.ID,
			failure.Operation,
			failure.Err,
		); err != nil {
			return err
		}
	}
	return nil
}

type taskSyncAllFailureError struct {
	failures []workflow.SyncAllFailure
}

func (e taskSyncAllFailureError) Error() string {
	return fmt.Sprintf("task sync --all failed for %d item(s)", len(e.failures))
}

type resolvedTaskContext struct {
	Resolved       taskmodel.ResolvedTaskSource
	Task           taskmodel.Task
	RegisteredRepo registry.Repo
}

type resolvedTaskRunContext struct {
	Resolved       taskmodel.ResolvedTaskSource
	RegisteredRepo registry.Repo
}

func resolveTaskContext(command *cobra.Command, operation string, taskID string) (resolvedTaskContext, error) {
	return resolveTaskContextWithScope(command, operation, taskID, true)
}

func resolveTaskShowContext(command *cobra.Command, taskID string) (resolvedTaskContext, error) {
	return resolveTaskContextWithScope(command, "task show", taskID, false)
}

func resolveTaskContextWithScope(
	command *cobra.Command,
	operation string,
	taskID string,
	requireActiveItem bool,
) (resolvedTaskContext, error) {
	taskCtx, err := loadTaskContext()
	if err != nil {
		return resolvedTaskContext{}, err
	}

	resolved, err := taskmodel.ResolveTaskSource(taskCtx.Sources, taskID)
	if err != nil {
		return resolvedTaskContext{}, err
	}

	repo, err := registeredRepoForSource(taskCtx.Registry, resolved.Source.Repository.ID)
	if err != nil {
		return resolvedTaskContext{}, err
	}

	taskItem, err := queryResolvedTask(command, operation, resolved)
	if err != nil {
		return resolvedTaskContext{}, err
	}
	if requireActiveItem && !taskmodel.IsM2TaskViewItem(taskItem) {
		return resolvedTaskContext{}, fmt.Errorf(
			"%s %s: item is out of scope for M2 task views; expected an active item, got issue_type=%s status=%s",
			operation,
			resolved.TaskID,
			formatTaskField(string(taskItem.IssueType)),
			formatTaskField(string(taskItem.Status)),
		)
	}
	return resolvedTaskContext{
		Resolved:       resolved,
		Task:           taskItem,
		RegisteredRepo: repo,
	}, nil
}

func resolveTaskRunContext(taskID string) (resolvedTaskRunContext, error) {
	taskCtx, err := loadTaskContext()
	if err != nil {
		return resolvedTaskRunContext{}, err
	}

	resolved, err := taskmodel.ResolveTaskSource(taskCtx.Sources, taskID)
	if err != nil {
		return resolvedTaskRunContext{}, err
	}

	repo, err := registeredRepoForSource(taskCtx.Registry, resolved.Source.Repository.ID)
	if err != nil {
		return resolvedTaskRunContext{}, err
	}

	return resolvedTaskRunContext{
		Resolved:       resolved,
		RegisteredRepo: repo,
	}, nil
}

func queryResolvedTask(command *cobra.Command, operation string, resolved taskmodel.ResolvedTaskSource) (taskmodel.Task, error) {
	backend, err := newBeadsTaskBackend(resolved.Source.BackendDir)
	if err != nil {
		return taskmodel.Task{}, fmt.Errorf("%s %s: create backend for repo %s (%s; prefix %s): %w",
			operation,
			resolved.TaskID,
			resolved.Source.Repository.ID,
			resolved.Source.Repository.Name,
			resolved.Source.Repository.TaskIDPrefix,
			err,
		)
	}

	taskItem, err := queryTaskFromBackend(command.Context(), operation, resolved, backend)
	if err != nil {
		return taskmodel.Task{}, err
	}
	return taskItem, nil
}

func queryTaskFromBackend(
	ctx context.Context,
	operation string,
	resolved taskmodel.ResolvedTaskSource,
	backend taskmodel.Getter,
) (taskmodel.Task, error) {
	taskItem, err := backend.Get(ctx, resolved.TaskID)
	if err != nil {
		if errors.Is(err, taskmodel.ErrNotFound) {
			return taskmodel.Task{}, fmt.Errorf(
				"%s %s: task was not found in repo %s (%s; prefix %s); check the task id or inspect the repo backend directory: %w",
				operation,
				resolved.TaskID,
				resolved.Source.Repository.ID,
				resolved.Source.Repository.Name,
				resolved.Source.Repository.TaskIDPrefix,
				err,
			)
		}
		return taskmodel.Task{}, fmt.Errorf("%s %s: query repo %s (%s; prefix %s): %w",
			operation,
			resolved.TaskID,
			resolved.Source.Repository.ID,
			resolved.Source.Repository.Name,
			resolved.Source.Repository.TaskIDPrefix,
			err,
		)
	}
	return taskItem, nil
}

func registeredRepoForSource(reg registry.Registry, repoID string) (registry.Repo, error) {
	for _, repo := range reg.Repos {
		if repo.ID == repoID {
			return repo, nil
		}
	}
	return registry.Repo{}, fmt.Errorf("registered repo %q was resolved for the task but is missing from the registry", repoID)
}

func taskRunEnvironment(repoID string, taskID string, worktree string, branch string, prompt string) []string {
	return []string{
		"ORPHEUS_REPO_ID=" + repoID,
		"ORPHEUS_TASK_ID=" + taskID,
		"ORPHEUS_WORKTREE=" + worktree,
		"ORPHEUS_BRANCH=" + branch,
		"ORPHEUS_AGENT_PROMPT=" + prompt,
	}
}

type taskRowsResult struct {
	Rows     []taskmodel.RepoTask
	Failures []taskmodel.RepoFailure
}

func (r taskRowsResult) HasFailures() bool {
	return len(r.Failures) > 0
}

type taskRowsOptions struct {
	operation    string
	logOperation string
	loadingLog   string
	queryingLog  string
	queriedLog   string
	detailed     bool
	query        func(context.Context, taskmodel.Aggregator) taskRowsResult
}

func taskRepositorySources(store registry.Store, reg registry.Registry) ([]taskmodel.RepositorySource, error) {
	sources := make([]taskmodel.RepositorySource, 0, len(reg.Repos))
	for _, repo := range reg.Repos {
		beadsDir, err := store.BeadsDir(repo)
		if err != nil {
			return nil, err
		}
		sources = append(sources, taskmodel.RepositorySource{
			Repository: taskmodel.Repository{
				ID:                    repo.ID,
				Name:                  repo.Name,
				TaskIDPrefix:          repo.BeadsPrefix,
				Path:                  repo.Path,
				DefaultBranch:         repo.DefaultBranch,
				TitleTemplate:         repo.TitleTemplate,
				ReviewPipeline:        repo.ReviewPipeline,
				ReviewPipelineAliases: cloneStringMap(repo.ReviewPipelineAliases),
			},
			BackendDir: beadsDir,
		})
	}
	return sources, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func renderTaskDetails(
	output interface{ Write([]byte) (int, error) },
	row taskmodel.RepoTask,
	state taskstate.TaskState,
) error {
	if err := renderTaskRepositoryDetails(output, row.Repository); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output); err != nil {
		return err
	}
	if err := renderTaskBodyDetails(output, row.Task); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output); err != nil {
		return err
	}
	if err := renderTaskOrpheusMetadata(output, row.Task.OrpheusMetadata()); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output); err != nil {
		return err
	}
	return renderTaskHistory(output, state)
}

type taskHistoryItem struct {
	at      time.Time
	display string
}

func renderTaskHistory(output interface{ Write([]byte) (int, error) }, state taskstate.TaskState) error {
	if _, err := fmt.Fprintln(output, "History:"); err != nil {
		return err
	}
	history := make([]taskHistoryItem, 0, len(state.Events)+len(state.Reviews)*2)
	for _, event := range state.Events {
		if event.Type != taskstate.EventWorktreeReused {
			history = append(history, taskHistoryItem{
				at:      event.At,
				display: event.DisplayName(),
			})
		}
	}
	history = append(history, reviewHistoryItems(state.Reviews)...)
	if len(history) == 0 {
		_, err := fmt.Fprintln(output, "  -")
		return err
	}

	sort.SliceStable(history, func(i, j int) bool {
		return history[i].at.Before(history[j].at)
	})
	for _, item := range history {
		if _, err := fmt.Fprintf(
			output,
			"  %s %s\n",
			item.at.UTC().Format(time.RFC3339),
			item.display,
		); err != nil {
			return err
		}
	}
	return nil
}

func reviewHistoryItems(reviews []taskstate.ReviewAttempt) []taskHistoryItem {
	items := make([]taskHistoryItem, 0, len(reviews)*2)
	for _, review := range reviews {
		if !review.StartedAt.IsZero() {
			items = append(items, taskHistoryItem{
				at:      review.StartedAt,
				display: fmt.Sprintf("Review attempt %d started", review.Attempt),
			})
		}
		items = append(items, reviewCreatedTaskHistoryItems(review)...)
		if review.Status == taskstate.ReviewStatusRunning || review.FinishedAt == nil || review.FinishedAt.IsZero() {
			continue
		}
		display := fmt.Sprintf("Review attempt %d %s", review.Attempt, review.Status)
		if review.AutonomousBudgetExhausted {
			display += " (autonomous review budget exhausted)"
		}
		items = append(items, taskHistoryItem{
			at:      *review.FinishedAt,
			display: display,
		})
	}
	return items
}

func reviewCreatedTaskHistoryItems(review taskstate.ReviewAttempt) []taskHistoryItem {
	items := make([]taskHistoryItem, 0, len(review.Findings))
	for index, finding := range review.Findings {
		if finding.CreatedTaskAt == nil || finding.CreatedTaskAt.IsZero() {
			continue
		}
		createdTaskID := strings.TrimSpace(finding.CreatedTaskID)
		if createdTaskID == "" {
			continue
		}
		items = append(items, taskHistoryItem{
			at: *finding.CreatedTaskAt,
			display: fmt.Sprintf(
				"Review attempt %d finding %d created follow-up task %s",
				review.Attempt,
				index+1,
				createdTaskID,
			),
		})
	}
	return items
}

func renderTaskRepositoryDetails(output interface{ Write([]byte) (int, error) }, repo taskmodel.Repository) error {
	if _, err := fmt.Fprintln(output, "Repository:"); err != nil {
		return err
	}
	for _, field := range []struct {
		label string
		value string
	}{
		{label: "  ID", value: repo.ID},
		{label: "  Name", value: repo.Name},
		{label: "  Task prefix", value: repo.TaskIDPrefix},
	} {
		if err := renderKeyValue(output, field.label, field.value); err != nil {
			return err
		}
	}
	return nil
}

func renderTaskBodyDetails(output interface{ Write([]byte) (int, error) }, task taskmodel.Task) error {
	if _, err := fmt.Fprintln(output, "Task:"); err != nil {
		return err
	}
	for _, field := range []struct {
		label string
		value string
	}{
		{label: "  ID", value: task.ID},
		{label: "  Title", value: task.Title},
		{label: "  External reference", value: task.ExternalRef},
		{label: "  Status", value: string(task.Status)},
		{label: "  Priority", value: fmt.Sprintf("%d", task.Priority)},
		{label: "  Type", value: string(task.IssueType)},
		{label: "  Labels", value: formatLabels(task.Labels)},
	} {
		if err := renderKeyValue(output, field.label, field.value); err != nil {
			return err
		}
	}
	return renderTaskBlockDetails(output, task)
}

func renderTaskBlockDetails(output interface{ Write([]byte) (int, error) }, task taskmodel.Task) error {
	for _, field := range []struct {
		label string
		value string
	}{
		{label: "  Description", value: task.Description},
		{label: "  Design", value: task.Design},
		{label: "  Acceptance criteria", value: task.AcceptanceCriteria},
	} {
		if err := renderBlockValue(output, field.label, field.value); err != nil {
			return err
		}
	}
	return nil
}

func renderTaskOrpheusMetadata(output interface{ Write([]byte) (int, error) }, metadata taskmodel.OrpheusMetadata) error {
	if _, err := fmt.Fprintln(output, "Orpheus metadata:"); err != nil {
		return err
	}
	for _, field := range []struct {
		label   string
		value   string
		present bool
	}{
		{label: "  Branch", value: metadata.Branch, present: metadata.HasBranch},
		{label: "  Worktree", value: metadata.Worktree, present: metadata.HasWorktree},
		{label: "  PR", value: metadata.PRURL, present: metadata.HasPRURL},
	} {
		if err := renderKeyValue(output, field.label, formatMetadataTableCell(field.value, field.present)); err != nil {
			return err
		}
	}
	return nil
}

func renderTaskStats(output interface{ Write([]byte) (int, error) }, state taskstate.TaskState) error {
	records := taskStatsExecutionRecords(state)
	rows := make([][]string, 0, len(records))
	for _, record := range records {
		rows = append(rows, taskStatsRow(record))
	}
	if _, err := fmt.Fprintln(output, "Executions"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, taskStatsCostEstimateDisclaimer); err != nil {
		return err
	}
	if err := renderTable(
		output,
		[]string{
			"TYPE",
			"ATTEMPT",
			"STEP",
			"PROFILE",
			"HARNESS",
			"MODEL",
			"COMMAND",
			"STARTED",
			"FINISHED",
			"DURATION",
			"STATUS",
			"SESSION",
			"USAGE",
			"ESTIMATED_API_EQUIVALENT_COST",
		},
		rows,
	); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "\nTotals"); err != nil {
		return err
	}
	return renderTable(
		output,
		[]string{
			"TYPE",
			"EXECUTIONS",
			"ACTIVE_AGENT_TIME",
			"TOTAL_TOKENS",
			"INPUT_TOKENS",
			"CACHED_INPUT_TOKENS",
			"OUTPUT_TOKENS",
			"REASONING_OUTPUT_TOKENS",
			"ESTIMATED_API_EQUIVALENT_COST",
			"UNKNOWN_USAGE",
			"UNKNOWN_COST",
		},
		taskStatsTotalRows(records),
	)
}

type taskStatsAggregateTable struct {
	title   string
	headers []string
	rows    [][]string
}

func renderTaskStatsAggregate(output interface{ Write([]byte) (int, error) }, report taskstats.AggregateReport) error {
	if _, err := fmt.Fprintf(output, "Aggregate stats grouped by %s\n", report.Group); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, taskStatsCostEstimateDisclaimer); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(
		output,
		"Tasks without resolved timestamp: %d\n\n",
		report.TasksWithoutResolvedTimestamp,
	); err != nil {
		return err
	}

	maxResolved := maxTaskStatsResolved(report.Periods)
	tables := []taskStatsAggregateTable{
		{
			title:   "Resolved Tasks",
			headers: []string{"PERIOD", "RESOLVED_TASKS", "TREND"},
			rows:    taskStatsAggregateResolvedRows(report.Periods, maxResolved),
		},
		{
			title:   "Lifecycle Time",
			headers: []string{"PERIOD", "FULL_AVG", "FULL_UNKNOWN", "IMPLEMENTATION_AVG", "IMPLEMENTATION_UNKNOWN"},
			rows:    taskStatsAggregateLifecycleRows(report.Periods),
		},
		{
			title:   "Agent Work",
			headers: []string{"PERIOD", "EXECUTIONS", "ACTIVE_AVG", "ACTIVE_TOTAL"},
			rows:    taskStatsAggregateWorkRows(report.Periods),
		},
		{
			title:   "Token Usage",
			headers: []string{"PERIOD", "TOTAL_TOKENS", "AVG_TOKENS", "UNKNOWN_USAGE"},
			rows:    taskStatsAggregateUsageRows(report.Periods),
		},
		{
			title:   "Estimated API-Equivalent Cost",
			headers: []string{"PERIOD", "TOTAL_COST", "AVG_COST", "UNKNOWN_COST"},
			rows:    taskStatsAggregateCostRows(report.Periods),
		},
	}
	return renderTaskStatsAggregateTables(output, tables)
}

func renderTaskStatsAggregateTables(
	output interface{ Write([]byte) (int, error) },
	tables []taskStatsAggregateTable,
) error {
	for i, table := range tables {
		if i > 0 {
			if _, err := fmt.Fprintln(output); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(output, table.title); err != nil {
			return err
		}
		if err := renderTable(output, table.headers, table.rows); err != nil {
			return err
		}
	}
	return nil
}

func maxTaskStatsResolved(periods []taskstats.AggregatePeriod) int {
	maxResolved := 0
	for _, period := range periods {
		if period.Resolved > maxResolved {
			maxResolved = period.Resolved
		}
	}
	return maxResolved
}

func taskStatsAggregateResolvedRows(periods []taskstats.AggregatePeriod, maxResolved int) [][]string {
	rows := make([][]string, 0, len(periods))
	for _, period := range periods {
		rows = append(rows, []string{
			period.Key,
			strconv.Itoa(period.Resolved),
			taskStatsTrendBar(period.Resolved, maxResolved),
		})
	}
	return rows
}

func taskStatsAggregateLifecycleRows(periods []taskstats.AggregatePeriod) [][]string {
	rows := make([][]string, 0, len(periods))
	for _, period := range periods {
		rows = append(rows, []string{
			period.Key,
			formatTaskStatsOptionalDuration(period.AverageFullTaskTime()),
			strconv.Itoa(period.UnknownFullTaskTime),
			formatTaskStatsOptionalDuration(period.AverageImplementationTime()),
			strconv.Itoa(period.UnknownImplementationTime),
		})
	}
	return rows
}

func taskStatsAggregateWorkRows(periods []taskstats.AggregatePeriod) [][]string {
	rows := make([][]string, 0, len(periods))
	for _, period := range periods {
		rows = append(rows, []string{
			period.Key,
			strconv.Itoa(period.Totals.Executions),
			formatTaskStatsOptionalDuration(period.AverageActiveAgentTime()),
			formatTaskStatsTotalDuration(period.Totals.Duration),
		})
	}
	return rows
}

func taskStatsAggregateUsageRows(periods []taskstats.AggregatePeriod) [][]string {
	rows := make([][]string, 0, len(periods))
	for _, period := range periods {
		rows = append(rows, []string{
			period.Key,
			formatTaskStatsTokenCount(period.Totals.Usage.TotalTokens),
			formatTaskStatsOptionalTokenCount(period.AverageTokenCount()),
			strconv.Itoa(period.Totals.UnknownUsage),
		})
	}
	return rows
}

func taskStatsAggregateCostRows(periods []taskstats.AggregatePeriod) [][]string {
	rows := make([][]string, 0, len(periods))
	for _, period := range periods {
		rows = append(rows, []string{
			period.Key,
			agent.FormatUsageCostUSD(period.Totals.CostMicroUSD),
			formatTaskStatsOptionalCost(period.AverageCostMicroUSD()),
			strconv.Itoa(period.Totals.UnknownCost),
		})
	}
	return rows
}

func formatTaskStatsOptionalDuration(duration time.Duration, ok bool) string {
	if !ok {
		return "-"
	}
	return duration.String()
}

func formatTaskStatsOptionalTokenCount(tokens int, ok bool) string {
	if !ok {
		return "-"
	}
	return formatTaskStatsTokenCount(tokens)
}

func formatTaskStatsTokenCount(tokens int) string {
	if tokens < 0 {
		tokens = 0
	}
	units := []struct {
		value int
		unit  string
	}{
		{value: 1_000_000_000, unit: "B"},
		{value: 1_000_000, unit: "M"},
		{value: 1_000, unit: "K"},
	}
	for _, unit := range units {
		if tokens >= unit.value {
			whole := tokens / unit.value
			tenth := tokens % unit.value * 10 / unit.value
			if tenth == 0 {
				return fmt.Sprintf("%d%s", whole, unit.unit)
			}
			return fmt.Sprintf("%d.%d%s", whole, tenth, unit.unit)
		}
	}
	return strconv.Itoa(tokens)
}

func formatTaskStatsOptionalCost(totalMicroUSD int64, ok bool) string {
	if !ok {
		return "-"
	}
	return agent.FormatUsageCostUSD(totalMicroUSD)
}

func taskStatsTrendBar(value int, maxValue int) string {
	const width = 20
	if value <= 0 || maxValue <= 0 {
		return "-"
	}
	length := value * width / maxValue
	if length == 0 {
		length = 1
	}
	return strings.Repeat("#", length)
}

type taskStatsExecutionRecord struct {
	activity  string
	attempt   string
	step      string
	status    string
	execution taskstate.AgentExecution
}

func taskStatsExecutionRecords(state taskstate.TaskState) []taskStatsExecutionRecord {
	records := make([]taskStatsExecutionRecord, 0, len(state.Runs))
	for _, run := range state.Runs {
		records = append(records, taskStatsExecutionRecord{
			activity:  "implementation",
			attempt:   strconv.Itoa(run.Attempt),
			step:      "-",
			status:    string(run.Status),
			execution: run.Execution,
		})
	}
	for _, reviewAttempt := range state.Reviews {
		for _, step := range reviewAttempt.Steps {
			if step.Execution == nil {
				continue
			}
			records = append(records, taskStatsExecutionRecord{
				activity:  "review-agent",
				attempt:   strconv.Itoa(reviewAttempt.Attempt),
				step:      step.Name,
				status:    string(step.Execution.Status),
				execution: *step.Execution,
			})
		}
	}
	return records
}

func taskStatsRow(record taskStatsExecutionRecord) []string {
	execution := record.execution
	return []string{
		record.activity,
		record.attempt,
		formatTaskStatsField(record.step),
		formatTaskStatsField(firstNonEmpty(execution.Profile, execution.Agent)),
		formatTaskStatsField(execution.Harness),
		formatTaskStatsField(execution.Model),
		formatTaskStatsCommand(execution),
		formatTaskStatsTime(execution.StartedAt),
		formatTaskStatsTimePointer(execution.FinishedAt),
		formatTaskStatsDuration(execution),
		formatTaskStatsField(record.status),
		formatTaskStatsSession(execution.Session),
		formatTaskStatsUsage(execution),
		formatTaskStatsUsageCost(execution),
	}
}

type taskStatsTotals struct {
	executions   int
	duration     time.Duration
	usage        taskstate.AgentUsage
	costMicroUSD int64
	unknownUsage int
	unknownCost  int
}

func taskStatsTotalRows(records []taskStatsExecutionRecord) [][]string {
	implementation := taskStatsTotals{}
	review := taskStatsTotals{}
	for _, record := range records {
		switch record.activity {
		case "implementation":
			implementation.add(record.execution)
		case "review-agent":
			review.add(record.execution)
		}
	}
	combined := implementation
	combined.addTotals(review)
	return [][]string{
		taskStatsTotalRow("implementation", implementation),
		taskStatsTotalRow("review-agent", review),
		taskStatsTotalRow("combined", combined),
	}
}

func (t *taskStatsTotals) add(execution taskstate.AgentExecution) {
	t.executions++
	if duration, ok := taskStatsDurationValue(execution); ok {
		t.duration += duration
	}
	if execution.Usage != nil {
		t.usage.InputTokens += execution.Usage.InputTokens
		t.usage.CachedInputTokens += execution.Usage.CachedInputTokens
		t.usage.OutputTokens += execution.Usage.OutputTokens
		t.usage.ReasoningOutputTokens += execution.Usage.ReasoningOutputTokens
		t.usage.TotalTokens += execution.Usage.TotalTokens
		if cost, ok := taskStatsExecutionCost(execution); ok {
			t.costMicroUSD += cost.AmountMicroUSD
		} else {
			t.unknownCost++
		}
		return
	}
	t.unknownUsage++
}

func (t *taskStatsTotals) addTotals(other taskStatsTotals) {
	t.executions += other.executions
	t.duration += other.duration
	t.usage.InputTokens += other.usage.InputTokens
	t.usage.CachedInputTokens += other.usage.CachedInputTokens
	t.usage.OutputTokens += other.usage.OutputTokens
	t.usage.ReasoningOutputTokens += other.usage.ReasoningOutputTokens
	t.usage.TotalTokens += other.usage.TotalTokens
	t.costMicroUSD += other.costMicroUSD
	t.unknownUsage += other.unknownUsage
	t.unknownCost += other.unknownCost
}

func taskStatsTotalRow(activity string, totals taskStatsTotals) []string {
	return []string{
		activity,
		strconv.Itoa(totals.executions),
		formatTaskStatsTotalDuration(totals.duration),
		strconv.Itoa(totals.usage.TotalTokens),
		strconv.Itoa(totals.usage.InputTokens),
		strconv.Itoa(totals.usage.CachedInputTokens),
		strconv.Itoa(totals.usage.OutputTokens),
		strconv.Itoa(totals.usage.ReasoningOutputTokens),
		agent.FormatUsageCostUSD(totals.costMicroUSD),
		strconv.Itoa(totals.unknownUsage),
		strconv.Itoa(totals.unknownCost),
	}
}

func taskStatsDurationValue(execution taskstate.AgentExecution) (time.Duration, bool) {
	if execution.DurationMillis > 0 {
		return time.Duration(execution.DurationMillis) * time.Millisecond, true
	}
	if execution.FinishedAt == nil || execution.StartedAt.IsZero() {
		return 0, false
	}
	duration := execution.FinishedAt.Sub(execution.StartedAt)
	if duration < 0 {
		return 0, false
	}
	return duration, true
}

func formatTaskStatsTotalDuration(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	return duration.String()
}

func formatTaskStatsCommand(execution taskstate.AgentExecution) string {
	if strings.TrimSpace(execution.Command) == "" {
		return "-"
	}
	return commandLineForStats(execution.Command, execution.Args)
}

func commandLineForStats(command string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, strconv.Quote(command))
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

func formatTaskStatsTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format(time.RFC3339)
}

func formatTaskStatsTimePointer(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return formatTaskStatsTime(*value)
}

func formatTaskStatsDuration(execution taskstate.AgentExecution) string {
	if execution.DurationMillis > 0 {
		return (time.Duration(execution.DurationMillis) * time.Millisecond).String()
	}
	if execution.FinishedAt == nil || execution.StartedAt.IsZero() {
		return "-"
	}
	duration := execution.FinishedAt.Sub(execution.StartedAt)
	if duration < 0 {
		return "-"
	}
	return duration.String()
}

func formatTaskStatsSession(session *taskstate.AgentSession) string {
	if session == nil {
		return "-"
	}
	return formatTaskStatsField(firstNonEmpty(session.ID, session.LogPath))
}

func formatTaskStatsUsage(execution taskstate.AgentExecution) string {
	if execution.Usage != nil {
		return fmt.Sprintf(
			"total=%d input=%d cached_input=%d output=%d reasoning_output=%d",
			execution.Usage.TotalTokens,
			execution.Usage.InputTokens,
			execution.Usage.CachedInputTokens,
			execution.Usage.OutputTokens,
			execution.Usage.ReasoningOutputTokens,
		)
	}
	capture := execution.UsageCapture
	status := string(capture.Status)
	if status == "" {
		status = string(taskstate.UsageCaptureUnknown)
	}
	reason := strings.TrimSpace(capture.Reason)
	if reason == "" {
		reason = "usage_not_recorded"
	}
	if capture.CandidateCount > 0 {
		return fmt.Sprintf("%s: %s (candidates=%d)", status, reason, capture.CandidateCount)
	}
	return fmt.Sprintf("%s: %s", status, reason)
}

func formatTaskStatsUsageCost(execution taskstate.AgentExecution) string {
	if execution.Usage == nil {
		return "-"
	}
	cost, ok := taskStatsExecutionCost(execution)
	if !ok {
		return fmt.Sprintf("unknown: no public pricing metadata for model %s", formatTaskStatsField(execution.Model))
	}
	kind := strings.TrimSpace(cost.Kind)
	if kind == "" {
		kind = agent.UsageCostKindEstimatedAPIEquivalent
	}
	pricing := cost.Pricing
	source := firstNonEmpty(pricing.SourceAccessed, pricing.SourcePublished, pricing.Source)
	if source == "" {
		source = "pricing_metadata"
	}
	return fmt.Sprintf(
		"estimated API-equivalent cost=%s kind=%s pricing=%s/%s/%s source=%s",
		agent.FormatUsageCostUSD(cost.AmountMicroUSD),
		kind,
		formatTaskStatsField(pricing.Provider),
		formatTaskStatsField(pricing.Model),
		formatTaskStatsField(pricing.ServiceTier),
		source,
	)
}

func taskStatsExecutionCost(execution taskstate.AgentExecution) (agent.UsageCost, bool) {
	if execution.Usage == nil {
		return agent.UsageCost{}, false
	}
	return agent.EstimateUsageCost(execution.Model, *execution.Usage)
}

func formatTaskStatsField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func renderKeyValue(output interface{ Write([]byte) (int, error) }, label string, value string) error {
	_, err := fmt.Fprintf(output, "%s: %s\n", label, formatTaskField(sanitizeTableCell(value)))
	return err
}

func renderBlockValue(output interface{ Write([]byte) (int, error) }, label string, value string) error {
	value = strings.TrimRight(strings.ReplaceAll(value, "\r\n", "\n"), "\r\n")
	if strings.TrimSpace(value) == "" {
		_, err := fmt.Fprintf(output, "%s: -\n", label)
		return err
	}
	if !strings.Contains(value, "\n") {
		_, err := fmt.Fprintf(output, "%s: %s\n", label, value)
		return err
	}

	if _, err := fmt.Fprintf(output, "%s:\n", label); err != nil {
		return err
	}
	for _, line := range strings.Split(value, "\n") {
		if _, err := fmt.Fprintf(output, "    %s\n", line); err != nil {
			return err
		}
	}
	return nil
}

func formatTaskField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}

func formatLabels(labels []string) string {
	if len(labels) == 0 {
		return "-"
	}
	return strings.Join(labels, ", ")
}

func renderTaskRows(output interface{ Write([]byte) (int, error) }, rows []taskmodel.RepoTask, detailed bool) error {
	if detailed {
		return renderDetailedTaskRows(output, rows)
	}

	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		tableRows = append(tableRows, taskTableRow(row))
	}
	return renderTable(output, []string{"REPO", "TASK_ID", "STATUS", "P", "TITLE"}, tableRows)
}

func renderDetailedTaskRows(output interface{ Write([]byte) (int, error) }, rows []taskmodel.RepoTask) error {
	tableRows := make([][]string, 0, len(rows))
	for _, row := range rows {
		tableRows = append(tableRows, detailedTaskTableRow(row))
	}
	return renderTable(
		output,
		[]string{"REPO_ID", "REPO", "TASK_PREFIX", "TASK_ID", "STATUS", "P", "BRANCH", "WORKTREE", "PR", "EXTERNAL_REF", "TITLE"},
		tableRows,
	)
}

func taskTableRow(row taskmodel.RepoTask) []string {
	return []string{
		row.Repository.Name,
		row.Task.ID,
		string(row.Task.Status),
		strconv.Itoa(row.Task.Priority),
		row.Task.Title,
	}
}

func detailedTaskTableRow(row taskmodel.RepoTask) []string {
	metadata := row.Task.OrpheusMetadata()
	return []string{
		row.Repository.ID,
		row.Repository.Name,
		row.Repository.TaskIDPrefix,
		row.Task.ID,
		string(row.Task.Status),
		strconv.Itoa(row.Task.Priority),
		formatMetadataTableCell(metadata.Branch, metadata.HasBranch),
		formatMetadataTableCell(metadata.Worktree, metadata.HasWorktree),
		formatMetadataTableCell(metadata.PRURL, metadata.HasPRURL),
		formatTaskField(row.Task.ExternalRef),
		row.Task.Title,
	}
}

func formatMetadataTableCell(value string, present bool) string {
	if !present || value == "" {
		return "-"
	}
	return value
}

func writeRepoFailures(output interface{ Write([]byte) (int, error) }, operation string, failures []taskmodel.RepoFailure) {
	for _, failure := range failures {
		_, _ = fmt.Fprintf(
			output,
			"%s: repo %s (%s; prefix %s): needs attention: source=%s operation=%s: %v\n",
			operation,
			failure.Repository.ID,
			failure.Repository.Name,
			failure.Repository.TaskIDPrefix,
			formatDiagnosticField(failure.Source),
			formatDiagnosticField(failure.Operation),
			failure.Err,
		)
	}
}

type partialRepoFailureError struct {
	operation string
	failures  []taskmodel.RepoFailure
}

func (e partialRepoFailureError) Error() string {
	if len(e.failures) == 0 {
		return fmt.Sprintf("%s completed with repo failures", e.operation)
	}

	summaries := make([]string, 0, len(e.failures))
	for _, failure := range e.failures {
		summaries = append(summaries, fmt.Sprintf("%s: %v", failure.Repository.ID, failure.Err))
	}
	return fmt.Sprintf("%s completed with %d repo failure(s): %s", e.operation, len(e.failures), strings.Join(summaries, "; "))
}

func formatDiagnosticField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}
