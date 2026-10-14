//go:build integration

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdapterContractDirectMergeSafetyPreservesLocalAndRemoteHistory(t *testing.T) {
	for _, kind := range []string{"published checkpoint", "reset checkpoint", "fast forward", "unrelated commit", "unsafe at", "unsafe HEAD", "same branch"} {
		t.Run(kind, func(t *testing.T) {
			repo := newGitRepoWithLocalOrigin(t)
			const branch = "orpheus/op-safety"
			parent := strings.TrimSpace(runGit(t, repo, "rev-parse", "main"))
			runGit(t, repo, "checkout", "-b", branch)
			commitFile(t, repo, "reviewed.txt", "reviewed\n", "Reviewed task")
			runGit(t, repo, "checkout", "main")
			destination, checkpoint, wantError := "main", "", ""
			switch kind {
			case "published checkpoint", "reset checkpoint", "unrelated commit":
				runGit(t, repo, "merge", "--no-ff", "--no-edit", branch)
				checkpoint = strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
				switch kind {
				case "published checkpoint":
					runGit(t, repo, "push", "origin", "main")
				case "reset checkpoint":
					runGit(t, repo, "reset", "--hard", parent)
					wantError = "not recorded merge"
				case "unrelated commit":
					commitFile(t, repo, "unrelated.txt", "do not publish\n", "Unrelated work")
					wantError = "local branch is ahead of or divergent"
				}
			case "fast forward":
				runGit(t, repo, "merge", "--ff-only", branch)
				wantError = "local branch is ahead of or divergent"
			case "unsafe at":
				destination, wantError = "@", "safe Git branch name"
			case "unsafe HEAD":
				destination, wantError = "HEAD", "safe Git branch name"
			case "same branch":
				destination, wantError = branch, "is the task branch"
			}
			origin := strings.TrimSpace(runGit(t, repo, "remote", "get-url", "origin"))
			beforeRefs, beforeRemote := runGit(t, repo, "show-ref", "--heads"), runGit(t, origin, "show-ref", "--heads")
			beforeStatus := runGit(t, repo, "status", "--porcelain=v1")
			registered := task.Repository{ID: "alpha", Path: repo, DefaultBranch: "main"}
			var err error

			if kind == "published checkpoint" || kind == "reset checkpoint" {
				var recovered bool
				recovered, err = gitmeta.ValidateRecordedDirectMerge(context.Background(), registered, checkpoint)
				assert.Equal(t, kind == "published checkpoint", recovered)
			} else {
				_, err = gitmeta.MergeTaskBranchIntoDestination(context.Background(), registered, destination, branch)
			}

			if wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, wantError)
			}
			assert.Equal(t, beforeRefs, runGit(t, repo, "show-ref", "--heads"))
			assert.Equal(t, beforeRemote, runGit(t, origin, "show-ref", "--heads"))
			assert.Equal(t, beforeStatus, runGit(t, repo, "status", "--porcelain=v1"))
			assertGitBranch(t, repo, "main")
		})
	}
}

func TestIntegrationAdapterContractSyncCurrentAndUnpushedBranchesPreservesHeadAndPublishesOnlyNeededWork(t *testing.T) {
	for _, remoteBehind := range []bool{false, true} {
		name := "already current"
		if remoteBehind {
			name = "local work not pushed"
		}
		t.Run(name, func(t *testing.T) {
			repo := newGitRepoWithLocalOrigin(t)
			const branch = "orpheus/op-current"
			worktree := addTaskBranchWorktree(t, repo, branch)
			commitFile(t, worktree, "task.txt", "task\n", "Task work")
			runGit(t, worktree, "push", "--set-upstream", "origin", branch)
			wantStatus := gitmeta.TaskBranchSyncAlreadyCurrent
			if remoteBehind {
				commitFile(t, worktree, "local.txt", "local only\n", "Unpushed work")
				wantStatus = gitmeta.TaskBranchSyncPushed
			}
			head := strings.TrimSpace(runGit(t, worktree, "rev-parse", "HEAD"))
			origin := strings.TrimSpace(runGit(t, repo, "remote", "get-url", "origin"))

			result, err := gitmeta.SyncTaskBranchWithDefault(context.Background(), gitmeta.TaskBranchSyncOptions{RepoPath: repo, Worktree: worktree, Branch: branch, DefaultBranch: "main", UpdatePolicy: gitmeta.TaskBranchUpdateAlways})

			require.NoError(t, err)
			assert.Equal(t, wantStatus, result.Status)
			assert.Equal(t, head, result.PreviousHead)
			assert.Equal(t, head, result.Head)
			assert.Equal(t, head, strings.TrimSpace(runGit(t, worktree, "rev-parse", "HEAD")))
			assert.Equal(t, head, strings.TrimSpace(runGit(t, origin, "rev-parse", "refs/heads/"+branch)))
			assert.Empty(t, strings.TrimSpace(runGit(t, worktree, "status", "--porcelain=v1")))
		})
	}
}

