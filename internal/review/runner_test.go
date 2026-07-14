package review_test

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

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
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

func TestRunPipelineInteractivePassingCheckClearsRollingTail(t *testing.T) {
	workdir := t.TempDir()
	initReviewTestGitRepo(t, workdir)
	check := writeReviewTestScript(t, workdir, "passing-check", `#!/bin/sh
i=1
while [ "$i" -le 12 ]; do
  printf 'stdout %02d\n' "$i"
  printf 'stderr %02d\n' "$i" >&2
  i=$((i + 1))
done
`)
	store, attempt := startReviewTestAttempt(t)
	var stdout bytes.Buffer
	terminal := newVisualTerminal()

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context:           context.Background(),
		Store:             store,
		RepoID:            "alpha",
		TaskID:            "op-1",
		Branch:            "main",
		Workdir:           workdir,
		Attempt:           attempt,
		Pipeline:          singleStepPipeline(review.KindCheck, "unit", check),
		Stdout:            &stdout,
		Stderr:            terminal,
		InteractiveOutput: true,
	})

	if err != nil {
		t.Fatalf("RunPipeline error = %v", err)
	}
	if outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome = %q, want passed", outcome.Status)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want rolling output captured away from stdout", stdout.String())
	}
	visible := terminal.Visible()
	if !strings.Contains(visible, "== Review step: unit (check) ==") {
		t.Fatalf("visible terminal = %q, want step header", visible)
	}
	if strings.Contains(visible, "stdout") || strings.Contains(visible, "stderr") {
		t.Fatalf("visible terminal = %q, want passing check tail cleared", visible)
	}
	if !strings.Contains(terminal.raw.String(), "\x1b[31mstderr 12\x1b[0m") {
		t.Fatalf("raw terminal output does not color stderr distinctly: %q", terminal.raw.String())
	}
}

func TestRunPipelineInteractiveBlockedCheckLeavesExpandedRollingTail(t *testing.T) {
	workdir := t.TempDir()
	initReviewTestGitRepo(t, workdir)
	check := writeReviewTestScript(t, workdir, "blocked-check", `#!/bin/sh
i=1
while [ "$i" -le 35 ]; do
  printf 'stdout %02d\n' "$i"
  i=$((i + 1))
done
exit 7
`)
	store, attempt := startReviewTestAttempt(t)
	var stdout bytes.Buffer
	terminal := newVisualTerminal()

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context:           context.Background(),
		Store:             store,
		RepoID:            "alpha",
		TaskID:            "op-1",
		Branch:            "main",
		Workdir:           workdir,
		Attempt:           attempt,
		Pipeline:          singleStepPipeline(review.KindCheck, "unit", check),
		Stdout:            &stdout,
		Stderr:            terminal,
		InteractiveOutput: true,
	})

	if err != nil {
		t.Fatalf("RunPipeline error = %v", err)
	}
	if outcome.Status != taskstate.ReviewStatusBlocked {
		t.Fatalf("outcome = %q, want blocked", outcome.Status)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want rolling output captured away from stdout", stdout.String())
	}
	visible := terminal.Visible()
	for _, want := range []string{"stdout 06", "stdout 35", "Review blocked for op-1 by check \"unit\"."} {
		if !strings.Contains(visible, want) {
			t.Fatalf("visible terminal = %q, want %q", visible, want)
		}
	}
	if strings.Contains(visible, "stdout 05") {
		t.Fatalf("visible terminal = %q, want expanded tail bounded to latest 30 lines", visible)
	}
}

