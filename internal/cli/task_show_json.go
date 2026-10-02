package cli

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/spf13/cobra"
)

// taskViewJSONTask embeds the summary so both views share fields and serialization.
type taskViewJSONTask struct {
	taskViewJSONTaskSummary
	Description        string                    `json:"description"`
	Design             string                    `json:"design"`
	AcceptanceCriteria string                    `json:"acceptance_criteria"`
	ExternalRef        string                    `json:"external_ref"`
	Labels             []string                  `json:"labels"`
	Relationships      taskViewJSONRelationships `json:"relationships"`
}

type taskViewJSONRelationships struct {
	Parent       *string  `json:"parent"`
	Children     []string `json:"children"`
	Dependencies []string `json:"dependencies"`
	Dependents   []string `json:"dependents"`
}

func renderResolvedTaskJSON(command *cobra.Command, deps *invocationDependencies, resolved resolvedTaskContext) error {
	if !resolved.Task.Relations.Complete {
		return fmt.Errorf("task show %s: task source returned incomplete relationships", resolved.Task.ID)
	}
	taskCtx, err := loadTaskContextFromInvocation(deps)
	if err != nil {
		return err
	}
	candidate := taskmodel.RepoTask{Repository: resolved.Resolved.Source.Repository, Task: resolved.Task}
	snapshot := taskCtx.Aggregator.SnapshotForTask(command.Context(), candidate)
	if snapshot.HasFailures() {
		writeRepoFailures(command.ErrOrStderr(), "task show", snapshot.Failures)
		return partialRepoFailureError{operation: "task show", failures: snapshot.Failures}
	}

	localState, hasState, err := loadLocalTaskState(deps.paths, deps.taskStateStore, candidate.Repository, candidate.Task)
	if err != nil {
		return fmt.Errorf("task show %s: load local task-state for repo %s: %w", candidate.Task.ID, candidate.Repository.ID, err)
	}
	states := status.LocalTaskStateIndex{}
	if hasState && status.LocalTaskStateCandidates(candidate.Repository, snapshot.Repositories[0].Tasks)[candidate.Task.ID] {
		states[status.RunStateKey(candidate.Repository.ID, candidate.Task.ID)] = localState
	}
	projection := status.ProjectWithLocalTaskStates(snapshot, states)
	for _, group := range projection.Groups {
		for _, entry := range group.Entries {
			if entry.Kind == status.EntryTask && entry.Task.ID == candidate.Task.ID && entry.Repository.ID == candidate.Repository.ID {
				return json.NewEncoder(command.OutOrStdout()).Encode(taskViewJSONTaskFor(statusDisplayRow{GroupID: group.ID, Entry: entry}))
			}
		}
	}
	return fmt.Errorf("task show %s: task missing from status projection", candidate.Task.ID)
}

func taskViewJSONTaskFor(row statusDisplayRow) taskViewJSONTask {
	item := row.Entry.Task
	relations := taskViewJSONRelationships{
		Children:     sortedTaskRelationshipIDs(item.Relations.ChildIDs),
		Dependencies: sortedTaskRelationshipIDs(item.Relations.DependencyIDs),
		Dependents:   sortedTaskRelationshipIDs(item.Relations.DependentIDs),
	}
	if item.Relations.ParentID != "" {
		parent := item.Relations.ParentID
		relations.Parent = &parent
	}
	return taskViewJSONTask{
		taskViewJSONTaskSummary: taskViewJSONTaskSummaryFor(row),
		Description:             item.Description,
		Design:                  item.Design,
		AcceptanceCriteria:      item.AcceptanceCriteria,
		ExternalRef:             item.ExternalRef,
		Labels:                  append([]string{}, item.Labels...),
		Relationships:           relations,
	}
}

func sortedTaskRelationshipIDs(ids []string) []string {
	result := append([]string{}, ids...)
	slices.Sort(result)
	return slices.Compact(result)
}