func TestIntegrationAdapterContractConflictCompletionRefusesUnsafeContentsWithoutPublishing(t *testing.T) {
	for _, kind := range []string{"staged extra", "unstaged extra", "untracked extra", "unresolved deleted", "conflict markers"} {
		t.Run(kind, func(t *testing.T) {
			const branch = "orpheus/op-unsafe"
			fixture := newConflictResolutionFixture(t, branch, func(t *testing.T, worktree string) {
				if kind == "unstaged extra" {
					commitFile(t, worktree, "unexpected.txt", "original\n", "Tracked file")
				}
			}, nil)
			worktree := fixture.worktreePath
			conflict := filepath.Join(worktree, "conflict.txt")
			wantError := "unexpected changes outside merge conflict files"
			wantContent := "resolved\n"
			if kind == "unresolved deleted" {
				require.NoError(t, os.Remove(conflict))
				wantError = "unresolved merge conflicts remain"
			} else {
				if kind == "conflict markers" {
					wantContent = "<<<<<<< HEAD\ntask\n=======\ndefault\n>>>>>>> origin/main\n"
					wantError = "still contains conflict markers"
				}
				require.NoError(t, os.WriteFile(conflict, []byte(wantContent), 0o644))
				runGit(t, worktree, "add", "conflict.txt")
				if strings.Contains(kind, "extra") {
					require.NoError(t, os.WriteFile(filepath.Join(worktree, "unexpected.txt"), []byte("preserve these bytes\n"), 0o644))
					if kind == "staged extra" {
						runGit(t, worktree, "add", "unexpected.txt")
					}
				}
			}
			origin := strings.TrimSpace(runGit(t, fixture.repoPath, "remote", "get-url", "origin"))
			beforeLocal := runGit(t, worktree, "rev-parse", "HEAD")
			beforeRemote := runGit(t, origin, "rev-parse", "refs/heads/"+branch)
			beforeStatus := runGit(t, worktree, "status", "--porcelain=v1")
			beforeIndex := runGit(t, worktree, "diff", "--cached", "--binary")

			result, err := gitmeta.CompleteTaskBranchConflictResolution(context.Background(), gitmeta.TaskBranchSyncOptions{RepoPath: fixture.repoPath, Worktree: worktree, Branch: branch, DefaultBranch: "main"}, fixture.result.ConflictFiles)

			require.ErrorContains(t, err, wantError)
			assert.Equal(t, beforeLocal, runGit(t, worktree, "rev-parse", "HEAD"))
			assert.Equal(t, beforeRemote, runGit(t, origin, "rev-parse", "refs/heads/"+branch))
			assert.Equal(t, beforeStatus, runGit(t, worktree, "status", "--porcelain=v1"), "refusal must preserve which changes are staged")
			assert.Equal(t, beforeIndex, runGit(t, worktree, "diff", "--cached", "--binary"), "refusal must not stage unrelated operator work")
			assert.NotEmpty(t, runGit(t, worktree, "rev-parse", "--verify", "MERGE_HEAD"))
			if kind == "unresolved deleted" {
				assert.Equal(t, gitmeta.TaskBranchSyncConflicted, result.Status)
				assert.Equal(t, []string{"conflict.txt"}, result.ConflictFiles)
				_, err := os.Stat(conflict)
				assert.ErrorIs(t, err, os.ErrNotExist)
			} else {
				assertFileContent(t, conflict, wantContent)
				if strings.Contains(kind, "extra") {
					assert.ErrorContains(t, err, "unexpected.txt")
					assertFileContent(t, filepath.Join(worktree, "unexpected.txt"), "preserve these bytes\n")
				}
			}
		})
	}
}
