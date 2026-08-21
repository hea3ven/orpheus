package tasktarget_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestExpectedTargetsForTaskItemRendersConfiguredTemplate(t *testing.T) {
	paths, err := state.NewPaths(testutil.CanonicalTempDir(t), testutil.CanonicalTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	repo := task.Repository{
		ID: "alpha", Name: "Alpha", Path: filepath.Join(testutil.CanonicalTempDir(t), "repo"), DefaultBranch: "main",
		BranchTemplate: "feature/{{external_ref}}/{{task_title}}",
	}
	taskItem := task.Task{ID: "op-7", ExternalRef: "PROJ 7", Title: "Ship the thing"}
	targets, err := tasktarget.ExpectedTargetsForTaskItem(repo, taskItem, paths)
	if err != nil {
		t.Fatal(err)
	}
	want := "feature/PROJ-7/Ship-the-thing"
	if targets.WorktreeTeam.Branch != want || targets.RepoRootTeam.Branch != want {
		t.Fatalf("targets = %#v, want branch %q", targets, want)
	}
}

func TestExpectedTargetsForTaskItemRejectsRenderedDefaultBranch(t *testing.T) {
	paths, err := state.NewPaths(testutil.CanonicalTempDir(t), testutil.CanonicalTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name     string
		template string
		taskItem task.Task
	}{
		{
			name:     "literal",
			template: "main",
			taskItem: task.Task{ID: "op-7"},
		},
		{
			name:     "task title placeholder",
			template: "{{task_title}}",
			taskItem: task.Task{ID: "op-7", Title: "main"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := task.Repository{
				ID: "alpha", Name: "Alpha", Path: filepath.Join(testutil.CanonicalTempDir(t), "repo"), DefaultBranch: "main",
				BranchTemplate: tt.template,
			}
			_, err := tasktarget.ExpectedTargetsForTaskItem(repo, tt.taskItem, paths)
			if err == nil || !strings.Contains(err.Error(), "matches registered default branch") {
				t.Fatalf("ExpectedTargetsForTaskItem() error = %v, want default branch rejection", err)
			}
		})
	}
}

func TestExpectedTargetsForTaskOrRecordedBranchUsesBackendMetadataAfterLocalDefault(t *testing.T) {
	paths, err := state.NewPaths(testutil.CanonicalTempDir(t), testutil.CanonicalTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	repo := task.Repository{
		ID: "alpha", Name: "Alpha", Path: filepath.Join(testutil.CanonicalTempDir(t), "repo"), DefaultBranch: "main",
		BranchTemplate: "changed/{{task_title}}",
	}
	taskItem := task.Task{
		ID: "op-7", Title: "Original branch",
		Metadata: task.Metadata{task.MetadataBranch: "feature/Original-branch"},
	}
	targets, err := tasktarget.ExpectedTargetsForTaskOrRecordedBranch(repo, taskItem, "main", paths)
	if err != nil {
		t.Fatal(err)
	}
	if targets.RepoRootTeam.Branch != "feature/Original-branch" {
		t.Fatalf("repo-root target = %#v, want backend-recorded branch", targets.RepoRootTeam)
	}
}

func TestExpectedTargetsForTaskBranchPreservesRecordedBranch(t *testing.T) {
	paths, err := state.NewPaths(testutil.CanonicalTempDir(t), testutil.CanonicalTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	repo := task.Repository{
		ID: "alpha", Name: "Alpha", Path: filepath.Join(testutil.CanonicalTempDir(t), "repo"), DefaultBranch: "main",
		BranchTemplate: "changed/{{task_title}}",
	}
	targets, err := tasktarget.ExpectedTargetsForTaskBranch(repo, "op-7", "recorded/branch", paths)
	if err != nil {
		t.Fatal(err)
	}
	if targets.WorktreeTeam.Branch != "recorded/branch" || targets.RepoRootTeam.Branch != "recorded/branch" {
		t.Fatalf("targets = %#v, want recorded branch", targets)
	}
}
