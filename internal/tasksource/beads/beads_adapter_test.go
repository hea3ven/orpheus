//go:build integration

package beads_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/tasksource/beads"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdapterContractBeadsRelationshipContracts(t *testing.T) {
	dir, runner := initializeBeadsWorkspace(t)
	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	fixture := beadsRelationshipFixture{backend: backend, runner: runner, dir: dir}
	// Cases own disjoint task IDs and assert only their own graph. Map iteration
	// varies their order; keep them serial to avoid concurrent database writers.
	for name, run := range map[string]func(*testing.T, beadsRelationshipFixture){
		"TaskBackendCrossRepositoryParents":                            checkCrossRepositoryParents,
		"TaskBackendCrossRepositoryReferences":                         checkCrossRepositoryReferences,
		"TaskBackendReadsParentRelationship":                           checkParentRelationshipReadback,
		"TaskBackendSupportsCrossTypeBlockingDependencies":             checkCrossTypeBlockingDependencies,
		"TaskBackendDoesNotRemoveRelatedDependency":                    checkRelatedDependencyPreserved,
		"TaskBackendRejectsNonBlockingDependencyBeforeContentMutation": checkRejectionBeforeContentMutation,
		"TaskBackendCreateRecordsBlockingDependencies":                 checkCreateWithBlockingDependencies,
		"TaskBackendRejectsIncompleteExternalDependencyRead":           checkIncompleteExternalDependencyRead,
	} {
		t.Run(name, func(t *testing.T) { run(t, fixture) })
	}
}

type beadsRelationshipFixture struct {
	backend beads.TaskBackend
	runner  beads.CommandRunner
	dir     string
}

