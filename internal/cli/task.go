package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/beads"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/taskstats"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

var (
	newBeadsTaskBackend                           = beads.NewTaskBackend
	attachedAgentLauncher      agentexec.Launcher = agentexec.AttachedLauncher{}
	taskDoneInputIsTerminal                       = readerIsTerminal
	taskReviewOutputIsTerminal                    = writerIsTerminal
)

const taskStatsCostEstimateDisclaimer = "Estimated cost uses harness-reported estimates when available; " +
	"otherwise Orpheus may calculate API-equivalent estimates from recorded token usage and public pricing metadata. " +
	"Costs may not match subscription billing or vendor invoices."

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
	var statsOpts taskStatsOptions
	cmd := &cobra.Command{
		Use:   "stats [<task-id>]",
		Short: "Show implementation execution stats for one task or aggregate analytical views",
		Long: "Show implementation execution stats for one task or aggregate analytical views.\n\n" +
			"Aggregate views use --group day|week|month and --view throughput|implementation|review|consumption|implementation-model|reviewer-model|model-pair. " +
			"Model comparison cohorts include known harness and thinking/default qualifiers. " +
			"Date filters use YYYY-MM-DD boundaries. Repository filters match registered repository id or name.",
		Args: func(command *cobra.Command, args []string) error {
			if !statsOpts.aggregateSelected() {
				return cobra.ExactArgs(1)(command, args)
			}
			if len(args) != 0 {
				return fmt.Errorf("aggregate stats flags cannot be combined with a task id")
			}
			return nil
		},
		RunE: func(command *cobra.Command, args []string) error {
			taskID := ""
			if len(args) == 1 {
				taskID = args[0]
			}
			return runTaskStats(command, opts, taskID, statsOpts)
		},
	}
	cmd.Flags().StringVar(&statsOpts.group, "group", "", "aggregate non-epic task stats by day, week, or month")
	cmd.Flags().StringVar(&statsOpts.view, "view", "", "aggregate view: throughput, implementation, review, consumption, implementation-model, reviewer-model, or model-pair")
	cmd.Flags().StringVar(&statsOpts.from, "from", "", "include aggregate rows anchored on or after YYYY-MM-DD")
	cmd.Flags().StringVar(&statsOpts.to, "to", "", "include aggregate rows anchored before the day after YYYY-MM-DD")
	cmd.Flags().StringArrayVar(&statsOpts.repos, "repo", nil, "limit aggregate stats to a repository id or name; repeatable")
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
			"the effective review pipeline. Automated blockers require an explicit " +
			"keep, downgrade, or waive/cancel decision. Kept blockers run targeted " +
			"fixes and start fresh review attempts until the review passes, fails " +
			"operationally, exhausts reviews.max_autonomous_review_attempts, or needs " +
			"operator input that is no longer available. Manual steps are persisted " +
			"before prompting and can be resumed with task review.",
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
			"name the already selected pipeline and cannot replace it. Automated blockers " +
			"require an explicit keep, downgrade, or waive/cancel decision. Kept blockers " +
			"run bounded targeted fixes and restart the pipeline from step 1 so manual " +
			"gates must pass again. If blocker-decision input disappears, the current " +
			"attempt is blocked and recovery starts a fresh task review.\n\n" +
			"Operational review failures require fixing the review command, environment, " +
			"or process and rerunning task review. Exhausted autonomous blockers stay " +
			"blocked until the operator explicitly continues. Use task review show to " +
			"inspect persisted findings and created follow-up tasks.",
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

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContextFromInvocation(deps)
	if err != nil {
		return err
	}
	logger.DebugContext(command.Context(), "querying task snapshots", slog.Int("repo_count", len(taskCtx.Sources)))

	snapshot := taskCtx.Aggregator.Snapshot(command.Context())
	runStates, runStateFailures := taskRunStateIndex(deps, snapshot)
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

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContextFromInvocation(deps)
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

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	resolvedCtx, err := resolveTaskShowContext(command, deps, taskID)
	if err != nil {
		return err
	}
	taskState, err := deps.taskStateStore.Load(
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
	view  string
	from  string
	to    string
	repos []string
}

func (o taskStatsOptions) aggregateSelected() bool {
	return strings.TrimSpace(o.group) != "" ||
		strings.TrimSpace(o.view) != "" ||
		strings.TrimSpace(o.from) != "" ||
		strings.TrimSpace(o.to) != "" ||
		len(o.repos) > 0
}

func runTaskStats(command *cobra.Command, opts *rootOptions, taskID string, statsOpts taskStatsOptions) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_stats"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task stats")

	if statsOpts.aggregateSelected() {
		return runAggregateTaskStats(command, opts, statsOpts)
	}

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	resolvedCtx, err := resolveTaskShowContext(command, deps, taskID)
	if err != nil {
		return err
	}
	taskState, err := deps.taskStateStore.Load(
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

func runAggregateTaskStats(command *cobra.Command, opts *rootOptions, statsOpts taskStatsOptions) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_stats_aggregate"),
	)
	aggregateOpts, err := parseTaskStatsAggregateOptions(statsOpts)
	if err != nil {
		return err
	}

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContextFromInvocation(deps)
	if err != nil {
		return err
	}

	sources := filterTaskStatsRepositorySources(taskCtx.Sources, statsOpts.repos)
	aggregator, err := taskmodel.NewAggregatorWithLogger(sources, deps.taskBackendFactory, deps.logger)
	if err != nil {
		return err
	}

	logger.DebugContext(
		command.Context(),
		"querying task snapshots for aggregate stats",
		slog.String("group", string(aggregateOpts.Group)),
		slog.String("view", string(aggregateOpts.View)),
		slog.Int("repo_count", len(sources)),
	)
	snapshot := aggregator.Snapshot(command.Context())
	report, stateFailures := taskstats.AggregateReportFromSnapshotWithOptions(
		snapshot,
		deps.taskStateStore,
		aggregateOpts,
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

func parseTaskStatsAggregateOptions(statsOpts taskStatsOptions) (taskstats.AggregateReportOptions, error) {
	group := strings.TrimSpace(statsOpts.group)
	if group == "" {
		group = string(taskstats.GroupDay)
	}
	normalizedGroup, err := taskstats.ParseGroup(group)
	if err != nil {
		return taskstats.AggregateReportOptions{}, err
	}
	normalizedView, err := taskstats.ParseView(statsOpts.view)
	if err != nil {
		return taskstats.AggregateReportOptions{}, err
	}
	rangeStart, rangeEnd, err := parseTaskStatsDateRange(statsOpts.from, statsOpts.to)
	if err != nil {
		return taskstats.AggregateReportOptions{}, err
	}
	return taskstats.AggregateReportOptions{
		Group:        normalizedGroup,
		View:         normalizedView,
		From:         rangeStart,
		To:           rangeEnd,
		Repositories: statsOpts.repos,
	}, nil
}

func filterTaskStatsRepositorySources(
	sources []taskmodel.RepositorySource,
	repositories []string,
) []taskmodel.RepositorySource {
	filter := taskStatsRepositoryFilter{}
	for _, repository := range repositories {
		repository = strings.ToLower(strings.TrimSpace(repository))
		if repository == "" {
			continue
		}
		filter[repository] = struct{}{}
	}
	if len(filter) == 0 {
		return sources
	}

	filtered := make([]taskmodel.RepositorySource, 0, len(sources))
	for _, source := range sources {
		if filter.matches(source.Repository) {
			filtered = append(filtered, source)
		}
	}
	return filtered
}

type taskStatsRepositoryFilter map[string]struct{}

func (f taskStatsRepositoryFilter) matches(repository taskmodel.Repository) bool {
	_, idOK := f[strings.ToLower(strings.TrimSpace(repository.ID))]
	_, nameOK := f[strings.ToLower(strings.TrimSpace(repository.Name))]
	return idOK || nameOK
}

func parseTaskStatsDateRange(from string, to string) (*time.Time, *time.Time, error) {
	var rangeStart *time.Time
	if strings.TrimSpace(from) != "" {
		parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(from), time.UTC)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid --from date %q; expected YYYY-MM-DD", from)
		}
		rangeStart = &parsed
	}

	var rangeEnd *time.Time
	if strings.TrimSpace(to) != "" {
		parsed, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(to), time.UTC)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid --to date %q; expected YYYY-MM-DD", to)
		}
		end := parsed.AddDate(0, 0, 1)
		rangeEnd = &end
	}
	if rangeStart != nil && rangeEnd != nil && !rangeStart.Before(*rangeEnd) {
		return nil, nil, fmt.Errorf("--from must be on or before --to")
	}
	return rangeStart, rangeEnd, nil
}

