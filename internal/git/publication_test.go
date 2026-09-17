//go:build integration

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	orpheusgit "github.com/hea3ven/orpheus/internal/git"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationPublicationCommitAndPushPreserveMessageAndRemoteRef(t *testing.T) {
	for _, branch := range []string{"main", "orpheus/op-publish"} {
		t.Run(branch, func(t *testing.T) {
			repo := newGitRepoWithLocalOrigin(t)
			if branch != "main" {
				runGit(t, repo, "checkout", "-b", branch)
			}
			parent := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			origin := strings.TrimSpace(runGit(t, repo, "remote", "get-url", "origin"))
			message := "feat: preserve publication text\n\nKeep `code`, $variables and apostrophes: O'Brien.\n\nSecond paragraph."
			require.NoError(t, os.WriteFile(filepath.Join(repo, "reviewed.txt"), []byte("reviewed\n"), 0o644))
			ctx := context.Background()

			require.NoError(t, orpheusgit.StageAll(ctx, repo))
			commit, err := orpheusgit.Commit(ctx, repo, message)
			require.NoError(t, err)
			if branch == "main" {
				err = orpheusgit.PushDefaultBranch(ctx, repo, branch)
			} else {
				err = orpheusgit.PushTaskBranch(ctx, repo, branch)
			}
			require.NoError(t, err)

			assert.Equal(t, message, strings.TrimSpace(runGit(t, repo, "log", "-1", "--format=%B")))
			assert.Equal(t, commit, strings.TrimSpace(runGit(t, origin, "rev-parse", "refs/heads/"+branch)))
			assert.Empty(t, strings.TrimSpace(runGit(t, repo, "status", "--porcelain=v1")))
			require.NoError(t, orpheusgit.VerifyCommit(ctx, repo, commit, parent, message))
			if branch != "main" {
				assert.Equal(t, "origin/"+branch, strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "@{upstream}")))
				assert.Equal(t, parent, strings.TrimSpace(runGit(t, origin, "rev-parse", "refs/heads/main")))
			}
		})
	}
}

func TestIntegrationPublicationPushReportsUnavailableOriginWithoutChangingCommit(t *testing.T) {
	for _, branch := range []string{"main", "orpheus/op-publish"} {
		t.Run(branch, func(t *testing.T) {
			repo := newGitRepoWithLocalOrigin(t)
			if branch != "main" {
				runGit(t, repo, "checkout", "-b", branch)
			}
			commitFile(t, repo, "reviewed.txt", "reviewed\n", "Reviewed publication")
			commit := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			runGit(t, repo, "remote", "set-url", "origin", filepath.Join(repo, "missing-origin.git"))

			var err error
			if branch == "main" {
				err = orpheusgit.PushDefaultBranch(context.Background(), repo, branch)
			} else {
				err = orpheusgit.PushTaskBranch(context.Background(), repo, branch)
			}

			require.ErrorContains(t, err, "origin")
			if branch == "main" {
				assert.ErrorContains(t, err, "push default branch")
			} else {
				assert.ErrorContains(t, err, "push task branch")
			}
			assert.Equal(t, commit, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")))
		})
	}
}
