//go:build integration

package beads_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"reflect"
	"testing"

	"github.com/hea3ven/orpheus/internal/beads"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestIntegrationBeadsRelationshipContracts(t *testing.T) {
	dir, runner := initializeBeadsWorkspace(t)
	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}

	source := task.RepositorySource{Repository: task.Repository{ID: "integration", TaskIDPrefix: "it", Path: dir}, BackendDir: dir}
	service := task.UpdateService{
		Sources:        []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.UpdateBackend, error) { return backend, nil },
	}

	fixture := beadsRelationshipFixture{backend: backend, runner: runner, source: source, service: service}
	// Cases own disjoint task IDs and assert only their own graph. Map iteration
	// varies their order; keep them serial to avoid concurrent database writers.
	for name, run := range map[string]func(*testing.T, beadsRelationshipFixture){
		"UpdateServiceRejectsRealBeadsParentDescendantCycle":             checkParentDescendantCycle,
		"UpdateServiceSupportsCrossTypeBlockingDependencies":             checkCrossTypeBlockingDependencies,
		"UpdateServiceDoesNotRemoveRelatedDependency":                    checkRelatedDependencyPreserved,
		"UpdateServiceRejectsNonBlockingDependencyBeforeContentMutation": checkRejectionBeforeContentMutation,
		"TaskBackendCreateRecordsBlockingDependencies":                   checkCreateWithBlockingDependencies,
	} {
		t.Run(name, func(t *testing.T) { run(t, fixture) })
	}
}

type beadsRelationshipFixture struct {
	backend beads.TaskBackend
	runner  beads.CommandRunner
	source  task.RepositorySource
	service task.UpdateService
}

func checkParentDescendantCycle(t *testing.T, fixture beadsRelationshipFixture) {
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

	_, err = fixture.service.Update(context.Background(), fixture.source, task.UpdateOptions{ID: parent.ID, ParentID: &child.ID})
	if err == nil {
		t.Fatal("Update() succeeded, want parent-child cycle rejection")
	}

	unchanged, err := fixture.backend.Get(context.Background(), parent.ID)
	if err != nil {
		t.Fatalf("inspect parent after rejected update: %v", err)
	}
	if unchanged.Relations.ParentID != "" {
		t.Fatalf("parent relation = %q, want unchanged root epic", unchanged.Relations.ParentID)
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
			updated, err := fixture.service.Update(context.Background(), fixture.source, task.UpdateOptions{
				ID: test.itemID, AddBlockingIDs: []string{test.dependency},
			})
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if !reflect.DeepEqual(updated.Relations.DependencyIDs, []string{test.dependency}) {
				t.Fatalf("dependencies = %#v, want %#v", updated.Relations.DependencyIDs, []string{test.dependency})
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
	if result, err := fixture.runner.Run(fixture.source.BackendDir, "dep", "add", current.ID, related.ID, "--type", "related"); err != nil {
		t.Fatalf("add related dependency: %v\n%s", err, result.Stderr)
	}

	if _, err := fixture.service.Update(context.Background(), fixture.source, task.UpdateOptions{
		ID: current.ID, RemoveBlockingIDs: []string{related.ID},
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	result, err := fixture.runner.Run(fixture.source.BackendDir, "dep", "list", current.ID, "--json")
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
	if result, err := fixture.runner.Run(fixture.source.BackendDir, "dep", "add", current.ID, related.ID, "--type", "related"); err != nil {
		t.Fatalf("add related dependency: %v\n%s", err, result.Stderr)
	}

	updatedTitle := "Updated title"
	_, err := fixture.service.Update(context.Background(), fixture.source, task.UpdateOptions{
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

func TestIntegrationManagedTaskBackendRepairsRealBeadsSchemaDrift(t *testing.T) {
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
	binary, err := exec.LookPath("bd")
	if err != nil {
		t.Skip("bd executable is required for Beads integration test")
	}
	dir := testutil.CanonicalTempDir(t)
	runner := isolatedBeadsRunner(t, binary)
	if result, err := runner.Run(dir, "init", "--prefix", "it", "--non-interactive", "--skip-agents", "--skip-hooks", "--quiet"); err != nil {
		t.Fatalf("initialize Beads workspace: %v\n%s\n%s", err, result.Stdout, result.Stderr)
	}
	return dir, runner
}
