package cli

import (
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

	cmd.AddCommand(newTaskReadyCommand(opts))
	return cmd
}

func newTaskReadyCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ready",
		Short: "List ready tasks across registered repositories",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			logger := opts.log().With(
				slog.String("component", "cli"),
				slog.String("operation", "task_ready"),
			)
			logger.DebugContext(command.Context(), "loading registered repos for ready task query")

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
			logger.DebugContext(command.Context(), "querying ready tasks", slog.Int("repo_count", len(sources)))

			aggregator, err := taskmodel.NewAggregator(sources, func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
				return newBeadsTaskBackend(source.BackendDir)
			})
			if err != nil {
				return err
			}

			result := aggregator.Ready(command.Context())
			logger.DebugContext(
				command.Context(),
				"queried ready tasks",
				slog.Int("row_count", len(result.Rows)),
				slog.Int("failure_count", len(result.Failures)),
			)

			if err := renderTaskRows(command.OutOrStdout(), result.Rows); err != nil {
				return err
			}
			if result.HasFailures() {
				writeRepoFailures(command.ErrOrStderr(), "task ready", result.Failures)
				return partialRepoFailureError{operation: "task ready", failures: result.Failures}
			}
			return nil
		},
	}
	return cmd
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

func renderTaskRows(output interface{ Write([]byte) (int, error) }, rows []taskmodel.RepoTask) error {
	writer := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "REPO_ID\tREPO_NAME\tBEADS_PREFIX\tTASK_ID\tSTATUS\tPRIORITY\tTITLE"); err != nil {
		return err
	}
	for _, row := range rows {
		if _, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			sanitizeTableCell(row.Repository.ID),
			sanitizeTableCell(row.Repository.Name),
			sanitizeTableCell(row.Repository.TaskIDPrefix),
			sanitizeTableCell(row.Task.ID),
			sanitizeTableCell(string(row.Task.Status)),
			row.Task.Priority,
			sanitizeTableCell(row.Task.Title),
		); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func writeRepoFailures(output interface{ Write([]byte) (int, error) }, operation string, failures []taskmodel.RepoFailure) {
	for _, failure := range failures {
		_, _ = fmt.Fprintf(
			output,
			"%s: repo %s (%s; prefix %s): %v\n",
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
