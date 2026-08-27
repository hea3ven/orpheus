package doctor_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/doctor"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/workflow"
)

type doctorClosedTaskBackend struct {
	task task.Task
}

func (b doctorClosedTaskBackend) Get(context.Context, string) (task.Task, error) {
	return b.task.Clone(), nil
}

func (doctorClosedTaskBackend) List(context.Context) ([]task.Task, error) {
	return nil, nil
}

type doctorCleanupGit struct {
	inspection gitmeta.ClosedTaskWorktreeInspection
	removal    gitmeta.ClosedTaskWorktreeRemoval
	inspects   int
	removes    int
	worktree   string
}

func (g *doctorCleanupGit) InspectClosedTaskWorktree(context.Context, gitmeta.ClosedTaskWorktreeOptions) gitmeta.ClosedTaskWorktreeInspection {
	g.inspects++
	inspection := g.inspection
	if inspection.Outcome == "" {
		inspection.Outcome = gitmeta.ClosedTaskWorktreeClean
	}
	if inspection.Worktree == "" {
		inspection.Worktree = g.worktree
	}
	return inspection
}

func (g *doctorCleanupGit) RemoveClosedTaskWorktree(context.Context, gitmeta.ClosedTaskWorktreeOptions) gitmeta.ClosedTaskWorktreeRemoval {
	g.removes++
	removal := g.removal
	if removal.Outcome == "" {
		removal.Outcome = gitmeta.ClosedTaskWorktreeRemoved
	}
	if removal.Worktree == "" {
		removal.Worktree = g.worktree
	}
	if removal.Outcome == gitmeta.ClosedTaskWorktreeRemoved {
		g.inspection = gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeAbsent, Worktree: removal.Worktree}
	}
	return removal
}

func TestRunReportsAndRepairsRecoverablePrimaryReviewer(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	review, err := store.StartReviewWithOptions("alpha", "op-review", taskstate.StartReviewOptions{Pipeline: "ai", Step: "ai-review"})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	execution := taskstate.AgentExecution{
		Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning,
		Agent: "reviewer", Harness: "codex", Model: "gpt-5", StartedAt: review.StartedAt, SupervisorPID: 10,
		Session: &taskstate.AgentSession{ID: "session"}, Usage: &taskstate.AgentUsage{InputTokens: 100},
		UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured},
	}
	if _, err := store.RecordReviewStep("alpha", "op-review", review.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution}); err != nil {
		t.Fatalf("record review step: %v", err)
	}
	if _, err := store.RecordReviewStepChildPID("alpha", "op-review", review.Attempt, "ai-review", 11); err != nil {
		t.Fatalf("record child PID: %v", err)
	}
	opts := doctor.Options{
		Paths: paths, Registry: registry.Registry{Repos: []registry.Repo{{ID: "alpha", Path: testutil.CanonicalTempDir(t)}}},
		Probe: workflow.ProcessProbe(func(int) (agentexec.ProcessLiveness, error) { return agentexec.ProcessAbsent, nil }),
	}
	result, err := doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if len(result.PrimaryReviewRows) != 1 || result.PrimaryReviewRows[0].Outcome != string(workflow.AttachedExecutionRecoverable) {
		t.Fatalf("primary review rows = %#v, want recoverable row", result.PrimaryReviewRows)
	}
	opts.Fix = true
	result, err = doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor --fix: %v", err)
	}
	if len(result.PrimaryReviewRows) != 1 || result.PrimaryReviewRows[0].Outcome != "interrupted" {
		t.Fatalf("fixed primary review rows = %#v, want interrupted row", result.PrimaryReviewRows)
	}
	if len(result.Rows) != 1 || result.Rows[0].Outcome != doctor.OutcomeRecovered {
		t.Fatalf("fixed telemetry rows = %#v, want recovered cost row", result.Rows)
	}
	loaded, err := store.Load("alpha", "op-review")
	if err != nil {
		t.Fatalf("load after fix: %v", err)
	}
	latest, _ := taskstate.LatestReview(loaded)
	if latest.Status != taskstate.ReviewStatusFailed || !taskstate.PrimaryReviewExecutionInterrupted(latest) {
		t.Fatalf("fixed review = %#v, want failed interrupted primary review", latest)
	}
	if latest.Steps[0].Execution.UsageCost == nil {
		t.Fatalf("fixed execution = %#v, want recovered usage cost without restoring running state", latest.Steps[0].Execution)
	}
}