func checkParentRelationshipReadback(t *testing.T, fixture beadsRelationshipFixture) {
	t.Helper()
	parent, err := fixture.backend.Create(context.Background(), task.CreateOptions{
		Title: t.Name() + "/Parent epic", Description: "Parent description.", AcceptanceCriteria: "Parent exists.", IssueType: task.IssueTypeEpic,
	})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child, err := fixture.backend.Create(context.Background(), task.CreateOptions{
		Title: t.Name() + "/Child epic", Description: "Child description.", AcceptanceCriteria: "Child exists.", IssueType: task.IssueTypeEpic, ParentID: parent.ID,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	got, err := fixture.backend.Get(context.Background(), child.ID)
	if err != nil {
		t.Fatalf("inspect child: %v", err)
	}
	if got.Relations.ParentID != parent.ID {
		t.Fatalf("parent relation = %q, want %q", got.Relations.ParentID, parent.ID)
	}
	if len(got.RelatedItems) != 1 || got.RelatedItems[0].ID != parent.ID || got.RelatedItems[0].Title != parent.Title {
		t.Fatalf("related parent = %#v", got.RelatedItems)
	}
	parentDetail, err := fixture.backend.Get(context.Background(), parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !parentDetail.Relations.Complete || !reflect.DeepEqual(parentDetail.Relations.ChildIDs, []string{child.ID}) || len(parentDetail.Relations.DependentIDs) != 0 {
		t.Fatalf("parent relationships = %#v", parentDetail.Relations)
	}
	if len(parentDetail.RelatedItems) != 1 || parentDetail.RelatedItems[0].ID != child.ID || parentDetail.RelatedItems[0].Relations.ParentID != parent.ID {
		t.Fatalf("related child = %#v", parentDetail.RelatedItems)
	}
}

func checkCrossTypeBlockingDependencies(t *testing.T, fixture beadsRelationshipFixture) {
	t.Helper()
	create := func(title string, issueType task.IssueType) task.Task {
		t.Helper()
		created, err := fixture.backend.Create(context.Background(), task.CreateOptions{
			Title: t.Name() + "/" + title, Description: title + " description.", AcceptanceCriteria: title + " exists.", IssueType: issueType,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return created
	}
	taskToEpic := create("Task depending on epic", task.IssueTypeTask)
	epicTarget := create("Epic dependency", task.IssueTypeEpic)
	epicToTask := create("Epic depending on task", task.IssueTypeEpic)
	taskTarget := create("Task dependency", task.IssueTypeTask)

	for _, test := range []struct {
		name       string
		itemID     string
		dependency string
	}{
		{"task to epic", taskToEpic.ID, epicTarget.ID},
		{"epic to task", epicToTask.ID, taskTarget.ID},
	} {
		t.Run(test.name, func(t *testing.T) {
			updated, err := fixture.backend.Update(context.Background(), task.UpdateOptions{
				ID: test.itemID, AddBlockingIDs: []string{test.dependency},
			})
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if !reflect.DeepEqual(updated.Relations.DependencyIDs, []string{test.dependency}) {
				t.Fatalf("dependencies = %#v, want %#v", updated.Relations.DependencyIDs, []string{test.dependency})
			}
			target, err := fixture.backend.Get(context.Background(), test.dependency)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(target.Relations.DependentIDs, []string{test.itemID}) || len(target.Relations.ChildIDs) != 0 {
				t.Fatalf("incoming relationships = %#v", target.Relations)
			}
		})
	}
}

func checkRelatedDependencyPreserved(t *testing.T, fixture beadsRelationshipFixture) {
	t.Helper()
	create := func(title string) task.Task {
		t.Helper()
		created, err := fixture.backend.Create(context.Background(), task.CreateOptions{
			Title: t.Name() + "/" + title, Description: title + " description.", AcceptanceCriteria: title + " exists.", IssueType: task.IssueTypeTask,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return created
	}
	current := create("Related source")
	related := create("Related target")
	if result, err := fixture.runner.Run(fixture.dir, "dep", "add", current.ID, related.ID, "--type", "related"); err != nil {
		t.Fatalf("add related dependency: %v\n%s", err, result.Stderr)
	}

	if _, err := fixture.backend.Update(context.Background(), task.UpdateOptions{
		ID: current.ID, RemoveBlockingIDs: []string{related.ID},
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	result, err := fixture.runner.Run(fixture.dir, "dep", "list", current.ID, "--json")
	if err != nil {
		t.Fatalf("list dependencies: %v", err)
	}
	var dependencies []struct {
		ID             string `json:"id"`
		DependencyType string `json:"dependency_type"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &dependencies); err != nil {
		t.Fatalf("parse dependencies: %v\n%s", err, result.Stdout)
	}
	if !reflect.DeepEqual(dependencies, []struct {
		ID             string `json:"id"`
		DependencyType string `json:"dependency_type"`
	}{{ID: related.ID, DependencyType: "related"}}) {
		t.Fatalf("dependencies = %#v, want related edge to %q", dependencies, related.ID)
	}
}

func checkRejectionBeforeContentMutation(t *testing.T, fixture beadsRelationshipFixture) {
	t.Helper()
	create := func(title string) task.Task {
		t.Helper()
		created, err := fixture.backend.Create(context.Background(), task.CreateOptions{
			Title: t.Name() + "/" + title, Description: title + " description.", AcceptanceCriteria: title + " exists.", IssueType: task.IssueTypeTask,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		return created
	}
	current := create("Related source")
	related := create("Related target")
	if result, err := fixture.runner.Run(fixture.dir, "dep", "add", current.ID, related.ID, "--type", "related"); err != nil {
		t.Fatalf("add related dependency: %v\n%s", err, result.Stderr)
	}

	updatedTitle := "Updated title"
	_, err := fixture.backend.Update(context.Background(), task.UpdateOptions{
		ID:             current.ID,
		Title:          &updatedTitle,
		AddBlockingIDs: []string{related.ID},
	})
	if err == nil {
		t.Fatal("Update() succeeded, want non-blocking relationship rejection")
	}

	unchanged, err := fixture.backend.Get(context.Background(), current.ID)
	if err != nil {
		t.Fatalf("inspect task after rejected update: %v", err)
	}
	if unchanged.Title != current.Title {
		t.Fatalf("title = %q, want unchanged %q", unchanged.Title, current.Title)
	}
}

func checkCreateWithBlockingDependencies(t *testing.T, fixture beadsRelationshipFixture) {
	t.Helper()
	blocker, err := fixture.backend.Create(context.Background(), task.CreateOptions{
		Title: t.Name() + "/Blocker", Description: "Blocks the dependent item.", AcceptanceCriteria: "Exists.", IssueType: task.IssueTypeTask,
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	created, err := fixture.backend.Create(context.Background(), task.CreateOptions{
		Title: t.Name() + "/Blocked", Description: "Depends on the blocker.", AcceptanceCriteria: "Exists.", IssueType: task.IssueTypeTask,
		BlockingIDs: []string{blocker.ID},
	})
	if err != nil {
		t.Fatalf("create blocked item: %v", err)
	}

	got, err := fixture.backend.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("inspect created item: %v", err)
	}
	if !reflect.DeepEqual(got.Relations.DependencyIDs, []string{blocker.ID}) {
		t.Fatalf("created dependencies = %#v, want %#v", got.Relations.DependencyIDs, []string{blocker.ID})
	}
}

func TestIntegrationAdapterContractManagedTaskBackendRepairsRealBeadsSchemaDrift(t *testing.T) {
	dolt, err := exec.LookPath("dolt")
	if err != nil {
		t.Skip("dolt executable is required to prepare stale Beads schema")
	}

	// Schema repair is destructive and must not share the healthy relationship workspace.
	dir, runner := initializeBeadsWorkspace(t)

	writer, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create task writer: %v", err)
	}
	created, err := writer.Create(context.Background(), task.CreateOptions{
		Title: "Retained across schema repair", Description: "Schema migration must not alter task content.",
		AcceptanceCriteria: "Task remains readable.", IssueType: task.IssueTypeTask,
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}

	databaseDir := dir + "/.beads/embeddeddolt/it"
	for _, args := range [][]string{
		{"config", "--local", "--add", "user.name", "Orpheus Tests"},
		{"config", "--local", "--add", "user.email", "tests@orpheus.invalid"},
		{"sql", "-q", "DELETE FROM schema_migrations WHERE version = (SELECT MAX(version) FROM schema_migrations)"},
		{"add", "schema_migrations"},
		{"commit", "-m", "test: stale schema"},
	} {
		command := exec.Command(dolt, args...)
		command.Dir = databaseDir
		command.Env = runner.Environment
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("prepare stale schema with dolt %v: %v\n%s", args, err, output)
		}
	}

	backend, err := beads.NewTaskBackendForSourceWithRunner(task.RepositorySource{
		Repository:       task.Repository{ID: "integration", TaskIDPrefix: "it", Path: dir},
		BackendDir:       dir,
		MaintenanceOwned: true,
	}, runner, nil)
	if err != nil {
		t.Fatalf("create managed backend: %v", err)
	}
	tasks, err := backend.List(context.Background())
	if err != nil {
		t.Fatalf("list repaired tasks: %v", err)
	}
	if len(tasks) != 1 || tasks[0].ID != created.ID || tasks[0].Title != created.Title {
		t.Fatalf("tasks after schema repair = %#v, want retained task %q", tasks, created.ID)
	}
}

func initializeBeadsWorkspace(t *testing.T) (string, beads.CommandRunner) {
	t.Helper()
	return initializeBeadsWorkspaceWithPrefix(t, "it")
}

func initializeBeadsWorkspaceWithPrefix(t *testing.T, prefix string) (string, beads.CommandRunner) {
	t.Helper()
	binary, err := exec.LookPath("bd")
	if err != nil {
		t.Skip("bd executable is required for Beads integration test")
	}
	dir := testutil.CanonicalTempDir(t)
	runner := isolatedBeadsRunner(t, binary)
	if result, err := runner.Run(dir, "init", "--prefix", prefix, "--non-interactive", "--skip-agents", "--skip-hooks", "--quiet"); err != nil {
		t.Fatalf("initialize Beads workspace: %v\n%s\n%s", err, result.Stdout, result.Stderr)
	}
	return dir, runner
}

func checkIncompleteExternalDependencyRead(t *testing.T, fixture beadsRelationshipFixture) {
	t.Helper()
	item, err := fixture.backend.Create(context.Background(), task.CreateOptions{
		Title: t.Name(), Description: "Reject omitted external dependencies.", AcceptanceCriteria: "Incomplete reads return an error.", IssueType: task.IssueTypeTask,
	})
	if err != nil {
		t.Fatal(err)
	}
	const reference = "external:other:missing"
	if result, err := fixture.runner.Run(fixture.dir, "dep", "add", item.ID, reference); err != nil {
		t.Fatalf("add external dependency: %v: %s", err, result.Stderr)
	}

	got, err := fixture.backend.Get(context.Background(), item.ID)

	if err == nil || !strings.Contains(err.Error(), "incomplete dependencies: source reports 1 but returned 0") {
		t.Fatalf("Get() error = %v, want incomplete dependencies", err)
	}
	if !reflect.DeepEqual(got, task.Task{}) {
		t.Fatalf("Get() = %#v, want no task on incomplete read", got)
	}
}

func checkCrossRepositoryReferences(t *testing.T, fixture beadsRelationshipFixture) {
	dir, runner := initializeBeadsWorkspaceWithPrefix(t, "other")
	owner, err := beads.NewTaskBackendWithRunner(dir, runner)
	require.NoError(t, err)
	prerequisite, err := owner.Create(t.Context(), task.CreateOptions{Title: "Remote epic", Description: "Plan", AcceptanceCriteria: "Done", IssueType: task.IssueTypeEpic})
	require.NoError(t, err)
	consumer, err := fixture.backend.Create(t.Context(), task.CreateOptions{Title: t.Name(), Description: "Plan", AcceptanceCriteria: "Done", BlockingIDs: []string{prerequisite.ID}, CrossRepositoryBlockingIDs: []string{prerequisite.ID}})
	require.NoError(t, err)

	for range 2 {
		updated, err := fixture.backend.Update(t.Context(), task.UpdateOptions{ID: consumer.ID, AddBlockingIDs: []string{prerequisite.ID}, CrossRepositoryBlockingIDs: []string{prerequisite.ID}})
		require.NoError(t, err)
		assert.Equal(t, []string{prerequisite.ID}, updated.Relations.DependencyIDs)
		assert.Empty(t, updated.RelatedItems, "consumer must not invent a cached remote lifecycle row")
	}
	listed, err := fixture.backend.List(t.Context())
	require.NoError(t, err)
	for _, item := range listed {
		if item.ID == consumer.ID {
			assert.Equal(t, []string{prerequisite.ID}, item.Relations.DependencyIDs)
		}
	}
	remote, err := owner.Get(t.Context(), prerequisite.ID)
	require.NoError(t, err)
	assert.Equal(t, task.StatusOpen, remote.Status)
	require.NoError(t, owner.StartEpic(t.Context(), prerequisite.ID))
	require.NoError(t, owner.Close(t.Context(), prerequisite.ID))
	remote, err = owner.Get(t.Context(), prerequisite.ID)
	require.NoError(t, err)
	assert.Equal(t, task.StatusClosed, remote.Status)
	retained, err := fixture.backend.Get(t.Context(), consumer.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{prerequisite.ID}, retained.Relations.DependencyIDs)
	_, err = fixture.backend.Get(t.Context(), prerequisite.ID)
	assert.ErrorIs(t, err, task.ErrNotFound, "no proxy item is created in the consuming source")

	// Removal is independent of the owning source, even after it is unavailable.
	require.NoError(t, os.RemoveAll(dir))
	for range 2 {
		updated, err := fixture.backend.Update(t.Context(), task.UpdateOptions{ID: consumer.ID, RemoveBlockingIDs: []string{prerequisite.ID}})
		require.NoError(t, err)
		assert.Empty(t, updated.Relations.DependencyIDs)
	}
}

func checkCrossRepositoryParents(t *testing.T, fixture beadsRelationshipFixture) {
	t.Helper()
	parent, err := fixture.backend.Create(t.Context(), task.CreateOptions{Title: t.Name() + "/Local", Description: "Plan", AcceptanceCriteria: "Done", IssueType: task.IssueTypeEpic})
	require.NoError(t, err)
	child, err := fixture.backend.Create(t.Context(), task.CreateOptions{Title: t.Name() + "/Child", Description: "Plan", AcceptanceCriteria: "Done", IssueType: task.IssueTypeEpic, ParentID: "remote-epic", CrossRepositoryParent: true})
	require.NoError(t, err)
	reopened, err := beads.NewTaskBackendWithRunner(fixture.dir, fixture.runner)
	require.NoError(t, err)
	for _, parentID := range []string{"remote-epic", parent.ID, "remote-other", ""} {
		if parentID != "remote-epic" {
			_, err := reopened.Update(t.Context(), task.UpdateOptions{ID: child.ID, ParentID: &parentID, CrossRepositoryParent: strings.HasPrefix(parentID, "remote-")})
			require.NoError(t, err)
		}
		got, err := reopened.Get(t.Context(), child.ID)
		require.NoError(t, err)
		assert.Equal(t, child.ID, got.ID)
		assert.Equal(t, parentID, got.Relations.ParentID)
		listed, err := reopened.ListFiltered(t.Context(), task.ListFilter{ParentID: parentID})
		require.NoError(t, err)
		assert.True(t, slices.ContainsFunc(listed, func(item task.Task) bool { return item.ID == child.ID }))
		local, err := reopened.Get(t.Context(), parent.ID)
		require.NoError(t, err)
		assert.Equal(t, parentID == parent.ID, slices.Contains(local.Relations.ChildIDs, child.ID))
	}
}
