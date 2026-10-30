package workflow

import (
	"context"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControllerRefusesStaleAdvancementBeforeCallingPhaseHandlers(t *testing.T) {
	for _, change := range []string{"version", "workflow", "phase", "step", "action"} {
		t.Run(change, func(t *testing.T) {
			snapshot := testutil.ReviewableWork("alpha", "op-1", "/fixture/repo", "orpheus/op-1")
			appendRouteBlocker(&snapshot.Workflows[0])
			view, err := taskstate.Project(snapshot)
			require.NoError(t, err)
			item := task.Task{ID: "op-1", Status: task.StatusInProgress}
			expected, err := SelectTaskRunRoute(item, view)
			require.NoError(t, err)
			switch change {
			case "version":
				expected.Version++
			case "workflow":
				expected.Owner.Workflow++
			case "phase":
				expected.Owner.Phase--
			case "step":
				expected.Owner.Step = routeNumber(1)
			case "action":
				expected.Action = TaskRunActionStartImplementation
			}
			calls := 0
			controller := Controller{ReviewLifecycleService: ReviewLifecycleService{Paths: newDispatchTestPaths(t), RunStore: &controllerReadStore{view: view}}}
			result, err := controller.Advance(context.Background(), AdvanceOptions{Task: item, RepositoryID: "alpha", Expected: expected}, PhaseHandlers{
				Prepare: func(context.Context, TaskRunRoute) (PreparedImplementation, error) {
					calls++
					return PreparedImplementation{}, nil
				},
				Implement: func(context.Context, PreparedImplementation) agent.ExecutionResult {
					calls++
					return agent.ExecutionResult{}
				},
			})
			require.ErrorContains(t, err, "workflow advancement is stale")
			assert.Zero(t, calls)
			assert.Nil(t, result.Review)
			assert.Nil(t, result.Delivery)
			assert.Equal(t, snapshot, *view.Work)
		})
	}
}

func TestControllerRequiresPreparationToReserveItsOwnImplementation(t *testing.T) {
	snapshot := testutil.ReviewableWork("alpha", "op-1", "/fixture/repo", "orpheus/op-1")
	snapshot.Workflows[0].Phases = snapshot.Workflows[0].Phases[:1]
	view, err := taskstate.Project(snapshot)
	require.NoError(t, err)
	item := task.Task{ID: "op-1", Status: task.StatusInProgress}
	expected, err := SelectTaskRunRoute(item, view)
	require.NoError(t, err)
	for _, owner := range []work.AttemptRef{
		{RepositoryID: "alpha", TaskID: "another-task", Phase: 1},
		{RepositoryID: "alpha", TaskID: "op-1", Phase: 0},
	} {
		controller := Controller{ReviewLifecycleService: ReviewLifecycleService{Paths: newDispatchTestPaths(t), RunStore: &controllerReadStore{view: view}}}
		launched := false
		_, err := controller.Advance(context.Background(), AdvanceOptions{Task: item, RepositoryID: "alpha", Expected: expected}, PhaseHandlers{
			Prepare: func(ctx context.Context, route TaskRunRoute) (PreparedImplementation, error) {
				assert.Equal(t, TaskRunActionStartImplementation, route.Action)
				return PreparedImplementation{Start: DispatchStartResult{Attempt: taskstate.RunAttempt{Address: owner}}}, nil
			},
			Implement: func(context.Context, PreparedImplementation) agent.ExecutionResult {
				launched = true
				return agent.ExecutionResult{}
			},
		})
		require.Error(t, err)
		assert.False(t, launched)
		assert.Equal(t, snapshot, *view.Work)
	}
}

func TestControllerReviewReservationCannotAppendAfterConcurrentAdvancement(t *testing.T) {
	paths := newDispatchTestPaths(t)
	adapter := workstate.NewStore(paths)
	snapshot := testutil.ReviewableWork("alpha", "op-1", "/fixture/repo", "orpheus/op-1")
	_, err := adapter.Save(snapshot, 0)
	require.NoError(t, err)
	store := taskstate.NewStore(paths)
	controller := Controller{ReviewLifecycleService: ReviewLifecycleService{Paths: paths, RunStore: store, CandidateGit: &reservationDestinationGit{beforeReturn: func() {
		// Another controller reserves review after this invocation read its input,
		// but before it can reserve its own occurrence.
		_, err := store.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{Pipeline: "other", Contract: work.ReviewContract{PipelineKey: "other", Slots: []work.ReviewSlot{{Kind: work.ReviewManual, ConfigKey: "operator"}}}, Scope: work.ReviewScope{Requirements: "Original task", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "destination"}}})
		require.NoError(t, err)
	}}}}
	base := ReviewContext{paths: paths, store: store, Source: task.RepositorySource{Repository: task.Repository{ID: "alpha", DefaultBranch: "main"}}, Task: task.Task{ID: "op-1", Title: "Task intent"}, Workdir: "/fixture/repo"}
	_, err = controller.startFreshReview(base, review.BuiltinManualPipeline())
	require.ErrorContains(t, err, "review reservation is stale")
	final, err := store.Load("alpha", "op-1")
	require.NoError(t, err)
	require.Len(t, final.Reviews, 1)
	assert.Equal(t, "other", final.Reviews[0].Pipeline)
	assert.Len(t, final.Work.Workflows[0].ReviewContracts, 1)
	assert.Len(t, final.Work.Workflows[0].Phases, 3)
}

type controllerReadStore struct {
	ReviewLifecycleStore
	view taskstate.TaskState
}

func (s *controllerReadStore) Load(string, string) (taskstate.TaskState, error) { return s.view, nil }

type reservationDestinationGit struct {
	ReviewCandidateGit
	beforeReturn func()
}

func (g *reservationDestinationGit) ObserveDestination(context.Context, string, string) (work.Destination, error) {
	g.beforeReturn()
	return work.Destination{Remote: "origin", Branch: "main", Commit: "destination"}, nil
}