func TestRunPipelinePausesBeforeManualStep(t *testing.T) {
	workdir := t.TempDir()
	initReviewTestGitRepo(t, workdir)
	check := writeReviewTestScript(t, workdir, "passing-check", `#!/bin/sh
printf 'checked\n'
`)
	store, attempt := startReviewTestAttempt(t)
	pipeline := review.Pipeline{
		Name: "standard",
		Steps: []review.Step{
			{Kind: review.KindCheck, Name: "lint", Command: check},
			{Kind: review.KindManual, Name: "inspect"},
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context:           context.Background(),
		Store:             store,
		RepoID:            "alpha",
		TaskID:            "op-1",
		Branch:            "main",
		Workdir:           workdir,
		Attempt:           attempt,
		Pipeline:          pipeline,
		Stdout:            &stdout,
		Stderr:            &stderr,
		PauseBeforeManual: true,
	})

	if err != nil {
		t.Fatalf("RunPipeline error = %v", err)
	}
	if outcome.Status != taskstate.ReviewStatusWaitingForManual {
		t.Fatalf("outcome = %q, want waiting_for_manual", outcome.Status)
	}
	taskState, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load task state: %v", err)
	}
	latest, ok := taskstate.LatestReview(taskState)
	if !ok {
		t.Fatal("latest review missing")
	}
	if latest.Status != taskstate.ReviewStatusWaitingForManual || latest.Step != "inspect" || len(latest.Steps) != 1 {
		t.Fatalf("latest review = %#v, want paused at inspect after one check", latest)
	}
	if !strings.Contains(stderr.String(), "Resume with `orpheus task review op-1`") {
		t.Fatalf("stderr = %q, want resume guidance", stderr.String())
	}
}

func TestRunPipelineHunkManualCommandCapturesNotesAfterCommandExit(t *testing.T) {
	workdir := t.TempDir()
	initReviewTestGitRepo(t, workdir)

	controlDir := t.TempDir()
	initialCapturePath := filepath.Join(controlDir, "initial-capture")
	commandCompletePath := filepath.Join(controlDir, "command-complete")
	installReviewTestHunkCommand(t, initialCapturePath, commandCompletePath)

	manual := writeReviewTestScript(t, workdir, "hunk-backed-manual", fmt.Sprintf(`#!/bin/sh
i=0
while [ ! -f %s ] && [ "$i" -lt 200 ]; do
  i=$((i + 1))
  sleep 0.01
done
[ -f %s ] || exit 70
	touch %s
`, shellQuoteReviewTest(initialCapturePath), shellQuoteReviewTest(initialCapturePath), shellQuoteReviewTest(commandCompletePath)))
	store, attempt := startReviewTestAttempt(t)
	pipeline := singleStepPipeline(review.KindManual, "inspect", manual)
	pipeline.Steps[0].HunkNotes = true
	var captured []review.HunkNote

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context:  context.Background(),
		Store:    store,
		RepoID:   "alpha",
		TaskID:   "op-1",
		Branch:   "main",
		Workdir:  workdir,
		Attempt:  attempt,
		Pipeline: pipeline,
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		RenderManualStep: func(step review.Step) error {
			return nil
		},
		ConfirmManualCommand: func(step review.Step) (bool, error) {
			return true, nil
		},
		PromptManualStep: func(step review.ManualStep) (review.ManualResult, error) {
			captured = append([]review.HunkNote{}, step.HunkNotes...)
			return review.ManualResult{Status: taskstate.ReviewStatusPassed}, nil
		},
	})

	if err != nil {
		t.Fatalf("RunPipeline error = %v", err)
	}
	if outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome = %q, want passed", outcome.Status)
	}
	if len(captured) != 1 {
		t.Fatalf("captured Hunk notes = %#v, want one post-exit note", captured)
	}
	if captured[0].NoteID != "user:late" {
		t.Fatalf("captured note ID = %q, want user:late", captured[0].NoteID)
	}
}