//nolint:funlen // The full dry-run, repair, and idempotency scenario is clearer as one flow.
func TestRunReportsAndFixesClosedTaskWorktree(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	const taskID = "op-cleanup"
	worktree, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", taskID))
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("create worktree path: %v", err)
	}
	store := taskstate.NewStore(paths)
	if _, err := store.StartRun("alpha", taskID, taskstate.StartRunOptions{
		Agent: "implementer", WorkDirectory: worktree, Branch: "orpheus/" + taskID, Worktree: worktree,
	}); err != nil {
		t.Fatalf("start run: %v", err)
	}
	source := task.RepositorySource{Repository: task.Repository{ID: "alpha", Name: "Alpha", Path: "/fixture/alpha", DefaultBranch: "main"}}
	backend := doctorClosedTaskBackend{task: task.Task{ID: taskID, Status: task.StatusClosed, Metadata: task.Metadata{
		task.MetadataBranch: "orpheus/" + taskID, task.MetadataWorktree: worktree,
	}}}
	git := &doctorCleanupGit{worktree: worktree}
	opts := doctor.Options{
		Paths:    paths,
		Registry: registry.Registry{Repos: []registry.Repo{{ID: "alpha", Path: "/fixture/alpha"}}},
		Sources:  []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.ReadBackend, error) {
			return backend, nil
		},
		CleanupGit: git,
	}

	result, err := doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if len(result.WorktreeRows) != 1 || result.WorktreeRows[0].Outcome != workflow.WorktreeCleanupWouldRemove || git.removes != 0 {
		t.Fatalf("doctor rows/removals = %#v/%d, want removable dry run", result.WorktreeRows, git.removes)
	}
	loaded, err := store.Load("alpha", taskID)
	if err != nil {
		t.Fatalf("load dry run state: %v", err)
	}
	if len(loaded.Events) != 1 {
		t.Fatalf("dry run events = %#v, want only start event", loaded.Events)
	}

	opts.Fix = true
	result, err = doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor --fix: %v", err)
	}
	if len(result.WorktreeRows) != 1 || result.WorktreeRows[0].Outcome != workflow.WorktreeCleanupRemoved || git.removes != 1 {
		t.Fatalf("fixed rows/removals = %#v/%d, want removed", result.WorktreeRows, git.removes)
	}
	loaded, err = store.Load("alpha", taskID)
	if err != nil {
		t.Fatalf("load fixed state: %v", err)
	}
	if got := loaded.Events[len(loaded.Events)-1]; got.Type != taskstate.EventWorktreeRemoved || got.Worktree != worktree {
		t.Fatalf("cleanup event = %#v, want removed worktree audit", got)
	}

	result, err = doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor after cleanup: %v", err)
	}
	if len(result.WorktreeRows) != 0 || git.removes != 1 {
		t.Fatalf("doctor rows/removals after cleanup = %#v/%d, want no lingering worktree", result.WorktreeRows, git.removes)
	}
	if err := os.RemoveAll(worktree); err != nil {
		t.Fatalf("remove cleaned worktree: %v", err)
	}

	opts.BackendFactory = func(task.RepositorySource) (task.ReadBackend, error) {
		return nil, errors.New("task source unavailable")
	}
	result, err = doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor with unavailable source after cleanup: %v", err)
	}
	if len(result.WorktreeRows) != 0 || git.removes != 1 {
		t.Fatalf("doctor rows/removals with unavailable source = %#v/%d, want absent worktree skipped", result.WorktreeRows, git.removes)
	}
}

