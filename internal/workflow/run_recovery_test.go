package workflow_test

import (
	"github.com/hea3ven/orpheus/internal/workfixture"
	implementation "github.com/hea3ven/orpheus/internal/workflow/implementation"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	reviewphase "github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"

	"context"
	"errors"
	workmodel "github.com/hea3ven/orpheus/internal/work"
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

func TestRecordReviewChildPIDPreservesConcurrentFinding(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := workfixture.NewStore(paths)
	snapshot := testutil.ReviewableWork("alpha", "op-review", "/fixture/work", "task-branch")
	if _, err := workstate.NewStore(paths).Save(snapshot, 0); err != nil {
		t.Fatal(err)
	}
	review, err := store.SeedReview("alpha", "op-review", reviewphase.Reservation{Pipeline: "ai", Contract: workmodel.ReviewContract{PipelineKey: "ai", Slots: []workmodel.ReviewSlot{{Kind: workmodel.ReviewAgent, ConfigKey: "ai-review"}}}, Scope: workmodel.ReviewScope{Requirements: "Fixture task requirements", Destination: &workmodel.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}, Output: workmodel.OutputRef{Phase: 1}}})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "reviewer", StartedAt: review.StartedAt, SupervisorPID: 10}
	startedReview, err := store.ObserveStep("alpha", "op-review", review.Attempt, reviewphase.StepObservation{Address: review.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution})
	if err != nil {
		t.Fatalf("record review step: %v", err)
	}
	actor := startedReview.Steps[len(startedReview.Steps)-1].Address
	service := workflow.ReviewLifecycleService{Paths: paths, WorkStore: workstate.NewStore(paths)}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		errs <- service.RecordReviewChildPID(actor, 4242)
	}()
	go func() {
		defer wait.Done()
		<-start
		errs <- retryTestMutationLock(paths, "test review finding", func() error {
			_, err := (phaseexec.Runner{Store: workstate.NewStore(paths), RepositoryID: "alpha", TaskID: "op-review"}).CommitHeld(context.Background(), reviewphase.FindingChange(actor, reviewphase.FindingInput{Classification: workmodel.FindingAdvisory, Title: "immediate", Description: "finding"}))
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

func TestPrepareTaskRunPreservesLiveReviewExecution(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := workfixture.NewStore(paths)
	snapshot := testutil.ReviewableWork("alpha", "op-review", "/fixture/work", "task-branch")
	if _, err := workstate.NewStore(paths).Save(snapshot, 0); err != nil {
		t.Fatal(err)
	}
	review, err := store.SeedReview("alpha", "op-review", reviewphase.Reservation{Pipeline: "ai", Contract: workmodel.ReviewContract{PipelineKey: "ai", Slots: []workmodel.ReviewSlot{{Kind: workmodel.ReviewAgent, ConfigKey: "ai-review"}}}, Scope: workmodel.ReviewScope{Requirements: "Fixture task requirements", Destination: &workmodel.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}, Output: workmodel.OutputRef{Phase: 1}}})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "reviewer", StartedAt: review.StartedAt, SupervisorPID: 10}
	if _, err := store.ObserveStep("alpha", "op-review", review.Attempt, reviewphase.StepObservation{Address: review.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution}); err != nil {
		t.Fatalf("record review step: %v", err)
	}

	prepared, err := workflow.PrepareTaskRun(context.Background(), workflow.PrepareTaskRunOptions{
		Runner: phaseexec.Runner{Paths: paths, Store: workstate.NewStore(paths), RepositoryID: "alpha", TaskID: "op-review"}, Task: task.Task{ID: "op-review"}, Trigger: "task_run",
		Probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessLive}),
	})
	if err != nil {
		t.Fatalf("prepare task run: %v", err)
	}
	if prepared.ReviewInspection.Condition != phaseexec.AttachedExecutionLive || prepared.ReviewInspection.Reason != "supervisor_pid_live" {
		t.Fatalf("review inspection = %#v, want live reviewer", prepared.ReviewInspection)
	}
	latest, ok := taskstate.LatestReview(prepared.State)
	if !ok || latest.Status != taskstate.ReviewStatusRunning || latest.Steps[0].Execution.Status != taskstate.RunStatusRunning {
		t.Fatalf("review state = %#v, want unchanged running reviewer", latest)
	}
}

