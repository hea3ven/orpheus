package workflow

import (
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workfixture"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workstate"

	"context"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewStartupStopsAfterConcurrentReviewRecovery(t *testing.T) {
	paths := newDispatchTestPaths(t)
	store := workfixture.NewStore(paths)
	snapshot := testutil.ReviewableWork("alpha", "op-review", "/fixture/work", "task-branch")
	if _, err := workstate.NewStore(paths).Save(snapshot, 0); err != nil {
		t.Fatal(err)
	}
	attempt, err := store.SeedReview("alpha", "op-review", review.Reservation{Pipeline: "ai", Contract: work.ReviewContract{PipelineKey: "ai", Slots: []work.ReviewSlot{{Kind: work.ReviewAgent, ConfigKey: "inspect"}}}, Scope: work.ReviewScope{Requirements: "Fixture task requirements", Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}, Output: work.OutputRef{Phase: 1}}})
	require.NoError(t, err)
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, SupervisorPID: 10, StartedAt: attempt.StartedAt}
	started, err := store.ObserveStep("alpha", "op-review", attempt.Attempt, review.StepObservation{Address: attempt.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "inspect", Execution: &execution})
	require.NoError(t, err)
	stale, err := store.Load("alpha", "op-review")
	require.NoError(t, err)
	_, err = (phaseexec.Runner{Store: workstate.NewStore(paths), RepositoryID: "alpha", TaskID: "op-review"}).CommitHeld(context.Background(), review.InterruptionChange(started.Steps[0].Address, "supervisor_absent_child_pid_not_recorded_assume_not_started", "task_run"))
	require.NoError(t, err)
	recovered, err := store.Load("alpha", "op-review")
	require.NoError(t, err)
	observed := &concurrentRecoveryLifecycleStore{Store: store, stale: stale}
	pipelineCalled := false
	service := ReviewLifecycleService{
		Paths: paths, RunStore: observed, Frontend: &freshReviewFrontend{},
		Sources:        []task.RepositorySource{{Repository: task.Repository{ID: "alpha", TaskIDPrefix: "op"}}},
		BackendFactory: func(task.RepositorySource) (ReviewLifecycleBackend, error) { return recoveryLifecycleBackend{}, nil },
		PipelineRunner: func(review.PipelineRunOptions) (review.PipelineOutcome, error) {
			pipelineCalled = true
			return review.PipelineOutcome{}, errors.New("unexpected replacement review")
		},
	}

	outcome, err := (Controller{ReviewLifecycleService: service}).Advance(context.Background(), AdvanceOptions{TaskID: "op-review"})

	require.NoError(t, err)
	assert.Equal(t, ReviewLifecycleOutcomeExecutionRecovered, outcome.Review.Kind)
	assert.Equal(t, phaseexec.AttachedExecutionAlreadyRecovered, outcome.Review.RecoveryInspection.Condition)
	assert.Equal(t, "review_interrupted_by_concurrent_recovery", outcome.Review.RecoveryInspection.Reason)
	assert.False(t, pipelineCalled)
	final, err := store.Load("alpha", "op-review")
	require.NoError(t, err)
	assert.Equal(t, recovered, final, "startup must preserve the recovered review and start no new execution")
}

type concurrentRecoveryLifecycleStore struct {
	workfixture.Store
	stale taskstate.TaskState
	loads int
}

func (s *concurrentRecoveryLifecycleStore) Load(repoID, taskID string) (taskstate.TaskState, error) {
	s.loads++
	if s.loads == 1 {
		return s.stale, nil
	}
	return s.Store.Load(repoID, taskID)
}

type recoveryLifecycleBackend struct{ ReviewLifecycleBackend }

func (recoveryLifecycleBackend) Get(context.Context, string) (task.Task, error) {
	return task.Task{ID: "op-review", IssueType: task.IssueTypeTask, Status: task.StatusInProgress}, nil
}

func TestImplementationRequiresRunnerBeforeAnyEffects(t *testing.T) {
	controller := Controller{}
	stop, err := controller.advanceImplementation(context.Background(), &advanceInvocation{}, taskstate.TaskState{}, TaskRunRoute{Action: TaskRunActionTargetedRepair}, &AdvanceResult{})
	require.ErrorContains(t, err, "implementation agent runner is required")
	assert.True(t, stop)
}
