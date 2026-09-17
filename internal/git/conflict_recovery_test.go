//go:build integration

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newCheckpointedConflict(t *testing.T) (gitmeta.TaskBranchSyncOptions, gitmeta.TaskBranchConflictCheckpoint, []string) {
	t.Helper()
	repo := newGitRepoWithLocalOrigin(t)
	branch := "orpheus/recovery"
	worktree := addTaskBranchWorktree(t, repo, branch)
	commitFile(t, worktree, "conflict.txt", "task\n", "task conflict")
	runGit(t, worktree, "push", "--set-upstream", "origin", branch)
	pushRemoteCommit(t, repo, "conflict.txt", "default\n")
	opts := gitmeta.TaskBranchSyncOptions{RepoPath: repo, Worktree: worktree, Branch: branch, DefaultBranch: "main"}
	checkpoint, err := gitmeta.InspectTaskBranchConflictCheckpoint(context.Background(), opts)
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(runGit(t, worktree, "rev-parse", "HEAD")), checkpoint.LocalHead)
	assert.Equal(t, checkpoint.LocalHead, checkpoint.RemoteHead)
	assert.Equal(t, strings.TrimSpace(runGit(t, worktree, "rev-parse", "origin/main")), checkpoint.MergeSource)
	assert.NotEqual(t, checkpoint.LocalHead, checkpoint.MergeSource)
	result, err := gitmeta.BeginTaskBranchConflictResolution(context.Background(), opts)
	require.NoError(t, err)
	require.Equal(t, gitmeta.TaskBranchSyncConflicted, result.Status)
	require.Equal(t, []string{"conflict.txt"}, result.ConflictFiles)
	return opts, checkpoint, result.ConflictFiles
}

func TestIntegrationConflictRecoveryCommitsLocallyBeforeSeparatePush(t *testing.T) {
	opts, checkpoint, files := newCheckpointedConflict(t)
	ctx := context.Background()
	require.NoError(t, os.WriteFile(filepath.Join(opts.Worktree, "conflict.txt"), []byte("resolved\n"), 0o644))
	runGit(t, opts.Worktree, "add", "conflict.txt")

	committed, err := gitmeta.CommitTaskBranchConflictResolution(ctx, opts, files)

	require.NoError(t, err)
	assert.Equal(t, gitmeta.TaskBranchSyncUpdated, committed.Status)
	assert.NotEqual(t, checkpoint.LocalHead, committed.Head)
	assert.Equal(t, checkpoint.LocalHead+" "+checkpoint.MergeSource, strings.TrimSpace(runGit(t, opts.Worktree, "show", "-s", "--format=%P", "HEAD")))
	remote, err := gitmeta.InspectRemoteTaskBranchHead(ctx, opts)
	require.NoError(t, err)
	assert.Equal(t, checkpoint.RemoteHead, remote, "local completion must not publish")
	require.NoError(t, gitmeta.InspectTaskBranchConflictRollbackEligibility(ctx, opts, checkpoint, committed.Head))
	pushed, err := gitmeta.PushCommittedTaskBranchConflictResolution(ctx, opts)
	require.NoError(t, err)
	assert.Equal(t, committed.Head, pushed.Head)
	remote, err = gitmeta.InspectRemoteTaskBranchHead(ctx, opts)
	require.NoError(t, err)
	assert.Equal(t, committed.Head, remote)
	assert.Empty(t, strings.TrimSpace(runGit(t, opts.Worktree, "status", "--porcelain=v1")))
	contents, err := os.ReadFile(filepath.Join(opts.Worktree, "conflict.txt"))
	require.NoError(t, err)
	assert.Equal(t, "resolved\n", string(contents))
	retried, err := gitmeta.PushCommittedTaskBranchConflictResolution(ctx, opts)
	require.NoError(t, err)
	assert.Equal(t, pushed.Head, retried.Head)
}

func TestIntegrationConflictRecoveryRollsBackOnlyMatchingCheckpoint(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "in progress merge"
		if completed {
			name = "locally committed merge"
		}
		t.Run(name, func(t *testing.T) {
			opts, checkpoint, files := newCheckpointedConflict(t)
			ctx := context.Background()
			localHead := ""
			if completed {
				require.NoError(t, os.WriteFile(filepath.Join(opts.Worktree, "conflict.txt"), []byte("resolved\n"), 0o644))
				runGit(t, opts.Worktree, "add", "conflict.txt")
				result, err := gitmeta.CommitTaskBranchConflictResolution(ctx, opts, files)
				require.NoError(t, err)
				localHead = result.Head
			}
			require.Error(t, gitmeta.VerifyTaskBranchConflictRollback(ctx, opts, checkpoint))
			require.Error(t, gitmeta.InspectTaskBranchConflictRollbackEligibility(ctx, opts, checkpoint, "wrong-head"))
			require.NoError(t, gitmeta.InspectTaskBranchConflictRollbackEligibility(ctx, opts, checkpoint, localHead))

			err := gitmeta.RollbackTaskBranchConflictResolution(ctx, opts, checkpoint)

			require.NoError(t, err)
			require.NoError(t, gitmeta.VerifyTaskBranchConflictRollback(ctx, opts, checkpoint))
			assert.Equal(t, checkpoint.LocalHead, strings.TrimSpace(runGit(t, opts.Worktree, "rev-parse", "HEAD")))
			assert.Empty(t, strings.TrimSpace(runGit(t, opts.Worktree, "status", "--porcelain=v1")))
			remote, err := gitmeta.InspectRemoteTaskBranchHead(ctx, opts)
			require.NoError(t, err)
			assert.Equal(t, checkpoint.RemoteHead, remote)
			contents, err := os.ReadFile(filepath.Join(opts.Worktree, "conflict.txt"))
			require.NoError(t, err)
			assert.Equal(t, "task\n", string(contents))
			wrong := checkpoint
			wrong.RemoteHead = "different-remote"
			require.ErrorContains(t, gitmeta.VerifyTaskBranchConflictRollback(ctx, opts, wrong), "remote task branch changed")
		})
	}
}
