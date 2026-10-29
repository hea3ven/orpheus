//go:build integration

//nolint:testpackage // Exercises private candidate and Hunk adapters directly.
package review

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAdapterContractCandidateSnapshotRestoresTrackedAndUntrackedMutations(t *testing.T) {
	workdir := testutil.CanonicalTempDir(t)
	initReviewAdapterGitRepo(t, workdir)
	trackedPath := filepath.Join(workdir, "tracked.txt")
	if err := os.WriteFile(trackedPath, []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base tracked file: %v", err)
	}
	runReviewAdapterGit(t, workdir, "add", "tracked.txt")
	runReviewAdapterGit(t, workdir,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "-m", "add tracked file",
	)
	if err := os.WriteFile(trackedPath, []byte("candidate\n"), 0o644); err != nil {
		t.Fatalf("write candidate tracked change: %v", err)
	}
	untrackedPath := filepath.Join(workdir, "untracked.txt")
	logPath := filepath.Join(workdir, "review.log")
	require.NoError(t, os.WriteFile(logPath, nil, 0o644))
	if err := os.WriteFile(untrackedPath, []byte("candidate untracked\n"), 0o644); err != nil {
		t.Fatalf("write candidate untracked file: %v", err)
	}

	snapshot, err := captureCandidateSnapshot(context.Background(), workdir, nil)
	if err != nil {
		t.Fatalf("capture candidate snapshot: %v", err)
	}
	if err := os.WriteFile(trackedPath, []byte("mutated\n"), 0o644); err != nil {
		t.Fatalf("mutate tracked file: %v", err)
	}
	require.NoError(t, os.WriteFile(logPath, []byte("review output\n"), 0o644))
	if err := os.Remove(untrackedPath); err != nil {
		t.Fatalf("remove untracked file: %v", err)
	}
	createdPath := filepath.Join(workdir, "created-by-review.txt")
	if err := os.WriteFile(createdPath, []byte("new\n"), 0o644); err != nil {
		t.Fatalf("write reviewer-created file: %v", err)
	}

	err = restoreCandidateIfMutated(context.Background(), snapshot, nil)

	if err == nil || !strings.Contains(err.Error(), "review step mutated candidate changes") {
		t.Fatalf("restore error = %v, want candidate mutation error", err)
	}
	if got := string(mustReadReviewAdapterFile(t, trackedPath)); got != "candidate\n" {
		t.Fatalf("tracked file = %q, want restored candidate change", got)
	}
	if got := string(mustReadReviewAdapterFile(t, untrackedPath)); got != "candidate untracked\n" {
		t.Fatalf("untracked file = %q, want restored candidate file", got)
	}
	assert.Empty(t, mustReadReviewAdapterFile(t, logPath), "initially empty file must survive restoration")
	if _, err := os.Stat(createdPath); !os.IsNotExist(err) {
		t.Fatalf("reviewer-created file stat error = %v, want file removed", err)
	}
	status := runReviewAdapterGit(t, workdir, "status", "--short")
	for _, want := range []string{" M tracked.txt", "?? untracked.txt", "?? review.log"} {
		if !strings.Contains(status, want) {
			t.Fatalf("git status = %q, want %q", status, want)
		}
	}
	if strings.Contains(status, "created-by-review.txt") {
		t.Fatalf("git status = %q, want reviewer-created file removed", status)
	}
	for name, mutate := range committedReviewMutations() {
		t.Run(name, func(t *testing.T) { assertCommittedReviewRestoration(t, mutate) })
	}
}

type reviewMutation func(*testing.T, string)