func runTaskDir(command *cobra.Command, opts *rootOptions, taskID string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_dir"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task dir")

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	resolvedCtx, err := resolveTaskContext(command, deps, "task dir", taskID)
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

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}

	resolvedCtx, err := resolveTaskRunContextFromInvocation(deps, taskID)
	if err != nil {
		return err
	}

	resolved := resolvedCtx.Resolved
	repo := resolvedCtx.RegisteredRepo
	paths := deps.paths

	readBackend, err := deps.taskBackendFactory(resolved.Source)
	if err != nil {
		return fmt.Errorf("task run %s: create backend for repo %s (%s; prefix %s): %w",
			resolved.TaskID,
			resolved.Source.Repository.ID,
			resolved.Source.Repository.Name,
			resolved.Source.Repository.TaskIDPrefix,
			err,
		)
	}
	taskBackend, ok := readBackend.(workflow.DispatchBackend)
	if !ok {
		return fmt.Errorf("task run %s: backend for repo %s does not support dispatch mutations", resolved.TaskID, resolved.Source.Repository.ID)
	}
	if err := validateTaskRunExternalRef(command, resolved, taskBackend); err != nil {
		return err
	}

	dispatch, err := startTaskRunDispatch(command, opts.log(), paths, resolved, taskBackend, agentName, mainMode, repoRootMode)
	if err != nil {
		return fmt.Errorf("task run %s: %w", resolved.TaskID, err)
	}

	logTaskRunLaunch(command, logger, repo.ID, resolved.TaskID, dispatch.start)

	if err := launchTaskRunAgent(command, opts.log(), dispatch.service, repo.ID, resolved.TaskID, dispatch.start, dispatch.prompt); err != nil {
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

	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_run_review"),
	)
	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContextFromInvocation(deps)
	if err != nil {
		return err
	}
	service := newTaskReviewLifecycleService(command, deps.paths, taskCtx, logger, bufio.NewReader(command.InOrStdin()))
	outcome, reviewed, err := service.RunAfterCompletedRun(command.Context(), workflow.ReviewAfterRunCompletionOptions{
		RepoID:                    repoID,
		TaskID:                    taskID,
		RunAttempt:                dispatch.start.Attempt.Attempt,
		SelectedDispatchAgentName: dispatch.start.Command.AgentName,
		FallbackDispatchAgentName: agentName,
		PipelineName:              pipelineName,
	})
	if err != nil {
		return err
	}
	if !reviewed {
		return nil
	}
	return renderTaskReviewLifecycleOutcome(command, logger, outcome)
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
	logger *slog.Logger,
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
			RunStore: taskstate.NewStoreWithLogger(paths, logger),
			Logger:   logger,
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
			return workflow.NewDispatchCommand(commandSnapshot), nil
		},
		ResolveFollowUpCommand: func(commandContext workflow.DispatchCommandContext) (workflow.DispatchCommand, error) {
			prompt, commandSnapshot, err := resolveTaskRunFollowUpAgentCommand(paths, agentName, commandContext.SessionName)
			if err != nil {
				return workflow.DispatchCommand{}, err
			}
			dispatch.prompt = prompt
			return workflow.NewDispatchCommand(commandSnapshot), nil
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
		slog.String("profile", start.Attempt.Execution.Profile),
		slog.String("harness", start.Command.Harness),
		slog.Int("arg_count", len(start.Command.Args)),
		slog.String("execution_dir", start.ExecutionDir),
		slog.String("branch", start.Setup.Branch),
		slog.String("worktree_lifecycle", string(start.Setup.Lifecycle)),
	)
}

