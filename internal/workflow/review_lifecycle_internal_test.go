package workflow

import (
	"context"
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewStartupStopsAfterConcurrentPrimaryRecovery(t *testing.T) {
	inspection := AttachedExecutionInspection{Condition: AttachedExecutionAlreadyRecovered, Reason: "primary_review_interrupted_by_concurrent_recovery"}
	assert.True(t, reviewStartupStopsForInspection(inspection))
	outcome := primaryReviewLifecycleOutcome(ReviewAttemptContext{}, inspection)
	assert.Equal(t, ReviewLifecycleOutcomePrimaryRecovered, outcome.Kind)
	assert.Equal(t, inspection, outcome.RecoveryInspection)
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
