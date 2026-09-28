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
			previous := ReviewAttemptContext{
				ReviewContext: ReviewContext{
					store:  store,
					Source: task.RepositorySource{Repository: task.Repository{ID: "alpha"}},
					Task:   task.Task{ID: "op-fresh"}, Workdir: "/fixture/worktree",
					Target: tasktarget.Target{Branch: "task-branch", Worktree: "/fixture/worktree"},
				},
				Review:      taskstate.ReviewAttempt{Attempt: 7, Step: "old-step", Findings: []taskstate.ReviewFinding{{Title: "old finding"}}},
				Pipeline:    review.Pipeline{Name: "old-pipeline"},
				AgentConfig: agent.Config{Defaults: agent.AgentDefaults{Reviewer: "old-reviewer"}},
				resumed:     true, resumeAutomatedBlockerDecision: true,
			}
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
			assert.False(t, opts.ResumeFromStep)
			assert.False(t, opts.ResumeAutomatedBlockerDecision)
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
			resumed := ReviewAttemptContext{
				Review:  taskstate.ReviewAttempt{Attempt: 3, Step: "paused-step"},
				resumed: true, resumeAutomatedBlockerDecision: automatedDecision,
			}

			opts, err := service.pipelineRunOptions(context.Background(), resumed)

			require.NoError(t, err)
			assert.Equal(t, resumed, frontend.presented)
			assert.Equal(t, resumed.Review, opts.Attempt)
			assert.True(t, opts.ResumeFromStep)
			assert.Equal(t, automatedDecision, opts.ResumeAutomatedBlockerDecision)
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
