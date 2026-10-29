package workflow

import (
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"

	"context"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewStartupStopsAfterConcurrentReviewRecovery(t *testing.T) {
	paths := newDispatchTestPaths(t)
	store := taskstate.NewStore(paths)
	snapshot := testutil.ReviewableWork("alpha", "op-review", "/fixture/work", "task-branch")
	if _, err := workstate.NewStore(paths).Save(snapshot, 0); err != nil {
		t.Fatal(err)
	}
	attempt, err := store.StartReviewWithOptions("alpha", "op-review", taskstate.StartReviewOptions{Pipeline: "ai", Step: "inspect", Contract: work.ReviewContract{PipelineKey: "ai", Slots: []work.ReviewSlot{{Kind: work.ReviewAgent, ConfigKey: "inspect"}}}, Scope: work.ReviewScope{Requirements: "Fixture task requirements", Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}, Output: work.OutputRef{Phase: 1}}})
	require.NoError(t, err)
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, SupervisorPID: 10, StartedAt: attempt.StartedAt}
	_, err = store.RecordReviewStep("alpha", "op-review", attempt.Attempt, taskstate.RecordReviewStepOptions{Address: attempt.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "inspect", Execution: &execution})
	require.NoError(t, err)
	stale, err := store.Load("alpha", "op-review")
	require.NoError(t, err)
	_, err = store.InterruptReviewExecution("alpha", "op-review", attempt.Attempt, "inspect", taskstate.InterruptReviewExecutionOptions{Reason: "supervisor_absent_child_pid_not_recorded_assume_not_started", Trigger: "task_run"})
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

	outcome, err := service.Run(context.Background(), ReviewLifecycleOptions{TaskID: "op-review"})

	require.NoError(t, err)
	assert.Equal(t, ReviewLifecycleOutcomeExecutionRecovered, outcome.Kind)
	assert.Equal(t, AttachedExecutionAlreadyRecovered, outcome.RecoveryInspection.Condition)
	assert.Equal(t, "review_interrupted_by_concurrent_recovery", outcome.RecoveryInspection.Reason)
	assert.False(t, pipelineCalled)
	final, err := store.Load("alpha", "op-review")
	require.NoError(t, err)
	assert.Equal(t, recovered, final, "startup must preserve the recovered review and start no new execution")
}

type concurrentRecoveryLifecycleStore struct {
	taskstate.Store
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

func TestAutonomousReviewFollowUpRequiresRunnerBeforeAnyEffects(t *testing.T) {
	// All other collaborators intentionally remain nil: none may be invoked when
	// the service is missing its required runner.
	service := ReviewLifecycleService{}
	err := service.runAutonomousReviewFollowUp(context.Background(), ReviewAttemptContext{}, "implementer", 1, []int{0})
	require.ErrorContains(t, err, "review lifecycle agent runner is required")
	outcome := reviewLifecycleOperationalFailure(ReviewAttemptContext{Review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusBlocked}}, err)
	assert.Equal(t, ReviewLifecycleOutcomeOperationalFail, outcome.Kind)
	assert.ErrorIs(t, outcome.Err, err)
	assert.Equal(t, taskstate.ReviewStatusBlocked, outcome.Context.Review.Status)
}
