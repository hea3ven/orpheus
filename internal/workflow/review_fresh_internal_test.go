package workflow

import (
	"github.com/hea3ven/orpheus/internal/workflow/review"
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
