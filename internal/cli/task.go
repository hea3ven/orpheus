package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/beads"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
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

const (
	taskRunSetupLockOperation        = "task run setup"
	taskRunFinalizationLockOperation = "task run finalization"
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
		newTaskRunCommand(opts),
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

func newTaskRunCommand(opts *rootOptions) *cobra.Command {
	var agentName string
	var mainMode bool
	cmd := &cobra.Command{
		Use:   "run <task-id>",
		Short: "Run an attached agent for a task",
		Long: "Run an attached agent for a task.\n\n" +
			"By default, Orpheus prepares a deterministic task branch and worktree, " +
			"records the attached run attempt, then runs the configured agent there. " +
			"Use --main to run explicitly from the registered repo root on the " +
			"registered default branch for local/manual review workflows.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskRun(command, opts, args[0], agentName, mainMode)
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "agent profile name to use instead of default_agent")
	cmd.Flags().BoolVar(&mainMode, "main", false, "run from the registered repo root on the registered default branch")
	return cmd
}

func newTaskDoneCommand(opts *rootOptions) *cobra.Command {
	var summary string
	var description string
	cmd := &cobra.Command{
		Use:   "done [<task-id>]",
		Short: "Finalize a reviewed main/solo task",
		Long: "Finalize a reviewed main/solo task.\n\n" +
			"Commits the reviewed repo-root changes, pushes the registered default branch, " +
			"then closes the backend task. Without a task id, the command only infers the task " +
			"when the current directory is exactly a registered repo root with one matching " +
			"main/solo local-ready task.",
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

func newTaskSyncCommand(opts *rootOptions) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "sync [<task-id>]",
		Short: "Sync a task with pull request review",
		Long: "Sync a task with pull request review.\n\n" +
			"Tasks with a recorded PR URL are polled from the PR provider. Tasks without a PR URL " +
			"must be PR-ready before Orpheus pushes the task branch and creates or recovers a PR.",
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

	resolvedCtx, err := resolveTaskContext(command, "task show", taskID)
	if err != nil {
		return err
	}

	logger.DebugContext(
		command.Context(),
		"queried task from resolved repo",
		slog.String("repo_id", resolvedCtx.Resolved.Source.Repository.ID),
		slog.String("task_id", resolvedCtx.Resolved.TaskID),
	)
	return renderTaskDetails(command.OutOrStdout(), taskmodel.RepoTask{
		Repository: resolvedCtx.Resolved.Source.Repository,
		Task:       resolvedCtx.Task,
	})
}

func runTaskRun(command *cobra.Command, opts *rootOptions, taskID string, agentName string, mainMode bool) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_run"),
	)
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

	taskStateStore := taskstate.Service(taskstate.NewStore(paths))
	start, err := startTaskRunAttempt(command, paths, taskStateStore, taskRunStartOptions{
		resolved:  resolved,
		repo:      repo,
		backend:   taskBackend,
		agentName: agentName,
		mainMode:  mainMode,
	})
	if err != nil {
		return fmt.Errorf("task run %s: %w", resolved.TaskID, err)
	}

	logger.DebugContext(
		command.Context(),
		"launching attached agent",
		slog.String("repo_id", repo.ID),
		slog.String("task_id", resolved.TaskID),
		slog.String("agent", start.command.AgentName),
		slog.String("command", start.command.Command),
		slog.Int("arg_count", len(start.command.Args)),
		slog.String("execution_dir", start.executionDir),
		slog.String("branch", start.setup.Branch),
		slog.String("worktree_lifecycle", string(start.setup.Lifecycle)),
	)

	if err := attachedAgentLauncher.Run(command.Context(), start.command, agent.LaunchOptions{
		Dir: start.executionDir,
		Env: taskRunEnvironment(
			repo.ID,
			start.task.ID,
			start.setup.WorktreePath,
			start.setup.Branch,
			start.prompt,
		),
		Stdin:  command.InOrStdin(),
		Stdout: command.OutOrStdout(),
		Stderr: command.ErrOrStderr(),
	}); err != nil {
		if recordErr := recordTaskRunFailure(
			paths,
			taskStateStore,
			repo.ID,
			resolved.TaskID,
			start.attempt.Attempt,
			err,
		); recordErr != nil {
			return fmt.Errorf("task run %s: %w; additionally failed to record run failure: %v", resolved.TaskID, err, recordErr)
		}
		return fmt.Errorf("task run %s: %w", resolved.TaskID, err)
	}

	if err := finishTaskRun(paths, taskStateStore, repo.ID, resolved.TaskID, start.attempt.Attempt); err != nil {
		return fmt.Errorf("task run %s: record run finish: %w", resolved.TaskID, err)
	}
	return nil
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

	store := taskstate.NewStore(paths)
	service := workflow.FinalizationService{
		Paths:   paths,
		Sources: taskCtx.Sources,
		BackendFactory: func(source taskmodel.RepositorySource) (workflow.FinalizationBackend, error) {
			return newBeadsTaskBackend(source.BackendDir)
		},
		RunStore: store,
	}
	finalizeOpts := workflow.FinalizeOptions{
		TaskID:      taskID,
		Summary:     summary,
		Description: description,
	}
	finalized, err := service.Finalize(command.Context(), finalizeOpts)
	if err != nil {
		if confirmation, ok := workflow.RunningCompletionConfirmationFromError(err); ok {
			confirmed, confirmErr := confirmRunningCompletionFinalization(command, confirmation)
			if confirmErr != nil {
				return fmt.Errorf("task done: %w", confirmErr)
			}
			if !confirmed {
				return fmt.Errorf("task done: %w", err)
			}
			finalizeOpts.AllowRunningCompleted = true
			finalized, err = service.Finalize(command.Context(), finalizeOpts)
			if err != nil {
				return fmt.Errorf("task done: %w", err)
			}
		} else {
			return fmt.Errorf("task done: %w", err)
		}
	}

	logger.DebugContext(
		command.Context(),
		"finalized task",
		slog.String("repo_id", finalized.Repository.ID),
		slog.String("task_id", finalized.Task.ID),
		slog.String("commit", finalized.Finalization.Commit),
	)
	_, err = fmt.Fprintf(
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

type taskRunStartOptions struct {
	resolved  taskmodel.ResolvedTaskSource
	repo      registry.Repo
	backend   taskRunBackend
	agentName string
	mainMode  bool
}

type taskRunBackend interface {
	taskmodel.DispatchBackend
	List(ctx context.Context) ([]taskmodel.Task, error)
}

type taskRunStartResult struct {
	setup        gitmeta.TaskWorktreeSetupResult
	command      agent.CommandSnapshot
	attempt      taskstate.RunAttempt
	task         taskmodel.Task
	executionDir string
	prompt       string
}

func startTaskRunAttempt(
	command *cobra.Command,
	paths state.Paths,
	taskStateStore taskstate.Service,
	opts taskRunStartOptions,
) (taskRunStartResult, error) {
	var result taskRunStartResult
	err := state.WithGlobalMutationLock(paths, taskRunSetupLockOperation, func() error {
		if opts.backend == nil {
			return errors.New("task dispatch backend is required")
		}

		taskItem, err := queryTaskFromBackend(command.Context(), "task run", opts.resolved, opts.backend)
		if err != nil {
			return err
		}

		if active, ok, err := taskStateStore.ActiveRun(opts.repo.ID, opts.resolved.TaskID); err != nil {
			return fmt.Errorf("inspect task state: %w", err)
		} else if ok {
			return activeTaskRunError(taskStateStore, opts.repo.ID, opts.resolved.TaskID, active)
		}

		expected, err := expectedTaskRunSetup(paths, opts)
		if err != nil {
			return err
		}
		if err := ensureTaskRunEligible(taskItem, expected, opts.repo, opts.mainMode); err != nil {
			return err
		}
		if opts.mainMode {
			if err := ensureRepoRootRunAvailable(command.Context(), opts.backend, opts.repo, opts.resolved.TaskID, expected); err != nil {
				return err
			}
		}

		executionDir := expected.WorktreePath
		prompt := agent.RenderBootstrapPrompt()
		agentConfig, err := agent.LoadConfig(paths)
		if err != nil {
			return err
		}
		commandSnapshot, err := agentConfig.ResolveCommand(opts.agentName, prompt)
		if err != nil {
			return fmt.Errorf("resolve agent profile: %w", err)
		}

		setup, err := setupTaskRunTarget(command.Context(), paths, opts)
		if err != nil {
			return err
		}

		if err := opts.backend.MarkInProgress(command.Context(), opts.resolved.TaskID, setup.Branch, setup.WorktreePath); err != nil {
			return fmt.Errorf("mark task in progress: %w", err)
		}

		worktreeEvent, err := taskRunWorktreeEvent(setup.Lifecycle)
		if err != nil {
			return err
		}
		if _, err := taskStateStore.RecordWorktreeEvent(
			opts.repo.ID,
			opts.resolved.TaskID,
			worktreeEvent,
			taskstate.WorktreeEventOptions{
				Branch:   setup.Branch,
				Worktree: setup.WorktreePath,
			},
		); err != nil {
			return fmt.Errorf("record worktree event: %w", err)
		}

		attempt, err := taskStateStore.StartRun(opts.repo.ID, opts.resolved.TaskID, taskstate.StartRunOptions{
			Agent:    commandSnapshot.AgentName,
			Command:  commandSnapshot.Command,
			Args:     commandSnapshot.Args,
			Branch:   setup.Branch,
			Worktree: setup.WorktreePath,
		})
		if err != nil {
			if errors.Is(err, taskstate.ErrActiveRun) {
				return fmt.Errorf("%w; M3 cannot reconcile stale attached runs automatically", err)
			}
			return fmt.Errorf("record run start: %w", err)
		}

		result = taskRunStartResult{
			setup:        setup,
			command:      commandSnapshot,
			attempt:      attempt,
			task:         taskItem,
			executionDir: executionDir,
			prompt:       prompt,
		}
		return nil
	})
	if err != nil {
		return taskRunStartResult{}, err
	}
	return result, nil
}

func expectedTaskRunSetup(paths state.Paths, opts taskRunStartOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	if opts.mainMode {
		return gitmeta.ExpectedRepoRoot(repoRootOptions(opts.repo))
	}
	return gitmeta.ExpectedTaskWorktree(taskWorktreeOptions(paths, opts.repo, opts.resolved.TaskID))
}

func setupTaskRunTarget(ctx context.Context, paths state.Paths, opts taskRunStartOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	if opts.mainMode {
		return gitmeta.SetupRepoRoot(ctx, repoRootOptions(opts.repo))
	}
	return gitmeta.SetupTaskWorktree(ctx, taskWorktreeOptions(paths, opts.repo, opts.resolved.TaskID))
}

func taskWorktreeOptions(paths state.Paths, repo registry.Repo, taskID string) gitmeta.TaskWorktreeOptions {
	return gitmeta.TaskWorktreeOptions{
		RepoID:        repo.ID,
		RepoName:      repo.Name,
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
		TaskID:        taskID,
		Paths:         paths,
	}
}

func repoRootOptions(repo registry.Repo) gitmeta.RepoRootOptions {
	return gitmeta.RepoRootOptions{
		RepoID:        repo.ID,
		RepoName:      repo.Name,
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
	}
}

func ensureTaskRunEligible(taskItem taskmodel.Task, expected gitmeta.TaskWorktreeSetupResult, repo registry.Repo, mainMode bool) error {
	metadata := taskItem.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return fmt.Errorf("task %s is not eligible for dispatch: %s is already set", taskItem.ID, taskmodel.MetadataPRURL)
	}

	switch taskItem.Status {
	case taskmodel.StatusOpen:
		if !mainMode && taskRunMetadataMatchesRepoRoot(metadata, repo) {
			return repoRootRetryRequiresMainError(taskItem.ID, metadata)
		}
		return nil
	case taskmodel.StatusInProgress:
		if taskRunMetadataMatches(metadata, expected) {
			return nil
		}
		if !mainMode && taskRunMetadataMatchesRepoRoot(metadata, repo) {
			return repoRootRetryRequiresMainError(taskItem.ID, metadata)
		}

		target := "the deterministic Orpheus branch/worktree"
		if mainMode {
			target = "the registered default branch/repo root"
		}
		return fmt.Errorf(
			"task %s is in_progress but is not tied to %s: %s",
			taskItem.ID,
			target,
			taskRunMetadataMismatchDetail(metadata, expected),
		)
	case taskmodel.StatusClosed:
		return fmt.Errorf("task %s is not eligible for dispatch: task is closed", taskItem.ID)
	default:
		return fmt.Errorf(
			"task %s is not eligible for dispatch: status %s is not open or Orpheus-owned in_progress",
			taskItem.ID,
			formatTaskField(string(taskItem.Status)),
		)
	}
}

func repoRootRetryRequiresMainError(taskID string, metadata taskmodel.OrpheusMetadata) error {
	return fmt.Errorf(
		"task %s is tied to repo-root/default-branch metadata (%s=%q, %s=%q); retry with `orpheus task run --main %s`",
		taskID,
		taskmodel.MetadataBranch,
		metadata.Branch,
		taskmodel.MetadataWorktree,
		metadata.Worktree,
		taskID,
	)
}

func taskRunMetadataMatches(metadata taskmodel.OrpheusMetadata, expected gitmeta.TaskWorktreeSetupResult) bool {
	return metadata.HasBranch && strings.TrimSpace(metadata.Branch) == expected.Branch &&
		metadata.HasWorktree && cleanTaskRunPath(metadata.Worktree) == cleanTaskRunPath(expected.WorktreePath)
}

func taskRunMetadataMatchesRepoRoot(metadata taskmodel.OrpheusMetadata, repo registry.Repo) bool {
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	repoPath := cleanTaskRunPath(repo.Path)
	if defaultBranch == "" || repoPath == "" {
		return false
	}
	return metadata.HasBranch && strings.TrimSpace(metadata.Branch) == defaultBranch &&
		metadata.HasWorktree && cleanTaskRunPath(metadata.Worktree) == repoPath
}

func ensureRepoRootRunAvailable(
	ctx context.Context,
	backend taskRunBackend,
	repo registry.Repo,
	currentTaskID string,
	expected gitmeta.TaskWorktreeSetupResult,
) error {
	tasks, err := backend.List(ctx)
	if err != nil {
		return fmt.Errorf("inspect repo-root/default-branch ownership: %w", err)
	}

	for _, taskItem := range tasks {
		if strings.TrimSpace(taskItem.ID) == currentTaskID || taskItem.Status == taskmodel.StatusClosed {
			continue
		}
		if !taskRunMetadataMatches(taskItem.OrpheusMetadata(), expected) {
			continue
		}
		return fmt.Errorf(
			"repo %s (%s) already has non-closed task %s owning repo-root/default-branch metadata (%s=%q, %s=%q); finish local review or clear that metadata before running another task with --main",
			repo.ID,
			repo.Name,
			taskItem.ID,
			taskmodel.MetadataBranch,
			expected.Branch,
			taskmodel.MetadataWorktree,
			expected.WorktreePath,
		)
	}
	return nil
}

func cleanTaskRunPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func taskRunMetadataMismatchDetail(metadata taskmodel.OrpheusMetadata, expected gitmeta.TaskWorktreeSetupResult) string {
	problems := make([]string, 0, 2)
	if !metadata.HasBranch {
		problems = append(problems, taskmodel.MetadataBranch+" is missing")
	} else if strings.TrimSpace(metadata.Branch) != expected.Branch {
		problems = append(problems, fmt.Sprintf("%s is %q, expected %q", taskmodel.MetadataBranch, metadata.Branch, expected.Branch))
	}

	if !metadata.HasWorktree {
		problems = append(problems, taskmodel.MetadataWorktree+" is missing")
	} else if strings.TrimSpace(metadata.Worktree) != expected.WorktreePath {
		problems = append(problems, fmt.Sprintf("%s is %q, expected %q", taskmodel.MetadataWorktree, metadata.Worktree, expected.WorktreePath))
	}

	if len(problems) == 0 {
		return "metadata does not match"
	}
	return strings.Join(problems, "; ")
}

func renderTaskSyncResult(output interface{ Write([]byte) (int, error) }, result workflow.SyncResult) error {
	switch result.Status {
	case workflow.SyncStatusPRCreated:
		_, err := fmt.Fprintf(
			output,
			"Synced %s: pushed branch %s to origin and created PR %s. Task is in review.\n",
			result.Task.ID,
			result.Branch,
			result.PRURL,
		)
		return err
	case workflow.SyncStatusPRRecovered:
		_, err := fmt.Fprintf(
			output,
			"Synced %s: pushed branch %s to origin and recovered existing PR %s. Task is in review.\n",
			result.Task.ID,
			result.Branch,
			result.PRURL,
		)
		return err
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
			"Skipped %s: %s. PR creation was not attempted.\n",
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

	if err := renderTaskSyncAllGroup(output, "Created/recovered PRs", result.Results, func(syncResult workflow.SyncResult) bool {
		return syncResult.Status == workflow.SyncStatusPRCreated ||
			syncResult.Status == workflow.SyncStatusPRRecovered
	}); err != nil {
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
	case workflow.SyncStatusPRCreated:
		_, err := fmt.Fprintf(output, "%spushed branch %s and created PR %s\n", prefix, result.Branch, result.PRURL)
		return err
	case workflow.SyncStatusPRRecovered:
		_, err := fmt.Fprintf(output, "%spushed branch %s and recovered existing PR %s\n", prefix, result.Branch, result.PRURL)
		return err
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

func activeTaskRunError(
	store taskstate.Service,
	repoID string,
	taskID string,
	active taskstate.RunAttempt,
) error {
	statePath, pathErr := store.Path(repoID, taskID)
	if pathErr != nil {
		statePath = "the per-task Orpheus state file"
	}
	return fmt.Errorf(
		"latest run attempt %d is still running; "+
			"M3 cannot reconcile stale attached runs automatically; "+
			"wait for the attached agent to finish or repair %s manually",
		active.Attempt,
		statePath,
	)
}

func taskRunWorktreeEvent(lifecycle gitmeta.TaskWorktreeLifecycle) (taskstate.EventType, error) {
	switch lifecycle {
	case gitmeta.TaskWorktreeLifecycleCreated:
		return taskstate.EventWorktreeCreated, nil
	case gitmeta.TaskWorktreeLifecycleReused:
		return taskstate.EventWorktreeReused, nil
	case gitmeta.TaskWorktreeLifecycleRecreated:
		return taskstate.EventWorktreeRecreated, nil
	default:
		return "", fmt.Errorf("unknown worktree lifecycle %q", lifecycle)
	}
}

func finishTaskRun(paths state.Paths, store taskstate.Service, repoID string, taskID string, attempt int) error {
	return state.WithGlobalMutationLock(paths, taskRunFinalizationLockOperation, func() error {
		latest, ok, err := store.LatestRun(repoID, taskID)
		if err != nil {
			return err
		}
		if ok && latest.Attempt == attempt && latest.Status == taskstate.RunStatusSucceeded && latest.Completion != nil {
			return nil
		}

		_, err = store.FinishRun(repoID, taskID, attempt, taskstate.RunStatusSucceeded)
		return err
	})
}

func recordTaskRunFailure(
	paths state.Paths,
	store taskstate.Service,
	repoID string,
	taskID string,
	attempt int,
	runErr error,
) error {
	return state.WithGlobalMutationLock(paths, taskRunFinalizationLockOperation, func() error {
		if agent.IsStartError(runErr) {
			_, err := store.FailRunStart(repoID, taskID, attempt, runErr)
			return err
		}
		_, err := store.FinishRun(repoID, taskID, attempt, taskstate.RunStatusFailed)
		return err
	})
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
	if !taskmodel.IsM2TaskViewItem(taskItem) {
		return taskmodel.Task{}, fmt.Errorf(
			"%s %s: item is out of scope for M2 task views; expected an active item, got issue_type=%s status=%s",
			operation,
			resolved.TaskID,
			formatTaskField(string(taskItem.IssueType)),
			formatTaskField(string(taskItem.Status)),
		)
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
			},
			BackendDir: beadsDir,
		})
	}
	return sources, nil
}

func renderTaskDetails(output interface{ Write([]byte) (int, error) }, row taskmodel.RepoTask) error {
	metadata := row.Task.OrpheusMetadata()

	if _, err := fmt.Fprintln(output, "Repository:"); err != nil {
		return err
	}
	if err := renderKeyValue(output, "  ID", row.Repository.ID); err != nil {
		return err
	}
	if err := renderKeyValue(output, "  Name", row.Repository.Name); err != nil {
		return err
	}
	if err := renderKeyValue(output, "  Task prefix", row.Repository.TaskIDPrefix); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(output); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "Task:"); err != nil {
		return err
	}
	for _, field := range []struct {
		label string
		value string
	}{
		{label: "  ID", value: row.Task.ID},
		{label: "  Title", value: row.Task.Title},
		{label: "  Status", value: string(row.Task.Status)},
		{label: "  Priority", value: fmt.Sprintf("%d", row.Task.Priority)},
		{label: "  Type", value: string(row.Task.IssueType)},
		{label: "  Labels", value: formatLabels(row.Task.Labels)},
	} {
		if err := renderKeyValue(output, field.label, field.value); err != nil {
			return err
		}
	}
	for _, field := range []struct {
		label string
		value string
	}{
		{label: "  Description", value: row.Task.Description},
		{label: "  Design", value: row.Task.Design},
		{label: "  Acceptance criteria", value: row.Task.AcceptanceCriteria},
	} {
		if err := renderBlockValue(output, field.label, field.value); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintln(output); err != nil {
		return err
	}
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
		[]string{"REPO_ID", "REPO", "TASK_PREFIX", "TASK_ID", "STATUS", "P", "BRANCH", "WORKTREE", "PR", "TITLE"},
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
