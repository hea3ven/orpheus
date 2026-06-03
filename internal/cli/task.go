package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"text/tabwriter"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/beads"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/spf13/cobra"
)

var (
	newBeadsTaskBackend                  = beads.NewTaskBackend
	attachedAgentLauncher agent.Launcher = agent.AttachedLauncher{}
)

func newTaskCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Inspect tasks across registered repositories",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(newTaskListCommand(opts), newTaskReadyCommand(opts), newTaskShowCommand(opts), newTaskRunCommand(opts))
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
	cmd := &cobra.Command{
		Use:   "run <task-id>",
		Short: "Run an attached agent for a task",
		Long: "Run an attached agent for a task.\n\n" +
			"Orpheus prepares a deterministic task branch and worktree, records " +
			"the attached run attempt, then runs the configured agent there.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskRun(command, opts, args[0], agentName)
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "agent profile name to use instead of default_agent")
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
	rows := status.ReadyRowsWithRunStates(snapshot, runStates)
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

func runTaskRun(command *cobra.Command, opts *rootOptions, taskID string, agentName string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_run"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task run")

	resolvedCtx, err := resolveTaskContext(command, "task run", taskID)
	if err != nil {
		return err
	}

	resolved := resolvedCtx.Resolved
	taskItem := resolvedCtx.Task
	repo := resolvedCtx.RegisteredRepo

	logger.DebugContext(
		command.Context(),
		"queried task from resolved repo",
		slog.String("repo_id", resolved.Source.Repository.ID),
		slog.String("task_id", resolved.TaskID),
	)

	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}

	taskStateStore := taskstate.Service(taskstate.NewStore(paths))
	if active, ok, err := taskStateStore.ActiveRun(repo.ID, resolved.TaskID); err != nil {
		return fmt.Errorf("task run %s: inspect task state: %w", resolved.TaskID, err)
	} else if ok {
		statePath, pathErr := taskStateStore.Path(repo.ID, resolved.TaskID)
		if pathErr != nil {
			statePath = "the per-task Orpheus state file"
		}
		return fmt.Errorf(
			"task run %s: latest run attempt %d is still running; M3 cannot reconcile stale attached runs automatically; wait for the attached agent to finish or repair %s manually",
			resolved.TaskID,
			active.Attempt,
			statePath,
		)
	}

	setup, err := gitmeta.SetupTaskWorktree(command.Context(), gitmeta.TaskWorktreeOptions{
		RepoID:        repo.ID,
		RepoName:      repo.Name,
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
		TaskID:        resolved.TaskID,
		Paths:         paths,
	})
	if err != nil {
		return fmt.Errorf("task run %s: %w", resolved.TaskID, err)
	}

	worktreeEvent, err := taskRunWorktreeEvent(setup.Lifecycle)
	if err != nil {
		return fmt.Errorf("task run %s: %w", resolved.TaskID, err)
	}
	if _, err := taskStateStore.RecordWorktreeEvent(repo.ID, resolved.TaskID, worktreeEvent, taskstate.WorktreeEventOptions{
		Branch:   setup.Branch,
		Worktree: setup.WorktreePath,
	}); err != nil {
		return fmt.Errorf("task run %s: record worktree event: %w", resolved.TaskID, err)
	}

	executionDir := setup.WorktreePath
	prompt := agent.RenderDispatchPrompt(agent.DispatchPromptContext{
		TaskID:                 taskItem.ID,
		TaskTitle:              taskItem.Title,
		TaskDescription:        taskItem.Description,
		TaskAcceptanceCriteria: taskItem.AcceptanceCriteria,
		RepositoryID:           repo.ID,
		RepositoryName:         repo.Name,
		ExecutionDir:           executionDir,
		WorktreePath:           setup.WorktreePath,
		Branch:                 setup.Branch,
	})
	agentConfig, err := agent.LoadConfig(paths)
	if err != nil {
		return err
	}
	commandSnapshot, err := agentConfig.ResolveCommand(agentName, prompt)
	if err != nil {
		return fmt.Errorf("task run %s: resolve agent profile: %w", resolved.TaskID, err)
	}

	logger.DebugContext(
		command.Context(),
		"launching attached agent",
		slog.String("repo_id", repo.ID),
		slog.String("task_id", resolved.TaskID),
		slog.String("agent", commandSnapshot.AgentName),
		slog.String("command", commandSnapshot.Command),
		slog.Int("arg_count", len(commandSnapshot.Args)),
		slog.String("execution_dir", executionDir),
		slog.String("branch", setup.Branch),
		slog.String("worktree_lifecycle", string(setup.Lifecycle)),
	)

	attempt, err := taskStateStore.StartRun(repo.ID, resolved.TaskID, taskstate.StartRunOptions{
		Agent:    commandSnapshot.AgentName,
		Command:  commandSnapshot.Command,
		Args:     commandSnapshot.Args,
		Branch:   setup.Branch,
		Worktree: setup.WorktreePath,
	})
	if err != nil {
		if errors.Is(err, taskstate.ErrActiveRun) {
			return fmt.Errorf("task run %s: %w; M3 cannot reconcile stale attached runs automatically", resolved.TaskID, err)
		}
		return fmt.Errorf("task run %s: record run start: %w", resolved.TaskID, err)
	}

	if err := attachedAgentLauncher.Run(command.Context(), commandSnapshot, agent.LaunchOptions{
		Dir:    executionDir,
		Env:    taskRunEnvironment(repo.ID, taskItem.ID, setup.WorktreePath, setup.Branch, prompt),
		Stdin:  command.InOrStdin(),
		Stdout: command.OutOrStdout(),
		Stderr: command.ErrOrStderr(),
	}); err != nil {
		if recordErr := recordTaskRunFailure(taskStateStore, repo.ID, resolved.TaskID, attempt.Attempt, err); recordErr != nil {
			return fmt.Errorf("task run %s: %w; additionally failed to record run failure: %v", resolved.TaskID, err, recordErr)
		}
		return fmt.Errorf("task run %s: %w", resolved.TaskID, err)
	}

	if _, err := taskStateStore.FinishRun(repo.ID, resolved.TaskID, attempt.Attempt, taskstate.RunStatusSucceeded); err != nil {
		return fmt.Errorf("task run %s: record run finish: %w", resolved.TaskID, err)
	}
	return nil
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

