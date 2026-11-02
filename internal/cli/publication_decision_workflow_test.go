//go:build integration

package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"

	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowPublicationPlanEditsAndInterruptedConsentNeverDeliverAnotherIntent(t *testing.T) {
	f := newFinalizationFixture(t, "op-plan")
	branch, dir := f.expectedTarget("op-plan")
	f.completion("op-plan", branch, dir, "feat: reviewed intent", "Retain the reviewed implementation.", taskstate.RunStatusSucceeded)
	f.run("a\n", "task", "run", "op-plan")
	original := f.inspectWorkflow("op-plan")
	require.NotNil(t, original.Pending)
	assert.Equal(t, "create-pr", original.Publication.Operation)
	assert.Equal(t, "initial-commit", original.Publication.Destination.Commit)
	assert.Equal(t, work.ReviewPassed, original.Publication.ReviewOutcome)
	assert.Equal(t, "output-1", original.Revision.Code.Commit)
	f.assertUnpublished("op-plan")

	proposal := workflow.PublicationPlanResponse{Flow: publication.IntegrationFlowPullRequest, DestinationBranch: "main", Content: "feat: edited publication title\n\nOperator-authored publication content."}
	originalID, err := json.Marshal(original.Pending.ID)
	require.NoError(t, err)
	beforeEdits, _ := f.loadFinalTask("op-plan")
	for _, invalid := range []struct {
		name    string
		change  func(*workflow.PublicationPlanResponse)
		message string
	}{
		{"unknown flow", func(p *workflow.PublicationPlanResponse) { p.Flow = "other" }, "integration_flow"},
		{"missing content", func(p *workflow.PublicationPlanResponse) { p.Content = "Title only" }, "title and body"},
		{"missing destination", func(p *workflow.PublicationPlanResponse) { p.DestinationBranch = "" }, "destination branch"},
		{"self destination", func(p *workflow.PublicationPlanResponse) { p.DestinationBranch = branch }, "cannot be the task branch"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			bad := proposal
			invalid.change(&bad)
			plan, err := json.Marshal(bad)
			require.NoError(t, err)
			_, _, err = f.runError("", "task", "respond", "op-plan", "--interaction", string(originalID), "--action", "revise", "--plan", string(plan))
			require.ErrorContains(t, err, invalid.message)
			preserved, _ := f.loadFinalTask("op-plan")
			assert.Equal(t, beforeEdits, preserved)
		})
	}
	edited := f.revisePublication("op-plan", original, proposal)
	require.NotNil(t, edited.Pending)
	assert.NotEqual(t, original.Pending.ID.Owner, edited.Pending.ID.Owner)
	assert.Equal(t, original.Publication.Review, edited.Publication.Review)
	assert.Equal(t, original.Revision, edited.Revision)
	assert.Equal(t, proposal.Content, edited.Publication.Content)
	assert.False(t, edited.Publication.Consented)
	unchanged := f.revisePublication("op-plan", edited, proposal)
	assert.Equal(t, edited.Pending.ID, unchanged.Pending.ID, "unchanged intent does not create a new operation")
	oldID, err := json.Marshal(original.Pending.ID)
	require.NoError(t, err)
	_, _, err = f.runError("", "task", "respond", "op-plan", "--interaction", string(oldID), "--action", "authorize", "--evidence", "Stale grant")
	require.ErrorIs(t, err, workflow.ErrStaleInteraction)

	id, err := json.Marshal(edited.Pending.ID)
	require.NoError(t, err)
	args := []string{"task", "respond", "op-plan", "--interaction", string(id), "--action", "authorize", "--evidence", "Operator accepts the edited plan"}
	before, _ := f.loadFinalTask("op-plan")
	item := f.backend.tasks["op-plan"]
	changedTask := item.Clone()
	changedTask.Title = "Changed requirements after review"
	f.backend.tasks["op-plan"] = changedTask
	_, _, err = f.runError("", args...)
	require.ErrorContains(t, err, "requirements changed")
	f.backend.tasks["op-plan"] = item
	f.candidate.destinationHeads = map[string]string{"main": "advanced-destination"}
	_, _, err = f.runError("", args...)
	require.ErrorContains(t, err, "destination context changed")
	f.candidate.destinationHeads = nil
	f.candidate.head = "different-clean-commit"
	_, _, err = f.runError("", args...)
	require.ErrorContains(t, err, "not at the reviewed commit")
	refused, _ := f.loadFinalTask("op-plan")
	assert.Equal(t, before, refused)
	f.candidate.head = "output-1"
	f.assertUnpublished("op-plan")

	// Respond records consent before rendering its result. A disconnected output
	// cannot undo that grant or launch delivery as a side effect of the answer.
	var diagnostics bytes.Buffer
	err = f.runWithWriters("", unavailableOutput{}, &diagnostics, args...)
	require.ErrorContains(t, err, "output unavailable")
	accepted := f.inspectWorkflow("op-plan")
	assert.Nil(t, accepted.Pending)
	assert.True(t, accepted.Publication.Consented)
	assert.Equal(t, edited.Publication.Plan, accepted.Publication.Plan)
	f.assertUnpublished("op-plan")
	authorized, _ := f.loadFinalTask("op-plan")
	grant := authorized.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase).Consent
	require.NotNil(t, grant)
	assert.NotZero(t, grant.IntentDigest)
	assert.Len(t, authorized.Reviews, 1, "content edits do not repeat review")
	assert.Equal(t, original.Publication.Content, authorized.Work.Workflows[0].Phases[original.Publication.Plan.Phase].(*work.PublicationPhase).Plan.Content)
	_, _, err = f.runError("", args...)
	require.ErrorIs(t, err, workflow.ErrStaleInteraction)

	// Interrupt after a real semantic PR creation but before its observation can
	// be saved. Continuation must reconcile that PR under the original consent.
	f.pr.statusError = errors.New("observation disconnected")
	_, _, err = f.runError("", "task", "run", "op-plan")
	require.ErrorIs(t, err, f.pr.statusError)
	pending, _ := f.loadFinalTask("op-plan")
	assert.Equal(t, authorized, pending)
	assert.Len(t, f.pr.created, 1)
	assert.Empty(t, f.tasks.closed)
	f.pr.statusError = nil
	f.setConfig("publication", map[string]any{"integration_flow": "direct-merge", "title_template": "Changed {{summary}}"})
	f.pipelines("new-policy", map[string][]map[string]any{"new-policy": {{"kind": "manual", "name": "new-earlier-requirement"}}})
	f.run("", "task", "run", "op-plan")
	f.assertPublishedPR("op-plan")
	final, _ := f.loadFinalTask("op-plan")
	assert.Equal(t, grant, final.Work.Workflows[0].Phases[accepted.Publication.Plan.Phase].(*work.PublicationPhase).Consent)
	assert.Len(t, final.Reviews, 1, "configuration edits do not retrospectively invalidate reviewed evidence")
	assert.Len(t, f.pr.created, 1, "retry observes the existing PR instead of creating another")
	assert.Equal(t, "feat: edited publication title", f.pr.created[0].Title)
	assert.Equal(t, "Operator-authored publication content.", f.pr.created[0].Body)
}

