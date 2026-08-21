package workflow_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/workflow"
)

func TestInspectImplementationRun(t *testing.T) {
	tests := []struct {
		name      string
		run       taskstate.RunAttempt
		probe     workflow.ProcessProbe
		condition workflow.ImplementationRunCondition
		reason    string
	}{
		{name: "legacy run is unverifiable", run: runningRecoveryRun(0, 0), condition: workflow.ImplementationRunUnverifiable, reason: "missing_supervisor_pid_legacy_run"},
		{name: "live supervisor blocks recovery", run: runningRecoveryRun(10, 0), probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessLive}), condition: workflow.ImplementationRunLive, reason: "supervisor_pid_live"},
		{name: "missing child is assumed not started", run: runningRecoveryRun(10, 0), probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent}), condition: workflow.ImplementationRunRecoverable, reason: "supervisor_absent_child_pid_not_recorded_assume_not_started"},
		{name: "live child blocks recovery", run: runningRecoveryRun(10, 11), probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent, 11: agentexec.ProcessLive}), condition: workflow.ImplementationRunLive, reason: "child_pid_live"},
		{name: "missing supervisor and child recover", run: runningRecoveryRun(10, 11), probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent, 11: agentexec.ProcessAbsent}), condition: workflow.ImplementationRunRecoverable, reason: "supervisor_and_child_pids_absent"},
		{name: "probe error is unverifiable", run: runningRecoveryRun(10, 11), probe: func(int) (agentexec.ProcessLiveness, error) {
			return agentexec.ProcessUnknown, errors.New("permission denied")
		}, condition: workflow.ImplementationRunUnverifiable, reason: "supervisor_pid_probe_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := workflow.InspectImplementationRun(tt.run, tt.probe)
			if got.Condition != tt.condition || got.Reason != tt.reason {
				t.Fatalf("inspection = %#v, want condition=%q reason=%q", got, tt.condition, tt.reason)
			}
		})
	}
}

func TestRecordPrimaryReviewChildPIDPreservesConcurrentFinding(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	review, err := store.StartReviewWithOptions("alpha", "op-review", taskstate.StartReviewOptions{Pipeline: "ai", Step: "ai-review"})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "reviewer", StartedAt: review.StartedAt, SupervisorPID: 10}
	if _, err := store.RecordReviewStep("alpha", "op-review", review.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution}); err != nil {
		t.Fatalf("record review step: %v", err)
	}
	service := workflow.ReviewLifecycleService{Paths: paths, RunStore: store}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		errs <- service.RecordPrimaryReviewChildPID("alpha", "op-review", review.Attempt, "ai-review", 4242)
	}()
	go func() {
		defer wait.Done()
		<-start
		errs <- retryTestMutationLock(paths, "test review finding", func() error {
			_, err := store.RecordReviewFinding("alpha", "op-review", review.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Step: "ai-review", Title: "immediate", Description: "finding"})
			return err
		})
	}()
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent mutation: %v", err)
		}
	}
	loaded, err := store.Load("alpha", "op-review")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	latest, _ := taskstate.LatestReview(loaded)
	if latest.Steps[0].Execution.ChildPID != 4242 || len(latest.Findings) != 1 {
		t.Fatalf("review state = %#v, want persisted child PID and finding", latest)
	}
}

func TestPrepareTaskRunPreservesLivePrimaryReviewExecution(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	review, err := store.StartReviewWithOptions("alpha", "op-review", taskstate.StartReviewOptions{Pipeline: "ai", Step: "ai-review"})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "reviewer", StartedAt: review.StartedAt, SupervisorPID: 10}
	if _, err := store.RecordReviewStep("alpha", "op-review", review.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution}); err != nil {
		t.Fatalf("record review step: %v", err)
	}

	prepared, err := workflow.PrepareTaskRun(context.Background(), workflow.PrepareTaskRunOptions{
		Paths: paths, Store: store, RepoID: "alpha", TaskID: "op-review", Task: task.Task{ID: "op-review"}, Trigger: "task_run",
		Probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessLive}),
	})
	if err != nil {
		t.Fatalf("prepare task run: %v", err)
	}
	if prepared.ReviewInspection.Condition != workflow.AttachedExecutionLive || prepared.ReviewInspection.Reason != "supervisor_pid_live" {
		t.Fatalf("review inspection = %#v, want live primary reviewer", prepared.ReviewInspection)
	}
	latest, ok := taskstate.LatestReview(prepared.State)
	if !ok || latest.Status != taskstate.ReviewStatusRunning || latest.Steps[0].Execution.Status != taskstate.RunStatusRunning {
		t.Fatalf("review state = %#v, want unchanged running primary reviewer", latest)
	}
}

