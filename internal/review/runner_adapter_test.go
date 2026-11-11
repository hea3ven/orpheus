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

func TestIntegrationAdapterContractReviewCheckoutDetectionAndConfirmedRollback(t *testing.T) {
	mutations := map[string]func(*testing.T, string){
		"unstaged": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "candidate.txt"), []byte("unstaged\n"), 0o644))
		},
		"staged and unstaged": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "candidate.txt"), []byte("staged\n"), 0o644))
			runReviewAdapterGit(t, dir, "add", "candidate.txt")
			require.NoError(t, os.WriteFile(filepath.Join(dir, "candidate.txt"), []byte("unstaged\n"), 0o644))
		},
		"untracked": func(t *testing.T, dir string) {
			require.NoError(t, os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644))
		},
		"committed": func(t *testing.T, dir string) {
			runReviewAdapterGit(t, dir, "-c", "user.name=Reviewer", "-c", "user.email=r@example.test", "commit", "--allow-empty", "-m", "unwanted")
		},
		"other branch": func(t *testing.T, dir string) { runReviewAdapterGit(t, dir, "checkout", "-b", "other", "HEAD^") },
		"detached":     func(t *testing.T, dir string) { runReviewAdapterGit(t, dir, "checkout", "--detach", "HEAD^") },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			dir, candidate := committedReviewAdapterCandidate(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			check, err := CaptureCommittedCandidate(ctx, dir, candidate.Identity, candidate.Revision, nil)
			require.NoError(t, err)
			require.NoError(t, check())
			wrong := candidate.Identity
			wrong.Ref = "refs/heads/wrong"
			refused, err := CaptureCommittedCandidate(ctx, dir, wrong, candidate.Revision, nil)
			require.Error(t, err)
			require.Nil(t, refused)
			require.NoError(t, os.WriteFile(filepath.Join(dir, "ignored.log"), []byte("keep ignored\n"), 0o644))
			mutate(t, dir)
			headBefore := runReviewAdapterGit(t, dir, "rev-parse", "HEAD")
			statusBefore := runReviewAdapterGit(t, dir, "status", "--porcelain=v1")
			diffBefore := runReviewAdapterGit(t, dir, "diff", "HEAD")
			indexBefore := runReviewAdapterGit(t, dir, "diff", "--cached")
			refsBefore := runReviewAdapterGit(t, dir, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads/other")
			cancel() // Detection still observes the checkout after child cancellation.
			require.ErrorContains(t, check(), "changes preserved")
			assert.Equal(t, headBefore, runReviewAdapterGit(t, dir, "rev-parse", "HEAD"))
			assert.Equal(t, statusBefore, runReviewAdapterGit(t, dir, "status", "--porcelain=v1"))
			assert.Equal(t, diffBefore, runReviewAdapterGit(t, dir, "diff", "HEAD"))
			assert.Equal(t, indexBefore, runReviewAdapterGit(t, dir, "diff", "--cached"))

			require.NoError(t, RestoreCommittedCandidate(context.Background(), dir, candidate))
			require.NoError(t, gitmeta.CheckCheckout(context.Background(), dir, candidate.Identity))
			assert.Equal(t, "candidate\n", string(mustReadReviewAdapterFile(t, filepath.Join(dir, "candidate.txt"))))
			assert.Equal(t, "keep ignored\n", string(mustReadReviewAdapterFile(t, filepath.Join(dir, "ignored.log"))))
			assert.Equal(t, refsBefore, runReviewAdapterGit(t, dir, "for-each-ref", "--format=%(refname):%(objectname)", "refs/heads/other"))
			assert.NoError(t, check())
		})
	}
}