func TestRunPipelineHunkManualCommandContinuesWhenSessionMissing(t *testing.T) {
	workdir := t.TempDir()
	initReviewTestGitRepo(t, workdir)

	controlDir := t.TempDir()
	hunkCallPath := filepath.Join(controlDir, "hunk-called")
	installReviewTestHunkCommandScript(t, fmt.Sprintf(`#!/bin/sh
if [ "$1" = "session" ] && [ "$2" = "comment" ] && [ "$3" = "list" ]; then
  : > %s
  exit 64
fi
printf 'unexpected fake hunk call: %%s\n' "$*" >&2
exit 65
`, shellQuoteReviewTest(hunkCallPath)))
	manualRanPath := filepath.Join(controlDir, "manual-ran")
	manual := writeReviewTestScript(t, workdir, "hunk-missing-session-manual", fmt.Sprintf(`#!/bin/sh
: > %s
`, shellQuoteReviewTest(manualRanPath)))
	store, attempt := startReviewTestAttempt(t)
	pipeline := singleStepPipeline(review.KindManual, "inspect", manual)
	pipeline.Steps[0].HunkNotes = true
	var captured []review.HunkNote

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context:  context.Background(),
		Store:    store,
		RepoID:   "alpha",
		TaskID:   "op-1",
		Branch:   "main",
		Workdir:  workdir,
		Attempt:  attempt,
		Pipeline: pipeline,
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		RenderManualStep: func(step review.Step) error {
			return nil
		},
		ConfirmManualCommand: func(step review.Step) (bool, error) {
			return true, nil
		},
		PromptManualStep: func(step review.ManualStep) (review.ManualResult, error) {
			captured = append([]review.HunkNote{}, step.HunkNotes...)
			return review.ManualResult{Status: taskstate.ReviewStatusPassed}, nil
		},
	})

	if err != nil {
		t.Fatalf("RunPipeline error = %v", err)
	}
	if outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome = %q, want passed", outcome.Status)
	}
	if len(captured) != 0 {
		t.Fatalf("captured Hunk notes = %#v, want none", captured)
	}
	if _, err := os.Stat(manualRanPath); err != nil {
		t.Fatalf("manual command marker: %v", err)
	}
	if _, err := os.Stat(hunkCallPath); err != nil {
		t.Fatalf("hunk capture marker: %v", err)
	}
}

func TestRunPipelineGenericManualCommandDoesNotPollHunkNotes(t *testing.T) {
	workdir := t.TempDir()
	initReviewTestGitRepo(t, workdir)

	controlDir := t.TempDir()
	hunkCallPath := filepath.Join(controlDir, "hunk-called")
	installReviewTestHunkCommandScript(t, fmt.Sprintf(`#!/bin/sh
: > %s
printf '{"comments":[{"noteId":"unexpected","source":"user","body":"unexpected"}]}\n'
`, shellQuoteReviewTest(hunkCallPath)))
	manual := writeReviewTestScript(t, workdir, "generic-manual", "#!/bin/sh\n")
	store, attempt := startReviewTestAttempt(t)
	pipeline := singleStepPipeline(review.KindManual, "inspect", manual)
	var captured []review.HunkNote

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context:  context.Background(),
		Store:    store,
		RepoID:   "alpha",
		TaskID:   "op-1",
		Branch:   "main",
		Workdir:  workdir,
		Attempt:  attempt,
		Pipeline: pipeline,
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		RenderManualStep: func(step review.Step) error {
			return nil
		},
		ConfirmManualCommand: func(step review.Step) (bool, error) {
			return true, nil
		},
		PromptManualStep: func(step review.ManualStep) (review.ManualResult, error) {
			captured = append([]review.HunkNote{}, step.HunkNotes...)
			return review.ManualResult{Status: taskstate.ReviewStatusPassed}, nil
		},
	})

	if err != nil {
		t.Fatalf("RunPipeline error = %v", err)
	}
	if outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome = %q, want passed", outcome.Status)
	}
	if len(captured) != 0 {
		t.Fatalf("captured Hunk notes = %#v, want none", captured)
	}
	if _, err := os.Stat(hunkCallPath); !os.IsNotExist(err) {
		t.Fatalf("hunk capture marker exists or stat failed: %v", err)
	}
}

