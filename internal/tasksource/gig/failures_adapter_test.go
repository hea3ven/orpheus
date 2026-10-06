//go:build integration

package gig_test

import (
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/NeerajG03/gig"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/tasksource/gig"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SQL here injects storage faults that the public SDK cannot produce on demand.
// The adapter itself must use only SDK operations.
func injectFault(t *testing.T, dir, statement string) {
	t.Helper()
	withNative(t, dir, func(store *sdk.Store) {
		_, err := store.DB().Exec(statement)
		require.NoError(t, err)
	})
}

func TestIntegrationAdapterContractGigPartialCreationReportsRecoverableID(t *testing.T) {
	backend, dir := newSource(t)
	blocker := createItem(t, backend, "Blocker", task.IssueTypeTask, "")
	injectFault(t, dir, `CREATE TRIGGER fail_dependency BEFORE INSERT ON dependencies BEGIN SELECT RAISE(FAIL, 'injected dependency failure'); END`)

	partial, err := backend.Create(t.Context(), task.CreateOptions{Title: "Partial", Description: "Description", AcceptanceCriteria: "Acceptance", BlockingIDs: []string{blocker.ID}})

	require.NotEmpty(t, partial.ID)
	require.ErrorContains(t, err, partial.ID)
	assert.ErrorContains(t, err, "was created")
	assert.ErrorContains(t, err, "do not repeat task create")
	assert.Empty(t, getItem(t, backend, partial.ID).Relations.DependencyIDs)
	injectFault(t, dir, "DROP TRIGGER fail_dependency")
	repaired, err := backend.Update(t.Context(), task.UpdateOptions{ID: partial.ID, AddBlockingIDs: []string{blocker.ID}})
	require.NoError(t, err)
	assert.Equal(t, []string{blocker.ID}, repaired.Relations.DependencyIDs)
	items, err := backend.List(t.Context())
	require.NoError(t, err)
	assert.Len(t, items, 2, "repair must not recreate the task")
}

func TestIntegrationAdapterContractGigPartialEditRetainsContentAndCanRetry(t *testing.T) {
	backend, dir := newSource(t)
	blocker := createItem(t, backend, "Blocker", task.IssueTypeTask, "")
	item := createItem(t, backend, "Original", task.IssueTypeTask, "")
	injectFault(t, dir, `CREATE TRIGGER fail_dependency BEFORE INSERT ON dependencies BEGIN SELECT RAISE(FAIL, 'injected dependency failure'); END`)
	opts := task.UpdateOptions{ID: item.ID, Title: stringPtr("Changed"), AddBlockingIDs: []string{blocker.ID}}

	_, err := backend.Update(t.Context(), opts)

	require.ErrorContains(t, err, "partially applied")
	var partial task.PartialUpdateError
	require.ErrorAs(t, err, &partial)
	assert.ErrorContains(t, partial.Cause, "injected dependency failure")
	assert.Equal(t, "Changed", getItem(t, backend, item.ID).Title)
	injectFault(t, dir, "DROP TRIGGER fail_dependency")
	repaired, err := backend.Update(t.Context(), opts)
	require.NoError(t, err)
	assert.Equal(t, "Changed", repaired.Title)
	assert.Equal(t, []string{blocker.ID}, repaired.Relations.DependencyIDs)
}

func TestIntegrationAdapterContractGigDispatchPersistsPointersBeforeStatusAndRetries(t *testing.T) {
	backend, dir := newSource(t)
	item := createItem(t, backend, "Dispatch", task.IssueTypeTask, "")
	injectFault(t, dir, `CREATE TRIGGER fail_start BEFORE UPDATE OF status ON tasks WHEN NEW.status = 'in_progress' BEGIN SELECT RAISE(FAIL, 'injected status failure'); END`)

	err := backend.MarkInProgress(t.Context(), item.ID, "branch", "/worktree")

	require.ErrorContains(t, err, "injected status failure")
	partial := getItem(t, backend, item.ID)
	assert.Equal(t, task.StatusOpen, partial.Status)
	assert.Equal(t, "branch", partial.OrpheusMetadata().Branch)
	assert.Equal(t, "/worktree", partial.OrpheusMetadata().Worktree)
	require.NotNil(t, partial.StartedAt)
	injectFault(t, dir, "DROP TRIGGER fail_start")
	require.NoError(t, backend.MarkInProgress(t.Context(), item.ID, "branch", "/worktree"))
	started := getItem(t, backend, item.ID)
	assert.Equal(t, task.StatusInProgress, started.Status)
	assert.Equal(t, partial.StartedAt, started.StartedAt)
	withNative(t, dir, func(store *sdk.Store) {
		events, err := store.Events(item.ID)
		require.NoError(t, err)
		var statuses []*sdk.Event
		for _, event := range events {
			if event.Type == sdk.EventStatusChanged {
				statuses = append(statuses, event)
			}
		}
		require.Len(t, statuses, 1)
		assert.Equal(t, "status", statuses[0].Field)
		assert.Equal(t, "open", statuses[0].OldValue)
		assert.Equal(t, "in_progress", statuses[0].NewValue)
	})
}

func TestIntegrationAdapterContractGigCloseCanCommitBeforeUnblockingFails(t *testing.T) {
	backend, dir := newSource(t)
	blocker := createItem(t, backend, "Blocker", task.IssueTypeTask, "")
	dependent := createItem(t, backend, "Dependent", task.IssueTypeTask, "", blocker.ID)
	withNative(t, dir, func(store *sdk.Store) {
		require.NoError(t, store.UpdateStatus(dependent.ID, sdk.StatusBlocked, "fixture"))
	})
	injectFault(t, dir, `CREATE TRIGGER fail_unblock BEFORE UPDATE OF status ON tasks WHEN OLD.status = 'blocked' AND NEW.status = 'open' BEGIN SELECT RAISE(FAIL, 'injected unblock failure'); END`)

	err := backend.Close(t.Context(), blocker.ID)

	require.ErrorContains(t, err, "closure may already be committed")
	assert.Equal(t, task.StatusClosed, getItem(t, backend, blocker.ID).Status)
	assert.Equal(t, task.Status("blocked"), getItem(t, backend, dependent.ID).Status)
	injectFault(t, dir, "DROP TRIGGER fail_unblock")
	require.NoError(t, backend.Close(t.Context(), blocker.ID))
	assert.Equal(t, task.Status("blocked"), getItem(t, backend, dependent.ID).Status, "a closure retry does not replay SDK secondary effects")
}

func TestIntegrationAdapterContractGigNativeTerminalChildrenPermitClosure(t *testing.T) {
	backend, dir := newSource(t)
	parent := createItem(t, backend, "Parent", task.IssueTypeEpic, "")
	child := createItem(t, backend, "Child", task.IssueTypeTask, parent.ID)
	require.Error(t, backend.Close(t.Context(), parent.ID))
	assert.Equal(t, task.StatusOpen, getItem(t, backend, parent.ID).Status)
	withNative(t, dir, func(store *sdk.Store) {
		require.NoError(t, store.UpdateStatus(child.ID, sdk.StatusCancelled, "fixture"))
	})

	require.NoError(t, backend.Close(t.Context(), parent.ID))

	assert.Equal(t, task.StatusClosed, getItem(t, backend, parent.ID).Status)
	assert.Equal(t, task.Status("cancelled"), getItem(t, backend, child.ID).Status)
}

func TestIntegrationAdapterContractGigAcceptsSDKDecoderLimits(t *testing.T) {
	backend, dir := newSource(t)
	item := createItem(t, backend, "Stored", task.IssueTypeTask, "")
	injectFault(t, dir, "UPDATE tasks SET labels = 'not-json', closed_at = 'yesterday'")

	got := getItem(t, backend, item.ID)

	assert.Empty(t, got.Labels)
	assert.Nil(t, got.ClosedAt)
	assert.Equal(t, item.Title, got.Title)
}

func TestIntegrationAdapterContractGigOpeningRejectsNonregularStorage(t *testing.T) {
	for _, kind := range []string{"directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := testutil.CanonicalTempDir(t)
			path := filepath.Join(root, "tasks.db")
			if kind == "directory" {
				require.NoError(t, os.Mkdir(path, 0o700))
			} else {
				require.NoError(t, os.Symlink(filepath.Join(root, "missing.db"), path))
			}
			backend, err := gig.New(task.RepositorySource{MaintenanceOwned: true, BackendDir: root, Repository: task.Repository{TaskIDPrefix: "op"}})
			require.NoError(t, err)

			_, err = backend.List(t.Context())

			assert.ErrorContains(t, err, "nonempty regular file")
			_, err = os.Stat(filepath.Join(root, "missing.db"))
			assert.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestIntegrationAdapterContractGigOpeningUsesSDKMigrationsWithoutDiscardingTasks(t *testing.T) {
	backend, dir := newSource(t)
	item := createItem(t, backend, "Existing", task.IssueTypeTask, "")
	withNative(t, dir, func(store *sdk.Store) {
		// Approximate a pre-checkpoints SDK store with the former adapter marker.
		_, err := store.DB().Exec(`PRAGMA application_id = 1330794568;
   DROP TABLE checkpoint_files; DROP TABLE checkpoints;
   DELETE FROM schema_migrations WHERE version = 3`)
		require.NoError(t, err)

		got := getItem(t, backend, item.ID)

		assert.Equal(t, item, got)
		var version int
		require.NoError(t, store.DB().QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version))
		assert.Equal(t, 3, version)
		_, err = store.DB().Exec("SELECT * FROM checkpoints")
		require.NoError(t, err, "opening delegates migrations to the SDK")
	})
}
