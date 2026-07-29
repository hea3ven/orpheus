//go:build integration

package beads_test

import (
	"context"
	"os/exec"
	"reflect"
	"testing"

	"github.com/hea3ven/orpheus/internal/beads"
	"github.com/hea3ven/orpheus/internal/task"
)

func TestIntegrationTaskBackendCreateRecordsBlockingDependencies(t *testing.T) {
	binary, err := exec.LookPath("bd")
	if err != nil {
		t.Skip("bd executable is required for Beads integration test")
	}

	dir := t.TempDir()
	t.Setenv("BEADS_DIR", "")
	initCommand := exec.Command(binary, "init", "--prefix", "it", "--non-interactive", "--skip-agents", "--skip-hooks", "--quiet")
	initCommand.Dir = dir
	if output, err := initCommand.CombinedOutput(); err != nil {
		t.Fatalf("initialize Beads workspace: %v\n%s", err, output)
	}

	backend, err := beads.NewTaskBackendWithRunner(dir, beads.CommandRunner{Binary: binary})
	if err != nil {
		t.Fatalf("create backend: %v", err)
	}
	blocker, err := backend.Create(context.Background(), task.CreateOptions{
		Title: "Blocker", Description: "Blocks the dependent item.", AcceptanceCriteria: "Exists.", IssueType: task.IssueTypeTask,
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	created, err := backend.Create(context.Background(), task.CreateOptions{
		Title: "Blocked", Description: "Depends on the blocker.", AcceptanceCriteria: "Exists.", IssueType: task.IssueTypeTask,
		BlockingIDs: []string{blocker.ID},
	})
	if err != nil {
		t.Fatalf("create blocked item: %v", err)
	}

	got, err := backend.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("inspect created item: %v", err)
	}
	if !reflect.DeepEqual(got.Relations.DependencyIDs, []string{blocker.ID}) {
		t.Fatalf("created dependencies = %#v, want %#v", got.Relations.DependencyIDs, []string{blocker.ID})
	}
}
