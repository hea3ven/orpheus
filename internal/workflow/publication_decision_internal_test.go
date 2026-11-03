package workflow

import (
	"context"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPublicationPlanReplacementRequiresValidIntentAndPreservesReviewedEvidence(t *testing.T) {
	snapshot, err := testutil.DeliveredPRWork("alpha", "op-review", "/fixture/task", "orpheus/op-review", "https://example.test/pr/1")
	require.NoError(t, err)
	snapshot.Workflows[0].Phases = snapshot.Workflows[0].Phases[:4]
	p := snapshot.Workflows[0].Phases[3].(*work.PublicationPhase)
	p.Consent, p.Delivery = nil, nil
	snapshot.Workflows[0].Phases[2].(*work.ReviewPhase).Scope.Requirements = "Task intent\n\n\n\n"
	controller, store, _ := interactionController(t, snapshot)
	original, err := controller.Inspect(context.Background(), "op-review")
	require.NoError(t, err)
	require.NotNil(t, original.Pending)
	before, err := store.Load("alpha", "op-review")
	require.NoError(t, err)
	proposal := PublicationPlanResponse{Flow: "direct-merge", DestinationBranch: "main", Content: "feat: operator-authored title\n\nExact publication content."}
	for _, invalid := range []struct {
		name    string
		change  func(*PublicationPlanResponse)
		message string
	}{
		{"unknown flow", func(p *PublicationPlanResponse) { p.Flow = "other" }, "integration_flow"},
		{"missing flow", func(p *PublicationPlanResponse) { p.Flow = "" }, "explicit flow"},
		{"missing content", func(p *PublicationPlanResponse) { p.Content = "Title only" }, "title and body"},
		{"missing destination", func(p *PublicationPlanResponse) { p.DestinationBranch = "" }, "destination branch"},
		{"self destination", func(p *PublicationPlanResponse) { p.DestinationBranch = "orpheus/op-review" }, "cannot be the task branch"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			bad := proposal
			invalid.change(&bad)
			_, err := controller.Respond(context.Background(), original.Pending.ID, bad)
			require.ErrorContains(t, err, invalid.message)
			preserved, err := store.Load("alpha", "op-review")
			require.NoError(t, err)
			assert.Equal(t, before, preserved)
		})
	}
	changed, err := controller.Respond(context.Background(), original.Pending.ID, proposal)
	require.NoError(t, err)
	require.NotNil(t, changed.Pending)
	assert.NotEqual(t, original.Pending.ID.Owner, changed.Pending.ID.Owner)
	assert.Equal(t, original.Revision, changed.Revision)
	assert.Equal(t, original.Publication.Review, changed.Publication.Review)
	assert.Equal(t, proposal.Content, changed.Publication.Content)
	assert.False(t, changed.Publication.Consented)
	identical, err := controller.Respond(context.Background(), changed.Pending.ID, proposal)
	require.NoError(t, err)
	assert.Equal(t, changed.Pending.ID, identical.Pending.ID)
	_, err = controller.Respond(context.Background(), original.Pending.ID, PublicationResponse{Action: DecisionAuthorize, Evidence: "Stale grant"})
	require.ErrorIs(t, err, ErrStaleInteraction)
	retained, err := store.Load("alpha", "op-review")
	require.NoError(t, err)
	assert.Equal(t, before.Workflows[0].Phases[3], retained.Workflows[0].Phases[3], "replaced intent remains immutable history")
}
