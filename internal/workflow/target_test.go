package workflow_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/workflow"
)

type classifyExpectedCompletionTargetCase struct {
	name          string
	taskItem      task.Task
	taskTarget    taskstate.TaskTarget
	run           taskstate.RunAttempt
	wantOK        bool
	wantTarget    tasktarget.TargetKind
	wantLifecycle workflow.ReviewLifecycle
}

var classifyExpectedCompletionTargetCases = []classifyExpectedCompletionTargetCase{
	{
		name: "main solo local ready",
		taskItem: task.Task{
			ID:       "op-1",
			Metadata: task.Metadata{task.MetadataBranch: "main", task.MetadataWorktree: "/repo/alpha"},
		},
		taskTarget: taskstate.TaskTarget{Branch: "main", Worktree: "/repo/alpha"},
		run: taskstate.RunAttempt{
			Status: taskstate.RunStatusSucceeded,
			Completion: &taskstate.Completion{
				Summary:             "Done",
				Description:         "Done.",
				DetailedDescription: "Detailed PR body.",
			},
		},
		wantOK:        true,
		wantTarget:    tasktarget.TargetMainSolo,
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
		taskTarget: taskstate.TaskTarget{Branch: "orpheus/op-1", Worktree: "/state/worktrees/alpha/op-1"},
		run: taskstate.RunAttempt{
			Status: taskstate.RunStatusSucceeded,
			Completion: &taskstate.Completion{
				Summary:             "Done",
				Description:         "Done.",
				DetailedDescription: "Detailed PR body.",
				Commit:              "abc123",
			},
		},
		wantOK:        true,
		wantTarget:    tasktarget.TargetWorktreeTeam,
		wantLifecycle: workflow.ReviewLifecyclePRReady,
	},
	{
		name: "repo-root team PR ready",
		taskItem: task.Task{
			ID: "op-1",
			Metadata: task.Metadata{
				task.MetadataBranch:   "orpheus/op-1",
				task.MetadataWorktree: "/repo/alpha",
			},
		},
		taskTarget: taskstate.TaskTarget{Branch: "orpheus/op-1", Worktree: "/repo/alpha"},
		run: taskstate.RunAttempt{
			Status: taskstate.RunStatusSucceeded,
			Completion: &taskstate.Completion{
				Summary:             "Done",
				Description:         "Done.",
				DetailedDescription: "Detailed PR body.",
				Commit:              "abc123",
			},
		},
		wantOK:        true,
		wantTarget:    tasktarget.TargetRepoRootTeam,
		wantLifecycle: workflow.ReviewLifecyclePRReady,
	},
	{
		name: "metadata matches expected but taskstate target differs",
		taskItem: task.Task{
			ID: "op-1",
			Metadata: task.Metadata{
				task.MetadataBranch:   "orpheus/op-1",
				task.MetadataWorktree: "/state/worktrees/alpha/op-1",
			},
		},
		taskTarget: taskstate.TaskTarget{Branch: "manual/op-1", Worktree: "/state/worktrees/alpha/op-1"},
		run: taskstate.RunAttempt{
			Status: taskstate.RunStatusSucceeded,
			Completion: &taskstate.Completion{
				Summary:             "Done",
				Description:         "Done.",
				DetailedDescription: "Detailed PR body.",
				Commit:              "abc123",
			},
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
		taskTarget: taskstate.TaskTarget{Branch: "manual/op-1", Worktree: "/tmp/manual-worktree"},
		run: taskstate.RunAttempt{
			Status: taskstate.RunStatusSucceeded,
			Completion: &taskstate.Completion{
				Summary:             "Done",
				Description:         "Done.",
				DetailedDescription: "Detailed PR body.",
				Commit:              "abc123",
			},
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
		taskTarget: taskstate.TaskTarget{Branch: "orpheus/op-1", Worktree: "/state/worktrees/alpha/op-1"},
		run: taskstate.RunAttempt{
			Status: taskstate.RunStatusSucceeded,
			Completion: &taskstate.Completion{
				Summary:             "Done",
				Description:         "Done.",
				DetailedDescription: "Detailed PR body.",
				Commit:              "abc123",
			},
		},
	},
	{
		name: "missing completion is not classified",
		taskItem: task.Task{
			ID:       "op-1",
			Metadata: task.Metadata{task.MetadataBranch: "main", task.MetadataWorktree: "/repo/alpha"},
		},
		taskTarget: taskstate.TaskTarget{Branch: "main", Worktree: "/repo/alpha"},
		run: taskstate.RunAttempt{
			Status: taskstate.RunStatusSucceeded,
		},
	},
}

func TestClassifyExpectedCompletionTarget(t *testing.T) {
	targets := tasktarget.ExpectedTargets{
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

	for _, tt := range classifyExpectedCompletionTargetCases {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := workflow.ClassifyExpectedCompletionTarget(targets, tt.taskItem, tt.taskTarget, &tt.run)
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