func TestRunPipelineHunkManualCommandFailureRemainsOperationalError(t *testing.T) {
	workdir := t.TempDir()
	initReviewTestGitRepo(t, workdir)

	installReviewTestHunkCommandScript(t, `#!/bin/sh
if [ "$1" = "session" ] && [ "$2" = "comment" ] && [ "$3" = "list" ]; then
  printf '{"comments":[]}\n'
  exit 0
fi
printf 'unexpected fake hunk call: %s\n' "$*" >&2
exit 65
`)
	manual := writeReviewTestScript(t, workdir, "failing-hunk-manual", `#!/bin/sh
exit 42
`)
	store, attempt := startReviewTestAttempt(t)
	pipeline := singleStepPipeline(review.KindManual, "inspect", manual)
	pipeline.Steps[0].HunkNotes = true
	prompted := false

	_, err := review.RunPipeline(review.PipelineRunOptions{
		Context:  context.Background(),
		Store:    store,
		RepoID:   "alpha",
		TaskID:   "op-1",
		Branch:   "main",
		Workdir:  workdir,
		Attempt:  attempt,
		Pipeline: pipeline,
		Stdout:   io.Discard,
		Stderr:   io.Discard,
		RenderManualStep: func(step review.Step) error {
			return nil
		},
		ConfirmManualCommand: func(step review.Step) (bool, error) {
			return true, nil
		},
		PromptManualStep: func(step review.ManualStep) (review.ManualResult, error) {
			prompted = true
			return review.ManualResult{Status: taskstate.ReviewStatusPassed}, nil
		},
	})

	if err == nil || !strings.Contains(err.Error(), "run manual step \"inspect\"") {
		t.Fatalf("RunPipeline error = %v, want manual command failure", err)
	}
	if prompted {
		t.Fatal("PromptManualStep was called, want operational command failure before prompt")
	}
}

func TestRunPipelineInteractiveAgentReviewOutputDependsOnProfileMode(t *testing.T) {
	tests := []struct {
		name           string
		interactive    bool
		wantStdout     string
		wantVisible    string
		notWantVisible string
	}{
		{
			name:        "interactive reviewer streams attached output",
			interactive: true,
			wantStdout:  "agent stdout\n",
			wantVisible: "agent stderr",
		},
		{
			name:           "non-interactive reviewer clears passing rolling output",
			interactive:    false,
			notWantVisible: "agent stderr",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := runAgentReviewPipelineTest(t, test.interactive, fakeReviewLauncher{})
			outcome := result.outcome
			err := result.err
			if err != nil {
				t.Fatalf("RunPipeline error = %v", err)
			}
			if outcome.Status != taskstate.ReviewStatusPassed {
				t.Fatalf("outcome = %q, want passed", outcome.Status)
			}
			if result.stdout != test.wantStdout {
				t.Fatalf("stdout = %q, want %q", result.stdout, test.wantStdout)
			}
			if test.wantVisible != "" && !strings.Contains(result.visible, test.wantVisible) {
				t.Fatalf("visible terminal = %q, want %q", result.visible, test.wantVisible)
			}
			if test.notWantVisible != "" && strings.Contains(result.visible, test.notWantVisible) {
				t.Fatalf("visible terminal = %q, do not want %q", result.visible, test.notWantVisible)
			}
		})
	}
}

func TestRunPipelineInteractiveAgentReviewNonBlockingFindingLeavesLiveTail(t *testing.T) {
	harness := newAgentReviewPipelineHarness(t)
	result := harness.run(t, false, fakeReviewLauncherFunc(func(
		ctx context.Context,
		command agentexec.Command,
		opts agentexec.LaunchOptions,
	) error {
		return writeAgentReviewAdvisoryOutput(ctx, opts, harness.store, harness.attempt)
	}))

	if result.err != nil {
		t.Fatalf("RunPipeline error = %v", result.err)
	}
	if result.outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome = %q, want passed", result.outcome.Status)
	}
	if result.stdout != "" {
		t.Fatalf("stdout = %q, want rolling output captured away from stdout", result.stdout)
	}
	for _, want := range []string{"agent stdout 05", "agent stdout 12"} {
		if !strings.Contains(result.visible, want) {
			t.Fatalf("visible terminal = %q, want %q", result.visible, want)
		}
	}
	if strings.Contains(result.visible, "agent stdout 04") {
		t.Fatalf("visible terminal = %q, want final live tail bounded to latest 8 lines", result.visible)
	}
}

