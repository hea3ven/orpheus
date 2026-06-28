package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
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
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

var (
	newBeadsTaskBackend                    = beads.NewTaskBackend
	attachedAgentLauncher   agent.Launcher = agent.AttachedLauncher{}
	taskDoneInputIsTerminal                = readerIsTerminal
)

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
			"Use --repo-root to run from the registered repo root on the task branch.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskRun(command, opts, args[0], agentName, mainMode, repoRootMode)
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "agent profile name to use instead of agents.defaults.implementer")
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
			"On the registered default branch, commits reviewed repo-root changes, pushes the " +
			"default branch, and closes the backend task. On a repo-root task branch, publishes " +
			"the feature branch as a pull request. Without a task id, the command infers one " +
			"ready task only when the current directory is exactly a registered repo root or " +
			"deterministic task worktree and the task owns the current branch.",
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
	cmd := &cobra.Command{
		Use:   "review <task-id>",
		Short: "Run the default local review gate for completed task work",
		Long: "Run the default local review gate for completed task work.\n\n" +
			"The built-in review pipeline contains one manual step named local-review. " +
			"Approval records a passed review attempt and then finalizes through the same " +
			"path as task done.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskReview(command, opts, args[0])
		},
	}
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
	}, taskState.Events)
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

func runTaskRun(command *cobra.Command, opts *rootOptions, taskID, agentName string, mainMode, repoRootMode bool) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_run"),
	)
	if mainMode && repoRootMode {
		return fmt.Errorf("task run %s: --main cannot be combined with --repo-root", taskID)
	}
	logger.DebugContext(command.Context(), "loading registered repos for task run")

	resolvedCtx, err := resolveTaskRunContext(taskID)
	if err != nil {
		return err
	}

	resolved := resolvedCtx.Resolved
	repo := resolvedCtx.RegisteredRepo

	logger.DebugContext(
		command.Context(),
		"resolved task source",
		slog.String("repo_id", resolved.Source.Repository.ID),
		slog.String("task_id", resolved.TaskID),
	)

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

	if err := dispatch.service.Finish(repo.ID, resolved.TaskID, dispatch.start.Attempt.Attempt); err != nil {
		return fmt.Errorf("task run %s: record run finish: %w", resolved.TaskID, err)
	}
	return nil
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

func runTaskReview(command *cobra.Command, opts *rootOptions, taskID string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_review"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task review")

	start, err := startTaskReview(command, taskID)
	if err != nil {
		return err
	}

	outcome, err := runReadOnlyManualReviewStep(command, start.store, start.resolvedCtx, start.review, start.workdir)
	if err != nil {
		_, _ = start.store.FinishReview(start.repoID(), start.taskID(), start.review.Attempt, taskstate.ReviewStatusFailed)
		return err
	}
	if _, err := start.store.FinishReview(start.repoID(), start.taskID(), start.review.Attempt, outcome.status); err != nil {
		_, _ = start.store.FinishReview(start.repoID(), start.taskID(), start.review.Attempt, taskstate.ReviewStatusFailed)
		return fmt.Errorf("task review %s: record %s review: %w", start.taskID(), outcome.status, err)
	}
	if outcome.result != manualReviewApproved {
		return nil
	}

	return finalizeApprovedTaskReview(command, logger, start.paths, start.resolvedCtx)
}

type taskReviewStart struct {
	paths       state.Paths
	resolvedCtx resolvedTaskContext
	store       taskstate.Store
	workdir     string
	review      taskstate.ReviewAttempt
}

func (s taskReviewStart) repoID() string {
	return s.resolvedCtx.Resolved.Source.Repository.ID
}

func (s taskReviewStart) taskID() string {
	return s.resolvedCtx.Resolved.TaskID
}

func startTaskReview(command *cobra.Command, taskID string) (taskReviewStart, error) {
	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return taskReviewStart{}, err
	}
	resolvedCtx, err := resolveTaskContext(command, "task review", taskID)
	if err != nil {
		return taskReviewStart{}, err
	}
	store := taskstate.NewStore(paths)
	workdir, err := taskWorkingDirectory(resolvedCtx.Resolved.Source.Repository, resolvedCtx.Task)
	if err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
	}
	if err := validateReviewCandidateReady(command.Context(), store, resolvedCtx, workdir); err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
	}

	review, err := store.StartReview(resolvedCtx.Resolved.Source.Repository.ID, resolvedCtx.Resolved.TaskID)
	if err != nil {
		return taskReviewStart{}, fmt.Errorf("task review %s: start review attempt: %w", resolvedCtx.Resolved.TaskID, err)
	}
	return taskReviewStart{
		paths:       paths,
		resolvedCtx: resolvedCtx,
		store:       store,
		workdir:     workdir,
		review:      review,
	}, nil
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
	status, err := reviewCandidateStatus(ctx, workdir)
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(status)) > 0, nil
}