func TestReconcilePrimaryReviewExecutionRecordsFailedReview(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	review, err := store.StartReviewWithOptions("alpha", "op-review", taskstate.StartReviewOptions{Pipeline: "ai", Step: "ai-review"})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "reviewer", StartedAt: review.StartedAt, SupervisorPID: 10}
	if _, err := store.RecordReviewStep("alpha", "op-review", review.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution}); err != nil {
		t.Fatalf("record review step: %v", err)
	}
	if _, err := store.RecordReviewStepChildPID("alpha", "op-review", review.Attempt, "ai-review", 11); err != nil {
		t.Fatalf("record child PID: %v", err)
	}

	inspection, err := workflow.ReconcilePrimaryReviewExecution(context.Background(), paths, store, "alpha", "op-review", review.Attempt, "task_run", recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent, 11: agentexec.ProcessAbsent}))
	if err != nil {
		t.Fatalf("reconcile primary review: %v", err)
	}
	if inspection.Condition != workflow.AttachedExecutionRecoverable || inspection.Reason != "supervisor_and_child_pids_absent" {
		t.Fatalf("inspection = %#v, want recoverable absent process inspection", inspection)
	}
	loaded, err := store.Load("alpha", "op-review")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	latest, ok := taskstate.LatestReview(loaded)
	if !ok || latest.Status != taskstate.ReviewStatusFailed || !taskstate.PrimaryReviewExecutionInterrupted(latest) {
		t.Fatalf("latest review = %#v, want failed interrupted primary review", latest)
	}
}

func TestReconcilePrimaryReviewExecutionRecognizesConcurrentRecovery(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	review, err := store.StartReviewWithOptions("alpha", "op-review", taskstate.StartReviewOptions{Pipeline: "ai", Step: "ai-review"})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "reviewer", StartedAt: review.StartedAt, SupervisorPID: 10}
	if _, err := store.RecordReviewStep("alpha", "op-review", review.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution}); err != nil {
		t.Fatalf("record review step: %v", err)
	}
	if _, err := store.InterruptPrimaryReviewExecution("alpha", "op-review", review.Attempt, "ai-review", taskstate.InterruptPrimaryReviewExecutionOptions{Reason: "supervisor_absent_child_pid_not_recorded_assume_not_started", Trigger: "task_run"}); err != nil {
		t.Fatalf("interrupt review: %v", err)
	}
	inspection, err := workflow.ReconcilePrimaryReviewExecution(context.Background(), paths, store, "alpha", "op-review", review.Attempt, "task_review", recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent}))
	if err != nil {
		t.Fatalf("reconcile concurrently recovered review: %v", err)
	}
	if inspection.Condition != workflow.AttachedExecutionAlreadyRecovered || inspection.Reason != "primary_review_interrupted_by_concurrent_recovery" {
		t.Fatalf("inspection = %#v, want concurrent recovery marker", inspection)
	}
}

