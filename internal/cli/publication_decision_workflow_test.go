//go:build integration

package cli_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/hea3ven/orpheus/internal/review"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowPublicationPlanEditsAndInterruptedConsentNeverDeliverAnotherIntent(t *testing.T) {
	f := newFinalizationFixture(t, "op-plan")
	branch, dir := f.expectedTarget("op-plan")
	f.completion("op-plan", branch, dir, "feat: reviewed intent", "Retain the reviewed implementation.", taskstate.RunStatusSucceeded)
	f.run("a\nq\n", "task", "run", "op-plan")
	original := f.inspectWorkflow("op-plan")
	require.NotNil(t, original.Pending)
	assert.Equal(t, "create-pr", original.Publication.Operation)
	assert.Equal(t, "initial-commit", original.Publication.Destination.Commit)
	assert.Equal(t, work.ReviewPassed, original.Publication.ReviewOutcome)
	assert.Equal(t, "output-1", original.Revision.Code.Commit)
	f.assertUnpublished("op-plan")

	beforeEdits, _ := f.loadFinalTask("op-plan")
	for _, input := range []string{"i\nother\n", "d\n" + branch + "\n"} {
		_, _, err := f.runError(input, "task", "run", "op-plan")
		require.Error(t, err)
		preserved, _ := f.loadFinalTask("op-plan")
		assert.Equal(t, beforeEdits, preserved)
	}
	f.run("i\ndirect-merge\ni\npull-request\nq\n", "task", "run", "op-plan")
	edited := f.inspectWorkflow("op-plan")
	require.NotNil(t, edited.Pending)
	assert.NotEqual(t, original.Pending.ID.Owner, edited.Pending.ID.Owner)
	assert.Equal(t, original.Publication.Review, edited.Publication.Review)
	assert.Equal(t, original.Revision, edited.Revision)
	assert.Equal(t, original.Publication.Content, edited.Publication.Content)
	assert.False(t, edited.Publication.Consented)
	f.run("i\npull-request\nq\n", "task", "run", "op-plan")
	assert.Equal(t, edited.Pending.ID, f.inspectWorkflow("op-plan").Pending.ID, "unchanged intent does not create a new operation")

	before, _ := f.loadFinalTask("op-plan")
	item := f.backend.tasks["op-plan"]
	changedTask := item.Clone()
	changedTask.Title = "Changed requirements after review"
	f.backend.tasks["op-plan"] = changedTask
	_, _, err := f.runError("a\n", "task", "run", "op-plan")
	require.ErrorContains(t, err, "requirements changed")
	f.backend.tasks["op-plan"] = item
	f.candidate.destinationHeads = map[string]string{"main": "advanced-destination"}
	_, _, err = f.runError("a\n", "task", "run", "op-plan")
	require.ErrorContains(t, err, "destination context changed")
	f.candidate.destinationHeads = nil
	f.candidate.head = "different-clean-commit"
	_, _, err = f.runError("a\n", "task", "run", "op-plan")
	require.ErrorContains(t, err, "not at the reviewed commit")
	refused, _ := f.loadFinalTask("op-plan")
	assert.Equal(t, before, refused)
	f.candidate.head = "output-1"
	f.assertUnpublished("op-plan")

	// Authorization continues immediately; interrupt after PR creation succeeded
	// but before its observation can be saved. The same operation must reconcile.
	f.pr.statusError = errors.New("observation disconnected")
	_, _, err = f.runError("a\n", "task", "run", "op-plan")
	require.ErrorIs(t, err, f.pr.statusError)
	authorized, _ := f.loadFinalTask("op-plan")
	grant := authorized.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase).Consent
	require.NotNil(t, grant)
	assert.NotZero(t, grant.IntentDigest)
	assert.Nil(t, authorized.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase).Delivery)
	assert.Len(t, authorized.Reviews, 1, "flow edits do not repeat review")
	assert.Len(t, f.pr.created, 1)
	assert.Empty(t, f.tasks.closed)
	f.pr.statusError = nil
	f.setConfig("publication", map[string]any{"integration_flow": "direct-merge", "title_template": "Changed {{summary}}"})
	f.pipelines("new-policy", map[string][]map[string]any{"new-policy": {{"kind": "manual", "name": "new-earlier-requirement"}}})
	f.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return false }
	f.run("", "task", "run", "op-plan")
	f.assertPublishedPR("op-plan")
	final, _ := f.loadFinalTask("op-plan")
	assert.Equal(t, grant, final.Work.Workflows[0].Phases[edited.Publication.Plan.Phase].(*work.PublicationPhase).Consent)
	assert.Len(t, final.Reviews, 1, "configuration edits do not retrospectively invalidate reviewed evidence")
	assert.Len(t, f.pr.created, 1, "retry observes the existing PR instead of creating another")
	assert.Equal(t, "feat: reviewed intent", f.pr.created[0].Title)
}

