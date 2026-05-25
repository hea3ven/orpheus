package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"text/tabwriter"

	"github.com/hea3ven/orpheus/internal/beads"
	"github.com/hea3ven/orpheus/internal/registry"
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

	cmd.AddCommand(newTaskListCommand(opts), newTaskReadyCommand(opts))
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
		Short: "List ready tasks across registered repositories",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskQuery(command, opts, taskQueryOptions{
				operation:    "task ready",
				logOperation: "task_ready",
				queryingLog:  "querying ready tasks",
				queriedLog:   "queried ready tasks",
				detailed:     detailed,
				query: func(ctx context.Context, aggregator taskmodel.Aggregator) taskmodel.QueryResult {
					return aggregator.Ready(ctx)
				},
			})
		},
	}
	addTaskDetailFlags(cmd, &detailed)
	return cmd
}

func addTaskDetailFlags(cmd *cobra.Command, detailed *bool) {
	cmd.Flags().BoolVar(detailed, "details", false, "show detailed table with repo ids, Beads prefixes, and Orpheus metadata")
	cmd.Flags().BoolVarP(detailed, "long", "l", false, "show detailed table with repo ids, Beads prefixes, and Orpheus metadata")
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
		sanitizeTableCell(formatOptionalTableCell(metadata.Branch)),
		sanitizeTableCell(formatOptionalTableCell(metadata.Worktree)),
		sanitizeTableCell(formatOptionalTableCell(metadata.PRURL)),
		sanitizeTableCell(row.Task.Title),
	)
	return err
}

func formatOptionalTableCell(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func writeRepoFailures(output interface{ Write([]byte) (int, error) }, operation string, failures []taskmodel.RepoFailure) {
	for _, failure := range failures {
		_, _ = fmt.Fprintf(
			output,
			"%s: repo %s (%s; prefix %s): needs attention: %v\n",
			operation,
			failure.Repository.ID,
			failure.Repository.Name,
			failure.Repository.TaskIDPrefix,
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

func sanitizeTableCell(value string) string {
	value = strings.ReplaceAll(value, "\t", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return value
}