func TestRunSkipsRepositoryRootTargetWhenBackendUnavailable(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	const repoPath = "/fixture/alpha"
	store := taskstate.NewStore(paths)
	if _, err := store.StartRun("alpha", "op-root", taskstate.StartRunOptions{
		Agent: "implementer", WorkDirectory: repoPath, Branch: "main", Worktree: repoPath,
	}); err != nil {
		t.Fatalf("start repo-root run: %v", err)
	}
	source := task.RepositorySource{Repository: task.Repository{ID: "alpha", Name: "Alpha", Path: repoPath, DefaultBranch: "main"}}
	opts := doctor.Options{
		Paths:    paths,
		Registry: registry.Registry{Repos: []registry.Repo{{ID: "alpha", Path: repoPath}}},
		Sources:  []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.ReadBackend, error) {
			return nil, errors.New("task source unavailable")
		},
	}

	for _, fix := range []bool{false, true} {
		opts.Fix = fix
		result, err := doctor.Run(opts)
		if err != nil {
			t.Fatalf("doctor fix=%v: %v", fix, err)
		}
		if len(result.WorktreeRows) != 0 {
			t.Fatalf("doctor fix=%v worktree rows = %#v, want none", fix, result.WorktreeRows)
		}
	}
}

func TestRunReportsAbsentRegisteredClosedTaskWorktree(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	const taskID = "op-partial-cleanup"
	worktree, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", taskID))
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}
	store := taskstate.NewStore(paths)
	if _, err := store.StartRun("alpha", taskID, taskstate.StartRunOptions{
		Agent: "implementer", WorkDirectory: worktree, Branch: "orpheus/" + taskID, Worktree: worktree,
	}); err != nil {
		t.Fatalf("start run: %v", err)
	}
	source := task.RepositorySource{Repository: task.Repository{ID: "alpha", Name: "Alpha", Path: "/fixture/alpha", DefaultBranch: "main"}}
	backend := doctorClosedTaskBackend{task: task.Task{ID: taskID, Status: task.StatusClosed, Metadata: task.Metadata{
		task.MetadataBranch: "orpheus/" + taskID, task.MetadataWorktree: worktree,
	}}}
	git := &doctorCleanupGit{
		worktree: worktree,
		inspection: gitmeta.ClosedTaskWorktreeInspection{
			Outcome: gitmeta.ClosedTaskWorktreeFailed,
			Reason:  "Git still registers the absent deterministic worktree after incomplete removal",
		},
	}
	opts := doctor.Options{
		Paths:    paths,
		Registry: registry.Registry{Repos: []registry.Repo{{ID: "alpha", Path: "/fixture/alpha"}}},
		Sources:  []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.ReadBackend, error) {
			return backend, nil
		},
		CleanupGit: git,
	}

	result, err := doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if len(result.WorktreeRows) != 1 || result.WorktreeRows[0].Outcome != workflow.WorktreeCleanupFailed || result.WorktreeRows[0].Worktree != worktree || git.inspects != 2 || git.removes != 0 {
		t.Fatalf("doctor rows/inspections/removals = %#v/%d/%d, want failed unresolved registration", result.WorktreeRows, git.inspects, git.removes)
	}
}

