package gig

import (
	"testing"
	"time"

	sdk "github.com/NeerajG03/gig"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTranslationKeepsNativeFieldsAndSupplementalMetadata(t *testing.T) {
	now := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	native := &sdk.Task{ID: "op-parent.1", ParentID: "op-parent", Title: "Native", Description: "Content",
		Type: sdk.TypeTask, Status: sdk.StatusClosed, Priority: sdk.P1, Assignee: "assignee",
		CreatedBy: "author", Labels: []string{"sdk"}, CreatedAt: now, UpdatedAt: now, ClosedAt: &now,
		Metadata: `{"orpheus.gig":{"owner":"operator","design":"plan"},"orpheus.branch":"branch"}`}

	stored, err := translate(native)

	require.NoError(t, err)
	assert.Equal(t, task.Task{ID: "op-parent.1", Title: "Native", Description: "Content", IssueType: task.IssueTypeTask,
		Status: task.StatusClosed, Priority: 1, Assignee: "assignee", CreatedBy: "author", Labels: []string{"sdk"},
		CreatedAt: &now, UpdatedAt: &now, ClosedAt: &now, Owner: "operator", Design: "plan",
		Relations: task.RelationSummary{ParentID: "op-parent"}, Metadata: task.Metadata{task.MetadataBranch: "branch"}}, stored.item)
}

func TestTranslationRejectsUnsupportedTypesBeforeDecodingTheirMetadata(t *testing.T) {
	_, err := translate(&sdk.Task{ID: "op-bug", Type: sdk.TypeBug, Metadata: "invalid"})

	assert.ErrorIs(t, err, task.ErrUnsupportedTaskSourceItem)
}

func TestTranslationRequiresIdentityTitleAndNativeTimestamps(t *testing.T) {
	for _, field := range []string{"id", "title", "created", "updated"} {
		t.Run(field, func(t *testing.T) {
			native := &sdk.Task{ID: "op-1", Title: "Task", Type: sdk.TypeTask, CreatedAt: time.Now(), UpdatedAt: time.Now()}
			switch field {
			case "id":
				native.ID = ""
			case "title":
				native.Title = ""
			case "created":
				native.CreatedAt = time.Time{}
			case "updated":
				native.UpdatedAt = time.Time{}
			}

			_, err := translate(native)

			assert.ErrorContains(t, err, "missing required fields or timestamps")
		})
	}
}