func committedReviewMutations() map[string]reviewMutation {
	return map[string]reviewMutation{
		"committed output and new tracked file": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("committed mutation\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644))
			runReviewAdapterGit(t, dir, "add", "--all")
			runReviewAdapterGit(t, dir, "-c", "user.name=Reviewer", "-c", "user.email=reviewer@example.test", "commit", "-m", "unwanted commit")
		},
		"clean wrong ref at expected commit": func(t *testing.T, dir string) {
			runReviewAdapterGit(t, dir, "checkout", "-b", "other")
		},
		"clean wrong ref at different commit": func(t *testing.T, dir string) {
			runReviewAdapterGit(t, dir, "checkout", "-b", "other", "HEAD^")
		},
		"clean detached wrong commit": func(t *testing.T, dir string) {
			runReviewAdapterGit(t, dir, "checkout", "--detach", "HEAD^")
		},
		"different index and worktree content": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("staged\n"), 0o644))
			runReviewAdapterGit(t, dir, "add", "tracked.txt")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("unstaged\n"), 0o644))
		},
		"tracked mode hidden by repository configuration": func(t *testing.T, dir string) {
			runReviewAdapterGit(t, dir, "config", "core.filemode", "false")
			require.NoError(t, os.Chmod(filepath.Join(dir, "tracked.txt"), 0o644))
		},
		"index flags hide tracked content": func(t *testing.T, dir string) {
			runReviewAdapterGit(t, dir, "update-index", "--assume-unchanged", "tracked.txt")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("hidden mutation\n"), 0o755))
		},
		"file replaced by symlink": func(t *testing.T, dir string) {
			require.NoError(t, os.Remove(filepath.Join(dir, "tracked.txt")))
			require.NoError(t, os.Symlink("nested/file.txt", filepath.Join(dir, "tracked.txt")))
		},
		"tracked parent replaced by symlink": func(t *testing.T, dir string) {
			outside := testutil.CanonicalTempDir(t)
			outsidePath := filepath.Join(outside, "file.txt")
			require.NoError(t, os.WriteFile(outsidePath, []byte("unrelated operator data\n"), 0o644))
			t.Cleanup(func() {
				assert.Equal(t, "unrelated operator data\n", string(mustReadReviewAdapterFile(t, outsidePath)))
			})
			require.NoError(t, os.Remove(filepath.Join(dir, "nested", "file.txt")))
			require.NoError(t, os.Remove(filepath.Join(dir, "nested")))
			require.NoError(t, os.Symlink(outside, filepath.Join(dir, "nested")))
		},
		"corrupt index": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "index"), []byte("corrupt"), 0o644))
		},
	}
}

func assertCommittedReviewRestoration(t *testing.T, mutate reviewMutation) {
	t.Helper()
	dir := testutil.CanonicalTempDir(t)
	initReviewAdapterGitRepo(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "tracked.txt"), []byte("reviewed\n"), 0o755))
	require.NoError(t, os.Chmod(filepath.Join(dir, "tracked.txt"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "nested"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "nested", "file.txt"), []byte("nested reviewed\n"), 0o640))
	runReviewAdapterGit(t, dir, "add", "--all")
	runReviewAdapterGit(t, dir, "-c", "user.name=Orpheus Test", "-c", "user.email=orpheus@example.com", "commit", "-m", "reviewed revision")
	head := strings.TrimSpace(runReviewAdapterGit(t, dir, "rev-parse", "HEAD"))
	revision := gitmeta.Revision{Commit: head, Tree: strings.TrimSpace(runReviewAdapterGit(t, dir, "rev-parse", "HEAD^{tree}")), RetainedRef: "refs/orpheus/revisions/reviewed"}
	runReviewAdapterGit(t, dir, "update-ref", revision.RetainedRef, head)
	expected := gitmeta.CheckoutIdentity{Commit: head, Ref: "refs/heads/main"}
	wrong := expected
	wrong.Ref = "refs/heads/another-task"
	refused, err := CaptureCommittedCandidate(context.Background(), dir, wrong, revision, nil)
	require.ErrorContains(t, err, "requires HEAD")
	require.Nil(t, refused, "a clean wrong checkout must not become the snapshot authority")
	entries := runReviewAdapterGit(t, dir, "ls-files", "--stage", "-v", "-z")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	check, err := CaptureCommittedCandidate(ctx, dir, expected, revision, nil)
	require.NoError(t, err)
	require.NoError(t, check(), "unchanged input must not be diagnosed as mutation")

	mutate(t, dir)
	otherBefore := runReviewAdapterGit(t, dir, "for-each-ref", "--format=%(objectname)", "refs/heads/other")
	// Restoration must remain possible after the reviewer process is canceled.
	cancel()
	err = check()
	require.ErrorContains(t, err, "restored the pre-step snapshot")
	assert.Equal(t, head, strings.TrimSpace(runReviewAdapterGit(t, dir, "rev-parse", "HEAD")))
	assert.Equal(t, "refs/heads/main", strings.TrimSpace(runReviewAdapterGit(t, dir, "symbolic-ref", "HEAD")))
	assert.Equal(t, otherBefore, runReviewAdapterGit(t, dir, "for-each-ref", "--format=%(objectname)", "refs/heads/other"), "rollback must not move an unrelated selected branch")
	assert.Equal(t, entries, runReviewAdapterGit(t, dir, "ls-files", "--stage", "-v", "-z"))
	assert.Equal(t, "reviewed\n", string(mustReadReviewAdapterFile(t, filepath.Join(dir, "tracked.txt"))))
	assert.Equal(t, "nested reviewed\n", string(mustReadReviewAdapterFile(t, filepath.Join(dir, "nested", "file.txt"))))
	info, err := os.Lstat(filepath.Join(dir, "tracked.txt"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	assert.True(t, info.Mode().IsRegular(), "a reviewer-created symlink must not survive restoration")
	assert.Empty(t, runReviewAdapterGit(t, dir, "status", "--porcelain=v1"))
	require.NoError(t, check(), "full checkout and index verification must agree after restore")
}

func TestIntegrationAdapterContractCandidateRollbackReportsIncompleteRestoration(t *testing.T) {
	dir := testutil.CanonicalTempDir(t)
	path := initReviewAdapterGitRepoWithCandidateChange(t, dir)
	snapshot, err := captureCandidateSnapshot(context.Background(), dir, nil)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("mutated\n"), 0o644))
	lockPath := filepath.Join(dir, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lockPath, []byte("other Git operation"), 0o644))

	err = restoreCandidateIfMutated(context.Background(), snapshot, nil)
	require.ErrorContains(t, err, "automatic restore failed")
	assert.ErrorContains(t, err, "manual repair required")
	assert.Equal(t, "mutated\n", string(mustReadReviewAdapterFile(t, path)))
	assert.Equal(t, "other Git operation", string(mustReadReviewAdapterFile(t, lockPath)))
	require.NoError(t, os.Remove(lockPath))
	require.ErrorContains(t, restoreCandidateIfMutated(context.Background(), snapshot, nil), "restored the pre-step snapshot")
	assert.Equal(t, "candidate\n", string(mustReadReviewAdapterFile(t, path)))

	outside := testutil.CanonicalTempDir(t)
	outsidePath := filepath.Join(outside, "candidate.txt")
	require.NoError(t, os.WriteFile(outsidePath, []byte("unrelated operator data\n"), 0o644))
	require.NoError(t, os.WriteFile(path, []byte("another mutation\n"), 0o644))
	runReviewAdapterGit(t, dir, "config", "core.worktree", outside)
	err = restoreCandidateIfMutated(context.Background(), snapshot, nil)
	require.ErrorContains(t, err, "automatic restore failed")
	assert.ErrorContains(t, err, "redirected")
	assert.Equal(t, "unrelated operator data\n", string(mustReadReviewAdapterFile(t, outsidePath)), "rollback must not reset a redirected work directory")
	assert.Equal(t, "another mutation\n", string(mustReadReviewAdapterFile(t, path)))
	runReviewAdapterGit(t, dir, "config", "--unset", "core.worktree")
	require.ErrorContains(t, restoreCandidateIfMutated(context.Background(), snapshot, nil), "restored the pre-step snapshot")
	assert.Equal(t, "candidate\n", string(mustReadReviewAdapterFile(t, path)))
}

