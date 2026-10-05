//go:build integration

package gig_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/NeerajG03/gig"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/tasksource/gig"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSource(t *testing.T) (gig.Backend, string) {
	t.Helper()
	dir := filepath.Join(testutil.CanonicalTempDir(t), "gig")
	require.NoError(t, gig.Initialize(dir, "op"))
	backend, err := gig.New(task.RepositorySource{Repository: task.Repository{TaskIDPrefix: "op"}, BackendDir: dir, MaintenanceOwned: true})
	require.NoError(t, err)
	return backend, dir
}

func createItem(t *testing.T, backend gig.Backend, title string, kind task.IssueType, parent string, blockers ...string) task.Task {
	t.Helper()
	item, err := backend.Create(t.Context(), task.CreateOptions{Title: title, Description: "Description", Design: "Design", AcceptanceCriteria: "Acceptance", ExternalRef: "OPS-42", IssueType: kind, ParentID: parent, BlockingIDs: blockers})
	require.NoError(t, err)
	return item
}

func getItem(t *testing.T, backend gig.Backend, id string) task.Task {
	t.Helper()
	item, err := backend.Get(t.Context(), id)
	require.NoError(t, err)
	return item
}

func withNative(t *testing.T, dir string, run func(*sdk.Store)) {
	t.Helper()
	store, err := sdk.Open(filepath.Join(dir, "tasks.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	run(store)
}

func TestIntegrationAdapterContractGigFieldsRelationshipsAndLifecycle(t *testing.T) {
	backend, dir := newSource(t)
	grandparent := createItem(t, backend, "Portfolio", task.IssueTypeEpic, "")
	epic := createItem(t, backend, "Delivery", task.IssueTypeEpic, grandparent.ID)
	blocker := createItem(t, backend, "Prerequisite", task.IssueTypeTask, "")
	child := createItem(t, backend, "Implementation", task.IssueTypeTask, epic.ID, blocker.ID)
	require.NoError(t, backend.MarkInProgress(t.Context(), child.ID, "branch", "/worktree"))

	running := getItem(t, backend, child.ID)
	assert.Equal(t, task.StatusInProgress, running.Status)
	assert.NotNil(t, running.StartedAt)
	assert.Equal(t, "Design", running.Design)
	assert.Equal(t, "Acceptance", running.AcceptanceCriteria)
	assert.Equal(t, "OPS-42", running.ExternalRef)
	assert.True(t, running.Relations.Complete)
	assert.Equal(t, epic.ID, running.Relations.ParentID)
	assert.Equal(t, []string{blocker.ID}, running.Relations.DependencyIDs)
	assert.Len(t, running.RelatedItems, 2)
	assert.Equal(t, task.StatusInProgress, getItem(t, backend, epic.ID).Status)
	assert.Nil(t, getItem(t, backend, epic.ID).StartedAt, "automatic transitions have no adapter-observed start timestamp")
	assert.Equal(t, task.StatusOpen, getItem(t, backend, grandparent.ID).Status, "native advancement affects only the immediate parent")
	assert.Equal(t, epic.ID+".1", child.ID)
	assert.Zero(t, running.CreatedAt.Nanosecond(), "SDK native timestamps persist at second precision")
	assert.Equal(t, []string{child.ID}, getItem(t, backend, epic.ID).Relations.ChildIDs)
	assert.Equal(t, []string{child.ID}, getItem(t, backend, blocker.ID).Relations.DependentIDs)
	require.NoError(t, backend.MarkInProgress(t.Context(), child.ID, "branch", "/worktree"))
	assert.Equal(t, running.UpdatedAt, getItem(t, backend, child.ID).UpdatedAt)
	assert.ErrorIs(t, backend.MarkInProgress(t.Context(), child.ID, "other", "/worktree"), task.ErrMutationConflict)
	require.NoError(t, backend.UpdateGitFacts(t.Context(), child.ID, "published", "/worktree"))
	require.NoError(t, backend.SetPRURL(t.Context(), child.ID, "https://example.invalid/pr/1"))
	assert.ErrorIs(t, backend.UpdateGitFacts(t.Context(), child.ID, "other", "/worktree"), task.ErrMutationConflict)
	require.NoError(t, backend.StartEpic(t.Context(), epic.ID))
	started := getItem(t, backend, epic.ID)
	require.NoError(t, backend.StartEpic(t.Context(), epic.ID))
	assert.Equal(t, started.UpdatedAt, getItem(t, backend, epic.ID).UpdatedAt)
	require.NoError(t, backend.StartEpic(t.Context(), grandparent.ID))
	assert.NotNil(t, getItem(t, backend, grandparent.ID).StartedAt)
	assert.Equal(t, task.StatusInProgress, getItem(t, backend, grandparent.ID).Status)
	assert.ErrorIs(t, backend.StartEpic(t.Context(), child.ID), task.ErrMutationConflict)

	withNative(t, dir, func(store *sdk.Store) {
		require.NoError(t, store.UpdateStatus(child.ID, sdk.StatusBlocked, "fixture"))
	})
	require.NoError(t, backend.Close(t.Context(), blocker.ID))
	assert.Equal(t, task.StatusOpen, getItem(t, backend, child.ID).Status, "native blocked dependents reopen after their blockers close")
	require.NoError(t, backend.Close(t.Context(), child.ID))
	closed := getItem(t, backend, child.ID)
	require.NoError(t, backend.Close(t.Context(), child.ID))
	assert.Equal(t, closed.UpdatedAt, getItem(t, backend, child.ID).UpdatedAt)
	assert.NotNil(t, closed.ClosedAt)
	assert.Equal(t, "published", closed.OrpheusMetadata().Branch)
	assert.Equal(t, "https://example.invalid/pr/1", closed.OrpheusMetadata().PRURL)
	assert.Equal(t, task.StatusInProgress, getItem(t, backend, epic.ID).Status)
	require.NoError(t, backend.Close(t.Context(), epic.ID))
	assert.ErrorIs(t, backend.StartEpic(t.Context(), epic.ID), task.ErrMutationConflict)
	assert.ErrorIs(t, backend.MarkInProgress(t.Context(), blocker.ID, "branch", "/worktree"), task.ErrMutationConflict)
	_, err := backend.Update(t.Context(), task.UpdateOptions{ID: child.ID, Title: stringPtr("Changed")})
	assert.ErrorIs(t, err, task.ErrMutationConflict)
}

func TestIntegrationAdapterContractGigEditsPreserveMetadataAndProtectNonblockingEdges(t *testing.T) {
	backend, dir := newSource(t)
	parent := createItem(t, backend, "Parent", task.IssueTypeEpic, "")
	other := createItem(t, backend, "Other", task.IssueTypeEpic, "")
	item := createItem(t, backend, "Original", task.IssueTypeTask, parent.ID)
	dependency := createItem(t, backend, "Dependency", task.IssueTypeTask, "")
	withNative(t, dir, func(store *sdk.Store) {
		raw := `{"unrelated":{"nested":[1,true]},"orpheus.gig":{"future":"keep","owner":"operator"}}`
		_, err := store.Update(item.ID, sdk.UpdateParams{Metadata: &raw}, "fixture")
		require.NoError(t, err)
		require.NoError(t, store.AddDependency(item.ID, parent.ID, sdk.RelatesTo))
	})
	updated, err := backend.Update(t.Context(), task.UpdateOptions{ID: item.ID, Title: stringPtr("Changed"), Description: stringPtr("New description"), Design: stringPtr("New design"), AcceptanceCriteria: stringPtr("New acceptance"), ExternalRef: stringPtr("NEW-9"), ParentID: &other.ID, AddBlockingIDs: []string{dependency.ID}})
	require.NoError(t, err)
	assert.Equal(t, "Changed", updated.Title)
	assert.Equal(t, "New description", updated.Description)
	assert.Equal(t, "New design", updated.Design)
	assert.Equal(t, "New acceptance", updated.AcceptanceCriteria)
	assert.Equal(t, "NEW-9", updated.ExternalRef)
	assert.Equal(t, "operator", updated.Owner)
	assert.Equal(t, other.ID, updated.Relations.ParentID)
	assert.Equal(t, []string{dependency.ID}, updated.Relations.DependencyIDs)
	require.NoError(t, backend.SetPRURL(t.Context(), item.ID, "url"))
	withNative(t, dir, func(store *sdk.Store) {
		native, err := store.Get(item.ID)
		require.NoError(t, err)
		var raw map[string]json.RawMessage
		require.NoError(t, json.Unmarshal([]byte(native.Metadata), &raw))
		assert.JSONEq(t, `{"nested":[1,true]}`, string(raw["unrelated"]))
		assert.Contains(t, string(raw["orpheus.gig"]), `"future":"keep"`)
		dependencies, err := store.ListDependencies(item.ID)
		require.NoError(t, err)
		assert.Len(t, dependencies, 2, "non-blocking relationship survives other edits")
	})
	_, err = backend.Update(t.Context(), task.UpdateOptions{ID: item.ID, Title: stringPtr("Must fail preflight"), AddBlockingIDs: []string{parent.ID}})
	assert.ErrorIs(t, err, task.ErrMutationConflict)
	assert.Equal(t, "Changed", getItem(t, backend, item.ID).Title)
	_, err = backend.Update(t.Context(), task.UpdateOptions{ID: item.ID, RemoveBlockingIDs: []string{parent.ID}})
	assert.ErrorIs(t, err, task.ErrMutationConflict)
	withNative(t, dir, func(store *sdk.Store) {
		edges, err := store.ListDependencies(item.ID)
		require.NoError(t, err)
		require.Len(t, edges, 2)
	})
	_, err = backend.Update(t.Context(), task.UpdateOptions{ID: other.ID, ParentID: &other.ID})
	assert.ErrorIs(t, err, task.ErrMutationConflict)
	_, err = backend.Update(t.Context(), task.UpdateOptions{ID: item.ID, ParentID: stringPtr(""), ExternalRef: stringPtr(""), AddBlockingIDs: []string{dependency.ID}})
	require.NoError(t, err)
	_, err = backend.Update(t.Context(), task.UpdateOptions{ID: item.ID, RemoveBlockingIDs: []string{dependency.ID}})
	require.NoError(t, err)
	_, err = backend.Update(t.Context(), task.UpdateOptions{ID: item.ID, RemoveBlockingIDs: []string{dependency.ID}})
	require.NoError(t, err)
	final := getItem(t, backend, item.ID)
	assert.Empty(t, final.Relations.ParentID)
	assert.Empty(t, final.ExternalRef)
	assert.Empty(t, final.Relations.DependencyIDs)
	before, err := backend.List(t.Context())
	require.NoError(t, err)
	_, err = backend.Create(t.Context(), task.CreateOptions{Title: "Fail", Description: "Description", AcceptanceCriteria: "Acceptance", IssueType: task.IssueTypeTask, BlockingIDs: []string{"op-missing"}})
	assert.ErrorIs(t, err, task.ErrNotFound)
	after, err := backend.List(t.Context())
	require.NoError(t, err)
	assert.Len(t, after, len(before), "missing references must fail before creation")
}

func TestIntegrationAdapterContractGigFiltersAndMissingItems(t *testing.T) {
	backend, dir := newSource(t)
	before := time.Now().Add(-time.Minute)
	epic := createItem(t, backend, "Search epic", task.IssueTypeEpic, "")
	item := createItem(t, backend, "Search child", task.IssueTypeTask, epic.ID)
	require.NoError(t, backend.Close(t.Context(), item.ID))
	after := time.Now().Add(time.Minute)
	cases := []struct {
		filter task.ListFilter
		ids    []string
	}{
		{task.ListFilter{Query: "sEaRcH", IssueTypes: []task.IssueType{task.IssueTypeTask}}, []string{item.ID}},
		{task.ListFilter{Query: item.ID}, []string{item.ID}},
		{task.ListFilter{Query: "Description"}, nil},
		{task.ListFilter{ParentID: epic.ID, CreatedAfter: &before, CreatedBefore: &after, UpdatedAfter: &before, UpdatedBefore: &after}, []string{item.ID}},
		{task.ListFilter{CreatedAfter: item.CreatedAt, IssueTypes: []task.IssueType{task.IssueTypeTask}}, nil},
	}
	for _, tc := range cases {
		items, err := backend.ListFiltered(t.Context(), tc.filter)
		require.NoError(t, err)
		var ids []string
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		assert.Equal(t, tc.ids, ids)
	}
	withNative(t, dir, func(store *sdk.Store) {
		bug, err := store.Create(sdk.CreateParams{Title: "Native bug", Type: sdk.TypeBug})
		require.NoError(t, err)
		_, err = backend.Get(t.Context(), bug.ID)
		assert.ErrorIs(t, err, task.ErrUnsupportedTaskSourceItem)
	})
	items, err := backend.List(t.Context())
	require.NoError(t, err)
	assert.Len(t, items, 2)
	assert.ErrorIs(t, backend.Close(t.Context(), "op-missing"), task.ErrNotFound)
	_, err = backend.Get(t.Context(), "op-missing")
	assert.ErrorIs(t, err, task.ErrNotFound)
}

func TestIntegrationAdapterContractGigInitializationRejectsPercentPathsWithoutTouchingDecodedStore(t *testing.T) {
	for _, names := range []struct{ encoded, decoded string }{
		{"repo%41", "repoA"},
		{"repo%20name", "repo name"},
	} {
		t.Run(names.encoded, func(t *testing.T) {
			root := testutil.CanonicalTempDir(t)
			decodedDir := filepath.Join(root, names.decoded, "gig")
			withNative(t, decodedDir, func(store *sdk.Store) {
				_, err := store.Create(sdk.CreateParams{Title: "Existing task"})
				require.NoError(t, err)
				_, err = store.DB().Exec("PRAGMA application_id = 123")
				require.NoError(t, err)
			})
			databasePath := filepath.Join(decodedDir, "tasks.db")
			before, err := os.ReadFile(databasePath)
			require.NoError(t, err)
			literalParent := filepath.Join(root, names.encoded)

			err = gig.Initialize(filepath.Join(literalParent, "gig"), "op")

			assert.ErrorContains(t, err, "%")
			_, err = os.Stat(literalParent)
			assert.ErrorIs(t, err, os.ErrNotExist, "reject before creating any directories")
			after, err := os.ReadFile(databasePath)
			require.NoError(t, err)
			assert.True(t, bytes.Equal(before, after), "decoded database must remain byte-for-byte unchanged, including its ownership marker")
		})
	}
}

func TestIntegrationAdapterContractGigStoreFailuresNeverCreateOrAdoptData(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	dir := filepath.Join(root, "gig")
	backend, err := gig.New(task.RepositorySource{Repository: task.Repository{TaskIDPrefix: "op"}, BackendDir: dir, MaintenanceOwned: true})
	require.NoError(t, err)
	_, err = backend.List(t.Context())
	require.Error(t, err)
	_, err = os.Stat(dir)
	assert.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.Mkdir(dir, 0o700))
	path := filepath.Join(dir, "tasks.db")
	require.NoError(t, os.WriteFile(path, []byte("corrupt"), 0o600))
	_, err = backend.List(t.Context())
	require.Error(t, err)
	require.Error(t, gig.Initialize(dir, "op"))
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "corrupt", string(content))
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	_, err = backend.List(t.Context())
	assert.ErrorContains(t, err, "nonempty regular file")
	assert.Error(t, gig.Initialize(filepath.Join(root, "unsupported?"), "op"))
	_, err = os.Stat(filepath.Join(root, "unsupported?"))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestIntegrationAdapterContractGigCancelledOperationsDoNotMutate(t *testing.T) {
	backend, _ := newSource(t)
	item := createItem(t, backend, "Stored", task.IssueTypeTask, "")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := backend.List(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	assert.ErrorIs(t, backend.MarkInProgress(ctx, item.ID, "branch", "/worktree"), context.Canceled)
	assert.Equal(t, task.StatusOpen, getItem(t, backend, item.ID).Status)
}

func stringPtr(value string) *string { return &value }

func TestIntegrationAdapterContractGigRejectsIncompleteAndMalformedStoreReads(t *testing.T) {
	for name, damage := range map[string]string{
		"missing relationship table": "DROP TABLE dependencies",
		"invalid timestamp":          "UPDATE tasks SET created_at = 'yesterday'",
		"invalid metadata":           "UPDATE tasks SET metadata = 'not-json'",
	} {
		t.Run(name, func(t *testing.T) {
			backend, dir := newSource(t)
			item := createItem(t, backend, "Stored task", task.IssueTypeTask, "")
			withNative(t, dir, func(store *sdk.Store) {
				_, err := store.DB().Exec(damage)
				require.NoError(t, err)
			})
			got, err := backend.Get(t.Context(), item.ID)
			require.Error(t, err)
			assert.NotErrorIs(t, err, task.ErrNotFound)
			assert.Empty(t, got.ID, "failed required reads must not return partial detail")
			items, err := backend.List(t.Context())
			require.Error(t, err)
			assert.Empty(t, items)
		})
	}
}

func TestIntegrationAdapterContractGigRelationshipRetriesRetainTimestamps(t *testing.T) {
	backend, _ := newSource(t)
	dependency := createItem(t, backend, "Dependency", task.IssueTypeTask, "")
	item := createItem(t, backend, "Task", task.IssueTypeTask, "", dependency.ID)
	added, err := backend.Update(t.Context(), task.UpdateOptions{ID: item.ID, AddBlockingIDs: []string{dependency.ID}})
	require.NoError(t, err)
	assert.Equal(t, item.UpdatedAt, added.UpdatedAt)
	removed, err := backend.Update(t.Context(), task.UpdateOptions{ID: item.ID, RemoveBlockingIDs: []string{dependency.ID}})
	require.NoError(t, err)
	retried, err := backend.Update(t.Context(), task.UpdateOptions{ID: item.ID, RemoveBlockingIDs: []string{dependency.ID}})
	require.NoError(t, err)
	assert.Equal(t, removed.UpdatedAt, retried.UpdatedAt)
	assert.Empty(t, retried.Relations.DependencyIDs)
}
