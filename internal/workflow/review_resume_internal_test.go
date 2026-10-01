package workflow

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResumeReviewRejectsInvalidOrStaleWaitingStateBeforeTransition(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*taskstate.ReviewAttempt, *resumeReviewStore)
	}{
		{"missing step", func(a *taskstate.ReviewAttempt, _ *resumeReviewStore) { a.Step = "" }},
		{"removed step", func(a *taskstate.ReviewAttempt, _ *resumeReviewStore) { a.Step = "removed" }},
		{"wrong interaction", func(a *taskstate.ReviewAttempt, _ *resumeReviewStore) {
			a.Status = taskstate.ReviewStatusWaitingForAutomatedDecision
		}},
		{"no latest", func(_ *taskstate.ReviewAttempt, s *resumeReviewStore) { s.state.Reviews = nil }},
		{"attempt changed", func(_ *taskstate.ReviewAttempt, s *resumeReviewStore) { s.state.Reviews[0].Attempt++ }},
		{"step changed", func(_ *taskstate.ReviewAttempt, s *resumeReviewStore) { s.state.Reviews[0].Step = "other" }},
		{"pipeline changed", func(_ *taskstate.ReviewAttempt, s *resumeReviewStore) { s.state.Reviews[0].Pipeline = "other" }},
		{"status changed", func(_ *taskstate.ReviewAttempt, s *resumeReviewStore) {
			s.state.Reviews[0].Status = taskstate.ReviewStatusRunning
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := testutil.CanonicalTempDir(t)
			paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
			require.NoError(t, err)
			pipeline := review.BuiltinManualPipeline()
			paused := taskstate.ReviewAttempt{Attempt: 2, Pipeline: pipeline.Name, Step: pipeline.Steps[0].Name, Status: taskstate.ReviewStatusWaitingForManual}
			store := &resumeReviewStore{state: taskstate.TaskState{Reviews: []taskstate.ReviewAttempt{paused}}}
			tt.change(&paused, store)
			base := ReviewContext{paths: paths, store: store, Source: task.RepositorySource{Repository: task.Repository{ID: "alpha"}}, Task: task.Task{ID: "op-resume"}}
			service := ReviewLifecycleService{}

			_, err = service.resumeReview(base, paused, "")

			assert.ErrorIs(t, err, review.ErrInvalidExecution)
			assert.Zero(t, store.resumes)
			assert.Zero(t, store.finishes)
		})
	}
}

func TestExecuteReviewAttemptDoesNotFinishRejectedRequest(t *testing.T) {
	for _, stalePersistedState := range []bool{false, true} {
		name := "invalid handoff"
		if stalePersistedState {
			name = "stale pipeline entry"
		}
		t.Run(name, func(t *testing.T) {
			ctx := pausedReviewContext(t, true)
			ctx.Review.Status = taskstate.ReviewStatusRunning
			store := &resumeReviewStore{state: taskstate.TaskState{Reviews: []taskstate.ReviewAttempt{ctx.Review}}}
			ctx.store = store
			if stalePersistedState {
				store.state.Reviews[0].Step = "other"
			} else {
				ctx.execution = review.ExecutionRequest{}
			}
			service := ReviewLifecycleService{Frontend: &freshReviewFrontend{}}

			status, err := service.executeReviewAttempt(context.Background(), ctx)

			assert.ErrorIs(t, err, review.ErrInvalidExecution)
			assert.Empty(t, status)
			assert.Zero(t, store.resumes)
			assert.Zero(t, store.finishes)
		})
	}
}

type resumeReviewStore struct {
	ReviewLifecycleStore
	state    taskstate.TaskState
	resumes  int
	finishes int
}

func (s *resumeReviewStore) Load(string, string) (taskstate.TaskState, error) {
	return s.state, nil
}

func (s *resumeReviewStore) ResumeReview(string, string, int) (taskstate.ReviewAttempt, error) {
	s.resumes++
	return taskstate.ReviewAttempt{}, nil
}

func (s *resumeReviewStore) FinishReview(string, string, int, taskstate.ReviewStatus) (taskstate.ReviewAttempt, error) {
	s.finishes++
	return taskstate.ReviewAttempt{}, nil
}
