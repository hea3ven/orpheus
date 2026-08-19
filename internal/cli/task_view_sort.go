package cli

import (
	"fmt"
	"strings"
)

// taskViewSort identifies the shared ordering contract for cross-repository task views.
type taskViewSort string

const (
	taskViewSortStatus  taskViewSort = "status"
	taskViewSortCreated taskViewSort = "created"
	taskViewSortUpdated taskViewSort = "updated"
)

func normalizeTaskViewSort(value string, defaultSort taskViewSort) (taskViewSort, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultSort, nil
	}

	sortMode := taskViewSort(value)
	switch sortMode {
	case taskViewSortStatus, taskViewSortCreated, taskViewSortUpdated:
		return sortMode, nil
	default:
		return "", fmt.Errorf("invalid --sort %q; expected status, created, or updated", value)
	}
}

func taskViewSortValues() []string {
	return []string{
		string(taskViewSortStatus),
		string(taskViewSortCreated),
		string(taskViewSortUpdated),
	}
}
