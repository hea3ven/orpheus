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
	if _, err := os.Stat(createdPath); !os.IsNotExist(err) {
		t.Fatalf("reviewer-created file stat error = %v, want file removed", err)
	}
	status := runReviewAdapterGit(t, workdir, "status", "--short")
	for _, want := range []string{" M tracked.txt", "?? untracked.txt"} {
		if !strings.Contains(status, want) {
			t.Fatalf("git status = %q, want %q", status, want)
		}
	}
	if strings.Contains(status, "created-by-review.txt") {
		t.Fatalf("git status = %q, want reviewer-created file removed", status)
	}
}

func TestIntegrationAdapterContractCandidateSnapshotRestoresRedirectedStderrFile(t *testing.T) {
	workdir := testutil.CanonicalTempDir(t)
	candidatePath := initReviewAdapterGitRepoWithCandidateChange(t, workdir)
	logPath := filepath.Join(workdir, "review.log")
	if err := os.WriteFile(logPath, nil, 0o644); err != nil {
		t.Fatalf("create redirected stderr file: %v", err)
	}

	snapshot, err := captureCandidateSnapshot(context.Background(), workdir, nil)
	if err != nil {
		t.Fatalf("capture candidate snapshot: %v", err)
	}
	if err := os.WriteFile(logPath, []byte("== Review step: unit (check) ==\n"), 0o644); err != nil {
		t.Fatalf("write redirected stderr: %v", err)
	}

	err = restoreCandidateIfMutated(context.Background(), snapshot, nil)

	if err == nil || !strings.Contains(err.Error(), "review step mutated candidate changes") {
		t.Fatalf("restore error = %v, want candidate mutation error", err)
	}
	if got := string(mustReadReviewAdapterFile(t, candidatePath)); got != "candidate\n" {
		t.Fatalf("candidate file = %q, want restored candidate change", got)
	}
	if got := string(mustReadReviewAdapterFile(t, logPath)); got != "" {
		t.Fatalf("redirected stderr file = %q, want restored empty file", got)
	}
	status := runReviewAdapterGit(t, workdir, "status", "--short")
	for _, want := range []string{" M candidate.txt", "?? review.log"} {
		if !strings.Contains(status, want) {
			t.Fatalf("git status = %q, want %q", status, want)
		}
	}
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
