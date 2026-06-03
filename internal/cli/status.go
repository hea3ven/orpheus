package cli

import (
	"fmt"
	"log/slog"
	"text/tabwriter"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/spf13/cobra"
)

func newStatusCommand(opts *rootOptions) *cobra.Command {
	var full bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the local cross-repository action queue",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runStatus(command, opts, full)
		},
	}
	cmd.Flags().BoolVar(&full, "full", false, "show lower-priority groups such as blocked and done/closed")
	return cmd
}

func runStatus(command *cobra.Command, opts *rootOptions, full bool) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "status"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for status projection")

	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}
	logger.DebugContext(command.Context(), "querying local task snapshots", slog.Int("repo_count", len(taskCtx.Sources)))

	snapshot := taskCtx.Aggregator.Snapshot(command.Context())
	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	runStates, runStateFailures := taskRunStateIndex(paths, snapshot)
	if len(runStateFailures) > 0 {
		snapshot.Failures = append(snapshot.Failures, runStateFailures...)
	}
	projection := status.ProjectWithRunStates(snapshot, runStates)
	logger.DebugContext(
		command.Context(),
		"projected local status",
		slog.Int("repo_count", len(snapshot.Repositories)),
		slog.Int("failure_count", len(snapshot.Failures)),
		slog.Int("run_state_count", len(runStates)),
	)

	if err := renderStatus(command.OutOrStdout(), projection, full); err != nil {
		return err
	}
	if snapshot.HasFailures() {
		writeRepoFailures(command.ErrOrStderr(), "status", snapshot.Failures)
		return partialRepoFailureError{operation: "status", failures: snapshot.Failures}
	}
	return nil
}

func taskRunStateIndex(
	paths state.Paths,
	snapshot taskmodel.SnapshotResult,
) (status.RunStateIndex, []taskmodel.RepoFailure) {
	store := taskstate.Service(taskstate.NewStore(paths))
	index := status.RunStateIndex{}
	failures := make([]taskmodel.RepoFailure, 0)

	for _, repoSnapshot := range snapshot.Repositories {
		for _, taskItem := range repoSnapshot.Tasks {
			latest, ok, err := store.LatestRun(repoSnapshot.Repository.ID, taskItem.ID)
			if err != nil {
				failures = append(failures, taskmodel.RepoFailure{
					Repository: repoSnapshot.Repository,
					Source:     "task_state",
					Operation:  "latest_run",
					Err:        err,
				})
				continue
			}
			if !ok {
				continue
			}
			index[status.RunStateKey(repoSnapshot.Repository.ID, taskItem.ID)] = latest
		}
	}
	return index, failures
}

func renderStatus(output interface{ Write([]byte) (int, error) }, projection status.Projection, full bool) error {
	visibleGroups := visibleStatusGroups(projection.Groups, full)
	for i, group := range visibleGroups {
		if i > 0 {
			if _, err := fmt.Fprintln(output); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(output, "%s (%d)\n", group.Title, len(group.Entries)); err != nil {
			return err
		}
		if len(group.Entries) == 0 {
			if _, err := fmt.Fprintln(output, "-"); err != nil {
				return err
			}
			continue
		}
		if err := renderStatusEntries(output, group); err != nil {
			return err
		}
	}
	return nil
}

func renderStatusEntries(output interface{ Write([]byte) (int, error) }, group status.Group) error {
	writer := tabwriter.NewWriter(output, 0, 0, 2, ' ', 0)
	showDetail := statusGroupShowsDetail(group.ID)
	if showDetail {
		if _, err := fmt.Fprintln(writer, "REPO\tTASK_ID\tP\tTITLE\tDETAIL"); err != nil {
			return err
		}
	} else {
		if _, err := fmt.Fprintln(writer, "REPO\tTASK_ID\tP\tTITLE"); err != nil {
			return err
		}
	}
	for _, entry := range group.Entries {
		switch entry.Kind {
		case status.EntryTask:
			if err := renderStatusTaskEntry(writer, entry, showDetail); err != nil {
				return err
			}
		case status.EntryRepoFailure:
			if err := renderStatusFailureEntry(writer, entry, showDetail); err != nil {
				return err
			}
		}
	}
	return writer.Flush()
}

func renderStatusTaskEntry(writer interface{ Write([]byte) (int, error) }, entry status.Entry, showDetail bool) error {
	if !showDetail {
		_, err := fmt.Fprintf(
			writer,
			"%s\t%s\t%d\t%s\n",
			sanitizeTableCell(entry.Repository.Name),
			sanitizeTableCell(entry.Task.ID),
			entry.Task.Priority,
			sanitizeTableCell(entry.Task.Title),
		)
		return err
	}

	_, err := fmt.Fprintf(
		writer,
		"%s\t%s\t%d\t%s\t%s\n",
		sanitizeTableCell(entry.Repository.Name),
		sanitizeTableCell(entry.Task.ID),
		entry.Task.Priority,
		sanitizeTableCell(entry.Task.Title),
		sanitizeTableCell(entry.Detail),
	)
	return err
}

func renderStatusFailureEntry(
	writer interface{ Write([]byte) (int, error) },
	entry status.Entry,
	showDetail bool,
) error {
	detail := entry.Detail
	if detail == "" && entry.Failure != nil {
		detail = entry.Failure.Error()
	}
	title := fmt.Sprintf("repo %s (prefix %s)", entry.Repository.ID, entry.Repository.TaskIDPrefix)
	if !showDetail {
		_, err := fmt.Fprintf(
			writer,
			"%s\t-\t-\t%s\n",
			sanitizeTableCell(entry.Repository.Name),
			sanitizeTableCell(title),
		)
		return err
	}

	_, err := fmt.Fprintf(
		writer,
		"%s\t-\t-\t%s\t%s\n",
		sanitizeTableCell(entry.Repository.Name),
		sanitizeTableCell(title),
		sanitizeTableCell(detail),
	)
	return err
}

func visibleStatusGroups(groups []status.Group, full bool) []status.Group {
	if full {
		return groups
	}

	visible := make([]status.Group, 0, len(groups))
	for _, group := range groups {
		if statusGroupHiddenByDefault(group.ID) {
			continue
		}
		visible = append(visible, group)
	}
	return visible
}

func statusGroupHiddenByDefault(groupID status.GroupID) bool {
	return groupID == status.GroupBlocked || groupID == status.GroupDoneClosed
}

func statusGroupShowsDetail(groupID status.GroupID) bool {
	switch groupID {
	case status.GroupReadyToRun, status.GroupDoneClosed:
		return false
	default:
		return true
	}
}
