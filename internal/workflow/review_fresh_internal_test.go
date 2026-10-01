package workflow

import (
	"context"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFreshReviewConstructionExcludesPreviousAttemptState(t *testing.T) {
	for _, interruptedComparison := range []bool{false, true} {
		name := "ordinary fresh review"
		if interruptedComparison {
			name = "interrupted comparison"
		}
		t.Run(name, func(t *testing.T) {
			store := &freshReviewStore{}
			frontend := &freshReviewFrontend{}
			service := ReviewLifecycleService{Frontend: frontend}
			previous := pausedReviewContext(t, true)
			previous.ReviewContext = ReviewContext{
				store:  store,
				Source: task.RepositorySource{Repository: task.Repository{ID: "alpha"}},
				Task:   task.Task{ID: "op-fresh"}, Workdir: "/fixture/worktree",
				Target: tasktarget.Target{Branch: "task-branch", Worktree: "/fixture/worktree"},
			}
			previous.AgentConfig = agent.Config{Defaults: agent.AgentDefaults{Reviewer: "old-reviewer"}}
			pipeline := review.Pipeline{Name: "fresh-pipeline", Steps: []review.Step{
				{Name: "first", Kind: review.KindCheck}, {Name: "second", Kind: review.KindCheck},
			}}
			start := service.startFreshReview
			if interruptedComparison {
				start = service.startFreshReviewAfterInterruptedComparison
			}

			fresh, err := start(previous.ReviewContext, pipeline)

			require.NoError(t, err)
			assert.Equal(t, previous.ReviewContext, fresh.ReviewContext)
			assert.Equal(t, pipeline, fresh.Pipeline)
			assert.Equal(t, taskstate.ReviewAttempt{Attempt: 8, Pipeline: pipeline.Name, Step: "first", Status: taskstate.ReviewStatusRunning}, fresh.Review)
			assert.Empty(t, fresh.AgentConfig)
			assert.False(t, fresh.Resumed())
			assert.False(t, fresh.ResumesAutomatedBlockerDecision())
			assert.True(t, previous.Resumed())
			assert.True(t, previous.ResumesAutomatedBlockerDecision())
			assert.Equal(t, []string{"alpha", "op-fresh"}, store.target)
			assert.Equal(t, []taskstate.StartReviewOptions{{Pipeline: "fresh-pipeline", Step: "first"}}, store.starts)
			if interruptedComparison {
				assert.Zero(t, store.loads, "interrupted comparisons skip blocker disposition")
			} else {
				assert.Equal(t, 1, store.loads, "ordinary fresh reviews retain the blocker guard")
			}

			opts, err := service.pipelineRunOptions(context.Background(), fresh)
			require.NoError(t, err)
			assert.Equal(t, fresh, frontend.presented)
			assert.Equal(t, fresh.Review, opts.Attempt)
			assert.Equal(t, pipeline, opts.Pipeline)
			assert.Equal(t, "task-branch", opts.Branch)
			assert.Equal(t, "/fixture/worktree", opts.Workdir)
			assert.False(t, opts.Execution.Resumed())
			assert.False(t, opts.Execution.ResumesAutomatedDecision())
		})
	}
}

func TestPipelineHandoffPreservesPausedAttemptInstructions(t *testing.T) {
	for _, automatedDecision := range []bool{false, true} {
		name := "manual step"
		if automatedDecision {
			name = "automated blocker decision"
		}
		t.Run(name, func(t *testing.T) {
			frontend := &freshReviewFrontend{}
			service := ReviewLifecycleService{Frontend: frontend}
			resumed := pausedReviewContext(t, automatedDecision)
			resumed.Review.Status = taskstate.ReviewStatusRunning

			opts, err := service.pipelineRunOptions(context.Background(), resumed)

			require.NoError(t, err)
			assert.Equal(t, resumed, frontend.presented)
			assert.Equal(t, resumed.Review, opts.Attempt)
			assert.True(t, opts.Execution.Resumed())
			assert.Equal(t, automatedDecision, opts.Execution.ResumesAutomatedDecision())
		})
	}
}

type freshReviewStore struct {
	ReviewLifecycleStore
	loads  int
	target []string
	starts []taskstate.StartReviewOptions
}

func (s *freshReviewStore) Load(string, string) (taskstate.TaskState, error) {
	s.loads++
	return taskstate.TaskState{}, nil
}

func (s *freshReviewStore) StartReviewWithOptions(repoID, taskID string, opts taskstate.StartReviewOptions) (taskstate.ReviewAttempt, error) {
	s.target = []string{repoID, taskID}
	s.starts = append(s.starts, opts)
	return taskstate.ReviewAttempt{Attempt: 8, Pipeline: opts.Pipeline, Step: opts.Step, Status: taskstate.ReviewStatusRunning}, nil
}

type freshReviewFrontend struct {
	ReviewLifecycleFrontend
	presented ReviewAttemptContext
}

func (f *freshReviewFrontend) PipelinePresentation(ctx ReviewAttemptContext) (ReviewPipelinePresentation, error) {
	f.presented = ctx
	return ReviewPipelinePresentation{}, nil
}

func pausedReviewContext(t *testing.T, automated bool) ReviewAttemptContext {
	t.Helper()
	kind, status := review.KindManual, taskstate.ReviewStatusWaitingForManual
	if automated {
		kind, status = review.KindCheck, taskstate.ReviewStatusWaitingForAutomatedDecision
	}
	ctx := ReviewAttemptContext{
		Review:   taskstate.ReviewAttempt{Attempt: 3, Pipeline: "standard", Step: "paused-step", Status: status},
		Pipeline: review.Pipeline{Name: "standard", Steps: []review.Step{{Name: "paused-step", Kind: kind}}},
	}
	if automated {
		ctx.Review.Findings = []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Step: "paused-step", Title: "existing blocker"}}
	}
	var err error
	ctx.execution, err = review.ResumeExecution(ctx.Pipeline, ctx.Review)
	require.NoError(t, err)
	return ctx
}