func TestIntegrationAdapterContractHunkManualCommandCapturesNotesAfterCommandExit(t *testing.T) {
	workdir := testutil.CanonicalTempDir(t)

	controlDir := testutil.CanonicalTempDir(t)
	initialCapturePath := filepath.Join(controlDir, "initial-capture")
	commandCompletePath := filepath.Join(controlDir, "command-complete")
	binDir := testutil.CanonicalTempDir(t)
	installReviewAdapterHunkCommand(t, binDir, initialCapturePath, commandCompletePath)

	manual := writeReviewAdapterScript(t, workdir, "hunk-backed-manual", fmt.Sprintf(`#!/bin/sh
i=0
while [ ! -f %s ] && [ "$i" -lt 200 ]; do
  i=$((i + 1))
  sleep 0.01
done
[ -f %s ] || exit 70
touch %s
`, shellQuoteReviewAdapter(initialCapturePath), shellQuoteReviewAdapter(initialCapturePath), shellQuoteReviewAdapter(commandCompletePath)))

	exitCode, notes, err := runHunkBackedManualCommand(PipelineRunOptions{
		Context:     context.Background(),
		Workdir:     workdir,
		Environment: []string{"PATH=" + binDir + string(os.PathListSeparator) + os.Getenv("PATH")},
		Stdout:      io.Discard,
		Stderr:      io.Discard,
	}, Step{Kind: KindManual, Name: "inspect", Command: manual, HunkNotes: true}, nil)

	if err != nil {
		t.Fatalf("run Hunk-backed manual command: %v", err)
	}
	if exitCode == nil || *exitCode != 0 {
		t.Fatalf("exit code = %v, want 0", exitCode)
	}
	if len(notes) != 1 {
		t.Fatalf("captured Hunk notes = %#v, want one post-exit note", notes)
	}
	if notes[0].NoteID != "user:late" {
		t.Fatalf("captured note ID = %q, want user:late", notes[0].NoteID)
	}
}

