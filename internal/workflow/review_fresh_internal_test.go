package workflow

import (
	"context"
	"log/slog"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewExecutionRestartsFreshAttemptsAndPreservesPausedInteractions(t *testing.T) {
	for _, tc := range []struct {
		name             string
		fresh, automated bool
	}{
		{"fresh after manual pause", true, false},
		{"resume manual step", false, false},
		{"resume automated decision", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &freshReviewStore{}
			frontend := &freshReviewFrontend{}
			ctx := pausedReviewContext(t, tc.automated)
			ctx.ReviewContext = ReviewContext{
				store: store, Source: task.RepositorySource{Repository: task.Repository{ID: "alpha"}},
				Task: task.Task{ID: "op-fresh"}, Workdir: "/fixture/worktree",
				Target: tasktarget.Target{Branch: "task-branch", Worktree: "/fixture/worktree"},
			}
			ctx.AgentConfig = agent.Config{Defaults: agent.AgentDefaults{Reviewer: "previous-reviewer"}}
			var commands []string
			service := ReviewLifecycleService{Frontend: frontend, ReviewEffects: review.Effects{
				CaptureCandidate: func(context.Context, string, *slog.Logger, ...slog.Attr) (review.CandidateCheck, error) {
					return func() error { return nil }, nil
				},
				RunCommand: func(opts review.CommandOptions) (*int, error) {
					commands = append(commands, opts.Step.Name)
					code := 0
					return &code, nil
				},
			}}
			if tc.fresh {
				pipeline := review.Pipeline{Name: "fresh-pipeline", Steps: []review.Step{
					{Name: "first", Kind: review.KindCheck, Command: "semantic-check"},
					{Name: "approval", Kind: review.KindManual},
				}}
				var err error
				ctx, err = service.startFreshReview(ctx.ReviewContext, pipeline)
				require.NoError(t, err)
			} else {
				ctx.Pipeline.Steps = append([]review.Step{{Name: "already-completed", Kind: review.KindCheck, Command: "must-not-rerun"}}, ctx.Pipeline.Steps...)
				ctx.Review.Status = taskstate.ReviewStatusRunning
				store.current = ctx.Review
			}
			captured := false
			service.PipelineRunner = func(opts review.PipelineRunOptions) (review.PipelineOutcome, error) {
				captured = true
				assert.Equal(t, !tc.fresh, opts.Execution.Resumed())
				assert.Equal(t, !tc.fresh && tc.automated, opts.Execution.ResumesAutomatedDecision())
				assert.Equal(t, "task-branch", opts.Branch)
				assert.Equal(t, "/fixture/worktree", opts.Workdir)
				if tc.fresh {
					assert.Empty(t, opts.AgentConfig)
				}
				return review.RunPipeline(opts)
			}

			status, err := service.executeReviewAttempt(context.Background(), ctx)

			require.NoError(t, err)
			assert.True(t, captured)
			assert.Equal(t, ctx, frontend.presented)
			if tc.fresh {
				assert.Equal(t, []string{"first"}, commands, "fresh attempts must execute step one before pausing")
				assert.Equal(t, taskstate.ReviewStatusWaitingForManual, status)
				assert.Empty(t, store.current.Findings, "fresh attempts must not inherit previous blockers")
			} else {
				assert.Empty(t, commands, "resuming a paused interaction must not rerun completed checks")
				if tc.automated {
					assert.Equal(t, taskstate.ReviewStatusWaitingForAutomatedDecision, status)
				} else {
					assert.Equal(t, taskstate.ReviewStatusWaitingForManual, status)
				}
			}
		})
	}
}

type freshReviewStore struct {
	ReviewLifecycleStore
	current taskstate.ReviewAttempt
}

func (s *freshReviewStore) Load(string, string) (taskstate.TaskState, error) {
	if s.current.Attempt == 0 {
		return taskstate.TaskState{}, nil
	}
	return taskstate.TaskState{Reviews: []taskstate.ReviewAttempt{s.current}}, nil
}
func (s *freshReviewStore) StartReviewWithOptions(_, _ string, opts taskstate.StartReviewOptions) (taskstate.ReviewAttempt, error) {
	s.current = taskstate.ReviewAttempt{Attempt: 8, Pipeline: opts.Pipeline, Step: opts.Step, Status: taskstate.ReviewStatusRunning}
	return s.current, nil
}
func (s *freshReviewStore) RecordReviewStep(_, _ string, _ int, opts taskstate.RecordReviewStepOptions) (taskstate.ReviewAttempt, error) {
	s.current.Steps = append(s.current.Steps, taskstate.ReviewStep{Kind: opts.Kind, Name: opts.Name})
	return s.current, nil
}
func (s *freshReviewStore) PauseReviewForManual(_, _ string, _ int, step string) (taskstate.ReviewAttempt, error) {
	s.current.Step = step
	s.current.Status = taskstate.ReviewStatusWaitingForManual
	return s.current, nil
}
func (s *freshReviewStore) PauseReviewForAutomatedBlockerDecision(_, _ string, _ int, step string) (taskstate.ReviewAttempt, error) {
	s.current.Step = step
	s.current.Status = taskstate.ReviewStatusWaitingForAutomatedDecision
	return s.current, nil
}

type freshReviewFrontend struct {
	ReviewLifecycleFrontend
	presented ReviewAttemptContext
}

func (f *freshReviewFrontend) PipelinePresentation(ctx ReviewAttemptContext) (ReviewPipelinePresentation, error) {
	f.presented = ctx
	return ReviewPipelinePresentation{
		PauseBeforeManual: true,
		PromptAutomatedBlockers: func(review.AutomatedBlockerReview) ([]review.AutomatedBlockerDecision, error) {
			return []review.AutomatedBlockerDecision{{Action: review.AutomatedBlockerActionPause}}, nil
		},
	}, nil
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
