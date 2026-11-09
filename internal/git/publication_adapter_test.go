//go:build integration

package git_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	orpheusgit "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdapterContractPublicationCommitAndPushPreserveMessageAndRemoteRef(t *testing.T) {
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
			require.Error(t, orpheusgit.VerifyCommit(ctx, repo, commit, parent, "Different publication message"))
			require.Error(t, orpheusgit.VerifyCommit(ctx, repo, commit, commit, message))
			if branch != "main" {
				assert.Equal(t, "origin/"+branch, strings.TrimSpace(runGit(t, repo, "rev-parse", "--abbrev-ref", "@{upstream}")))
				assert.Equal(t, parent, strings.TrimSpace(runGit(t, origin, "rev-parse", "refs/heads/main")))
			}
		})
	}
}

func TestIntegrationAdapterContractPublicationPushReportsUnavailableOriginWithoutChangingCommit(t *testing.T) {
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

func TestIntegrationAdapterContractAuthorizedPushPinsSourceDespiteLocalRefChange(t *testing.T) {
	repo := newGitRepoWithLocalOrigin(t)
	origin := strings.TrimSpace(runGit(t, repo, "remote", "get-url", "origin"))
	branch := "orpheus/op-pin"
	runGit(t, repo, "checkout", "-b", branch)
	baseline := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	commitFile(t, repo, "selected.txt", "authorized input\n", "selected publication input")
	selected := orpheusgit.Revision{Commit: strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")), Tree: strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD^{tree}")), RetainedRef: "refs/orpheus/selected-input"}
	runGit(t, repo, "update-ref", selected.RetainedRef, selected.Commit)
	commitFile(t, repo, "later.txt", "not authorized yet\n", "later operator input")
	later := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	runGit(t, repo, "reset", "--hard", selected.Commit)
	req := orpheusgit.PublicCommitRequest{Directory: repo, Branch: branch, Parent: baseline, Ref: "refs/orpheus/publication/pin", Message: "feat: pinned public output\n\nBody", Input: selected}
	require.NoError(t, orpheusgit.RetainPublicationInput(context.Background(), req))
	public, err := orpheusgit.PackagePublicCommit(context.Background(), req)
	require.NoError(t, err)
	hooks := testutil.CanonicalTempDir(t)
	runGit(t, repo, "config", "core.hooksPath", hooks)
	require.NoError(t, testguard.WriteExecutable(filepath.Join(hooks, "pre-push"), []byte("#!/bin/sh\ngit update-ref refs/heads/"+branch+" "+later+"\n")))

	require.NoError(t, orpheusgit.PushPublicCommit(context.Background(), req, public))
	assert.Equal(t, public.Commit, strings.TrimSpace(runGit(t, origin, "rev-parse", "refs/heads/"+branch)), "the hook must not substitute newer code for the authorized source")
	assert.Equal(t, later, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")), "normal hooks ran without being bypassed")
	runGit(t, repo, "reset", "--hard", baseline)
	older := orpheusgit.Revision{Commit: baseline, Tree: strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD^{tree}")), RetainedRef: "refs/orpheus/older-input"}
	runGit(t, repo, "update-ref", older.RetainedRef, older.Commit)
	req.Input = older
	req.Ref = "refs/orpheus/publication/older"
	require.NoError(t, orpheusgit.RetainPublicationInput(context.Background(), req))
	stale, err := orpheusgit.PackagePublicCommit(context.Background(), req)
	require.NoError(t, err)
	err = orpheusgit.PushPublicCommit(context.Background(), req, stale)
	require.ErrorContains(t, err, "unexpected public head")
	assert.Equal(t, public.Commit, strings.TrimSpace(runGit(t, origin, "rev-parse", "refs/heads/"+branch)), "proceed never authorizes force-pushing over unexpected remote history")
}