func TestPrepareTaskRunMarksConcurrentlyRecoveredReviewForInspectionStop(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := workfixture.NewStore(paths)
	snapshot := testutil.ReviewableWork("alpha", "op-review", "/fixture/work", "task-branch")
	if _, err := workstate.NewStore(paths).Save(snapshot, 0); err != nil {
		t.Fatal(err)
	}
	review, err := store.SeedReview("alpha", "op-review", reviewphase.Reservation{Pipeline: "ai", Contract: workmodel.ReviewContract{PipelineKey: "ai", Slots: []workmodel.ReviewSlot{{Kind: workmodel.ReviewAgent, ConfigKey: "ai-review"}}}, Scope: workmodel.ReviewScope{Requirements: "Fixture task requirements", Destination: &workmodel.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}, Output: workmodel.OutputRef{Phase: 1}}})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "reviewer", StartedAt: review.StartedAt, SupervisorPID: 10}
	if _, err := store.ObserveStep("alpha", "op-review", review.Attempt, reviewphase.StepObservation{Address: review.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution}); err != nil {
		t.Fatalf("record review step: %v", err)
	}
	stale, err := store.Load("alpha", "op-review")
	if err != nil {
		t.Fatalf("load stale state: %v", err)
	}
	if _, err := (phaseexec.Runner{Store: workstate.NewStore(paths), RepositoryID: "alpha", TaskID: "op-review"}).CommitHeld(context.Background(), reviewphase.InterruptionChange(workmodel.AttemptRef{RepositoryID: "alpha", TaskID: "op-review", Workflow: review.Address.Workflow, Phase: review.Address.Phase, Step: new(int)}, "supervisor_absent_child_pid_not_recorded_assume_not_started", "task_run")); err != nil {
		t.Fatalf("interrupt review: %v", err)
	}
	prepared, err := workflow.PrepareTaskRun(context.Background(), workflow.PrepareTaskRunOptions{
		Runner: phaseexec.Runner{Paths: paths, Store: &staleThenRecoveredStore{Store: workstate.NewStore(paths), stale: *stale.Work}, RepositoryID: "alpha", TaskID: "op-review"}, Task: task.Task{ID: "op-review"}, Trigger: "task_run",
		Probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent}),
	})
	if err != nil {
		t.Fatalf("prepare task run: %v", err)
	}
	if prepared.ReviewInspection.Condition != phaseexec.AttachedExecutionAlreadyRecovered || prepared.ReviewInspection.Reason != "review_interrupted_by_concurrent_recovery" {
		t.Fatalf("review inspection = %#v, want concurrent recovery marker", prepared.ReviewInspection)
	}
}

func TestPrepareTaskRunReconcilesAndRoutesRecoveredAttempt(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	store := workfixture.NewStore(paths)
	run, err := store.SeedImplementation("alpha", "op-1", implementation.Reservation{Context: &workmodel.WorkContext{Directory: "/fixture/work", TaskBranch: "task-branch", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}}, Agent: "implementer", SupervisorPID: 10})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	// The data store is independent of the paths used for the mutation lock.
	lockPaths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := workflow.PrepareTaskRun(context.Background(), workflow.PrepareTaskRunOptions{
		Runner: phaseexec.Runner{Paths: lockPaths, Store: workstate.NewStore(paths), RepositoryID: "alpha", TaskID: "op-1"}, Task: task.Task{ID: "op-1"}, Trigger: "task_run",
		Probe: recoveryProbe(map[int]agentexec.ProcessLiveness{10: agentexec.ProcessAbsent}),
	})
	if err != nil {
		t.Fatalf("prepare task run: %v", err)
	}
	if prepared.Inspection.Condition != phaseexec.AttachedExecutionRecoverable {
		t.Fatalf("inspection = %#v, want recoverable", prepared.Inspection)
	}
	if prepared.State.Runs[0].Attempt != run.Attempt || prepared.State.Runs[0].Status != taskstate.RunStatusInterrupted {
		t.Fatalf("prepared state = %#v, want interrupted attempt", prepared.State.Runs)
	}
	if prepared.Route.Action != workflow.TaskRunActionStartImplementation {
		t.Fatalf("route = %#v, want replacement implementation", prepared.Route)
	}
}

func recoveryProbe(values map[int]agentexec.ProcessLiveness) phaseexec.ProcessProbe {
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
	workstate.Store
	stale workmodel.State
	loads int
}

func (s *staleThenRecoveredStore) Load(repoID, taskID string) (workmodel.State, error) {
	if s.loads == 0 {
		s.loads++
		return s.stale, nil
	}
	return s.Store.Load(repoID, taskID)
}