func TestIntegrationWorkflowPublicationPromptPausesOnEOFAndRecordsConsentWithoutDelivery(t *testing.T) {
	f := newFinalizationFixture(t, "op-prompt")
	branch, dir := f.expectedTarget("op-prompt")
	f.completion("op-prompt", branch, dir, "feat: terminal decision", "Separate assessment and delivery.", taskstate.RunStatusSucceeded)
	f.options.Dependencies.Terminal = cli.TerminalCapabilities{InputIsTerminal: func(io.Reader) bool { return true }, OutputIsTerminal: func(io.Writer) bool { return true }, OutputWidth: func(io.Writer) (int, bool) { return 0, false }}
	_, output := f.run("a\nauthorize", "task", "run", "op-prompt")
	for _, fact := range []string{"PUBLICATION", "publication-decision (manual)", "PUBLICATION CONTENT", "feat: terminal decision", "FLOW", "DESTINATION", "EFFECTS", "Publication decision"} {
		assert.Contains(t, output, fact)
	}
	paused := f.inspectWorkflow("op-prompt")
	require.NotNil(t, paused.Pending, "an unterminated answer at EOF is not consent")
	f.assertUnpublished("op-prompt")

	_, output = f.run("i\ndirect-merge\nq\n", "task", "run", "op-prompt")
	assert.Contains(t, output, "Push destination")
	changed := f.inspectWorkflow("op-prompt")
	require.NotNil(t, changed.Pending)
	assert.Equal(t, "direct-merge", changed.Publication.Flow)
	assert.NotEqual(t, paused.Pending.ID.Owner, changed.Pending.ID.Owner)
	f.assertUnpublished("op-prompt")
	var diagnostics bytes.Buffer
	err := f.runWithWriters("a\n", unavailableOutput{}, &diagnostics, "task", "run", "op-prompt")
	require.ErrorContains(t, err, "output unavailable")
	accepted := f.inspectWorkflow("op-prompt")
	assert.Nil(t, accepted.Pending)
	assert.True(t, accepted.Publication.Consented)
	f.assertUnpublished("op-prompt")
}
