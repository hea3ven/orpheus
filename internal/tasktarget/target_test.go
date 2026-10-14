package tasktarget_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
)

func TestClassifyRunTargetDescribesShapeWithoutAuthorizingTarget(t *testing.T) {
	repo := task.Repository{Path: "/repo/alpha", DefaultBranch: "main"}
	for _, tc := range []struct {
		name, branch, worktree string
		want                   tasktarget.TargetKind
	}{
		{"main", "main", "/repo/alpha", tasktarget.TargetMainSolo},
		{"repository task branch", "orpheus/op-1", "/repo/alpha", tasktarget.TargetRepoRootTeam},
		{"managed worktree", "orpheus/op-1", "/state/worktrees/alpha/op-1", tasktarget.TargetWorktreeTeam},
		{"unmanaged diagnostic shape", "manual/op-1", "/fixture/manual-worktree", tasktarget.TargetWorktreeTeam},
		{"default branch outside root", "main", "/fixture/manual-worktree", tasktarget.TargetUnknown},
		{"missing branch", "", "/repo/alpha", tasktarget.TargetUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tasktarget.ClassifyRunTarget(repo, tc.branch, tc.worktree); got != tc.want {
				t.Fatalf("shape = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestStrictTargetClassifiersMatchOnlyExpectedBranchAndLocation(t *testing.T) {
	targets := testExpectedTargets()
	for _, tc := range []struct {
		name, branch, worktree string
		want                   tasktarget.Target
		valid                  bool
	}{
		{"main", "main", "/repo/alpha", targets.MainSolo, true},
		{"worktree", "orpheus/op-1", "/state/worktrees/alpha/op-1", targets.WorktreeTeam, true},
		{"repo root", "orpheus/op-1", "/repo/alpha", targets.RepoRootTeam, true},
		{"manual branch", "manual/op-1", "/state/worktrees/alpha/op-1", tasktarget.Target{}, false},
		{"manual location", "orpheus/op-1", "/fixture/manual-worktree", tasktarget.Target{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metadata := task.OrpheusMetadata{Branch: tc.branch, HasBranch: true, Worktree: tc.worktree, HasWorktree: true}
			fromMetadata, metadataErr := tasktarget.ClassifyMetadataTarget(metadata, targets)
			fromGit, gitErr := tasktarget.ClassifyGitFacts(taskstate.TaskTarget{Branch: tc.branch, Worktree: tc.worktree}, targets)

			if !tc.valid {
				if metadataErr == nil || gitErr == nil {
					t.Fatalf("invalid target accepted: metadata error = %v, Git error = %v", metadataErr, gitErr)
				}
				return
			}
			if metadataErr != nil || gitErr != nil {
				t.Fatalf("classify: metadata error = %v, Git error = %v", metadataErr, gitErr)
			}
			if fromMetadata != tc.want || fromGit != tc.want {
				t.Fatalf("metadata = %#v, Git = %#v, want %#v", fromMetadata, fromGit, tc.want)
			}
		})
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
