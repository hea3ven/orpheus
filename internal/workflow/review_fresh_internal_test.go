package workflow

import (
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/require"
	"testing"
)

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
		Review:   taskstate.ReviewAttempt{Address: work.AttemptRef{RepositoryID: "alpha", TaskID: "op-resume", Phase: 2}, Attempt: 3, Pipeline: "standard", Step: "paused-step", Status: status},
		Pipeline: review.Pipeline{Name: "standard", Steps: []review.Step{{Name: "paused-step", Kind: kind}}},
	}
	if automated {
		ctx.Review.Findings = []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Step: "paused-step", Title: "existing blocker"}}
	}
	index := 0
	owner := ctx.Review.Address
	owner.Step = &index
	ctx.Review.Steps = []taskstate.ReviewStep{{Address: owner, Kind: kind, Name: "paused-step"}}
	var err error
	ctx.execution, err = review.ResumeExecution(ctx.Pipeline, ctx.Review)
	require.NoError(t, err)
	return ctx
}