func TestRunPipelineInteractivePassingAgentReviewClearsWrappedRollingTail(t *testing.T) {
	harness := newAgentReviewPipelineHarness(t)
	var stdout bytes.Buffer
	terminal := newVisualTerminalWithWidth(20)

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context:           context.Background(),
		Store:             harness.store,
		RepoID:            "alpha",
		TaskID:            "op-1",
		Branch:            "main",
		Workdir:           harness.workdir,
		Attempt:           harness.attempt,
		Pipeline:          agentReviewPipeline(),
		Stdout:            &stdout,
		Stderr:            terminal,
		InteractiveOutput: true,
		OutputWidth:       20,
		AgentConfig:       reviewAgentConfig(false),
		AgentLauncher: fakeReviewLauncherFunc(func(
			ctx context.Context,
			command agentexec.Command,
			opts agentexec.LaunchOptions,
		) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := fmt.Fprintln(opts.Stdout, "wrapped "+strings.Repeat("x", 80))
			return err
		}),
	})

	if err != nil {
		t.Fatalf("RunPipeline error = %v", err)
	}
	if outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome = %q, want passed", outcome.Status)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want rolling output captured away from stdout", stdout.String())
	}
	if visible := terminal.Visible(); strings.Contains(visible, "wrapped") || strings.Contains(visible, "xxxx") {
		t.Fatalf("visible terminal = %q, want passing wrapped output cleared", visible)
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

func startReviewTestAttempt(t *testing.T) (taskstate.Store, taskstate.ReviewAttempt) {
	t.Helper()

	store := taskstate.NewStore(newTestPaths(t))
	attempt, err := store.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "unit",
	})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	return store, attempt
}

func singleStepPipeline(kind string, name string, command string) review.Pipeline {
	return review.Pipeline{
		Name: "standard",
		Steps: []review.Step{{
			Kind:    kind,
			Name:    name,
			Command: command,
		}},
	}
}

type agentReviewPipelineHarness struct {
	store   taskstate.Store
	attempt taskstate.ReviewAttempt
	workdir string
}

type agentReviewPipelineResult struct {
	outcome review.PipelineOutcome
	stdout  string
	visible string
	err     error
}

func newAgentReviewPipelineHarness(t *testing.T) agentReviewPipelineHarness {
	t.Helper()

	workdir := t.TempDir()
	initReviewTestGitRepo(t, workdir)
	store, attempt := startReviewTestAttempt(t)
	return agentReviewPipelineHarness{store: store, attempt: attempt, workdir: workdir}
}

func runAgentReviewPipelineTest(
	t *testing.T,
	interactive bool,
	launcher agentexec.Launcher,
) agentReviewPipelineResult {
	t.Helper()

	harness := newAgentReviewPipelineHarness(t)
	return harness.run(t, interactive, launcher)
}

func (h agentReviewPipelineHarness) run(
	t *testing.T,
	interactive bool,
	launcher agentexec.Launcher,
) agentReviewPipelineResult {
	t.Helper()

	var stdout bytes.Buffer
	terminal := newVisualTerminal()
	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context:           context.Background(),
		Store:             h.store,
		RepoID:            "alpha",
		TaskID:            "op-1",
		Branch:            "main",
		Workdir:           h.workdir,
		Attempt:           h.attempt,
		Pipeline:          agentReviewPipeline(),
		Stdout:            &stdout,
		Stderr:            terminal,
		InteractiveOutput: true,
		AgentConfig:       reviewAgentConfig(interactive),
		AgentLauncher:     launcher,
	})
	return agentReviewPipelineResult{
		outcome: outcome,
		stdout:  stdout.String(),
		visible: terminal.Visible(),
		err:     err,
	}
}