func launchTaskRunAgent(
	command *cobra.Command,
	logger *slog.Logger,
	service workflow.DispatchService,
	repoID string,
	taskID string,
	start workflow.DispatchStartResult,
	prompt string,
) error {
	span := logging.Start(command.Context(), logger, "attached agent process",
		taskRunProcessAttrs(repoID, taskID, start)...,
	)
	err := attachedAgentLauncher.Run(command.Context(), agentexec.Command{
		Name:    start.Command.AgentName,
		Command: start.Command.Command,
		Args:    append([]string{}, start.Command.Args...),
	}, agentexec.LaunchOptions{
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
		span.Finish(command.Context(), logging.StatusSuccess,
			slog.String("lifecycle_outcome", "success"),
			slog.Int("exit_code", 0),
		)
		return nil
	}

	span.Finish(command.Context(), taskRunProcessStatus(command.Context(), err), taskRunProcessOutcomeAttrs(command.Context(), err)...)
	recordErr := service.Fail(workflow.DispatchFailureOptions{
		RepoID:      repoID,
		TaskID:      taskID,
		Attempt:     start.Attempt.Attempt,
		Cause:       err,
		StartFailed: agentexec.IsStartError(err),
	})
	if recordErr != nil {
		return fmt.Errorf("task run %s: %w; additionally failed to record run failure: %w", taskID, err, recordErr)
	}
	return fmt.Errorf("task run %s: %w", taskID, err)
}

func taskRunProcessAttrs(repoID string, taskID string, start workflow.DispatchStartResult) []slog.Attr {
	return []slog.Attr{
		slog.String("component", "cli"),
		slog.String("operation", "task_run_agent_process"),
		slog.String("repo_id", repoID),
		slog.String("task_id", taskID),
		slog.Int("attempt", start.Attempt.Attempt),
		slog.String("purpose", string(start.Attempt.Execution.Purpose)),
		slog.String("agent", start.Command.AgentName),
		slog.String("profile", start.Attempt.Execution.Profile),
		slog.String("harness", start.Command.Harness),
		slog.String("execution_dir", start.ExecutionDir),
	}
}

func taskRunProcessStatus(ctx context.Context, err error) string {
	if taskRunProcessCanceled(ctx, err) {
		return "canceled"
	}
	return logging.StatusFailure
}

func taskRunProcessOutcomeAttrs(ctx context.Context, err error) []slog.Attr {
	outcome := "runtime_failure"
	if taskRunProcessCanceled(ctx, err) {
		outcome = "canceled"
	} else if agentexec.IsStartError(err) {
		outcome = "start_failure"
	} else if _, ok := logging.ExitCode(err); ok {
		outcome = "nonzero_exit"
	}
	attrs := []slog.Attr{slog.String("lifecycle_outcome", outcome)}
	if exitCode, ok := logging.ExitCode(err); ok {
		attrs = append(attrs, slog.Int("exit_code", exitCode))
	}
	return attrs
}

func taskRunProcessCanceled(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil
}

func recordTaskRunUsage(
	command *cobra.Command,
	service workflow.DispatchService,
	repoID string,
	taskID string,
	start workflow.DispatchStartResult,
) error {
	usageOpts := taskRunUsageOptions(command, repoID, taskID, start, service.Logger)
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

func taskRunUsageOptions(
	command *cobra.Command,
	repoID string,
	taskID string,
	start workflow.DispatchStartResult,
	logger *slog.Logger,
) taskstate.RecordRunUsageOptions {
	return agent.CaptureUsage(agent.UsageCaptureOptions{
		Harness:      start.Attempt.Execution.Harness,
		ExecutionDir: start.ExecutionDir,
		SessionName:  start.Attempt.Execution.SessionName,
		StartedAt:    start.Attempt.Execution.StartedAt,
		Env:          agent.UsageCaptureEnvironment(),
		Logger:       logger,
		Context:      command.Context(),
		RepoID:       repoID,
		TaskID:       taskID,
		Attempt:      start.Attempt.Attempt,
	})
}

func runTaskReview(command *cobra.Command, opts *rootOptions, taskID string, pipelineName string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_review"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task review")

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContextFromInvocation(deps)
	if err != nil {
		return err
	}
	service := newTaskReviewLifecycleService(command, deps.paths, taskCtx, logger, bufio.NewReader(command.InOrStdin()))
	outcome, err := service.Run(command.Context(), workflow.ReviewLifecycleOptions{
		TaskID:       taskID,
		PipelineName: pipelineName,
	})
	if err != nil {
		return err
	}
	return renderTaskReviewLifecycleOutcome(command, logger, outcome)
}

func renderTaskReviewLifecycleOutcome(
	command *cobra.Command,
	logger *slog.Logger,
	outcome workflow.ReviewLifecycleOutcome,
) error {
	if outcome.Kind != workflow.ReviewLifecycleOutcomePassed {
		return nil
	}
	logger.DebugContext(
		command.Context(),
		"review approved and finalized task",
		slog.String("repo_id", outcome.Finalization.Repository.ID),
		slog.String("task_id", outcome.Finalization.Task.ID),
		slog.String("commit", outcome.Finalization.Finalization.Commit),
	)
	return renderTaskDoneResult(command, outcome.Finalization)
}

type taskReviewExecutionOptions struct {
	pauseBeforeManual bool
	interactiveManual bool
}

func taskReviewPipelinePresentation(command *cobra.Command,
	start taskReviewStart,
	reviewInput *bufio.Reader,
	logger *slog.Logger,
	execOptions ...taskReviewExecutionOptions,
) workflow.ReviewPipelinePresentation {
	execOpts := taskReviewExecutionOptions{interactiveManual: true}
	if len(execOptions) > 0 {
		execOpts = execOptions[0]
	}
	outputMode := taskReviewOutputMode(command, logger)
	presentation := workflow.ReviewPipelinePresentation{
		Stdout:            outputMode.stdout,
		Stderr:            outputMode.stderr,
		Stdin:             command.InOrStdin(),
		InteractiveOutput: outputMode.interactive,
		OutputWidth:       outputMode.width,
		OutputWidthFunc:   outputMode.widthFunc,
		PauseBeforeManual: execOpts.pauseBeforeManual,
	}
	if !execOpts.interactiveManual {
		return presentation
	}
	return attachInteractiveReviewHooks(command, start, reviewInput, presentation)
}

func attachInteractiveReviewHooks(
	command *cobra.Command,
	start taskReviewStart,
	reviewInput *bufio.Reader,
	presentation workflow.ReviewPipelinePresentation,
) workflow.ReviewPipelinePresentation {
	presentation.RenderManualStep = func(ctx workflow.ReviewManualStepContext) error {
		return renderManualReviewContext(command, ctx)
	}
	presentation.ConfirmManualCommand = func(step review.Step) (bool, error) {
		confirmed, err := promptManualCommandConfirmation(command, reviewInput, step)
		if err != nil {
			if errors.Is(err, review.ErrManualInputUnavailable) {
				return false, renderManualReviewHandoff(command, start.taskID(), step.Name)
			}
			return false, fmt.Errorf("task review %s: %w", start.taskID(), err)
		}
		if confirmed {
			return true, nil
		}
		_, err = fmt.Fprintf(command.ErrOrStderr(), "Review aborted for %s.\n", start.taskID())
		return false, err
	}
	presentation.PromptManualStep = func(prompt workflow.ReviewManualStepPrompt) (review.ManualResult, error) {
		outcome, err := runManualReviewPrompt(
			command,
			reviewInput,
			prompt.Recorder,
			prompt.TaskID(),
			prompt.Step.Name,
			prompt.HunkNotes,
		)
		if err != nil {
			if errors.Is(err, review.ErrManualInputUnavailable) {
				return review.ManualResult{}, renderManualReviewHandoff(command, prompt.TaskID(), prompt.Step.Name)
			}
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
	presentation.PromptAutomatedBlockers = taskReviewAutomatedBlockerPrompt(command, reviewInput)
	return presentation
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
	widthFunc   func() (int, bool)
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
		widthFunc: func() (int, bool) {
			return interactiveTerminalWidth(stderr)
		},
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
	resolvedCtx resolvedTaskContext
	workdir     string
	target      tasktarget.Target
	review      taskstate.ReviewAttempt
	pipeline    review.Pipeline
	resumed     bool
}

func (s taskReviewStart) taskID() string {
	return s.resolvedCtx.Resolved.TaskID
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

func renderManualReviewContext(command *cobra.Command, ctx workflow.ReviewManualStepContext) error {
	output := command.ErrOrStderr()
	taskItem := ctx.Task
	taskState := ctx.TaskState
	completions, err := manualReviewCompletions(taskState)
	if err != nil {
		return err
	}

	status := ctx.GitStatus

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

	if err := renderCurrentManualStepFindings(output, taskState, ctx.Review.Attempt, ctx.Step.Name); err != nil {
		return err
	}
	return renderPriorReviewAdvisories(output, taskState, ctx.Review.Attempt, ctx.Step.Name)
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
	recorder workflow.ReviewManualStepRecorder,
	taskID string,
	stepName string,
	hunkNotes []review.HunkNote,
) (manualReviewOutcome, error) {
	session := manualReviewSession{
		command:  command,
		recorder: recorder,
		taskID:   taskID,
		stepName: stepName,
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
			return manualReviewOutcome{}, fmt.Errorf("task review %s: %w", taskID, err)
		}

		result, done, err := session.handleManualReviewAction(action, reader, actions)
		if err != nil || done {
			return result, err
		}
	}
}

type manualReviewSession struct {
	command  *cobra.Command
	recorder workflow.ReviewManualStepRecorder
	taskID   string
	stepName string
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
			return fmt.Errorf("task review %s: import Hunk note %s: %w", s.taskID, hunkNoteID(note), err)
		}
		if !importNote {
			continue
		}
		if _, err := s.recorder.RecordFinding(finding); err != nil {
			return fmt.Errorf("task review %s: record Hunk note finding: %w", s.taskID, err)
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
		_, err := fmt.Fprintf(s.command.ErrOrStderr(), "Review blocked for %s.\n", s.taskID)
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
	_, err := fmt.Fprintf(s.command.ErrOrStderr(), "Review blocked for %s.\n", s.taskID)
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
		return manualReviewOutcome{}, true, fmt.Errorf("task review %s: %w", s.taskID, err)
	}
	if _, err := s.recorder.RecordFinding(finding); err != nil {
		return manualReviewOutcome{}, true, fmt.Errorf("task review %s: record %s finding: %w", s.taskID, label, err)
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
		return manualReviewOutcome{}, true, fmt.Errorf("task review %s: %w", s.taskID, err)
	}
	if len(indexes) == 0 {
		return manualReviewOutcome{}, false, nil
	}
	for _, index := range indexes {
		if _, err := s.recorder.PromoteAdvisoryFinding(index); err != nil {
			return manualReviewOutcome{}, true, fmt.Errorf("task review %s: promote advisory finding %d: %w", s.taskID, index+1, err)
		}
		if _, err := fmt.Fprintf(s.command.ErrOrStderr(), "Promoted advisory finding %d to blocking.\n", index+1); err != nil {
			return manualReviewOutcome{}, true, err
		}
	}
	return manualReviewOutcome{}, false, nil
}

func (s manualReviewSession) loadLatestReview() (taskstate.ReviewAttempt, error) {
	latest, err := s.recorder.LatestReview()
	if err != nil {
		return taskstate.ReviewAttempt{}, fmt.Errorf("task review %s: %w", s.taskID, err)
	}
	return latest, nil
}

func (s manualReviewSession) abort() (manualReviewOutcome, bool, error) {
	_, err := fmt.Fprintf(s.command.ErrOrStderr(), "Review aborted for %s.\n", s.taskID)
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
			return taskstate.ReviewFinding{}, false, manualInputReadError("read Hunk note classification", err)
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
		return "", manualInputReadError("read review action", err)
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
		line, err := readReviewLineWithReadError(
			reader,
			"read automated blocker decision",
			automatedBlockerInputReadError,
		)
		if err != nil {
			return review.AutomatedBlockerDecision{}, err
		}

		switch strings.ToLower(strings.TrimSpace(line)) {
		case "", "k", "keep":
			return keepAutomatedBlockerDecision(blocker), nil
		case "d", "downgrade", "advisory":
			reason, err := promptRequiredAutomatedBlockerReason(command, reader, "Downgrade reason")
			if err != nil {
				return review.AutomatedBlockerDecision{}, err
			}
			return review.AutomatedBlockerDecision{
				FindingIndex: blocker.Index,
				Action:       review.AutomatedBlockerActionDowngrade,
				Reason:       reason,
			}, nil
		case "w", "waive", "c", "cancel":
			reason, err := promptRequiredAutomatedBlockerReason(command, reader, "Waiver reason")
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

func promptRequiredAutomatedBlockerReason(command *cobra.Command, reader *bufio.Reader, label string) (string, error) {
	return promptRequiredReviewReasonWithReadError(command, reader, label, automatedBlockerInputReadError)
}

func promptRequiredReviewReasonWithReadError(
	command *cobra.Command,
	reader *bufio.Reader,
	label string,
	readError func(string, error) error,
) (string, error) {
	for {
		reason, err := promptReviewLineWithReadError(command, reader, label, readError)
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

func renderCurrentManualStepFindings(
	output io.Writer,
	taskState taskstate.TaskState,
	reviewAttempt int,
	currentStep string,
) error {
	latest, ok := taskstate.LatestReview(taskState)
	if !ok || latest.Attempt != reviewAttempt {
		return nil
	}
	findings := currentManualStepFindings(latest, currentStep)
	if len(findings) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(output, "Recorded findings for this manual step:"); err != nil {
		return err
	}
	for _, finding := range findings {
		if err := renderManualStepFinding(output, finding); err != nil {
			return err
		}
	}
	return nil
}

func currentManualStepFindings(
	reviewAttempt taskstate.ReviewAttempt,
	currentStep string,
) []indexedReviewFinding {
	currentStep = strings.TrimSpace(currentStep)
	findings := make([]indexedReviewFinding, 0)
	for index, finding := range reviewAttempt.Findings {
		if strings.TrimSpace(finding.Step) != currentStep {
			continue
		}
		findings = append(findings, indexedReviewFinding{index: index, finding: finding})
	}
	return findings
}

func renderManualStepFinding(output io.Writer, indexed indexedReviewFinding) error {
	finding := indexed.finding
	if _, err := fmt.Fprintf(
		output,
		"  Finding %d (%s): %s\n",
		indexed.index+1,
		formatReviewValue(string(finding.Type)),
		formatReviewValue(finding.Title),
	); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "    Description: %s\n", formatReviewValue(finding.Description)); err != nil {
		return err
	}
	if strings.TrimSpace(finding.SuggestedAction) != "" {
		if _, err := fmt.Fprintf(output, "    Suggested action: %s\n", finding.SuggestedAction); err != nil {
			return err
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
		return nil, manualInputReadError("read advisory promotion selection", err)
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
			return false, manualInputReadError("read manual command confirmation", err)
		}
		if !errors.Is(err, io.EOF) {
			return false, manualInputReadError("read manual command confirmation", err)
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
	return promptReviewLineWithReadError(command, reader, label, manualInputReadError)
}

func promptReviewLineWithReadError(
	command *cobra.Command,
	reader *bufio.Reader,
	label string,
	readError func(string, error) error,
) (string, error) {
	if _, err := fmt.Fprintf(command.ErrOrStderr(), "%s: ", label); err != nil {
		return "", err
	}
	return readReviewLineWithReadError(reader, "read "+strings.ToLower(label), readError)
}

func readReviewLineWithReadError(
	reader *bufio.Reader,
	operation string,
	readError func(string, error) error,
) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && line != "" {
			return strings.TrimSpace(line), nil
		}
		return "", readError(operation, err)
	}
	return strings.TrimSpace(line), nil
}

func manualInputReadError(operation string, err error) error {
	return fmt.Errorf("%s: %w: %w", operation, err, review.ErrManualInputUnavailable)
}

func automatedBlockerInputReadError(operation string, err error) error {
	return fmt.Errorf("%s: %w: %w", operation, err, review.ErrAutomatedBlockerInputUnavailable)
}

func renderManualReviewHandoff(command *cobra.Command, taskID string, stepName string) error {
	_, err := fmt.Fprintf(
		command.ErrOrStderr(),
		"\nReview for %s is waiting for manual step %q because manual review input is unavailable. "+
			"Resume with `orpheus task review %s`.\n",
		taskID,
		stepName,
		taskID,
	)
	if err != nil {
		return err
	}
	return review.ErrManualInputUnavailable
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

	service := newTaskFinalizationService(paths, taskCtx, logger)
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

func newTaskFinalizationService(paths state.Paths, taskCtx taskContext, logger *slog.Logger) workflow.FinalizationService {
	return workflow.FinalizationService{
		Paths:   paths,
		Sources: taskCtx.Sources,
		BackendFactory: func(source taskmodel.RepositorySource) (workflow.FinalizationBackend, error) {
			return newDiagnosticBeadsTaskBackend(source, logger)
		},
		RunStore:   taskstate.NewStoreWithLogger(paths, logger),
		PRProvider: pullrequest.GHProvider{Logger: logger},
		Logger:     logger,
	}
}

func newDiagnosticBeadsTaskBackend(source taskmodel.RepositorySource, logger *slog.Logger) (beads.TaskBackend, error) {
	return beads.NewTaskBackendWithRunner(source.BackendDir, beads.CommandRunner{
		Logger: logger,
		DiagnosticAttrs: []slog.Attr{
			slog.String("repo_id", source.Repository.ID),
		},
	})
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
	return confirmRunningCompletionFinalizationWithReader(command, confirmation, nil)
}

func confirmRunningCompletionFinalizationWithReader(
	command *cobra.Command,
	confirmation workflow.RunningCompletionConfirmation,
	reader *bufio.Reader,
) (bool, error) {
	input := command.InOrStdin()
	if !taskDoneInputIsTerminal(input) {
		return false, nil
	}
	if reader == nil {
		reader = bufio.NewReader(input)
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

	line, err := reader.ReadString('\n')
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
			return newDiagnosticBeadsTaskBackend(source, logger)
		},
		RunStore: taskstate.NewStoreWithLogger(paths, logger),
		ConflictResolver: syncConflictAgentResolver{
			paths:    paths,
			stdout:   command.OutOrStdout(),
			stderr:   command.ErrOrStderr(),
			launcher: attachedAgentLauncher,
		},
		PRProvider: pullrequest.GHProvider{Logger: logger},
		Logger:     logger,
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
		slog.String("sync_status", string(result.Status)),
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
			return newDiagnosticBeadsTaskBackend(source, logger)
		},
		ScanFactory: func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
			return newDiagnosticBeadsTaskBackend(source, logger)
		},
		RunStore: taskstate.NewStoreWithLogger(paths, logger),
		ConflictResolver: syncConflictAgentResolver{
			paths:    paths,
			stdout:   command.OutOrStdout(),
			stderr:   command.ErrOrStderr(),
			launcher: attachedAgentLauncher,
		},
		PRProvider: pullrequest.GHProvider{Logger: logger},
		Logger:     logger,
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
	return commandSnapshot.Prompt, commandSnapshot, nil
}

func resolveTaskRunFollowUpAgentCommand(paths state.Paths, agentName string, sessionName string) (string, agent.CommandSnapshot, error) {
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
	return commandSnapshot.Prompt, commandSnapshot, nil
}

type syncConflictAgentResolver struct {
	paths    state.Paths
	stdout   io.Writer
	stderr   io.Writer
	launcher agentexec.Launcher
}

func (r syncConflictAgentResolver) PrepareSyncConflictResolution(
	_ context.Context,
	opts workflow.SyncConflictResolutionOptions,
) (workflow.PreparedSyncConflictResolution, error) {
	agentConfig, err := agent.LoadConfig(r.paths)
	if err != nil {
		return workflow.PreparedSyncConflictResolution{}, err
	}
	sessionName := syncConflictAgentSessionName(opts.Task.ID)
	commandSnapshot, err := agentConfig.ResolveSyncConflictResolverCommand(agent.InterpolationValues{
		SessionName: sessionName,
	})
	if err != nil {
		return workflow.PreparedSyncConflictResolution{}, fmt.Errorf("resolve conflict agent profile: %w", err)
	}

	launcher := r.launcher
	if launcher == nil {
		launcher = agentexec.AttachedLauncher{}
	}
	selection := commandSnapshot.AgentSelection()
	return workflow.PreparedSyncConflictResolution{
		Execution: taskstate.AgentExecution{
			Purpose:     taskstate.AgentExecutionPurposeSyncConflictResolution,
			Status:      taskstate.RunStatusRunning,
			Agent:       commandSnapshot.AgentName,
			Profile:     commandSnapshot.AgentName,
			Harness:     selection.Harness,
			Model:       selection.Model,
			Thinking:    selection.Thinking,
			Command:     commandSnapshot.Command,
			Args:        append([]string{}, commandSnapshot.Args...),
			SessionName: sessionName,
		},
		Resolve: func(ctx context.Context) error {
			return launcher.Run(ctx, commandSnapshot.ExecCommand(), agentexec.LaunchOptions{
				Dir: opts.Worktree,
				Env: syncConflictAgentEnvironment(
					opts.Repository.ID,
					opts.Task.ID,
					opts.Worktree,
					opts.Branch,
					commandSnapshot.Prompt,
					opts.ConflictFiles,
				),
				Stdin:  strings.NewReader(""),
				Stdout: r.stdout,
				Stderr: r.stderr,
			})
		},
		CaptureUsage: syncConflictAgentUsageOptions(commandSnapshot, opts.Worktree),
	}, nil
}

func syncConflictAgentUsageOptions(
	command agent.CommandSnapshot,
	worktree string,
) func(taskstate.AgentExecution, error) taskstate.RecordRunUsageOptions {
	return func(execution taskstate.AgentExecution, runErr error) taskstate.RecordRunUsageOptions {
		if agentexec.IsStartError(runErr) {
			return taskstate.RecordRunUsageOptions{
				UsageCapture: taskstate.AgentUsageCapture{
					Status: taskstate.UsageCaptureUnknown,
					Reason: "agent process failed before usage capture",
				},
			}
		}
		return agent.CaptureUsage(agent.UsageCaptureOptions{
			Harness:      command.Harness,
			ExecutionDir: worktree,
			SessionName:  execution.SessionName,
			StartedAt:    execution.StartedAt,
			Env:          agent.UsageCaptureEnvironment(),
		})
	}
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

	switch tasktarget.ClassifyRunTarget(repo, metadata.Branch, worktree) {
	case tasktarget.TargetMainSolo:
		return cleanTaskRunPath(repo.Path), nil
	case tasktarget.TargetWorktreeTeam:
		return worktree, nil
	case tasktarget.TargetRepoRootTeam:
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

func resolveTaskContext(command *cobra.Command, deps *invocationDependencies, operation string, taskID string) (resolvedTaskContext, error) {
	return resolveTaskContextWithScope(command, deps, operation, taskID, true)
}

func resolveTaskShowContext(command *cobra.Command, deps *invocationDependencies, taskID string) (resolvedTaskContext, error) {
	return resolveTaskContextWithScope(command, deps, "task show", taskID, false)
}

func resolveTaskContextWithScope(
	command *cobra.Command,
	deps *invocationDependencies,
	operation string,
	taskID string,
	requireActiveItem bool,
) (resolvedTaskContext, error) {
	taskCtx, err := loadTaskContextFromInvocation(deps)
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

	taskItem, err := queryResolvedTask(command, deps, operation, resolved)
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

func resolveTaskRunContextFromInvocation(deps *invocationDependencies, taskID string) (resolvedTaskRunContext, error) {
	taskCtx, err := loadTaskContextFromInvocation(deps)
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

func queryResolvedTask(
	command *cobra.Command,
	deps *invocationDependencies,
	operation string,
	resolved taskmodel.ResolvedTaskSource,
) (taskmodel.Task, error) {
	backend, err := deps.taskBackendFactory(resolved.Source)
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
		if review.AutomatedBlockerDecisionInterrupted {
			display += " (automated blocker decision interrupted)"
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
			"ESTIMATED_COST",
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
			"ESTIMATED_COST",
			"UNKNOWN_USAGE",
			"UNKNOWN_COST",
		},
		taskStatsTotalRows(records),
	)
}

func renderTaskStatsAggregate(output interface{ Write([]byte) (int, error) }, report taskstats.AggregateReport) error {
	if taskStatsAggregateIsModelView(report.View) {
		if _, err := fmt.Fprintf(output, "Task stats %s comparison view\n", report.View); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(output, "Task stats %s view grouped by %s\n", report.View, report.Group); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(output, "Date anchor: %s\n", taskStatsAggregateDateAnchor(report.View)); err != nil {
		return err
	}
	if filter := taskStatsAggregateFilterLine(report); filter != "" {
		if _, err := fmt.Fprintln(output, filter); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(output, taskStatsCostEstimateDisclaimer); err != nil {
		return err
	}
	if warning := taskStatsAggregateUnknownAnchorLine(report); warning != "" {
		if _, err := fmt.Fprintln(output, warning); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(output); err != nil {
		return err
	}

	headers, rows := taskStatsAggregateViewRows(report)
	if err := renderTable(output, headers, rows); err != nil {
		return err
	}
	if taskStatsAggregateIsModelView(report.View) {
		return renderTaskStatsAggregateModelCoverageNotes(output, report.Cohorts)
	}
	return nil
}

func taskStatsAggregateDateAnchor(view taskstats.View) string {
	switch view {
	case taskstats.ViewImplementation:
		return "first implementation launch; implementation duration sums launch-to-agent-done for completed runs"
	case taskstats.ViewReview:
		return "first review activity; repair cycles count blocked reviews followed by implementation runs"
	case taskstats.ViewConsumption:
		return "execution launch; token and cost totals include only known captured values"
	case taskstats.ViewImplementationModel:
		return "first implementation launch; task outcomes use implementation-model cohorts"
	case taskstats.ViewReviewerModel:
		return "first review activity; task outcomes use reviewer-model cohorts"
	case taskstats.ViewModelPair:
		return "first implementation launch; task outcomes use implementation/reviewer model-pair cohorts"
	default:
		return "task resolution; workflow duration is first implementation launch to resolution"
	}
}

func taskStatsAggregateIsModelView(view taskstats.View) bool {
	return view == taskstats.ViewImplementationModel ||
		view == taskstats.ViewReviewerModel ||
		view == taskstats.ViewModelPair
}

func taskStatsAggregateFilterLine(report taskstats.AggregateReport) string {
	filters := make([]string, 0, 3)
	if report.From != nil {
		filters = append(filters, "from="+report.From.Format("2006-01-02"))
	}
	if report.To != nil {
		filters = append(filters, "to="+report.To.AddDate(0, 0, -1).Format("2006-01-02"))
	}
	if len(report.Repos) > 0 {
		filters = append(filters, "repo="+strings.Join(report.Repos, ","))
	}
	if len(filters) == 0 {
		return ""
	}
	return "Filters: " + strings.Join(filters, " ")
}

func taskStatsAggregateUnknownAnchorLine(report taskstats.AggregateReport) string {
	switch report.View {
	case taskstats.ViewImplementation, taskstats.ViewImplementationModel, taskstats.ViewModelPair:
		return fmt.Sprintf("Tasks without implementation launch timestamp: %d", report.TasksWithoutImplementation)
	case taskstats.ViewReview, taskstats.ViewReviewerModel:
		return fmt.Sprintf("Tasks without review activity timestamp: %d", report.TasksWithoutReviewActivity)
	case taskstats.ViewConsumption:
		return fmt.Sprintf("Executions without launch timestamp: %d", report.ExecutionsWithoutStartedAt)
	default:
		return fmt.Sprintf("Tasks without resolved timestamp: %d", report.TasksWithoutResolvedTimestamp)
	}
}

func taskStatsAggregateViewRows(report taskstats.AggregateReport) ([]string, [][]string) {
	switch report.View {
	case taskstats.ViewImplementation:
		return []string{
			"PERIOD",
			"TASKS",
			"AGENT_MEDIAN",
			"AGENT_P75",
			"AGENT_COVERAGE",
			"TOKEN_MEDIAN",
			"TOKEN_COVERAGE",
			"COST_MEDIAN",
			"COST_COVERAGE",
			"FAILURES",
		}, taskStatsAggregateImplementationRows(report.Periods)
	case taskstats.ViewReview:
		return []string{
			"PERIOD",
			"TASKS",
			"REVIEW_MEDIAN",
			"REVIEW_P75",
			"REVIEW_COVERAGE",
			"REPAIR_MEDIAN",
			"REPAIR_COVERAGE",
			"FIRST_PASS",
			"REPAIRED",
			"BLOCKING_FINDINGS",
			"OP_FAIL",
			"ABORTED",
			"PAUSED",
		}, taskStatsAggregateReviewRows(report.Periods)
	case taskstats.ViewConsumption:
		return []string{
			"PERIOD",
			"TASKS",
			"EXECUTIONS",
			"TOTAL_TOKENS",
			"TOKEN_MEDIAN",
			"TOKEN_COVERAGE",
			"TOTAL_COST",
			"COST_MEDIAN",
			"COST_COVERAGE",
		}, taskStatsAggregateConsumptionRows(report.Periods)
	case taskstats.ViewImplementationModel:
		return taskStatsAggregateModelHeaders("IMPLEMENTATION_MODEL"),
			taskStatsAggregateModelRows(report.Cohorts, taskStatsImplementationModelCohortLabel)
	case taskstats.ViewReviewerModel:
		return taskStatsAggregateModelHeaders("REVIEWER_MODEL"),
			taskStatsAggregateModelRows(report.Cohorts, taskStatsReviewerModelCohortLabel)
	case taskstats.ViewModelPair:
		return taskStatsAggregatePairHeaders(), taskStatsAggregatePairRows(report.Cohorts)
	default:
		return []string{
			"PERIOD",
			"RESOLVED",
			"WORKFLOW_MEDIAN",
			"WORKFLOW_P75",
			"WORKFLOW_COVERAGE",
		}, taskStatsAggregateThroughputRows(report.Periods)
	}
}

func taskStatsAggregateThroughputRows(periods []taskstats.AggregatePeriod) [][]string {
	rows := make([][]string, 0, len(periods))
	for _, period := range periods {
		rows = append(rows, []string{
			period.Key,
			strconv.Itoa(period.Resolved),
			formatTaskStatsKnownDuration(period.WorkflowTime.Median, period.WorkflowTime.Known > 0),
			formatTaskStatsKnownDuration(period.WorkflowTime.P75, period.WorkflowTime.Known > 0),
			formatTaskStatsCoverage(period.WorkflowTime.Known, period.WorkflowTime.Samples),
		})
	}
	return rows
}

func taskStatsAggregateImplementationRows(periods []taskstats.AggregatePeriod) [][]string {
	rows := make([][]string, 0, len(periods))
	for _, period := range periods {
		rows = append(rows, []string{
			period.Key,
			strconv.Itoa(period.Tasks),
			formatTaskStatsKnownDuration(period.ImplementationAgentTime.Median, period.ImplementationAgentTime.Known > 0),
			formatTaskStatsKnownDuration(period.ImplementationAgentTime.P75, period.ImplementationAgentTime.Known > 0),
			formatTaskStatsCoverage(period.ImplementationAgentTime.Known, period.ImplementationAgentTime.Samples),
			formatTaskStatsOptionalTokenCount(period.Tokens.Median, period.Tokens.Known > 0),
			formatTaskStatsCoverage(period.Tokens.Known, period.Tokens.Samples),
			formatTaskStatsOptionalCost(period.Cost.MedianMicroUSD, period.Cost.Known > 0),
			formatTaskStatsCoverage(period.Cost.Known, period.Cost.Samples),
			strconv.Itoa(period.ImplementationFails),
		})
	}
	return rows
}

func taskStatsAggregateReviewRows(periods []taskstats.AggregatePeriod) [][]string {
	rows := make([][]string, 0, len(periods))
	for _, period := range periods {
		rows = append(rows, []string{
			period.Key,
			strconv.Itoa(period.Tasks),
			formatTaskStatsKnownDuration(period.ReviewTime.Median, period.ReviewTime.Known > 0),
			formatTaskStatsKnownDuration(period.ReviewTime.P75, period.ReviewTime.Known > 0),
			formatTaskStatsCoverage(period.ReviewTime.Known, period.ReviewTime.Samples),
			formatTaskStatsOptionalTokenCount(period.RepairCycles.Median, period.RepairCycles.Known > 0),
			formatTaskStatsCoverage(period.RepairCycles.Known, period.RepairCycles.Samples),
			strconv.Itoa(period.FirstPassApprovals),
			strconv.Itoa(period.RepairTasks),
			strconv.Itoa(period.BlockingFindings),
			strconv.Itoa(period.OperationalFailures),
			strconv.Itoa(period.AbortedReviews),
			strconv.Itoa(period.PausedReviews),
		})
	}
	return rows
}

func taskStatsAggregateConsumptionRows(periods []taskstats.AggregatePeriod) [][]string {
	rows := make([][]string, 0, len(periods))
	for _, period := range periods {
		rows = append(rows, []string{
			period.Key,
			strconv.Itoa(period.Tasks),
			strconv.Itoa(period.Executions),
			formatTaskStatsTotalTokens(period.Tokens),
			formatTaskStatsOptionalTokenCount(period.Tokens.Median, period.Tokens.Known > 0),
			formatTaskStatsCoverage(period.Tokens.Known, period.Tokens.Samples),
			formatTaskStatsTotalCost(period.Cost),
			formatTaskStatsOptionalCost(period.Cost.MedianMicroUSD, period.Cost.Known > 0),
			formatTaskStatsCoverage(period.Cost.Known, period.Cost.Samples),
		})
	}
	return rows
}

func taskStatsAggregateModelHeaders(cohortHeader string) []string {
	return append([]string{cohortHeader}, taskStatsAggregateModelMetricHeaders()...)
}

func taskStatsAggregatePairHeaders() []string {
	return append([]string{"PAIR"}, taskStatsAggregateModelMetricHeaders()...)
}

func taskStatsAggregateModelMetricHeaders() []string {
	return []string{
		"TASKS",
		"COMPLETION",
		"WORKFLOW",
		"REPAIR_MEDIAN",
		"FIRST_PASS",
		"REPAIRED",
		"BLOCKED_REVIEWS",
		"BLOCKING_FINDINGS",
		"OP_FAIL",
		"TOTAL_TOKENS",
		"TOKEN_MEDIAN",
		"TOTAL_COST",
		"COST_MEDIAN",
	}
}

func taskStatsAggregateModelRows(
	cohorts []taskstats.AggregateModelCohort,
	label func(taskstats.AggregateModelCohort) string,
) [][]string {
	rows := make([][]string, 0, len(cohorts))
	for _, cohort := range cohorts {
		rows = append(rows, append([]string{label(cohort)}, taskStatsAggregateModelMetricRow(cohort)...))
	}
	return rows
}

func taskStatsAggregatePairRows(cohorts []taskstats.AggregateModelCohort) [][]string {
	rows := make([][]string, 0, len(cohorts))
	for _, cohort := range cohorts {
		rows = append(rows, append([]string{cohort.Key}, taskStatsAggregateModelMetricRow(cohort)...))
	}
	return rows
}

func taskStatsAggregateModelMetricRow(cohort taskstats.AggregateModelCohort) []string {
	return []string{
		strconv.Itoa(cohort.Tasks),
		formatTaskStatsKnownDuration(cohort.CompletionTime.Median, cohort.CompletionTime.Known > 0),
		formatTaskStatsKnownDuration(cohort.WorkflowTime.Median, cohort.WorkflowTime.Known > 0),
		formatTaskStatsOptionalTokenCount(cohort.RepairCycles.Median, cohort.RepairCycles.Known > 0),
		strconv.Itoa(cohort.FirstPassApprovals),
		strconv.Itoa(cohort.RepairTasks),
		strconv.Itoa(cohort.BlockedReviews),
		strconv.Itoa(cohort.BlockingFindings),
		strconv.Itoa(cohort.OperationalFailures),
		formatTaskStatsTotalTokens(cohort.Tokens),
		formatTaskStatsOptionalTokenCount(cohort.Tokens.Median, cohort.Tokens.Known > 0),
		formatTaskStatsTotalCost(cohort.Cost),
		formatTaskStatsOptionalCost(cohort.Cost.MedianMicroUSD, cohort.Cost.Known > 0),
	}
}

func renderTaskStatsAggregateModelCoverageNotes(
	output interface{ Write([]byte) (int, error) },
	cohorts []taskstats.AggregateModelCohort,
) error {
	notes := taskStatsAggregateModelCoverageNotes(cohorts)
	if len(notes) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(output); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(output, "Missing data coverage (known/samples; omitted cohorts are complete or have no samples):"); err != nil {
		return err
	}
	for _, note := range notes {
		if _, err := fmt.Fprintln(output, "- "+note); err != nil {
			return err
		}
	}
	return nil
}

func taskStatsAggregateModelCoverageNotes(cohorts []taskstats.AggregateModelCohort) []string {
	notes := make([]string, 0, len(cohorts))
	for _, cohort := range cohorts {
		missing := taskStatsAggregateModelCoverageMissing(cohort)
		if len(missing) == 0 {
			continue
		}
		notes = append(notes, formatTaskStatsField(cohort.Key)+": "+strings.Join(missing, ", "))
	}
	return notes
}

func taskStatsAggregateModelCoverageMissing(cohort taskstats.AggregateModelCohort) []string {
	missing := make([]string, 0, 5)
	if cohort.CompletionTime.Unknown() > 0 {
		missing = append(missing, taskStatsCoverageNote("completion", cohort.CompletionTime.Known, cohort.CompletionTime.Samples))
	}
	if cohort.WorkflowTime.Unknown() > 0 {
		missing = append(missing, taskStatsCoverageNote("workflow", cohort.WorkflowTime.Known, cohort.WorkflowTime.Samples))
	}
	if cohort.RepairCycles.Unknown() > 0 {
		missing = append(missing, taskStatsCoverageNote("repair", cohort.RepairCycles.Known, cohort.RepairCycles.Samples))
	}
	if cohort.Tokens.Unknown() > 0 {
		missing = append(missing, taskStatsCoverageNote("tokens", cohort.Tokens.Known, cohort.Tokens.Samples))
	}
	if cohort.Cost.Unknown() > 0 {
		missing = append(missing, taskStatsCoverageNote("cost", cohort.Cost.Known, cohort.Cost.Samples))
	}
	return missing
}

func taskStatsCoverageNote(label string, known int, samples int) string {
	return fmt.Sprintf("%s %s", label, formatTaskStatsCoverage(known, samples))
}

func taskStatsImplementationModelCohortLabel(cohort taskstats.AggregateModelCohort) string {
	return formatTaskStatsField(cohort.ImplementationModel)
}

func taskStatsReviewerModelCohortLabel(cohort taskstats.AggregateModelCohort) string {
	return formatTaskStatsField(cohort.ReviewerModel)
}

func formatTaskStatsCoverage(known int, samples int) string {
	return fmt.Sprintf("%d/%d", known, samples)
}

func formatTaskStatsTotalTokens(cohort taskstats.IntCohort) string {
	if cohort.Known == 0 && cohort.Total == 0 {
		return "-"
	}
	return formatTaskStatsTokenCount(cohort.Total)
}

func formatTaskStatsTotalCost(cohort taskstats.CostCohort) string {
	if cohort.Known == 0 && cohort.TotalMicroUSD == 0 {
		return "-"
	}
	return agent.FormatUsageCostUSD(cohort.TotalMicroUSD)
}

func formatTaskStatsKnownDuration(duration time.Duration, ok bool) string {
	if !ok {
		return "-"
	}
	return formatTaskStatsRoundedDuration(duration)
}

func formatTaskStatsRoundedDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	duration = duration.Round(time.Second)
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
	for _, event := range state.Events {
		if !isTerminalTaskStatsSyncConflictEvent(event) || event.Execution == nil {
			continue
		}
		status := event.Status
		if status == "" {
			status = event.Execution.Status
		}
		records = append(records, taskStatsExecutionRecord{
			activity:  "sync-conflict-resolution",
			attempt:   "-",
			step:      "-",
			status:    string(status),
			execution: *event.Execution,
		})
	}
	return records
}

func isTerminalTaskStatsSyncConflictEvent(event taskstate.Event) bool {
	return event.Type == taskstate.EventSyncConflictFinished ||
		event.Type == taskstate.EventSyncConflictFailed
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
	syncConflictResolution := taskStatsTotals{}
	for _, record := range records {
		switch record.activity {
		case "implementation":
			implementation.add(record.execution)
		case "review-agent":
			review.add(record.execution)
		case "sync-conflict-resolution":
			syncConflictResolution.add(record.execution)
		}
	}
	combined := implementation
	combined.addTotals(review)
	combined.addTotals(syncConflictResolution)
	return [][]string{
		taskStatsTotalRow("implementation", implementation),
		taskStatsTotalRow("review-agent", review),
		taskStatsTotalRow("sync-conflict-resolution", syncConflictResolution),
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
		resolved := agent.ResolveExecutionUsageCost(execution)
		if resolved.Known {
			t.costMicroUSD += resolved.Cost.AmountMicroUSD
		} else {
			t.unknownCost++
		}
		return
	}
	t.unknownUsage++
	t.unknownCost++
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
	return formatTaskStatsRoundedDuration(duration)
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
		return formatTaskStatsRoundedDuration(time.Duration(execution.DurationMillis) * time.Millisecond)
	}
	if execution.FinishedAt == nil || execution.StartedAt.IsZero() {
		return "-"
	}
	duration := execution.FinishedAt.Sub(execution.StartedAt)
	if duration < 0 {
		return "-"
	}
	return formatTaskStatsRoundedDuration(duration)
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
	resolved := agent.ResolveExecutionUsageCost(execution)
	if !resolved.Known {
		return formatTaskStatsUsageCostUnknown(execution, resolved.UnknownReason)
	}
	cost := resolved.Cost
	kind := strings.TrimSpace(cost.Kind)
	if kind == "" {
		kind = agent.UsageCostKindEstimatedAPIEquivalent
	}
	if kind == agent.UsageCostKindPiReportedEstimated {
		source := firstNonEmpty(cost.Pricing.Source, "usage.cost.total")
		notes := strings.TrimSpace(cost.Pricing.Notes)
		if notes == "" {
			notes = "estimate only"
		}
		return fmt.Sprintf(
			"Pi-reported estimated cost=%s kind=%s source=%s note=%s",
			agent.FormatUsageCostUSD(cost.AmountMicroUSD),
			kind,
			formatTaskStatsField(source),
			notes,
		)
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

func formatTaskStatsUsageCostUnknown(execution taskstate.AgentExecution, reason string) string {
	switch reason {
	case agent.UsageCostUnknownPiReportedCostMissing:
		return "unknown: Pi usage.cost.total was not captured"
	case agent.UsageCostUnknownStoredCostInvalid:
		return "unknown: stored usage_cost is incomplete"
	case agent.UsageCostUnknownNoUsage:
		return "unknown: usage was not recorded"
	default:
		return fmt.Sprintf("unknown: no public pricing metadata for model %s", formatTaskStatsField(execution.Model))
	}
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
