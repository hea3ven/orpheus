//go:build integration

package beads_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func checkBlockingPreservesPlansAndRestoresLifecycle(t *testing.T, fixture beadsRelationshipFixture) {
	t.Helper()
	parent, err := fixture.backend.Create(t.Context(), task.CreateOptions{Title: t.Name() + "/Parent", Description: "Plan", AcceptanceCriteria: "Done", IssueType: task.IssueTypeEpic})
	require.NoError(t, err)
	require.NoError(t, fixture.backend.StartEpic(t.Context(), parent.ID))
	for _, tc := range []struct {
		kind   task.IssueType
		status task.Status
	}{
		{task.IssueTypeTask, task.StatusInProgress},
		{task.IssueTypeEpic, task.StatusOpen},
	} {
		kind, status := tc.kind, tc.status
		t.Run(string(kind)+"/"+string(status), func(t *testing.T) {
			blocker, err := fixture.backend.Create(t.Context(), task.CreateOptions{Title: t.Name() + "/Prerequisite", Description: "Plan", AcceptanceCriteria: "Done", IssueType: kind})
			require.NoError(t, err)
			item, err := fixture.backend.Create(t.Context(), task.CreateOptions{
				Title: t.Name(), Description: "Plan", Design: "Design", AcceptanceCriteria: "Done", ExternalRef: "PLAN-1",
				IssueType: kind, ParentID: parent.ID, BlockingIDs: []string{blocker.ID}, Priority: new(1),
			})
			require.NoError(t, err)
			_, err = fixture.runner.Run(fixture.dir, "update", item.ID, "--assignee", "operator", "--add-label", "planning", "--set-metadata", "team=platform")
			require.NoError(t, err)
			if status == task.StatusInProgress {
				require.NoError(t, fixture.backend.MarkInProgress(t.Context(), item.ID, "work/block", "/fixture/worktree"))
			}
			before, err := fixture.backend.Get(t.Context(), item.ID)
			require.NoError(t, err)

			require.NoError(t, fixture.backend.Block(t.Context(), item.ID))
			blocked, err := fixture.backend.Get(t.Context(), item.ID)
			require.NoError(t, err)
			assert.Equal(t, task.StatusBlocked, blocked.Status)
			assert.Equal(t, string(status), blocked.Metadata[task.MetadataPreBlockStatus])
			require.NoError(t, fixture.backend.Block(t.Context(), item.ID))
			repeated, err := fixture.backend.Get(t.Context(), item.ID)
			require.NoError(t, err)
			assert.Equal(t, blocked, repeated)
			expected := before.Clone()
			expected.Status, expected.UpdatedAt = task.StatusBlocked, blocked.UpdatedAt
			delete(blocked.Metadata, task.MetadataPreBlockStatus)
			assert.Equal(t, expected, blocked, "native blocking preserves plans, relationships, ownership and Git facts")

			require.NoError(t, fixture.backend.Unblock(t.Context(), item.ID))
			restored, err := fixture.backend.Get(t.Context(), item.ID)
			require.NoError(t, err)
			expected.Status, expected.UpdatedAt = status, restored.UpdatedAt
			assert.Equal(t, expected, restored)
			require.NoError(t, fixture.backend.Unblock(t.Context(), item.ID))
			repeated, err = fixture.backend.Get(t.Context(), item.ID)
			require.NoError(t, err)
			assert.Equal(t, restored, repeated)

			_, err = fixture.runner.Run(fixture.dir, "update", item.ID, "--status", "blocked")
			require.NoError(t, err)
			require.NoError(t, fixture.backend.Unblock(t.Context(), item.ID))
			legacy, err := fixture.backend.Get(t.Context(), item.ID)
			require.NoError(t, err)
			assert.Equal(t, task.StatusOpen, legacy.Status, "native blocked items without restoration reopen")
			require.NoError(t, fixture.backend.Close(t.Context(), blocker.ID))
			require.NoError(t, fixture.backend.Close(t.Context(), item.ID))
			closed, err := fixture.backend.Get(t.Context(), item.ID)
			require.NoError(t, err)
			assert.ErrorIs(t, fixture.backend.Block(t.Context(), item.ID), task.ErrMutationConflict)
			repeated, err = fixture.backend.Get(t.Context(), item.ID)
			require.NoError(t, err)
			assert.Equal(t, closed, repeated)
		})
	}
}
