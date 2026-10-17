//go:build integration

package gig_test

import (
	"testing"

	sdk "github.com/NeerajG03/gig"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdapterContractGigBlockingRestoresLifecycleAndRespectsNativeReopening(t *testing.T) {
	backend, dir := newSource(t)
	parent := createItem(t, backend, "Parent", task.IssueTypeEpic, "")
	require.NoError(t, backend.StartEpic(t.Context(), parent.ID))
	for _, tc := range []struct {
		kind   task.IssueType
		status task.Status
	}{
		{task.IssueTypeTask, task.StatusInProgress},
		{task.IssueTypeEpic, task.StatusOpen},
	} {
		kind, status := tc.kind, tc.status
		t.Run(string(kind)+"/"+string(status), func(t *testing.T) {
			blocker := createItem(t, backend, "Prerequisite", task.IssueTypeTask, "")
			item := createItem(t, backend, t.Name(), kind, parent.ID, blocker.ID)
			withNative(t, dir, func(store *sdk.Store) {
				raw := `{"team":{"name":"platform"},"orpheus.cross_repository_blocks":"null","orpheus.gig":{"owner":"operator","future":true,"design":"Design","acceptance_criteria":"Acceptance","external_ref":"OPS-42"}}`
				_, err := store.Update(item.ID, sdk.UpdateParams{Metadata: &raw}, "fixture")
				require.NoError(t, err)
			})
			if status == task.StatusInProgress {
				require.NoError(t, backend.MarkInProgress(t.Context(), item.ID, "work/block", "/fixture/worktree"))
			}
			before := getItem(t, backend, item.ID)

			require.NoError(t, backend.Block(t.Context(), item.ID))
			blocked := getItem(t, backend, item.ID)
			assert.Equal(t, task.StatusBlocked, blocked.Status)
			assert.Equal(t, string(status), blocked.Metadata[task.MetadataPreBlockStatus])
			require.NoError(t, backend.Block(t.Context(), item.ID))
			assert.Equal(t, blocked, getItem(t, backend, item.ID), "repeated blocking preserves restoration and timestamps")
			expected := before.Clone()
			expected.Status, expected.UpdatedAt = task.StatusBlocked, blocked.UpdatedAt
			delete(blocked.Metadata, task.MetadataPreBlockStatus)
			delete(blocked.Metadata, "orpheus.gig.block_after_event")
			assert.Equal(t, expected, blocked, "blocking changes no plan, relationship, Git fact or start timestamp")

			_, err := backend.Update(t.Context(), task.UpdateOptions{ID: item.ID, Priority: new(0)})
			require.NoError(t, err)
			expected.Priority = 0

			require.NoError(t, backend.Unblock(t.Context(), item.ID))
			restored := getItem(t, backend, item.ID)
			expected.Status, expected.UpdatedAt = status, restored.UpdatedAt
			assert.Equal(t, expected, restored)
			require.NoError(t, backend.Unblock(t.Context(), item.ID))
			assert.Equal(t, restored, getItem(t, backend, item.ID))

			require.NoError(t, backend.Block(t.Context(), item.ID))
			require.NoError(t, backend.Close(t.Context(), blocker.ID))
			assert.Equal(t, task.StatusOpen, getItem(t, backend, item.ID).Status, "Gig's native reopening is not suppressed")
			require.NoError(t, backend.Unblock(t.Context(), item.ID))
			assert.Equal(t, task.StatusOpen, getItem(t, backend, item.ID).Status)
			withNative(t, dir, func(store *sdk.Store) {
				require.NoError(t, store.UpdateStatus(item.ID, sdk.StatusBlocked, "external"))
			})
			require.NoError(t, backend.Unblock(t.Context(), item.ID))
			assert.Equal(t, task.StatusOpen, getItem(t, backend, item.ID).Status, "a later native block must not resurrect obsolete in-progress restoration")
			withNative(t, dir, func(store *sdk.Store) {
				require.NoError(t, store.UpdateStatus(item.ID, sdk.StatusBlocked, "external"))
			})
			require.NoError(t, backend.Unblock(t.Context(), item.ID))
			assert.Equal(t, task.StatusOpen, getItem(t, backend, item.ID).Status, "native blocked items without restoration reopen")
			require.NoError(t, backend.Close(t.Context(), item.ID))
			closed := getItem(t, backend, item.ID)
			assert.ErrorIs(t, backend.Block(t.Context(), item.ID), task.ErrMutationConflict)
			assert.Equal(t, closed, getItem(t, backend, item.ID))
		})
	}
}

func TestIntegrationAdapterContractGigBlockingPartialWritesRemainRetrySafe(t *testing.T) {
	backend, dir := newSource(t)
	item := createItem(t, backend, "Interrupted implementation", task.IssueTypeTask, "")
	require.NoError(t, backend.MarkInProgress(t.Context(), item.ID, "work/block", "/fixture/worktree"))
	before := getItem(t, backend, item.ID)
	injectFault(t, dir, `CREATE TRIGGER fail_block BEFORE UPDATE OF status ON tasks WHEN NEW.status = 'blocked' BEGIN SELECT RAISE(FAIL, 'injected block failure'); END`)

	require.ErrorContains(t, backend.Block(t.Context(), item.ID), "injected block failure")
	partial := getItem(t, backend, item.ID)
	assert.Equal(t, task.StatusInProgress, partial.Status)
	assert.Equal(t, "in_progress", partial.Metadata[task.MetadataPreBlockStatus])
	assert.Equal(t, before.OrpheusMetadata(), partial.OrpheusMetadata())
	injectFault(t, dir, "DROP TRIGGER fail_block")
	require.NoError(t, backend.Block(t.Context(), item.ID))
	blocked := getItem(t, backend, item.ID)
	injectFault(t, dir, `CREATE TRIGGER fail_restore BEFORE UPDATE OF status ON tasks WHEN OLD.status = 'blocked' BEGIN SELECT RAISE(FAIL, 'injected restore failure'); END`)
	require.ErrorContains(t, backend.Unblock(t.Context(), item.ID), "injected restore failure")
	assert.Equal(t, blocked, getItem(t, backend, item.ID), "failed restoration preserves the blocked interval")
	injectFault(t, dir, "DROP TRIGGER fail_restore")
	injectFault(t, dir, `CREATE TRIGGER fail_cleanup BEFORE UPDATE OF metadata ON tasks BEGIN SELECT RAISE(FAIL, 'injected cleanup failure'); END`)
	require.ErrorContains(t, backend.Unblock(t.Context(), item.ID), "item unblocked but restoration metadata cleanup failed")
	assert.Equal(t, task.StatusInProgress, getItem(t, backend, item.ID).Status)
	injectFault(t, dir, "DROP TRIGGER fail_cleanup")
	require.NoError(t, backend.Unblock(t.Context(), item.ID))
	require.NoError(t, backend.Block(t.Context(), item.ID))
	require.NoError(t, backend.Unblock(t.Context(), item.ID))
	assert.Equal(t, task.StatusInProgress, getItem(t, backend, item.ID).Status)
}