func recordTaskRunFailure(store taskstate.Service, repoID string, taskID string, attempt int, runErr error) error {
	if agent.IsStartError(runErr) {
		_, err := store.FailRunStart(repoID, taskID, attempt, runErr)
		return err
	}
	_, err := store.FinishRun(repoID, taskID, attempt, taskstate.RunStatusFailed)
	return err
}

type resolvedTaskContext struct {
	Resolved       taskmodel.ResolvedTaskSource
	Task           taskmodel.Task
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

	taskItem, err := backend.Get(command.Context(), resolved.TaskID)
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
				ID:           repo.ID,
				Name:         repo.Name,
				TaskIDPrefix: repo.BeadsPrefix,
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
	writer := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	if detailed {
		if _, err := fmt.Fprintln(writer, "REPO_ID\tREPO\tTASK_PREFIX\tTASK_ID\tSTATUS\tP\tBRANCH\tWORKTREE\tPR\tTITLE"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(writer, "REPO\tTASK_ID\tSTATUS\tP\tTITLE"); err != nil {
			return err
		}
	}
	for _, row := range rows {
		if detailed {
			if err := renderDetailedTaskRow(writer, row); err != nil {
				return err
			}
			continue
		}
		if err := renderTaskRow(writer, row); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func renderTaskRow(writer interface{ Write([]byte) (int, error) }, row taskmodel.RepoTask) error {
	_, err := fmt.Fprintf(
		writer,
		"%s\t%s\t%s\t%d\t%s\n",
		sanitizeTableCell(row.Repository.Name),
		sanitizeTableCell(row.Task.ID),
		sanitizeTableCell(string(row.Task.Status)),
		row.Task.Priority,
		sanitizeTableCell(row.Task.Title),
	)
	return err
}

func renderDetailedTaskRow(writer interface{ Write([]byte) (int, error) }, row taskmodel.RepoTask) error {
	metadata := row.Task.OrpheusMetadata()
	_, err := fmt.Fprintf(
		writer,
		"%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
		sanitizeTableCell(row.Repository.ID),
		sanitizeTableCell(row.Repository.Name),
		sanitizeTableCell(row.Repository.TaskIDPrefix),
		sanitizeTableCell(row.Task.ID),
		sanitizeTableCell(string(row.Task.Status)),
		row.Task.Priority,
		sanitizeTableCell(formatMetadataTableCell(metadata.Branch, metadata.HasBranch)),
		sanitizeTableCell(formatMetadataTableCell(metadata.Worktree, metadata.HasWorktree)),
		sanitizeTableCell(formatMetadataTableCell(metadata.PRURL, metadata.HasPRURL)),
		sanitizeTableCell(row.Task.Title),
	)
	return err
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

func sanitizeTableCell(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}