func TestIntegrationWorkflowPublicationPromptPausesOnEOFAndContinuesDeliveryWithRedirectedOutput(t *testing.T) {
	f := newFinalizationFixture(t, "op-prompt")
	branch, dir := f.expectedTarget("op-prompt")
	f.completion("op-prompt", branch, dir, "feat: terminal decision", "Separate assessment and delivery.", taskstate.RunStatusSucceeded)
	f.options.Dependencies.Terminal = cli.TerminalCapabilities{InputIsTerminal: func(io.Reader) bool { return true }, OutputIsTerminal: func(io.Writer) bool { return false }}
	_, output := f.run("a\nauthorize", "task", "run", "op-prompt")
	for _, fact := range []string{"PUBLICATION", "publication-decision (manual)", "PUBLICATION CONTENT", "feat: terminal decision", "FLOW", "DESTINATION", "EFFECTS", "Publication decision"} {
		assert.Contains(t, output, fact)
	}
	assert.NotContains(t, output, "\x1b")
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
	final, item := f.loadFinalTask("op-prompt")
	assert.Equal(t, taskmodel.StatusClosed, item.Status, "authorization delivers and closes in the same invocation, even with redirected output")
	assert.NotNil(t, taskstate.FinalizationFacts(final).ClosedAt, "failed result output cannot erase observed closure")
	assert.Equal(t, "merge-commit", f.publication.remote["main"])
	assert.Equal(t, 1, f.publication.merges)
	assert.Empty(t, f.pr.created)
}

func TestIntegrationWorkflowNoninteractiveDecisionsRetainWorkAndResumeWithoutReplay(t *testing.T) {
	for _, boundary := range []string{"manual assessment", "manual command", "findings", "follow-up selection", "publication"} {
		t.Run(boundary, func(t *testing.T) {
			f := newFinalizationFixture(t, "op-input")
			branch, dir := f.expectedTarget("op-input")
			f.completion("op-input", branch, dir, "feat: retained work", "Await the operator without replay.", taskstate.RunStatusSucceeded)
			prior := f.check("prior", checkResult{})
			steps := []map[string]any{{"kind": "check", "name": "prior", "command": prior}}
			input := "a\n"
			switch boundary {
			case "manual assessment":
				steps = append(steps, map[string]any{"kind": "manual", "name": "assessment"})
				input = "a\na\n"
			case "manual command":
				command := f.check("inspection", checkResult{})
				steps = append(steps, map[string]any{"kind": "manual", "name": "assessment", "command": command})
				input = "y\na\na\n"
			case "findings":
				command := f.check("blocker", checkResult{code: 7})
				steps = append(steps, map[string]any{"kind": "check", "name": "blocker", "command": command})
				input = "w\nAccepted risk\na\n"
			case "follow-up selection":
				f.reviewers(semanticAgentOutcome{findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeSeparateTask, Title: "Follow up", Description: "Retain this proposal", TaskProposal: taskstate.ReviewTaskProposal{Title: "Separate improvement", Description: "Improve it later", AcceptanceCriteria: "Verified separately"}}}})
				steps = append(steps, map[string]any{"kind": "agent_review", "name": "reviewer"})
				input = "n\na\n"
			}
			f.pipelines("standard", map[string][]map[string]any{"standard": steps})
			f.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return false }
			piped := &countedDecisionInput{Reader: strings.NewReader(input)}
			command := cli.NewRootCommandWithOptions(f.options)
			command.SetIn(piped)
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SetArgs([]string{"task", "run", "op-input"})
			err := command.Execute()
			require.ErrorIs(t, err, review.ErrInteractionRequired)
			require.ErrorContains(t, err, "orpheus task run op-input")
			require.ErrorContains(t, err, "interactive terminal")
			assert.Zero(t, piped.reads, "piped answers must never be read as operator decisions")
			f.assertUnpublished("op-input")
			before, _ := f.loadFinalTask("op-input")
			require.Len(t, before.Reviews, 1)
			assert.NotEqual(t, taskstate.ReviewStatusFailed, before.Reviews[0].Status)
			_, _, err = f.runError(input, "task", "run", "op-input")
			require.ErrorIs(t, err, review.ErrInteractionRequired)
			preserved, _ := f.loadFinalTask("op-input")
			assert.Equal(t, before, preserved, "unavailable input must retain the exact pending work")
			f.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return true }
			f.run(input, "task", "run", "op-input")
			f.assertPublishedPR("op-input")
			final, _ := f.loadFinalTask("op-input")
			assert.Len(t, final.Reviews, 1)
			assert.Equal(t, before.Reviews[0].StartedAt, final.Reviews[0].StartedAt)
			assert.Equal(t, 1, strings.Count(strings.Join(f.checkCalls, ","), "prior"), "completed checks must not replay")
			assert.Empty(t, f.tasks.created)
			if boundary == "follow-up selection" {
				assert.Len(t, f.agent.launches, 1)
				assert.Equal(t, before.Reviews[0].Findings, final.Reviews[0].Findings)
			}
		})
	}
}

type countedDecisionInput struct {
	io.Reader
	reads int
}

func (r *countedDecisionInput) Read(p []byte) (int, error) {
	r.reads++
	return r.Reader.Read(p)
}