//nolint:funlen // The dry-run and repair checks keep lock handling together.
func TestRunReportsLockedClosedTaskWorktreeWithoutRemovingIt(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	const taskID = "op-locked-cleanup"
	worktree, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", taskID))
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("create locked worktree path: %v", err)
	}
	store := taskstate.NewStore(paths)
	if _, err := store.StartRun("alpha", taskID, taskstate.StartRunOptions{
		Agent: "implementer", WorkDirectory: worktree, Branch: "orpheus/" + taskID, Worktree: worktree,
	}); err != nil {
		t.Fatalf("start run: %v", err)
	}
	source := task.RepositorySource{Repository: task.Repository{ID: "alpha", Name: "Alpha", Path: "/fixture/alpha", DefaultBranch: "main"}}
	backend := doctorClosedTaskBackend{task: task.Task{ID: taskID, Status: task.StatusClosed, Metadata: task.Metadata{
		task.MetadataBranch: "orpheus/" + taskID, task.MetadataWorktree: worktree,
	}}}
	git := &doctorCleanupGit{
		worktree: worktree,
		inspection: gitmeta.ClosedTaskWorktreeInspection{
			Outcome: gitmeta.ClosedTaskWorktreeUnsafe,
			Reason:  "Git marks the deterministic worktree as locked: operator repair",
		},
	}
	opts := doctor.Options{
		Paths:    paths,
		Registry: registry.Registry{Repos: []registry.Repo{{ID: "alpha", Path: "/fixture/alpha"}}},
		Sources:  []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.ReadBackend, error) {
			return backend, nil
		},
		CleanupGit: git,
	}

	result, err := doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if len(result.WorktreeRows) != 1 || result.WorktreeRows[0].Outcome != workflow.WorktreeCleanupUnsafe || git.inspects != 1 || git.removes != 0 {
		t.Fatalf("doctor rows/inspections/removals = %#v/%d/%d, want unsafe without removal", result.WorktreeRows, git.inspects, git.removes)
	}

	opts.Fix = true
	result, err = doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor --fix: %v", err)
	}
	if len(result.WorktreeRows) != 1 || result.WorktreeRows[0].Outcome != workflow.WorktreeCleanupUnsafe || git.inspects != 2 || git.removes != 0 {
		t.Fatalf("fixed rows/inspections/removals = %#v/%d/%d, want unsafe without removal", result.WorktreeRows, git.inspects, git.removes)
	}
	loaded, err := store.Load("alpha", taskID)
	if err != nil {
		t.Fatalf("load after fix: %v", err)
	}
	if len(loaded.Events) != 1 {
		t.Fatalf("events = %#v, want only start event", loaded.Events)
	}
}

func TestRunReportsAndRepairsRecoverableImplementationRun(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-recover", taskstate.StartRunOptions{Agent: "implementer", SupervisorPID: 10})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.RecordRunChildPID("alpha", "op-recover", run.Attempt, 11); err != nil {
		t.Fatalf("record child PID: %v", err)
	}
	opts := doctor.Options{
		Paths:    paths,
		Registry: registry.Registry{Repos: []registry.Repo{{ID: "alpha", Path: testutil.CanonicalTempDir(t)}}},
		Probe: workflow.ProcessProbe(func(int) (agentexec.ProcessLiveness, error) {
			return agentexec.ProcessAbsent, nil
		}),
	}

	result, err := doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if len(result.ImplementationRows) != 1 || result.ImplementationRows[0].Outcome != string(workflow.ImplementationRunRecoverable) {
		t.Fatalf("implementation rows = %#v, want recoverable row", result.ImplementationRows)
	}
	loaded, err := store.Load("alpha", "op-recover")
	if err != nil {
		t.Fatalf("load after report: %v", err)
	}
	if loaded.Runs[0].Status != taskstate.RunStatusRunning {
		t.Fatalf("reported run status = %q, want running", loaded.Runs[0].Status)
	}

	opts.Fix = true
	result, err = doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor --fix: %v", err)
	}
	if len(result.ImplementationRows) != 1 || result.ImplementationRows[0].Outcome != "interrupted" {
		t.Fatalf("fixed implementation rows = %#v, want interrupted row", result.ImplementationRows)
	}
	loaded, err = store.Load("alpha", "op-recover")
	if err != nil {
		t.Fatalf("load after fix: %v", err)
	}
	if loaded.Runs[0].Status != taskstate.RunStatusInterrupted {
		t.Fatalf("fixed run status = %q, want interrupted", loaded.Runs[0].Status)
	}
}
