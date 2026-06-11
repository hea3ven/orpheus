package workflow_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

func TestClassifyExpectedCompletionTarget(t *testing.T) {
	targets := workflow.ExpectedTargets{
		MainSolo: workflow.Target{
			Kind:     workflow.TargetMainSolo,
			Branch:   "main",
			Worktree: "/repo/alpha",
		},
		WorktreeTeam: workflow.Target{
			Kind:     workflow.TargetWorktreeTeam,
			Branch:   "orpheus/op-1",
			Worktree: "/state/worktrees/alpha/op-1",
		},
	}

	tests := []struct {
		name          string
		taskItem      task.Task
		run           taskstate.RunAttempt
		wantOK        bool
		wantTarget    workflow.TargetKind
		wantLifecycle workflow.ReviewLifecycle
	}{
		{
			name: "main solo local ready",
			taskItem: task.Task{
				ID:       "op-1",
				Metadata: task.Metadata{task.MetadataBranch: "main", task.MetadataWorktree: "/repo/alpha"},
			},
			run: taskstate.RunAttempt{
				Status:   taskstate.RunStatusSucceeded,
				Branch:   "main",
				Worktree: "/repo/alpha",
				Completion: &taskstate.Completion{
					Summary:             "Done",
					Description:         "Done.",
					DetailedDescription: "Detailed PR body.",
				},
			},
			wantOK:        true,
			wantTarget:    workflow.TargetMainSolo,
			wantLifecycle: workflow.ReviewLifecycleLocalReady,
		},
		{
			name: "worktree team PR ready",
			taskItem: task.Task{
				ID: "op-1",
				Metadata: task.Metadata{
					task.MetadataBranch:   "orpheus/op-1",
					task.MetadataWorktree: "/state/worktrees/alpha/op-1",
				},
			},
			run: taskstate.RunAttempt{
				Status:   taskstate.RunStatusSucceeded,
				Branch:   "orpheus/op-1",
				Worktree: "/state/worktrees/alpha/op-1",
				Completion: &taskstate.Completion{Summary: "Done", Description: "Done.",
					DetailedDescription: "Detailed PR body.", Commit: "abc123"},
			},
			wantOK:        true,
			wantTarget:    workflow.TargetWorktreeTeam,
			wantLifecycle: workflow.ReviewLifecyclePRReady,
		},
		{
			name: "metadata matches expected but run branch differs",
			taskItem: task.Task{
				ID: "op-1",
				Metadata: task.Metadata{
					task.MetadataBranch:   "orpheus/op-1",
					task.MetadataWorktree: "/state/worktrees/alpha/op-1",
				},
			},
			run: taskstate.RunAttempt{
				Status:   taskstate.RunStatusSucceeded,
				Branch:   "manual/op-1",
				Worktree: "/state/worktrees/alpha/op-1",
				Completion: &taskstate.Completion{Summary: "Done", Description: "Done.",
					DetailedDescription: "Detailed PR body.", Commit: "abc123"},
			},
		},
		{
			name: "non deterministic branch and worktree are not strict PR ready",
			taskItem: task.Task{
				ID: "op-1",
				Metadata: task.Metadata{
					task.MetadataBranch:   "manual/op-1",
					task.MetadataWorktree: "/tmp/manual-worktree",
				},
			},
			run: taskstate.RunAttempt{
				Status:   taskstate.RunStatusSucceeded,
				Branch:   "manual/op-1",
				Worktree: "/tmp/manual-worktree",
				Completion: &taskstate.Completion{Summary: "Done", Description: "Done.",
					DetailedDescription: "Detailed PR body.", Commit: "abc123"},
			},
		},
		{
			name: "PR URL suppresses lifecycle classification",
			taskItem: task.Task{
				ID: "op-1",
				Metadata: task.Metadata{
					task.MetadataBranch:   "orpheus/op-1",
					task.MetadataWorktree: "/state/worktrees/alpha/op-1",
					task.MetadataPRURL:    "https://example.test/pr/1",
				},
			},
			run: taskstate.RunAttempt{
				Status:   taskstate.RunStatusSucceeded,
				Branch:   "orpheus/op-1",
				Worktree: "/state/worktrees/alpha/op-1",
				Completion: &taskstate.Completion{Summary: "Done", Description: "Done.",
					DetailedDescription: "Detailed PR body.", Commit: "abc123"},
			},
		},
		{
			name: "missing completion is not classified",
			taskItem: task.Task{
				ID:       "op-1",
				Metadata: task.Metadata{task.MetadataBranch: "main", task.MetadataWorktree: "/repo/alpha"},
			},
			run: taskstate.RunAttempt{
				Status:   taskstate.RunStatusSucceeded,
				Branch:   "main",
				Worktree: "/repo/alpha",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := workflow.ClassifyExpectedCompletionTarget(targets, tt.taskItem, &tt.run)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v; classification = %#v", ok, tt.wantOK, got)
			}
			if !tt.wantOK {
				return
			}
			if got.Target.Kind != tt.wantTarget || got.Lifecycle != tt.wantLifecycle {
				t.Fatalf("classification = %#v, want target %q lifecycle %q", got, tt.wantTarget, tt.wantLifecycle)
			}
		})
	}
}

func TestClassifyRunTargetRemainsShapeOnlyForDiagnostics(t *testing.T) {
	repo := task.Repository{Path: "/repo/alpha", DefaultBranch: "main"}

	got := workflow.ClassifyRunTarget(repo, "manual/op-1", "/tmp/manual-worktree")

	if got != workflow.TargetWorktreeTeam {
		t.Fatalf("shape target = %q, want %q", got, workflow.TargetWorktreeTeam)
	}
}
