//go:build integration

package cli_test

import (
	"context"
	"maps"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationSyncGitRejectsUnknownRecoveryTargetsWithoutMutation(t *testing.T) {
	operations := map[string]func(*memorySyncGit, gitmeta.TaskBranchSyncOptions, gitmeta.TaskBranchConflictCheckpoint) error{
		"sync": func(g *memorySyncGit, opts gitmeta.TaskBranchSyncOptions, _ gitmeta.TaskBranchConflictCheckpoint) error {
			_, err := g.SyncTaskBranchWithDefault(context.Background(), opts)
			return err
		},
		"begin": func(g *memorySyncGit, opts gitmeta.TaskBranchSyncOptions, _ gitmeta.TaskBranchConflictCheckpoint) error {
			_, err := g.BeginTaskBranchConflictResolution(context.Background(), opts)
			return err
		},
		"combined completion": func(g *memorySyncGit, opts gitmeta.TaskBranchSyncOptions, _ gitmeta.TaskBranchConflictCheckpoint) error {
			_, err := g.CompleteTaskBranchConflictResolution(context.Background(), opts, g.conflicts)
			return err
		},
		"commit": func(g *memorySyncGit, opts gitmeta.TaskBranchSyncOptions, _ gitmeta.TaskBranchConflictCheckpoint) error {
			_, err := g.CommitTaskBranchConflictResolution(context.Background(), opts, g.conflicts)
			return err
		},
		"push": func(g *memorySyncGit, opts gitmeta.TaskBranchSyncOptions, _ gitmeta.TaskBranchConflictCheckpoint) error {
			_, err := g.PushCommittedTaskBranchConflictResolution(context.Background(), opts)
			return err
		},
		"checkpoint": func(g *memorySyncGit, opts gitmeta.TaskBranchSyncOptions, _ gitmeta.TaskBranchConflictCheckpoint) error {
			_, err := g.InspectTaskBranchConflictCheckpoint(context.Background(), opts)
			return err
		},
		"remote head": func(g *memorySyncGit, opts gitmeta.TaskBranchSyncOptions, _ gitmeta.TaskBranchConflictCheckpoint) error {
			_, err := g.InspectRemoteTaskBranchHead(context.Background(), opts)
			return err
		},
		"rollback eligibility": func(g *memorySyncGit, opts gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint) error {
			return g.InspectTaskBranchConflictRollbackEligibility(context.Background(), opts, checkpoint, g.head)
		},
		"rollback": func(g *memorySyncGit, opts gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint) error {
			return g.RollbackTaskBranchConflictResolution(context.Background(), opts, checkpoint)
		},
		"verify rollback": func(g *memorySyncGit, opts gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint) error {
			return g.VerifyTaskBranchConflictRollback(context.Background(), opts, checkpoint)
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			for _, field := range []string{"repository", "default branch", "branch", "worktree"} {
				t.Run(field, func(t *testing.T) {
					f := newSyncFixture(t)
					g := f.syncGit
					branch, dir := f.expectedTarget("op-sync")
					opts := gitmeta.TaskBranchSyncOptions{RepoPath: taskWorkflowRepoRoot, DefaultBranch: "main", Branch: branch, Worktree: dir}
					switch field {
					case "repository":
						opts.RepoPath = "/fixture/other-repository"
					case "default branch":
						opts.DefaultBranch = "other-default"
					case "branch":
						opts.Branch = "other-task"
					case "worktree":
						opts.Worktree = "/fixture/other-worktree"
					}
					g.conflicts = []string{"conflict.txt"}
					g.mergeActive, g.resolved = true, true
					checkpoint := gitmeta.TaskBranchConflictCheckpoint{LocalHead: g.head, RemoteHead: g.remote[branch], MergeSource: "default-head"}
					remote := maps.Clone(g.remote)

					err := operation(g, opts, checkpoint)

					require.ErrorContains(t, err, "unknown sync target")
					assert.Equal(t, checkpoint.LocalHead, g.head)
					assert.Equal(t, remote, g.remote)
					assert.True(t, g.mergeActive)
					assert.True(t, g.resolved)
					assert.False(t, g.behind)
					assert.Zero(t, g.commits)
					assert.Zero(t, g.rollbacks)
					assert.Empty(t, g.pushes)
					assert.Empty(t, g.calls)
				})
			}
		})
	}
}