type manualReviewOutcome struct {
	result manualReviewResult
	status taskstate.ReviewStatus
}

func runReadOnlyManualReviewStep(
	command *cobra.Command,
	store taskstate.Store,
	resolvedCtx resolvedTaskContext,
	review taskstate.ReviewAttempt,
	workdir string,
) (manualReviewOutcome, error) {
	snapshot, err := captureReviewCandidateSnapshot(command.Context(), workdir)
	if err != nil {
		return manualReviewOutcome{}, fmt.Errorf("task review %s: snapshot candidate changes: %w", resolvedCtx.Resolved.TaskID, err)
	}

	outcome, stepErr := runManualReviewStep(command, store, resolvedCtx, review)
	mutationErr := restoreReviewCandidateIfMutated(command.Context(), snapshot)
	if mutationErr != nil {
		return manualReviewOutcome{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, mutationErr)
	}
	if stepErr != nil {
		return manualReviewOutcome{}, stepErr
	}
	return outcome, nil
}

type reviewCandidateSnapshot struct {
	workdir   string
	status    []byte
	patch     []byte
	untracked []reviewSnapshotFile
}

type reviewSnapshotFile struct {
	path          string
	mode          fs.FileMode
	data          []byte
	symlinkTarget string
	isSymlink     bool
}

func captureReviewCandidateSnapshot(ctx context.Context, workdir string) (reviewCandidateSnapshot, error) {
	status, err := reviewCandidateStatus(ctx, workdir)
	if err != nil {
		return reviewCandidateSnapshot{}, err
	}
	patch, err := gitCombinedOutput(ctx, workdir, "diff", "--binary", "--no-ext-diff")
	if err != nil {
		return reviewCandidateSnapshot{}, fmt.Errorf("capture tracked diff: %w: %s", err, strings.TrimSpace(string(patch)))
	}
	untracked, err := captureUntrackedReviewFiles(ctx, workdir)
	if err != nil {
		return reviewCandidateSnapshot{}, err
	}
	return reviewCandidateSnapshot{
		workdir:   workdir,
		status:    status,
		patch:     patch,
		untracked: untracked,
	}, nil
}

func reviewCandidateStatus(ctx context.Context, workdir string) ([]byte, error) {
	status, err := gitCombinedOutput(ctx, workdir, "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	if err != nil {
		return nil, fmt.Errorf("read candidate status: %w: %s", err, strings.TrimSpace(string(status)))
	}
	return status, nil
}