func TestPrepareTaskRunMarksConcurrentlyRecoveredPrimaryForInspectionStop(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	review, err := store.StartReviewWithOptions("alpha", "op-review", taskstate.StartReviewOptions{Pipeline: "ai", Step: "ai-review"})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "reviewer", StartedAt: review.StartedAt, SupervisorPID: 10}
	if _, err := store.RecordReviewStep("alpha", "op-review", review.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution}); err != nil {
		t.Fatalf("record review step: %v", err)
	}
	stale, err := store.Load("alpha", "op-review")
	if err != nil {
		t.Fatalf("load stale state: %v", err)
	}
	if _, err := store.InterruptPrimaryReviewExecution("alpha", "op-review", review.Attempt, "ai-review", taskstate.InterruptPrimaryReviewExecutionOptions{Reason: "supervisor_absent_child_pid_not_recorded_assume_not_started", Trigger: "task_run"}); err != nil {
		t.Fatalf("interrupt review: %v", err)
	}
	prepared, err := workflow.PrepareTaskRun(context.Background(), workflow.PrepareTaskRunOptions{
		Paths: paths, Store: &staleThenRecoveredStore{Store: store, stale: stale}, RepoID: "alpha", TaskID: "op-review", Task: task.Task{ID: "op-review"}, Trigger: "task_run",
		Probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent}),
	})
	if err != nil {
		t.Fatalf("prepare task run: %v", err)
	}
	if prepared.ReviewInspection.Condition != workflow.AttachedExecutionAlreadyRecovered {
		t.Fatalf("review inspection = %#v, want concurrent recovery marker", prepared.ReviewInspection)
	}
}

func TestPrepareTaskRunReconcilesAndRoutesRecoveredAttempt(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "implementer", SupervisorPID: 10})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	prepared, err := workflow.PrepareTaskRun(context.Background(), workflow.PrepareTaskRunOptions{
		Paths: paths, Store: store, RepoID: "alpha", TaskID: "op-1", Task: task.Task{ID: "op-1"}, Trigger: "task_run",
		Probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent}),
	})
	if err != nil {
		t.Fatalf("prepare task run: %v", err)
	}
	if prepared.Inspection.Condition != workflow.ImplementationRunRecoverable {
		t.Fatalf("inspection = %#v, want recoverable", prepared.Inspection)
	}
	if prepared.State.Runs[0].Attempt != run.Attempt || prepared.State.Runs[0].Status != taskstate.RunStatusInterrupted {
		t.Fatalf("prepared state = %#v, want interrupted attempt", prepared.State.Runs)
	}
	if prepared.Route.Action != workflow.TaskRunActionStartImplementation {
		t.Fatalf("route = %#v, want replacement implementation", prepared.Route)
	}
}

func TestReconcileImplementationRunRequiresExpectedActiveAttempt(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := taskstate.NewStore(paths)
	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "implementer", SupervisorPID: 10})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	inspection, err := workflow.ReconcileImplementationRun(
		context.Background(), paths, store, "alpha", "op-1", run.Attempt+1, "doctor_fix", recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent}),
	)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if inspection.Condition != workflow.ImplementationRunNotRunning {
		t.Fatalf("inspection = %#v, want not running due to attempt mismatch", inspection)
	}
	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Runs[0].Status != taskstate.RunStatusRunning {
		t.Fatalf("run status = %q, want unchanged running", loaded.Runs[0].Status)
	}
}

func runningRecoveryRun(supervisorPID, childPID int) taskstate.RunAttempt {
	return taskstate.RunAttempt{Attempt: 1, Status: taskstate.RunStatusRunning, Execution: taskstate.AgentExecution{
		SupervisorPID: supervisorPID,
		ChildPID:      childPID,
	}}
}

func recoveryProbe(values map[int]agentexec.ProcessLiveness) workflow.ProcessProbe {
	return func(pid int) (agentexec.ProcessLiveness, error) {
		return values[pid], nil
	}
}

func retryTestMutationLock(paths state.Paths, operation string, mutate func() error) error {
	var err error
	for retry := 0; retry < 100; retry++ {
		err = state.WithGlobalMutationLock(paths, operation, mutate)
		if err == nil || !errors.Is(err, os.ErrExist) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	return err
}

type staleThenRecoveredStore struct {
	taskstate.Store
	stale taskstate.TaskState
	loads int
}

func (s *staleThenRecoveredStore) Load(repoID, taskID string) (taskstate.TaskState, error) {
	if s.loads == 0 {
		s.loads++
		return s.stale, nil
	}
	return s.Store.Load(repoID, taskID)
}
