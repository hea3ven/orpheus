package cli

import (
	"fmt"
	"log/slog"
	"strconv"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
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
	projection := status.ProjectWithLocalTaskStates(snapshot, runStates)
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
) (status.LocalTaskStateIndex, []taskmodel.RepoFailure) {
	store := taskstate.Service(taskstate.NewStore(paths))
	index := status.LocalTaskStateIndex{}
	failures := make([]taskmodel.RepoFailure, 0)

	for _, repoSnapshot := range snapshot.Repositories {
		for _, taskItem := range repoSnapshot.Tasks {
			state, err := store.Load(repoSnapshot.Repository.ID, taskItem.ID)
			if err != nil {
				failures = append(failures, taskmodel.RepoFailure{
					Repository: repoSnapshot.Repository,
					Source:     "task_state",
					Operation:  "load",
					Err:        err,
				})
				continue
			}
			latest, ok := taskstate.LatestRun(state)
			if !ok {
				continue
			}
			latestCopy := latest
			expectedTargets, err := workflow.ExpectedTargetsForTask(repoSnapshot.Repository, taskItem.ID, paths)
			latestReview, hasReview := taskstate.LatestReview(state)
			latestFinalizationFailure, hasFinalizationFailure := taskstate.LatestFinalizationFailure(state)
			localState := status.LocalTaskState{
				LatestRun:    &latestCopy,
				Finalization: taskstate.FinalizationFacts(state),
			}
			if hasReview {
				localState.LatestReview = &latestReview
			}
			if hasFinalizationFailure {
				localState.LatestFinalizationFailure = &latestFinalizationFailure
			}
			if err == nil {
				localState.ExpectedTargets = &expectedTargets
			}
			index[status.RunStateKey(repoSnapshot.Repository.ID, taskItem.ID)] = localState
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
	showDetail := statusGroupShowsDetail(group.ID)
	headers := []string{"REPO", "TASK_ID", "P", "TITLE"}
	if showDetail {
		headers = append(headers, "DETAIL")
	}

	rows := make([][]string, 0, len(group.Entries))
	for _, entry := range group.Entries {
		switch entry.Kind {
		case status.EntryTask:
			rows = append(rows, statusTaskEntryTableRow(entry, showDetail))
		case status.EntryRepoFailure:
			rows = append(rows, statusFailureEntryTableRow(entry, showDetail))
		}
	}
	return renderTable(output, headers, rows)
}

func statusTaskEntryTableRow(entry status.Entry, showDetail bool) []string {
	row := []string{
		entry.Repository.Name,
		entry.Task.ID,
		strconv.Itoa(entry.Task.Priority),
		entry.Task.Title,
	}
	if showDetail {
		row = append(row, entry.Detail)
	}
	return row
}

func statusFailureEntryTableRow(entry status.Entry, showDetail bool) []string {
	detail := entry.Detail
	if detail == "" && entry.Failure != nil {
		detail = entry.Failure.Error()
	}
	title := fmt.Sprintf("repo %s (prefix %s)", entry.Repository.ID, entry.Repository.TaskIDPrefix)

	row := []string{
		entry.Repository.Name,
		"-",
		"-",
		title,
	}
	if showDetail {
		row = append(row, detail)
	}
	return row
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