func captureUntrackedReviewFiles(ctx context.Context, workdir string) ([]reviewSnapshotFile, error) {
	output, err := gitCombinedOutput(ctx, workdir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("list untracked candidate files: %w: %s", err, strings.TrimSpace(string(output)))
	}
	paths := splitNUL(output)
	files := make([]reviewSnapshotFile, 0, len(paths))
	for _, path := range paths {
		file, err := captureReviewSnapshotFile(workdir, path)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func captureReviewSnapshotFile(workdir string, path string) (reviewSnapshotFile, error) {
	fullPath := filepath.Join(workdir, filepath.FromSlash(path))
	info, err := os.Lstat(fullPath)
	if err != nil {
		return reviewSnapshotFile{}, fmt.Errorf("snapshot untracked file %q: %w", path, err)
	}
	file := reviewSnapshotFile{
		path: path,
		mode: info.Mode(),
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(fullPath)
		if err != nil {
			return reviewSnapshotFile{}, fmt.Errorf("snapshot untracked symlink %q: %w", path, err)
		}
		file.symlinkTarget = target
		file.isSymlink = true
		return file, nil
	}
	if !info.Mode().IsRegular() {
		return reviewSnapshotFile{}, fmt.Errorf("snapshot untracked file %q: unsupported mode %s", path, info.Mode())
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return reviewSnapshotFile{}, fmt.Errorf("snapshot untracked file %q: %w", path, err)
	}
	file.data = data
	return file, nil
}

func splitNUL(output []byte) []string {
	if len(output) == 0 {
		return nil
	}
	parts := bytes.Split(output, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		paths = append(paths, string(part))
	}
	return paths
}

func restoreReviewCandidateIfMutated(ctx context.Context, snapshot reviewCandidateSnapshot) error {
	current, err := reviewCandidateStatus(ctx, snapshot.workdir)
	if err != nil {
		return err
	}
	if bytes.Equal(current, snapshot.status) {
		return nil
	}
	if err := restoreReviewCandidateSnapshot(ctx, snapshot); err != nil {
		return fmt.Errorf(
			"review step mutated candidate changes and automatic restore failed: %w; "+
				"manual repair required in %q: inspect `git status --short`, restore the intended candidate changes, then rerun `orpheus task review`",
			err,
			snapshot.workdir,
		)
	}
	restored, err := reviewCandidateStatus(ctx, snapshot.workdir)
	if err != nil {
		return err
	}
	if !bytes.Equal(restored, snapshot.status) {
		return fmt.Errorf(
			"review step mutated candidate changes and automatic restore did not return the worktree to the pre-step snapshot; "+
				"manual repair required in %q: inspect `git status --short`, restore the intended candidate changes, then rerun `orpheus task review`",
			snapshot.workdir,
		)
	}
	return errors.New("review step mutated candidate changes; restored the pre-step snapshot and marked review failed")
}

func restoreReviewCandidateSnapshot(ctx context.Context, snapshot reviewCandidateSnapshot) error {
	if output, err := gitCombinedOutput(ctx, snapshot.workdir, "reset", "--mixed", "HEAD", "--"); err != nil {
		return fmt.Errorf("reset Git index: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := gitCombinedOutput(ctx, snapshot.workdir, "clean", "-fd", "--"); err != nil {
		return fmt.Errorf("remove new untracked files: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := gitCombinedOutput(ctx, snapshot.workdir, "restore", "--worktree", "--source=HEAD", "--", "."); err != nil {
		return fmt.Errorf("restore tracked files from HEAD: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if len(bytes.TrimSpace(snapshot.patch)) > 0 {
		output, err := gitCombinedOutputWithInput(
			ctx,
			snapshot.workdir,
			snapshot.patch,
			"apply",
			"--binary",
			"--whitespace=nowarn",
		)
		if err != nil {
			return fmt.Errorf("reapply tracked candidate patch: %w: %s", err, strings.TrimSpace(string(output)))
		}
	}
	for _, file := range snapshot.untracked {
		if err := restoreReviewSnapshotFile(snapshot.workdir, file); err != nil {
			return err
		}
	}
	return nil
}

func restoreReviewSnapshotFile(workdir string, file reviewSnapshotFile) error {
	fullPath := filepath.Join(workdir, filepath.FromSlash(file.path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("restore untracked file %q: %w", file.path, err)
	}
	if file.isSymlink {
		if err := os.Symlink(file.symlinkTarget, fullPath); err != nil {
			return fmt.Errorf("restore untracked symlink %q: %w", file.path, err)
		}
		return nil
	}
	if err := os.WriteFile(fullPath, file.data, file.mode.Perm()); err != nil {
		return fmt.Errorf("restore untracked file %q: %w", file.path, err)
	}
	return nil
}

type manualReviewResult int

const (
	manualReviewApproved manualReviewResult = iota
	manualReviewBlocked
	manualReviewAborted
)

func runManualReviewStep(
	command *cobra.Command,
	store taskstate.Store,
	resolvedCtx resolvedTaskContext,
	review taskstate.ReviewAttempt,
) (manualReviewOutcome, error) {
	if err := renderManualReviewContext(command, store, resolvedCtx); err != nil {
		return manualReviewOutcome{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
	}
	return runManualReviewPrompt(command, store, resolvedCtx, review)
}

func renderManualReviewContext(
	command *cobra.Command,
	store taskstate.Store,
	resolvedCtx resolvedTaskContext,
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

	workdir, err := taskWorkingDirectory(repo, taskItem)
	if err != nil {
		return err
	}
	status, err := gitOutput(command.Context(), workdir, "status", "--short")
	if err != nil {
		return fmt.Errorf("read git status: %w", err)
	}
	diffStat, err := gitOutput(command.Context(), workdir, "diff", "--stat")
	if err != nil {
		return fmt.Errorf("read git diff stat: %w", err)
	}

	if _, err := fmt.Fprintf(output, "Task: %s - %s\n", taskItem.ID, taskItem.Title); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "Latest completion: %s\n\n", strings.TrimSpace(latest.Completion.Summary)); err != nil {
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
	if _, err := fmt.Fprintln(output, "\ngit diff --stat:"); err != nil {
		return err
	}
	if strings.TrimSpace(diffStat) == "" {
		_, err = fmt.Fprintln(output, "(no diff)")
		return err
	}
	_, err = fmt.Fprint(output, diffStat)
	return err
}

func runManualReviewPrompt(
	command *cobra.Command,
	store taskstate.Store,
	resolvedCtx resolvedTaskContext,
	review taskstate.ReviewAttempt,
) (manualReviewOutcome, error) {
	reader := bufio.NewReader(command.InOrStdin())
	session := manualReviewSession{
		command:     command,
		store:       store,
		resolvedCtx: resolvedCtx,
		review:      review,
	}
	for {
		action, err := promptManualReviewAction(command, reader)
		if err != nil {
			return manualReviewOutcome{}, fmt.Errorf("task review %s: %w", resolvedCtx.Resolved.TaskID, err)
		}

		result, done, err := session.handleManualReviewAction(action, reader)
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
}

func (s manualReviewSession) handleManualReviewAction(
	action string,
	reader *bufio.Reader,
) (manualReviewOutcome, bool, error) {
	switch action {
	case "a", "approve":
		return s.approve()
	case "b", "block":
		return s.block(reader)
	case "v", "advisory":
		return s.recordFinding(reader, taskstate.FindingTypeAdvisory, "advisory")
	case "t", "task":
		return s.recordFinding(reader, taskstate.FindingTypeSeparateTask, "separate-task")
	case "q", "abort":
		return s.abort()
	default:
		err := s.writeInvalidAction()
		return manualReviewOutcome{}, false, err
	}
}

func (s manualReviewSession) approve() (manualReviewOutcome, bool, error) {
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

func (s manualReviewSession) abort() (manualReviewOutcome, bool, error) {
	_, err := fmt.Fprintf(s.command.ErrOrStderr(), "Review aborted for %s.\n", s.resolvedCtx.Resolved.TaskID)
	return manualReviewOutcome{
		result: manualReviewAborted,
		status: taskstate.ReviewStatusAborted,
	}, true, err
}

func (s manualReviewSession) writeInvalidAction() error {
	_, err := fmt.Fprintln(s.command.ErrOrStderr(), "Choose approve, block, advisory, task, or abort.")
	return err
}

func promptManualReviewAction(command *cobra.Command, reader *bufio.Reader) (string, error) {
	if _, err := fmt.Fprint(command.ErrOrStderr(), "\nReview action [a=approve, b=block, v=advisory, t=task, q=abort]: "); err != nil {
		return "", err
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("read review action: %w", err)
	}
	return strings.ToLower(strings.TrimSpace(line)), nil
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
		taskProposal, err := promptReviewLine(command, reader, "Separate task proposal")
		if err != nil {
			return taskstate.ReviewFinding{}, err
		}
		finding.TaskProposal = taskProposal
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

func gitCombinedOutputWithInput(ctx context.Context, dir string, input []byte, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Stdin = bytes.NewReader(input)
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
		RunStore:   taskstate.NewStore(paths),
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
		RunStore:   taskstate.NewStore(paths),
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
		Prompt:      prompt,
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
		Prompt:      prompt,
		SessionName: sessionName,
	})
	if err != nil {
		return "", agent.CommandSnapshot{}, fmt.Errorf("resolve agent profile: %w", err)
	}
	return prompt, commandSnapshot, nil
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
			"Synced %s: PR %s is still open for review. No backend changes were made.\n",
			result.Task.ID,
			result.PRURL,
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
		_, err := fmt.Fprintf(output, "%sPR %s is still open for review\n", prefix, result.PRURL)
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
				ID:            repo.ID,
				Name:          repo.Name,
				TaskIDPrefix:  repo.BeadsPrefix,
				Path:          repo.Path,
				DefaultBranch: repo.DefaultBranch,
				TitleTemplate: repo.TitleTemplate,
			},
			BackendDir: beadsDir,
		})
	}
	return sources, nil
}

func renderTaskDetails(
	output interface{ Write([]byte) (int, error) },
	row taskmodel.RepoTask,
	events []taskstate.Event,
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
	return renderTaskHistory(output, events)
}

func renderTaskHistory(output interface{ Write([]byte) (int, error) }, events []taskstate.Event) error {
	if _, err := fmt.Fprintln(output, "History:"); err != nil {
		return err
	}
	history := make([]taskstate.Event, 0, len(events))
	for _, event := range events {
		if event.Type != taskstate.EventWorktreeReused {
			history = append(history, event)
		}
	}
	if len(history) == 0 {
		_, err := fmt.Fprintln(output, "  -")
		return err
	}

	sort.SliceStable(history, func(i, j int) bool {
		return history[i].At.Before(history[j].At)
	})
	for _, event := range history {
		if _, err := fmt.Fprintf(
			output,
			"  %s %s\n",
			event.At.UTC().Format(time.RFC3339),
			event.DisplayName(),
		); err != nil {
			return err
		}
	}
	return nil
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
