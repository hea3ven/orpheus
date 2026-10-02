package cli

import (
	"encoding/json"
	"testing"

	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskJSONSerializesClassifiedRelationshipsAsSortedUniqueIDs(t *testing.T) {
	item := taskmodel.Task{
		ID: "op-1", Description: "First line\nSecond line", Design: "Design", AcceptanceCriteria: "Acceptance", ExternalRef: "REF-1",
		Labels: []string{"second", "first"},
		Relations: taskmodel.RelationSummary{
			ParentID: "op-parent", Complete: true,
			ChildIDs: []string{"op-z", "op-a", "op-z"}, DependencyIDs: []string{"op-y", "op-b", "op-y"}, DependentIDs: []string{"op-x", "op-c", "op-x"},
		},
	}
	row := statusDisplayRow{GroupID: status.GroupBlocked, Entry: status.Entry{Kind: status.EntryTask, Task: item}}

	data, err := json.Marshal(taskViewJSONTaskFor(row))
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	assert.Equal(t, map[string]any{
		"parent": "op-parent", "children": []any{"op-a", "op-z"}, "dependencies": []any{"op-b", "op-y"}, "dependents": []any{"op-c", "op-x"},
	}, result["relationships"])
	assert.Equal(t, "First line\nSecond line", result["description"])
	assert.Equal(t, "Design", result["design"])
	assert.Equal(t, "Acceptance", result["acceptance_criteria"])
	assert.Equal(t, "REF-1", result["external_ref"])
	assert.Equal(t, []any{"second", "first"}, result["labels"])
	assert.Equal(t, []string{"op-z", "op-a", "op-z"}, item.Relations.ChildIDs, "serialization must not mutate the source")
}

func TestTaskJSONIncludesEmptyFieldsAndRelationshipsWithoutHistory(t *testing.T) {
	row := statusDisplayRow{GroupID: status.GroupReadyToRun, Entry: status.Entry{Kind: status.EntryTask, Task: taskmodel.Task{ID: "op-1"}}}

	data, err := json.Marshal(taskViewJSONTaskFor(row))
	require.NoError(t, err)
	var result map[string]any
	require.NoError(t, json.Unmarshal(data, &result))
	assert.Equal(t, map[string]any{
		"parent": nil, "children": []any{}, "dependencies": []any{}, "dependents": []any{},
	}, result["relationships"])
	assert.Equal(t, []any{}, result["labels"])
	for _, field := range []string{"description", "design", "acceptance_criteria", "external_ref"} {
		assert.IsType(t, "", result[field], field)
		assert.Empty(t, result[field], field)
	}
	for _, field := range []string{"history", "runs", "reviews", "events", "metadata", "complete", "issues"} {
		assert.NotContains(t, result, field)
	}
}