func committedReviewAdapterCandidate(t *testing.T) (string, CommittedCandidate) {
	t.Helper()
	dir := testutil.CanonicalTempDir(t)
	initReviewAdapterGitRepoWithCandidateChange(t, dir)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("*.log\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\n"), 0o644))
	runReviewAdapterGit(t, dir, "add", "--all")
	runReviewAdapterGit(t, dir, "-c", "user.name=Test", "-c", "user.email=t@example.test", "commit", "-m", "reviewed")
	head := strings.TrimSpace(runReviewAdapterGit(t, dir, "rev-parse", "HEAD"))
	revision := gitmeta.Revision{Commit: head, Tree: strings.TrimSpace(runReviewAdapterGit(t, dir, "rev-parse", "HEAD^{tree}")), RetainedRef: "refs/orpheus/revisions/reviewed"}
	runReviewAdapterGit(t, dir, "update-ref", revision.RetainedRef, head)
	return dir, CommittedCandidate{Identity: gitmeta.CheckoutIdentity{Commit: head, Ref: "refs/heads/main"}, Revision: revision}
}

func TestIntegrationAdapterContractReviewRollbackRefusesDataLossAndReportsFailure(t *testing.T) {
	dir, candidate := committedReviewAdapterCandidate(t)
	path := filepath.Join(dir, "candidate.txt")
	require.NoError(t, os.WriteFile(path, []byte("inspect me\n"), 0o644))
	lock := filepath.Join(dir, ".git", "index.lock")
	require.NoError(t, os.WriteFile(lock, []byte("existing lock"), 0o644))
	require.ErrorContains(t, RestoreCommittedCandidate(context.Background(), dir, candidate), "restore review checkout")
	assert.Equal(t, "inspect me\n", string(mustReadReviewAdapterFile(t, path)))
	assert.Equal(t, "existing lock", string(mustReadReviewAdapterFile(t, lock)))
	require.NoError(t, os.Remove(lock))

	outside := testutil.CanonicalTempDir(t)
	outsidePath := filepath.Join(outside, "candidate.txt")
	require.NoError(t, os.WriteFile(outsidePath, []byte("unrelated\n"), 0o644))
	runReviewAdapterGit(t, dir, "config", "core.worktree", outside)
	require.ErrorContains(t, RestoreCommittedCandidate(context.Background(), dir, candidate), "redirected")
	assert.Equal(t, "unrelated\n", string(mustReadReviewAdapterFile(t, outsidePath)))
	assert.Equal(t, "inspect me\n", string(mustReadReviewAdapterFile(t, path)))
	runReviewAdapterGit(t, dir, "config", "--unset", "core.worktree")

	// Resetting .gitignore must not turn an ignored file into clean's victim.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*.log\ninspection.txt\nscratch/\n"), 0o644))
	inspection := filepath.Join(dir, "inspection.txt")
	require.NoError(t, os.WriteFile(inspection, []byte("preserve ignored output\n"), 0o644))
	scratch := filepath.Join(dir, "scratch")
	require.NoError(t, os.Mkdir(scratch, 0o755))
	wildcard := filepath.Join(scratch, "[keep]*?.txt")
	require.NoError(t, os.WriteFile(wildcard, []byte("literal filename\n"), 0o644))
	require.ErrorContains(t, RestoreCommittedCandidate(context.Background(), dir, candidate), "uncommitted changes")
	assert.Equal(t, "preserve ignored output\n", string(mustReadReviewAdapterFile(t, inspection)))
	assert.Equal(t, "literal filename\n", string(mustReadReviewAdapterFile(t, wildcard)))
	require.NoError(t, os.Remove(inspection))
	require.NoError(t, os.RemoveAll(scratch))

	runReviewAdapterGit(t, dir, "rm", "--force", "candidate.txt")
	require.NoError(t, os.WriteFile(path, []byte("ignored operator data\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("candidate.txt\n"), 0o644))
	require.ErrorContains(t, RestoreCommittedCandidate(context.Background(), dir, candidate), "ignored path")
	assert.Equal(t, "ignored operator data\n", string(mustReadReviewAdapterFile(t, path)))
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
