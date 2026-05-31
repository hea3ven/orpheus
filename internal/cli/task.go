package cli

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"text/tabwriter"

	"github.com/hea3ven/orpheus/internal/beads"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/spf13/cobra"
)

var newBeadsTaskBackend = beads.NewTaskBackend

func newTaskCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Inspect tasks across registered repositories",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(newTaskListCommand(opts), newTaskReadyCommand(opts), newTaskShowCommand(opts))
	return cmd
}

func newTaskListCommand(opts *rootOptions) *cobra.Command {
	var detailed bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List active tasks across registered repositories",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskQuery(command, opts, taskQueryOptions{
				operation:    "task list",
				logOperation: "task_list",
				queryingLog:  "querying active tasks",
				queriedLog:   "queried active tasks",
				detailed:     detailed,
				query: func(ctx context.Context, aggregator taskmodel.Aggregator) taskmodel.QueryResult {
					return aggregator.List(ctx)
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
		Short: "Show a task from its registered repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskShow(command, opts, args[0])
		},
	}
	return cmd
}

func addTaskDetailFlags(cmd *cobra.Command, detailed *bool) {
	cmd.Flags().BoolVar(detailed, "details", false, "show detailed table with repo ids, Beads prefixes, and Orpheus metadata")
	cmd.Flags().BoolVarP(detailed, "long", "l", false, "show detailed table with repo ids, Beads prefixes, and Orpheus metadata")
}

func runTaskReady(command *cobra.Command, opts *rootOptions, detailed bool) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_ready"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task ready")

	store, err := newRegistryStoreFromEnvironment()
	if err != nil {
		return err
	}

	reg, err := store.Load()
	if err != nil {
		return err
	}

	sources, err := taskRepositorySources(store, reg)
	if err != nil {
		return err
	}
	logger.DebugContext(command.Context(), "querying task snapshots", slog.Int("repo_count", len(sources)))

	aggregator, err := taskmodel.NewAggregator(sources, func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		return newBeadsTaskBackend(source.BackendDir)
	})
	if err != nil {
		return err
	}

	snapshot := aggregator.Snapshot(command.Context())
	rows := status.ReadyRows(snapshot)
	logger.DebugContext(
		command.Context(),
		"projected ready tasks",
		slog.Int("row_count", len(rows)),
		slog.Int("failure_count", len(snapshot.Failures)),
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

func runTaskQuery(command *cobra.Command, opts *rootOptions, queryOpts taskQueryOptions) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", queryOpts.logOperation),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task query")

	store, err := newRegistryStoreFromEnvironment()
	if err != nil {
		return err
	}

	reg, err := store.Load()
	if err != nil {
		return err
	}

	sources, err := taskRepositorySources(store, reg)
	if err != nil {
		return err
	}
	logger.DebugContext(command.Context(), queryOpts.queryingLog, slog.Int("repo_count", len(sources)))

	aggregator, err := taskmodel.NewAggregator(sources, func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		return newBeadsTaskBackend(source.BackendDir)
	})
	if err != nil {
		return err
	}

	result := queryOpts.query(command.Context(), aggregator)
	logger.DebugContext(
		command.Context(),
		queryOpts.queriedLog,
		slog.Int("row_count", len(result.Rows)),
		slog.Int("failure_count", len(result.Failures)),
	)

	if err := renderTaskRows(command.OutOrStdout(), result.Rows, queryOpts.detailed); err != nil {
		return err
	}
	if result.HasFailures() {
		writeRepoFailures(command.ErrOrStderr(), queryOpts.operation, result.Failures)
		return partialRepoFailureError{operation: queryOpts.operation, failures: result.Failures}
	}
	return nil
}

func runTaskShow(command *cobra.Command, opts *rootOptions, taskID string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_show"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task show")

	store, err := newRegistryStoreFromEnvironment()
	if err != nil {
		return err
	}

	reg, err := store.Load()
	if err != nil {
		return err
	}

	sources, err := taskRepositorySources(store, reg)
	if err != nil {
		return err
	}

	resolved, err := taskmodel.ResolveTaskSource(sources, taskID)
	if err != nil {
		return err
	}

	logger.DebugContext(
		command.Context(),
		"querying task from resolved repo",
		slog.String("repo_id", resolved.Source.Repository.ID),
		slog.String("task_id", resolved.TaskID),
	)

	backend, err := newBeadsTaskBackend(resolved.Source.BackendDir)
	if err != nil {
		return fmt.Errorf("task show %s: create backend for repo %s (%s; prefix %s): %w",
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
			return fmt.Errorf(
				"task show %s: task was not found in repo %s (%s; prefix %s); check the task id or run `orpheus repo beads-dir %s` to inspect the backend: %w",
				resolved.TaskID,
				resolved.Source.Repository.ID,
				resolved.Source.Repository.Name,
				resolved.Source.Repository.TaskIDPrefix,
				resolved.Source.Repository.ID,
				err,
			)
		}
		return fmt.Errorf("task show %s: query repo %s (%s; prefix %s): %w",
			resolved.TaskID,
			resolved.Source.Repository.ID,
			resolved.Source.Repository.Name,
			resolved.Source.Repository.TaskIDPrefix,
			err,
		)
	}

	if !taskmodel.IsM2TaskViewItem(taskItem) {
		return fmt.Errorf(
			"task show %s: item is out of scope for M2 task views; expected an active issue_type=task item, got issue_type=%s status=%s",
			resolved.TaskID,
			formatTaskField(string(taskItem.IssueType)),
			formatTaskField(string(taskItem.Status)),
		)
	}

	logger.DebugContext(command.Context(), "queried task from resolved repo")
	return renderTaskDetails(command.OutOrStdout(), taskmodel.RepoTask{
		Repository: resolved.Source.Repository,
		Task:       taskItem,
	})
}

type taskQueryOptions struct {
	operation    string
	logOperation string
	queryingLog  string
	queriedLog   string
	detailed     bool
	query        func(context.Context, taskmodel.Aggregator) taskmodel.QueryResult
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
	if err := renderKeyValue(output, "  Beads prefix", row.Repository.TaskIDPrefix); err != nil {
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
		if _, err := fmt.Fprintln(writer, "REPO_ID\tREPO\tBEADS_PREFIX\tTASK_ID\tSTATUS\tP\tBRANCH\tWORKTREE\tPR\tTITLE"); err != nil {
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
