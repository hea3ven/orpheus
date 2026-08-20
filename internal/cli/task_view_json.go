package cli

import (
	"encoding/json"
	"io"
	"time"

	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
)

type taskViewJSONRepository struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	TaskIDPrefix string `json:"task_id_prefix,omitempty"`
}

type taskViewJSONDetail struct {
	Kind      string   `json:"kind"`
	URL       string   `json:"url,omitempty"`
	ID        string   `json:"id,omitempty"`
	IDs       []string `json:"ids,omitempty"`
	Attempt   int      `json:"attempt,omitempty"`
	State     string   `json:"state,omitempty"`
	Step      string   `json:"step,omitempty"`
	Count     int      `json:"count,omitempty"`
	Source    string   `json:"source,omitempty"`
	Operation string   `json:"operation,omitempty"`
	Message   string   `json:"message,omitempty"`
}

type taskViewJSONEpicProgress struct {
	Completed int `json:"completed"`
	Total     int `json:"total"`
}

type taskViewJSONTaskEntry struct {
	Kind         string                    `json:"kind"`
	Repository   taskViewJSONRepository    `json:"repository"`
	ID           string                    `json:"id"`
	Title        string                    `json:"title"`
	IssueType    string                    `json:"issue_type"`
	Priority     int                       `json:"priority"`
	Status       string                    `json:"status"`
	Detail       taskViewJSONDetail        `json:"detail"`
	EpicProgress *taskViewJSONEpicProgress `json:"epic_progress,omitempty"`
	CreatedAt    *time.Time                `json:"created_at"`
	UpdatedAt    *time.Time                `json:"updated_at"`
}

type taskViewJSONRepoFailureEntry struct {
	Kind       string                 `json:"kind"`
	Repository taskViewJSONRepository `json:"repository"`
	Status     string                 `json:"status"`
	Detail     taskViewJSONDetail     `json:"detail"`
}

// renderTaskViewJSON serializes the final, selected status projection order.
// It deliberately accepts display rows only for shared selection and ordering;
// JSON values are derived from the projection rather than table presentation.
func renderTaskViewJSON(
	output io.Writer,
	projection status.Projection,
	full bool,
	sortMode taskViewSort,
	tasksOnly bool,
) error {
	rows := statusDisplayRowsForSort(visibleStatusGroups(projection.Groups, full), sortMode)
	entries := make([]any, 0, len(rows))
	for _, row := range rows {
		if tasksOnly && row.Entry.Kind != status.EntryTask {
			continue
		}
		switch row.Entry.Kind {
		case status.EntryTask:
			entries = append(entries, taskViewJSONTaskEntryFor(row))
		case status.EntryRepoFailure:
			entries = append(entries, taskViewJSONRepoFailureEntryFor(row))
		}
	}
	return json.NewEncoder(output).Encode(entries)
}

func taskViewJSONTaskEntryFor(row statusDisplayRow) taskViewJSONTaskEntry {
	entry := row.Entry
	taskItem := entry.Task
	result := taskViewJSONTaskEntry{
		Kind:       string(entry.Kind),
		Repository: taskViewJSONRepositoryFor(entry.Repository),
		ID:         taskItem.ID,
		Title:      taskItem.Title,
		IssueType:  string(taskItem.IssueType),
		Priority:   taskItem.Priority,
		Status:     taskViewJSONStatus(row.GroupID),
		Detail:     taskViewJSONDetailFor(entry.SemanticDetail, nil),
		CreatedAt:  cloneTaskViewJSONTime(taskItem.CreatedAt),
		UpdatedAt:  cloneTaskViewJSONTime(taskItem.UpdatedAt),
	}
	if entry.EpicProgress.Kind == status.DetailEpicProgress {
		result.EpicProgress = &taskViewJSONEpicProgress{
			Completed: entry.EpicProgress.Completed,
			Total:     entry.EpicProgress.Total,
		}
	}
	return result
}

func taskViewJSONRepoFailureEntryFor(row statusDisplayRow) taskViewJSONRepoFailureEntry {
	entry := row.Entry
	return taskViewJSONRepoFailureEntry{
		Kind:       string(entry.Kind),
		Repository: taskViewJSONRepositoryFor(entry.Repository),
		Status:     taskViewJSONStatus(row.GroupID),
		Detail:     taskViewJSONDetailFor(entry.SemanticDetail, entry.Failure),
	}
}

func taskViewJSONRepositoryFor(repository taskmodel.Repository) taskViewJSONRepository {
	return taskViewJSONRepository{
		ID:           repository.ID,
		Name:         repository.Name,
		TaskIDPrefix: repository.TaskIDPrefix,
	}
}

func taskViewJSONStatus(groupID status.GroupID) string {
	switch groupID {
	case status.GroupNeedsAttention:
		return "needs_attention"
	case status.GroupInReview:
		return "reviewing"
	case status.GroupWorking:
		return "working"
	case status.GroupIdle:
		return "idle"
	case status.GroupReadyToRun:
		return "ready"
	case status.GroupBlocked:
		return "blocked"
	case status.GroupDoneClosed:
		return "closed"
	default:
		return string(groupID)
	}
}

func taskViewJSONDetailFor(detail status.Detail, failure error) taskViewJSONDetail {
	kind := string(detail.Kind)
	if kind == "" {
		kind = "none"
	}
	result := taskViewJSONDetail{
		Kind:      kind,
		URL:       detail.URL,
		ID:        detail.ID,
		IDs:       append([]string(nil), detail.IDs...),
		Attempt:   detail.Attempt,
		State:     taskViewJSONDetailState(detail),
		Step:      detail.Step,
		Count:     detail.Count,
		Source:    detail.Source,
		Operation: detail.Operation,
	}
	if failure != nil {
		result.Message = failure.Error()
	}
	return result
}

// taskViewJSONDetailState retains only state values owned by Orpheus. Task
// source lifecycle values affect classification but are not part of this JSON
// contract.
func taskViewJSONDetailState(detail status.Detail) string {
	switch detail.Kind {
	case status.DetailUnknownTaskStatus, status.DetailParentNotReady:
		return ""
	default:
		return detail.State
	}
}

func cloneTaskViewJSONTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
