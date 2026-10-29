//go:build integration

package git_test

import (
	"context"
	"errors"
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

func TestIntegrationAdapterContractRevisionCaptureRetainsOrdinaryHookOutputInBothDirectories(t *testing.T) {
	for key, value := range map[string]string{
		"GIT_AUTHOR_NAME": "Inherited Author", "GIT_AUTHOR_EMAIL": "author@example.invalid",
		"GIT_COMMITTER_NAME": "Inherited Committer", "GIT_COMMITTER_EMAIL": "committer@example.invalid",
	} {
		t.Setenv(key, value)
	}
	for _, linked := range []bool{false, true} {
		t.Run(map[bool]string{false: "repository root", true: "linked worktree"}[linked], func(t *testing.T) {
			repo := newGitRepo(t)
			runGit(t, repo, "config", "user.name", "Configured Author")
			runGit(t, repo, "config", "user.email", "configured@example.test")
			dir, branch := repo, "main"
			if linked {
				dir, branch = filepath.Join(testutil.CanonicalTempDir(t), "work"), "task"
				runGit(t, repo, "worktree", "add", "-b", branch, dir)
			}
			input := strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD"))
			hooks := testutil.CanonicalTempDir(t)
			runGit(t, repo, "config", "core.hooksPath", hooks)
			require.NoError(t, testguard.WriteExecutable(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nprintf 'hook output\\n' > hook.txt\ngit add hook.txt\n")))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("feature\n"), 0o644))
			opts := orpheusgit.RevisionCapture{Directory: dir, Input: orpheusgit.CheckoutIdentity{Commit: input, Ref: "refs/heads/" + branch}, Ref: "refs/orpheus/revisions/task/0/1", Message: "feat: retain output\n\nKeep O'Brien, `code` and $variables."}

			revision, err := orpheusgit.CaptureRevision(context.Background(), opts)
			require.NoError(t, err)
			assert.NotEqual(t, input, revision.Commit)
			assert.False(t, revision.Reused)
			assert.Equal(t, "refs/orpheus/revisions/task/0/1/output", revision.RetainedRef)
			assert.Equal(t, "hook output\n", runGit(t, dir, "show", revision.Commit+":hook.txt"))
			assert.Equal(t, "feature\n", runGit(t, dir, "show", revision.Commit+":feature.txt"))
			assert.Equal(t, opts.Message, strings.TrimSpace(runGit(t, dir, "show", "-s", "--format=%B", revision.Commit)))
			assert.Equal(t, "Configured Author <configured@example.test>", strings.TrimSpace(runGit(t, dir, "show", "-s", "--format=%an <%ae>", revision.Commit)))
			assert.Equal(t, "Configured Author <configured@example.test>", strings.TrimSpace(runGit(t, dir, "show", "-s", "--format=%cn <%ce>", revision.Commit)))
			assert.Equal(t, input, strings.TrimSpace(runGit(t, dir, "show", "-s", "--format=%P", revision.Commit)))
			assert.Equal(t, revision.Tree, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD^{tree}")))
			require.NoError(t, orpheusgit.VerifyRevision(context.Background(), dir, orpheusgit.CheckoutIdentity{Commit: revision.Commit, Ref: opts.Input.Ref}, revision))
			repeated, err := orpheusgit.CaptureRevision(context.Background(), opts)
			require.NoError(t, err)
			assert.Equal(t, revision, repeated, "retry must not create another implementation commit")

			unchanged := opts
			unchanged.Input.Commit, unchanged.Ref, unchanged.Message = revision.Commit, "refs/orpheus/revisions/task/0/3", "fix: no further changes"
			reused, err := orpheusgit.CaptureRevision(context.Background(), unchanged)
			require.NoError(t, err)
			assert.True(t, reused.Reused)
			assert.Equal(t, revision.Commit, reused.Commit)
			assert.Equal(t, "2", strings.TrimSpace(runGit(t, dir, "rev-list", "--count", "HEAD")))

			runGit(t, dir, "reset", "--hard", input)
			runGit(t, repo, "reflog", "expire", "--expire=now", "--all")
			runGit(t, repo, "gc", "--prune=now")
			assert.Equal(t, revision.Commit, strings.TrimSpace(runGit(t, repo, "rev-parse", revision.RetainedRef)))
			assert.Equal(t, "feature\n", runGit(t, repo, "show", revision.Commit+":feature.txt"), "owned refs, not YAML SHAs or reflogs, retain output")
			require.ErrorContains(t, orpheusgit.VerifyRevision(context.Background(), dir, orpheusgit.CheckoutIdentity{Commit: revision.Commit, Ref: opts.Input.Ref}, revision), "requires HEAD")
			require.NoError(t, orpheusgit.VerifyRetainedRevision(context.Background(), dir, revision), "historical retention verification must not require its checkout")
			wrongTree := revision
			wrongTree.Tree = strings.TrimSpace(runGit(t, dir, "rev-parse", input+"^{tree}"))
			require.ErrorContains(t, orpheusgit.VerifyRetainedRevision(context.Background(), dir, wrongTree), "tree differs")
			unowned := revision
			unowned.RetainedRef = opts.Input.Ref
			require.ErrorContains(t, orpheusgit.VerifyRetainedRevision(context.Background(), dir, unowned), "owned retention ref")
			runGit(t, dir, "update-ref", "refs/heads/retention-target", revision.Commit)
			runGit(t, dir, "symbolic-ref", "refs/orpheus/symbolic-retention", "refs/heads/retention-target")
			symbolic := revision
			symbolic.RetainedRef = "refs/orpheus/symbolic-retention"
			require.ErrorContains(t, orpheusgit.VerifyRetainedRevision(context.Background(), dir, symbolic), "must directly retain")
			// A descendant with the right object must not replace a missing exact ref.
			runGit(t, dir, "update-ref", "refs/orpheus/missing-retention/child", revision.Commit)
			missing := revision
			missing.RetainedRef = "refs/orpheus/missing-retention"
			require.ErrorContains(t, orpheusgit.VerifyRetainedRevision(context.Background(), dir, missing), "retention ref")
			runGit(t, dir, "update-ref", "-d", revision.RetainedRef)
			require.ErrorContains(t, orpheusgit.VerifyRetainedRevision(context.Background(), dir, revision), "retention ref")
			assert.Equal(t, input, strings.TrimSpace(runGit(t, dir, "rev-parse", "HEAD")), "retention checks must not move the checkout")
		})
	}
}

func TestIntegrationAdapterContractRevisionCaptureReportsHookAndSigningFailuresWithoutBypass(t *testing.T) {
	for _, failure := range []string{"hook rejection", "signing rejection", "hook leaves checkout changes"} {
		t.Run(failure, func(t *testing.T) {
			repo := newGitRepo(t)
			runGit(t, repo, "config", "user.name", "Test")
			runGit(t, repo, "config", "user.email", "test@example.test")
			input := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			hooks := testutil.CanonicalTempDir(t)
			runGit(t, repo, "config", "core.hooksPath", hooks)
			switch failure {
			case "hook rejection":
				require.NoError(t, testguard.WriteExecutable(filepath.Join(hooks, "pre-commit"), []byte("#!/bin/sh\nprintf 'hook preserved\\n' > hook-leftover.txt\nprintf 'hook refused\\n' >&2\nexit 1\n")))
			case "signing rejection":
				signer := filepath.Join(hooks, "signer")
				require.NoError(t, testguard.WriteExecutable(signer, []byte("#!/bin/sh\nprintf 'signer refused\\n' >&2\nexit 1\n")))
				runGit(t, repo, "config", "commit.gpgSign", "true")
				runGit(t, repo, "config", "gpg.program", signer)
			case "hook leaves checkout changes":
				require.NoError(t, testguard.WriteExecutable(filepath.Join(hooks, "post-commit"), []byte("#!/bin/sh\nprintf 'hook preserved\\n' > hook-leftover.txt\n")))
			}
			require.NoError(t, os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("feature\n"), 0o644))
			opts := orpheusgit.RevisionCapture{Directory: repo, Input: orpheusgit.CheckoutIdentity{Commit: input, Ref: "refs/heads/main"}, Ref: "refs/orpheus/revisions/task/0/1", Message: "feat: ordinary policy"}

			revision, err := orpheusgit.CaptureRevision(context.Background(), opts)
			require.Error(t, err)
			assert.Empty(t, revision.Commit, "failure must not authorize review")
			assert.Equal(t, "feature\n", readRevisionTestFile(t, repo, "feature.txt"))
			if failure == "hook leaves checkout changes" {
				assert.ErrorContains(t, err, "retained but not safe for review")
				head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
				assert.NotEqual(t, input, head)
				assert.Equal(t, head, strings.TrimSpace(runGit(t, repo, "rev-parse", opts.Ref+"/output")))
				require.NoError(t, os.Remove(filepath.Join(repo, "hook-leftover.txt")), "only explicit operator cleanup settles the hook's uncommitted output")
				reconciled, retryErr := orpheusgit.CaptureRevision(context.Background(), opts)
				require.NoError(t, retryErr)
				assert.Equal(t, head, reconciled.Commit)
				assert.Equal(t, "2", strings.TrimSpace(runGit(t, repo, "rev-list", "--count", "HEAD")))
			} else {
				assert.Equal(t, input, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")))
				assert.Equal(t, "feature\n", runGit(t, repo, "show", ":feature.txt"), "failed hooks/signing must not erase staged output")
				assert.ErrorContains(t, err, "revision commit")
			}
			if failure == "hook rejection" {
				assert.Equal(t, "hook preserved\n", readRevisionTestFile(t, repo, "hook-leftover.txt"))
			}
		})
	}
}

func TestIntegrationAdapterContractInterruptedRevisionCaptureReconcilesWithoutAnotherCommit(t *testing.T) {
	for _, loseObservation := range []bool{false, true} {
		t.Run(map[bool]string{false: "lost command result", true: "lost result and reconciliation"}[loseObservation], func(t *testing.T) {
			repo := newGitRepo(t)
			runGit(t, repo, "config", "user.name", "Test")
			runGit(t, repo, "config", "user.email", "test@example.test")
			input := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			require.NoError(t, os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("retained\n"), 0o644))
			opts := orpheusgit.RevisionCapture{Directory: repo, Input: orpheusgit.CheckoutIdentity{Commit: input, Ref: "refs/heads/main"}, Ref: "refs/orpheus/revisions/task/0/1", Message: "feat: recover interrupted output"}
			runner := &interruptedRevisionRunner{delegate: orpheusgit.NewCommandRunner(nil), loseObservation: loseObservation}
			_, err := orpheusgit.CaptureRevision(orpheusgit.ContextWithRunner(context.Background(), runner), opts)
			require.ErrorIs(t, err, context.Canceled)
			head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
			assert.NotEqual(t, input, head)

			revision, err := orpheusgit.CaptureRevision(context.Background(), opts)
			require.NoError(t, err)
			assert.Equal(t, head, revision.Commit)
			assert.Equal(t, "2", strings.TrimSpace(runGit(t, repo, "rev-list", "--count", "HEAD")))
			assert.Equal(t, head, strings.TrimSpace(runGit(t, repo, "rev-parse", revision.RetainedRef)))
			assert.Empty(t, runGit(t, repo, "status", "--porcelain=v1"))
		})
	}
}

func TestIntegrationAdapterContractRevisionCaptureRejectsUnrelatedMovementAndUnfinishedOperations(t *testing.T) {
	repo := newGitRepo(t)
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "user.email", "test@example.test")
	input := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	opts := orpheusgit.RevisionCapture{Directory: repo, Input: orpheusgit.CheckoutIdentity{Commit: input, Ref: "refs/heads/main"}, Ref: "refs/orpheus/revisions/task/0/1", Message: "feat: expected"}
	runGit(t, repo, "update-ref", opts.Ref+"/input", input)
	commitFile(t, repo, "unrelated.txt", "unrelated\n", "feat: expected")
	head := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	_, err := orpheusgit.CaptureRevision(context.Background(), opts)
	require.ErrorContains(t, err, "reflog does not identify this effort")
	assert.Equal(t, head, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")))
	assert.Equal(t, "unrelated\n", readRevisionTestFile(t, repo, "unrelated.txt"))

	runGit(t, repo, "checkout", "-b", "other")
	_, err = orpheusgit.CaptureRevision(context.Background(), opts)
	require.ErrorContains(t, err, "requires HEAD attached")
	runGit(t, repo, "checkout", "main")
	opts.Input.Commit = head
	opts.Ref = "refs/orpheus/revisions/task/0/2"
	for _, operation := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-apply", "rebase-merge", "sequencer", "index.lock"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(repo, ".git", operation)
			require.NoError(t, os.WriteFile(path, []byte(head), 0o644))
			_, err := orpheusgit.CaptureRevision(context.Background(), opts)
			require.ErrorContains(t, err, "settled Git state")
			assert.Equal(t, head, strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD")))
			assert.Equal(t, head, readRevisionTestFile(t, repo, filepath.Join(".git", operation)))
			require.NoError(t, os.Remove(path))
		})
	}
}

func TestIntegrationAdapterContractRevisionAdmissionChecksHeadRefRetentionIndexContentAndModes(t *testing.T) {
	repo := newGitRepo(t)
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "config", "user.email", "test@example.test")
	input := strings.TrimSpace(runGit(t, repo, "rev-parse", "HEAD"))
	require.NoError(t, os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("reviewed\n"), 0o644))
	require.NoError(t, os.Symlink("feature.txt", filepath.Join(repo, "link")))
	opts := orpheusgit.RevisionCapture{Directory: repo, Input: orpheusgit.CheckoutIdentity{Commit: input, Ref: "refs/heads/main"}, Ref: "refs/orpheus/revisions/task/0/1", Message: "feat: reviewed output"}
	revision, err := orpheusgit.CaptureRevision(context.Background(), opts)
	require.NoError(t, err)
	expected := orpheusgit.CheckoutIdentity{Commit: revision.Commit, Ref: opts.Input.Ref}
	runGit(t, repo, "config", "core.filemode", "false")
	for _, mutation := range []string{"wrong HEAD", "wrong branch", "detached HEAD", "wrong retention", "index", "tracked bytes", "executable mode", "untracked", "assume unchanged", "skip worktree", "symlink target", "symlink replacement"} {
		t.Run(mutation, func(t *testing.T) {
			switch mutation {
			case "wrong HEAD":
				runGit(t, repo, "reset", "--hard", input)
			case "wrong branch":
				runGit(t, repo, "checkout", "-b", "other")
			case "detached HEAD":
				runGit(t, repo, "checkout", "--detach", revision.Commit)
			case "wrong retention":
				runGit(t, repo, "update-ref", revision.RetainedRef, input)
			case "index":
				require.NoError(t, os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("staged mutation\n"), 0o644))
				runGit(t, repo, "add", "feature.txt")
				require.NoError(t, os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("reviewed\n"), 0o644))
			case "tracked bytes":
				require.NoError(t, os.WriteFile(filepath.Join(repo, "feature.txt"), []byte("mutation\n"), 0o644))
			case "executable mode":
				require.NoError(t, os.Chmod(filepath.Join(repo, "feature.txt"), 0o755))
			case "untracked":
				require.NoError(t, os.WriteFile(filepath.Join(repo, "extra.txt"), []byte("extra\n"), 0o644))
			case "assume unchanged":
				runGit(t, repo, "update-index", "--assume-unchanged", "feature.txt")
			case "skip worktree":
				runGit(t, repo, "update-index", "--skip-worktree", "feature.txt")
			case "symlink target":
				require.NoError(t, os.Remove(filepath.Join(repo, "link")))
				require.NoError(t, os.Symlink("missing.txt", filepath.Join(repo, "link")))
			case "symlink replacement":
				require.NoError(t, os.Remove(filepath.Join(repo, "feature.txt")))
				require.NoError(t, os.Symlink("link", filepath.Join(repo, "feature.txt")))
			}
			require.Error(t, orpheusgit.VerifyRevision(context.Background(), repo, expected, revision))
			runGit(t, repo, "checkout", "main")
			runGit(t, repo, "reset", "--hard", revision.Commit)
			runGit(t, repo, "update-index", "--no-assume-unchanged", "feature.txt")
			runGit(t, repo, "update-index", "--no-skip-worktree", "feature.txt")
			runGit(t, repo, "reset", "--hard", revision.Commit)
			runGit(t, repo, "update-ref", revision.RetainedRef, revision.Commit)
			require.NoError(t, os.Chmod(filepath.Join(repo, "feature.txt"), 0o644))
			runGit(t, repo, "clean", "-fd")
			require.NoError(t, orpheusgit.VerifyRevision(context.Background(), repo, expected, revision))
		})
	}
}

type interruptedRevisionRunner struct {
	delegate        orpheusgit.CommandRunner
	committed       bool
	loseObservation bool
}

func (r *interruptedRevisionRunner) Run(ctx context.Context, command orpheusgit.Command) (orpheusgit.CommandResult, error) {
	if r.committed && r.loseObservation {
		return orpheusgit.CommandResult{}, context.Canceled
	}
	result, err := r.delegate.Run(ctx, command)
	if len(command.Args) > 0 && command.Args[0] == "commit" && err == nil {
		r.committed = true
		return result, errors.Join(context.Canceled, errors.New("lost commit process observation"))
	}
	return result, err
}

func readRevisionTestFile(t *testing.T, dir, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, path))
	require.NoError(t, err)
	return string(data)
}
