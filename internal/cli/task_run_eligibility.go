package cli

import (
	"fmt"

	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/spf13/cobra"
)

// Eligibility governs activation, not retries or review repairs. Keep policy in
// the status projection so task run rejects the same problems shown in task views.
func validateTaskRunInitialEligibility(command *cobra.Command, execution taskRunRouteExecution) error {
	if execution.task.Status != taskmodel.StatusOpen {
		return nil
	}
	taskCtx, err := loadTaskContextFromInvocation(execution.deps)
	if err != nil {
		return err
	}
	candidate := taskmodel.RepoTask{Repository: execution.resolved.Source.Repository, Task: execution.task}
	snapshot := taskCtx.Aggregator.SnapshotForTask(command.Context(), candidate)
	localStates, failures := taskRunStateIndexForCandidates(execution.deps, snapshot, []taskmodel.RepoTask{candidate})
	snapshot.Failures = append(snapshot.Failures, failures...)
	if snapshot.HasFailures() {
		writeRepoFailures(command.ErrOrStderr(), "task run", snapshot.Failures)
		return partialRepoFailureError{operation: "task run", failures: snapshot.Failures}
	}
	projection := status.ProjectWithLocalTaskStates(snapshot, localStates)
	for _, group := range projection.Groups {
		for _, entry := range group.Entries {
			if entry.Kind != status.EntryTask || entry.Task.ID != candidate.Task.ID || entry.Repository.ID != candidate.Repository.ID {
				continue
			}
			if group.ID == status.GroupBlocked || group.ID == status.GroupNeedsAttention {
				return fmt.Errorf("task run %s: not eligible for dispatch: %s", candidate.Task.ID, entry.Detail)
			}
			return nil
		}
	}
	return fmt.Errorf("task run %s: task missing from status projection", candidate.Task.ID)
}