func agentReviewPipeline() review.Pipeline {
	return review.Pipeline{
		Name: "standard",
		Steps: []review.Step{{
			Kind: review.KindAgentReview,
			Name: "ai-review",
		}},
	}
}

func reviewAgentConfig(interactive bool) agent.Config {
	return agent.Config{
		Defaults: agent.AgentDefaults{Implementer: "impl", Reviewer: "reviewer"},
		Agents: map[string]agent.Profile{
			"impl":     {Command: "impl"},
			"reviewer": {Command: "review-agent", Interactive: interactive},
		},
	}
}

func writeAgentReviewAdvisoryOutput(
	ctx context.Context,
	opts agentexec.LaunchOptions,
	store taskstate.Store,
	attempt taskstate.ReviewAttempt,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for i := 1; i <= 12; i++ {
		if _, err := fmt.Fprintf(opts.Stdout, "agent stdout %02d\n", i); err != nil {
			return err
		}
	}
	_, err := store.RecordReviewFinding("alpha", "op-1", attempt.Attempt, taskstate.ReviewFinding{
		Type:        taskstate.FindingTypeAdvisory,
		Title:       "Advisory finding",
		Description: "The reviewer left an advisory note.",
		Step:        "ai-review",
	})
	return err
}

func writeReviewTestScript(t *testing.T, dir string, name string, content string) string {
	t.Helper()

	path := filepath.Join(dir, name+".sh")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

func installReviewTestHunkCommand(t *testing.T, initialCapturePath string, commandCompletePath string) {
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
		shellQuoteReviewTest(commandCompletePath),
		shellQuoteReviewTest(noteResponse),
		shellQuoteReviewTest(initialCapturePath),
	)
	installReviewTestHunkCommandScript(t, script)
}

func installReviewTestHunkCommandScript(t *testing.T, script string) {
	t.Helper()

	binDir := t.TempDir()
	hunkPath := filepath.Join(binDir, "hunk")
	if err := os.WriteFile(hunkPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake hunk command: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shellQuoteReviewTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

type fakeReviewLauncher struct{}

func (fakeReviewLauncher) Run(ctx context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(opts.Stdout, "agent stdout"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(opts.Stderr, "agent stderr"); err != nil {
		return err
	}
	return nil
}

type fakeReviewLauncherFunc func(context.Context, agentexec.Command, agentexec.LaunchOptions) error

func (f fakeReviewLauncherFunc) Run(ctx context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
	return f(ctx, command, opts)
}

type visualTerminal struct {
	raw   bytes.Buffer
	lines []string
	row   int
	width int
}

func newVisualTerminal() *visualTerminal {
	return &visualTerminal{lines: []string{""}}
}

func newVisualTerminalWithWidth(width int) *visualTerminal {
	return &visualTerminal{lines: []string{""}, width: width}
}

func (t *visualTerminal) Write(p []byte) (int, error) {
	t.raw.Write(p)
	for index := 0; index < len(p); {
		if bytes.HasPrefix(p[index:], []byte("\x1b[1A\r\x1b[2K")) {
			if t.row > 0 {
				t.row--
			}
			t.lines[t.row] = ""
			index += len("\x1b[1A\r\x1b[2K")
			continue
		}
		if bytes.HasPrefix(p[index:], []byte("\x1b[2K")) {
			t.lines[t.row] = ""
			index += len("\x1b[2K")
			continue
		}
		switch p[index] {
		case '\r':
			t.lines[t.row] = ""
		case '\n':
			t.row++
			t.ensureRow()
		default:
			if t.width > 0 && len([]rune(t.lines[t.row])) >= t.width {
				t.row++
				t.ensureRow()
			}
			t.lines[t.row] += string(p[index])
		}
		index++
	}
	return len(p), nil
}

func (t *visualTerminal) Visible() string {
	last := len(t.lines)
	for last > 0 && t.lines[last-1] == "" {
		last--
	}
	return strings.Join(t.lines[:last], "\n")
}

func (t *visualTerminal) ensureRow() {
	for t.row >= len(t.lines) {
		t.lines = append(t.lines, "")
	}
}
