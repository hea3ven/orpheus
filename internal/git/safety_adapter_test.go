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

func TestIntegrationAdapterContractDirectIntegrationSafetyPreservesLocalAndRemoteHistory(t *testing.T) {
	for _, tc := range []struct {
		name, wantError string
		prepared        bool
	}{
		{name: "identical tips"},
		{name: "reset prepared destination", prepared: true, wantError: "differs from recorded integration"},
		{name: "unrelated local commit before preparation", wantError: "local destination has unrecorded work"},
		{name: "unrelated local commit after preparation", prepared: true, wantError: "differs from recorded integration"},
		{name: "same-tree commit substituted for result", prepared: true, wantError: "differs from the synchronized task commit"},
		{name: "wrong recorded tree on already delivered commit", prepared: true, wantError: "differs from the synchronized task commit or tree"},
		{name: "wrong synchronized tree", wantError: "synchronized public checkout changed"},
		{name: "source has not incorporated destination", wantError: "does not contain the recorded destination"},
		{name: "unsafe at", wantError: "safe Git branch name"},
		{name: "unsafe HEAD", wantError: "safe Git branch name"},
		{name: "same branch", wantError: "is the task branch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			repo := newGitRepoWithLocalOrigin(t)
			const branch = "orpheus/op-safety"
			parent := strings.TrimSpace(runGit(t, repo, "rev-parse", "main"))
			runGit(t, repo, "checkout", "-b", branch)
			if tc.name != "identical tips" {
				commitFile(t, repo, "reviewed.txt", "reviewed\n", "Reviewed task")
			}
			source, err := gitmeta.InspectPublicationCheckout(ctx, repo, branch)
			require.NoError(t, err)
			req := gitmeta.DirectIntegrationRequest{Repository: task.Repository{Path: repo}, Directory: repo, Branch: branch, Destination: "main", DestinationCommit: parent, Source: source}
			var result gitmeta.Revision
			if tc.prepared {
				result, err = gitmeta.PrepareDirectIntegration(ctx, req)
				require.NoError(t, err)
			}
			switch tc.name {
			case "reset prepared destination":
				runGit(t, repo, "reset", "--hard", parent)
			case "unrelated local commit before preparation":
				runGit(t, repo, "checkout", "main")
				commitFile(t, repo, "unrelated.txt", "do not publish\n", "Unrelated work")
				runGit(t, repo, "checkout", branch)
			case "unrelated local commit after preparation":
				commitFile(t, repo, "unrelated.txt", "do not publish\n", "Unrelated work")
			case "same-tree commit substituted for result":
				runGit(t, repo, "commit", "--allow-empty", "-m", "Different commit, same tree")
				result.Commit = strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			case "wrong recorded tree on already delivered commit":
				runGit(t, repo, "push", "origin", "main")
				result.Tree = strings.TrimSpace(runGit(t, repo, "rev-parse", parent+"^{tree}"))
			case "wrong synchronized tree":
				req.Source.Tree = strings.TrimSpace(runGit(t, repo, "rev-parse", parent+"^{tree}"))
			case "source has not incorporated destination":
				runGit(t, repo, "checkout", "main")
				commitFile(t, repo, "destination.txt", "destination\n", "Destination work")
				runGit(t, repo, "push", "origin", "main")
				req.DestinationCommit = strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
				runGit(t, repo, "checkout", branch)
			case "unsafe at":
				req.Destination = "@"
			case "unsafe HEAD":
				req.Destination = "HEAD"
			case "same branch":
				req.Destination = branch
			}
			origin := strings.TrimSpace(runGit(t, repo, "remote", "get-url", "origin"))
			beforeRefs, beforeRemote := runGit(t, repo, "show-ref", "--heads"), runGit(t, origin, "show-ref", "--heads")
			beforeLog := runGit(t, repo, "reflog", "show", "main")
			beforeStatus := runGit(t, repo, "status", "--porcelain=v1")

			switch {
			case tc.prepared:
				_, err = gitmeta.PushDirectIntegration(ctx, req, result)
			case tc.name == "identical tips":
				result, err = gitmeta.PrepareDirectIntegration(ctx, req)
				require.NoError(t, err)
				assert.Equal(t, source, result)
				var delivered gitmeta.Revision
				delivered, err = gitmeta.PushDirectIntegration(ctx, req, result)
				assert.Equal(t, source, delivered)
			default:
				_, err = gitmeta.PrepareDirectIntegration(ctx, req)
			}

			if tc.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantError)
			}
			assert.Equal(t, beforeRefs, runGit(t, repo, "show-ref", "--heads"))
			assert.Equal(t, beforeRemote, runGit(t, origin, "show-ref", "--heads"))
			assert.Equal(t, beforeLog, runGit(t, repo, "reflog", "show", "main"))
			assert.Equal(t, beforeStatus, runGit(t, repo, "status", "--porcelain=v1"))
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
			conflictFile := "conflict.txt"
			if kind == "conflict markers" {
				conflictFile = " café\n[1].txt "
			}
			fixture := newConflictResolutionFixture(t, branch, conflictFile, func(t *testing.T, worktree string) {
				if kind == "unstaged extra" {
					commitFile(t, worktree, "unexpected.txt", "original\n", "Tracked file")
				}
			}, nil)
			worktree := fixture.worktreePath
			conflict := filepath.Join(worktree, conflictFile)
			wantError := "unexpected changes outside merge conflict files"
			wantContent := "resolved\n"
			if kind == "unresolved deleted" {
				require.NoError(t, os.Remove(conflict))
				wantError = "unresolved merge conflicts remain"
			} else {
				if kind == "conflict markers" {
					// Keep Git's original marker bytes, which also match AUTO_MERGE.
					content, err := os.ReadFile(conflict)
					require.NoError(t, err)
					wantContent = string(content)
					require.Contains(t, wantContent, "<<<<<<< ")
					wantError = "still contains conflict markers"
				} else {
					require.NoError(t, os.WriteFile(conflict, []byte(wantContent), 0o644))
				}
				runGit(t, worktree, "--literal-pathspecs", "add", "--", conflictFile)
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

			assert.Equal(t, []string{conflictFile}, fixture.result.ConflictFiles)
			assert.Equal(t, beforeLocal, runGit(t, worktree, "rev-parse", "HEAD"))
			assert.Equal(t, beforeRemote, runGit(t, origin, "rev-parse", "refs/heads/"+branch))
			assert.Equal(t, beforeStatus, runGit(t, worktree, "status", "--porcelain=v1"), "refusal must preserve which changes are staged")
			assert.Equal(t, beforeIndex, runGit(t, worktree, "diff", "--cached", "--binary"), "refusal must not stage unrelated operator work")
			require.ErrorContains(t, err, wantError)
			assert.NotEmpty(t, runGit(t, worktree, "rev-parse", "--verify", "MERGE_HEAD"))
			if kind == "unresolved deleted" {
				assert.Equal(t, gitmeta.TaskBranchSyncConflicted, result.Status)
				assert.Equal(t, []string{conflictFile}, result.ConflictFiles)
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
