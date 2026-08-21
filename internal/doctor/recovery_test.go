package doctor_test

import (
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/doctor"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/workflow"
)

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