func initReviewAdapterGitRepoWithCandidateChange(t *testing.T, workdir string) string {
	t.Helper()

	initReviewAdapterGitRepo(t, workdir)
	candidatePath := filepath.Join(workdir, "candidate.txt")
	if err := os.WriteFile(candidatePath, []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base candidate file: %v", err)
	}
	runReviewAdapterGit(t, workdir, "add", "candidate.txt")
	runReviewAdapterGit(t, workdir,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "-m", "add candidate file",
	)
	if err := os.WriteFile(candidatePath, []byte("candidate\n"), 0o644); err != nil {
		t.Fatalf("write candidate change: %v", err)
	}
	return candidatePath
}

func initReviewAdapterGitRepo(t *testing.T, dir string) {
	t.Helper()

	runReviewAdapterGit(t, dir, "init")
	runReviewAdapterGit(t, dir, "checkout", "-b", "main")
	runReviewAdapterGit(t, dir,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "--allow-empty", "-m", "initial",
	)
}

func runReviewAdapterGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return string(output)
}

func mustReadReviewAdapterFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func writeReviewAdapterScript(t *testing.T, dir string, name string, content string) string {
	t.Helper()

	path := filepath.Join(dir, name+".sh")
	if err := testguard.WriteExecutable(path, []byte(content)); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func installReviewAdapterHunkCommand(t *testing.T, binDir string, initialCapturePath string, commandCompletePath string) {
	t.Helper()

	noteResponse := `{"comments":[{"noteId":"user:late","source":"user","filePath":"late.go","newRange":[42,42],"body":"late note"}]}`
	script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "session" ] && [ "$2" = "comment" ] && [ "$3" = "list" ]; then
  if [ -f %s ]; then
    printf '%%s\n' %s
  else
    : > %s
    printf '%%s\n' '{"comments":[]}'
  fi
  exit 0
fi
printf 'unexpected fake hunk call: %%s\n' "$*" >&2
exit 65
`,
		shellQuoteReviewAdapter(commandCompletePath),
		shellQuoteReviewAdapter(noteResponse),
		shellQuoteReviewAdapter(initialCapturePath),
	)
	hunkPath := filepath.Join(binDir, "hunk")
	if err := testguard.WriteExecutable(hunkPath, []byte(script)); err != nil {
		t.Fatalf("write fake hunk command: %v", err)
	}
}

func shellQuoteReviewAdapter(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func TestIntegrationAdapterContractHunkManualCommandHandlesMissingSessionAndExitFailure(t *testing.T) {
	for _, code := range []int{0, 7} {
		t.Run(fmt.Sprintf("exit-%d", code), func(t *testing.T) {
			workdir := testutil.CanonicalTempDir(t)
			binDir := testutil.CanonicalTempDir(t)
			pollLog := filepath.Join(binDir, "polls")
			hunk := fmt.Sprintf(`#!/bin/sh
[ "$*" = "session comment list --repo %s --type user --json" ] || exit 65
[ "$PWD" = "%s" ] || exit 66
[ "$INVOCATION_SCOPE" = isolated ] || exit 67
printf 'poll\n' >> %s
printf 'no active session\n' >&2
exit 1
`, workdir, workdir, shellQuoteReviewAdapter(pollLog))
			if err := testguard.WriteExecutable(filepath.Join(binDir, "hunk"), []byte(hunk)); err != nil {
				t.Fatal(err)
			}
			manual := writeReviewAdapterScript(t, binDir, "manual", fmt.Sprintf(`#!/bin/sh
printf '%%s/%%s/%%s/%%s' "$PWD" "$1" "$INVOCATION_SCOPE" "$STEP_SCOPE"
printf 'manual stderr\n' >&2
exit %d
`, code))
			var stdout, stderr bytes.Buffer
			gotCode, notes, err := runHunkBackedManualCommand(PipelineRunOptions{Context: context.Background(), Workdir: workdir, Environment: []string{"PATH=" + binDir, "INVOCATION_SCOPE=isolated", "STEP_SCOPE=stale"}, Stdout: &stdout, Stderr: &stderr}, Step{Kind: KindManual, Name: "inspect", Command: manual, Args: []string{"argument with spaces"}, HunkNotes: true}, []string{"STEP_SCOPE=step"})
			require.NotNil(t, gotCode)
			assert.Equal(t, code, *gotCode)
			if code == 0 {
				require.NoError(t, err)
			} else {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr)
				assert.Equal(t, code, exitErr.ExitCode())
			}
			assert.Empty(t, notes, "missing session must not produce notes")
			assert.Equal(t, workdir+"/argument with spaces/isolated/step", stdout.String())
			assert.Equal(t, "manual stderr\n", stderr.String())
			assert.GreaterOrEqual(t, strings.Count(string(mustReadReviewAdapterFile(t, pollLog)), "poll\n"), 2, "initial and post-exit capture")
		})
	}
}
