package tasktarget_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
)

func TestClassifyRunTargetRecognizesRepoRootTaskBranch(t *testing.T) {
	repo := task.Repository{Path: "/repo/alpha", DefaultBranch: "main"}

	got := tasktarget.ClassifyRunTarget(repo, "orpheus/op-1", "/repo/alpha")

	if got != tasktarget.TargetRepoRootTeam {
		t.Fatalf("shape target = %q, want %q", got, tasktarget.TargetRepoRootTeam)
	}
}

func TestClassifyRunTargetRemainsShapeOnlyForDiagnostics(t *testing.T) {
	repo := task.Repository{Path: "/repo/alpha", DefaultBranch: "main"}

	got := tasktarget.ClassifyRunTarget(repo, "manual/op-1", "/tmp/manual-worktree")

	if got != tasktarget.TargetWorktreeTeam {
		t.Fatalf("shape target = %q, want %q", got, tasktarget.TargetWorktreeTeam)
	}
}

func TestClassifyMetadataTargetMatchesExpectedTargets(t *testing.T) {
	targets := testExpectedTargets()
	metadata := task.OrpheusMetadata{
		Branch:      "orpheus/op-1",
		HasBranch:   true,
		Worktree:    "/state/worktrees/alpha/op-1",
		HasWorktree: true,
	}

	got, err := tasktarget.ClassifyMetadataTarget(metadata, targets)
	if err != nil {
		t.Fatalf("classify metadata target: %v", err)
	}
	if got != targets.WorktreeTeam {
		t.Fatalf("target = %#v, want %#v", got, targets.WorktreeTeam)
	}
}

func TestClassifyTaskStateTargetMatchesExpectedTargets(t *testing.T) {
	targets := testExpectedTargets()
	taskTarget := taskstate.TaskTarget{Branch: "orpheus/op-1", Worktree: "/repo/alpha"}

	got, err := tasktarget.ClassifyTaskStateTarget(taskTarget, targets)
	if err != nil {
		t.Fatalf("classify taskstate target: %v", err)
	}
	if got != targets.RepoRootTeam {
		t.Fatalf("target = %#v, want %#v", got, targets.RepoRootTeam)
	}
}

func testExpectedTargets() tasktarget.ExpectedTargets {
	return tasktarget.ExpectedTargets{
		MainSolo: tasktarget.Target{
			Kind:     tasktarget.TargetMainSolo,
			Branch:   "main",
			Worktree: "/repo/alpha",
		},
		WorktreeTeam: tasktarget.Target{
			Kind:     tasktarget.TargetWorktreeTeam,
			Branch:   "orpheus/op-1",
			Worktree: "/state/worktrees/alpha/op-1",
		},
		RepoRootTeam: tasktarget.Target{
			Kind:     tasktarget.TargetRepoRootTeam,
			Branch:   "orpheus/op-1",
			Worktree: "/repo/alpha",
		},
	}
}
