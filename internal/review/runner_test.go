package review_test

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

//nolint:funlen // The redirected-output regression is clearer as one end-to-end runner test.
func TestRunPipelineRestoresHeaderWrittenToWorktreeStderr(t *testing.T) {
	workdir := t.TempDir()
	initReviewTestGitRepo(t, workdir)
	candidatePath := filepath.Join(workdir, "candidate.txt")
	if err := os.WriteFile(candidatePath, []byte("base\n"), 0o644); err != nil {
		t.Fatalf("write base candidate file: %v", err)
	}
	runReviewTestGit(t, workdir, "add", "candidate.txt")
	runReviewTestGit(t, workdir,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "-m", "add candidate file",
	)
	if err := os.WriteFile(candidatePath, []byte("candidate\n"), 0o644); err != nil {
		t.Fatalf("write candidate change: %v", err)
	}

	paths := newTestPaths(t)
	store := taskstate.NewStore(paths)
	attempt, err := store.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "unit",
	})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}

	logPath := filepath.Join(workdir, "review.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create review log: %v", err)
	}

	_, err = review.RunPipeline(review.PipelineRunOptions{
		Context: context.Background(),
		Store:   store,
		RepoID:  "alpha",
		TaskID:  "op-1",
		Branch:  "main",
		Workdir: workdir,
		Attempt: attempt,
		Pipeline: review.Pipeline{
			Name: "standard",
			Steps: []review.Step{{
				Kind:    review.KindCheck,
				Name:    "unit",
				Command: "sh",
				Args:    []string{"-c", "true"},
			}},
		},
		Stdout: io.Discard,
		Stderr: logFile,
	})
	if err == nil || !strings.Contains(err.Error(), "review step mutated candidate changes") {
		t.Fatalf("RunPipeline error = %v, want candidate mutation error", err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatalf("close review log: %v", err)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read restored review log: %v", err)
	}
	if err == nil && len(logData) != 0 {
		t.Fatalf("review log = %q, want restored empty file", logData)
	}
	status := runReviewTestGit(t, workdir, "status", "--short")
	if !strings.Contains(status, " M candidate.txt") {
		t.Fatalf("git status = %q, want restored candidate change", status)
	}
	if err == nil && !strings.Contains(status, "?? review.log") {
		t.Fatalf("git status = %q, want restored redirected stderr file", status)
	}
	if strings.Contains(status, "review.log") && err != nil {
		t.Fatalf("git status = %q, want redirected stderr file removed", status)
	}
}

func initReviewTestGitRepo(t *testing.T, dir string) {
	t.Helper()

	runReviewTestGit(t, dir, "init")
	runReviewTestGit(t, dir, "checkout", "-b", "main")
	runReviewTestGit(t, dir,
		"-c", "user.name=Orpheus Test",
		"-c", "user.email=orpheus@example.com",
		"commit", "--allow-empty", "-m", "initial",
	)
}

func runReviewTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()

	command := exec.Command("git", args...)
	command.Dir = dir
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}
	return string(output)
}
