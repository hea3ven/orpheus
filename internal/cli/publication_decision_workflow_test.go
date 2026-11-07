//go:build integration

package cli_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/hea3ven/orpheus/internal/pullrequest"
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
	for _, input := range []string{"i\nother\n"} {
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
	f.candidate.destinationHeads = map[string]string{"main": "advanced-destination"}
	_, notice := f.run("b\n", "task", "run", "op-plan")
	assert.Contains(t, notice, "advanced-destination")
	refreshed := f.inspectWorkflow("op-plan")
	assert.Equal(t, "advanced-destination", refreshed.Publication.Destination.Commit)
	assert.Equal(t, "initial-commit", refreshed.Publication.ReviewedDestination.Commit)
	assert.Equal(t, original.Publication.Review, refreshed.Publication.Review, "same-branch advancement does not replay review")
	before, _ = f.loadFinalTask("op-plan")
	f.candidate.head = "different-clean-commit"
	_, _, err := f.runError("a\n", "task", "run", "op-plan")
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
	for _, lifecycle := range []pullrequest.State{pullrequest.StateClosed, pullrequest.StateMerged} {
		f.pr.state = lifecycle
		_, _, err = f.runError("", "task", "run", "op-plan")
		require.ErrorContains(t, err, "reconcile lifecycle")
		assert.Len(t, f.pr.created, 1, "a recovered closed/merged PR must not produce duplicate creation")
	}
	f.pr.state = pullrequest.StateOpen
	f.pr.existing.BaseBranch = "retargeted"
	_, _, err = f.runError("", "task", "run", "op-plan")
	require.ErrorContains(t, err, "destination, branch or public head changed")
	assert.Len(t, f.pr.created, 1, "retargeted PR is not absence")
	f.pr.existing.BaseBranch = "main"
	f.setConfig("publication", map[string]any{"integration_flow": "direct-merge", "title_template": "Changed {{summary}}"})
	f.pipelines("new-policy", map[string][]map[string]any{"new-policy": {{"kind": "manual", "name": "new-earlier-requirement"}}})
	f.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return false }
	f.run("", "task", "run", "op-plan")
	f.assertPublishedPR("op-plan")
	final, _ := f.loadFinalTask("op-plan")
	assert.Equal(t, grant, final.Work.Workflows[0].Phases[refreshed.Publication.Plan.Phase].(*work.PublicationPhase).Consent)
	assert.Len(t, final.Reviews, 1, "configuration edits do not retrospectively invalidate reviewed evidence")
	assert.Equal(t, authorized.Reviews, final.Reviews, "configuration edits preserve actual review results and findings")
	assert.Equal(t, authorized.Work.Workflows[0].ReviewContracts, final.Work.Workflows[0].ReviewContracts, "delivery does not reconcile earlier review requirements")
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

func selectOperatorCommit(f *finalizationWorkflowFixture, dir, commit, contents, relationship string) {
	f.t.Helper()
	f.candidate.contents = contents
	f.candidate.head = commit
	f.candidate.heads[dir] = commit
	f.candidate.trees[commit] = fmt.Sprintf("tree-%x", sha256.Sum256([]byte(contents)))
	f.candidate.hasCandidateChanges, f.candidate.hasStagedChanges = false, false
	f.candidate.publicationRelationship = relationship
}

func TestIntegrationWorkflowPublicationOperatorInputIsDistinctFromReviewAndRequiresProceed(t *testing.T) {
	for _, flow := range []string{"pull-request", "direct-merge"} {
		for _, repoRoot := range []bool{false, true} {
			for _, kind := range []string{"added", "amended", "rewritten"} {
				t.Run(fmt.Sprintf("%s/root_%t/%s", flow, repoRoot, kind), func(t *testing.T) {
					f := newFinalizationFixture(t, "op-operator")
					f.setConfig("publication", map[string]any{"integration_flow": flow})
					branch, dir := f.expectedTarget("op-operator")
					if repoRoot {
						dir = taskWorkflowRepoRoot
					}
					f.completion("op-operator", branch, dir, "feat: reviewed implementation", "Keep original review evidence.", taskstate.RunStatusSucceeded)
					f.run("a\nb\n", "task", "run", "op-operator")
					before, _ := f.loadFinalTask("op-operator")
					require.Len(t, before.Reviews, 1)
					reviewed := f.inspectWorkflow("op-operator")
					assert.Equal(t, "output-1", reviewed.Revision.Code.Commit)
					actual := "operator-" + kind
					content := "operator " + kind + " output\n"
					relation := "amended or rewritten history"
					if kind == "added" {
						relation = "additional commits after review"
					}
					if kind == "amended" {
						content = f.candidate.contents
					}
					selectOperatorCommit(f, dir, actual, content, relation)
					registered := f.loadFinalRegistry()
					registered.Repos[0].DefaultBranch = "other-default"
					require.NoError(t, f.registryStore.Save(registered))
					_, notice := f.run("b\n", "task", "run", "op-operator")
					for _, fact := range []string{"NOTICE", "output-1", actual, relation, "review gap"} {
						assert.Contains(t, notice, fact)
					}
					assert.NotContains(t, notice, "d=destination")
					pending := f.inspectWorkflow("op-operator")
					require.NotNil(t, pending.Pending)
					assert.Equal(t, work.InputOperator, pending.Publication.Input.Provenance)
					assert.Equal(t, actual, pending.Publication.Input.Code.Commit)
					assert.Equal(t, f.candidate.trees[actual], pending.Publication.Input.Code.Tree)
					assert.Equal(t, "main", pending.Publication.Destination.Branch)
					assert.Equal(t, reviewed.Publication.Review, pending.Publication.Review)
					f.assertUnpublished("op-operator")
					if flow == "direct-merge" && repoRoot && kind == "rewritten" {
						f.publication.pushError = errors.New("destination disconnected")
						_, _, err := f.runError("p\n", "task", "run", "op-operator")
						require.ErrorIs(t, err, f.publication.pushError)
						accepted, _ := f.loadFinalTask("op-operator")
						grant := *accepted.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase).Consent
						f.publication.pushError = nil
						f.run("", "task", "run", "op-operator")
						recovered, _ := f.loadFinalTask("op-operator")
						assert.Equal(t, grant, *recovered.Work.Workflows[0].Phases[pending.Publication.Plan.Phase].(*work.PublicationPhase).Consent)
					} else {
						f.run("p\n", "task", "run", "op-operator")
					}
					final, item := f.loadFinalTask("op-operator")
					assert.Equal(t, before.Reviews, final.Reviews, "operator input must not replay or fabricate review")
					assert.Equal(t, before.Runs, final.Runs, "operator commits must not manufacture an agent effort or completion")
					assert.Len(t, f.candidate.commits, 1, "only the original implementation was committed by Orpheus")
					plan := final.Work.Workflows[0].Phases[pending.Publication.Plan.Phase].(*work.PublicationPhase)
					require.NotNil(t, plan.Consent)
					assert.Equal(t, actual, plan.Plan.Input.Code.Commit)
					assert.Equal(t, "output-1", final.Runs[0].Completion.Commit)
					if flow == "pull-request" {
						assert.Equal(t, "public-"+actual, taskstate.FinalizationFacts(final).Commit)
					} else {
						assert.Equal(t, actual, taskstate.FinalizationFacts(final).Commit)
					}
					if flow == "pull-request" {
						assert.Equal(t, taskmodel.StatusInProgress, item.Status)
						assert.Equal(t, "public-"+actual, f.publication.remote[branch])
						assert.Equal(t, "main", f.pr.created[0].BaseBranch)
						assert.Equal(t, f.candidate.trees[actual], plan.Delivery.(*work.PullRequestDelivery).Tree)
					} else {
						assert.Equal(t, taskmodel.StatusClosed, item.Status)
						assert.Equal(t, "merge-commit", f.publication.remote["main"])
						assert.Equal(t, f.candidate.trees[actual], plan.Delivery.(*work.DirectDelivery).Destination.Tree)
						assert.Empty(t, f.pr.created)
					}
				})
			}
		}
	}
}

func TestIntegrationWorkflowPublicationInputChangingBetweenConsentAndDeliveryNeedsNewDecision(t *testing.T) {
	for _, interruption := range []string{"before push", "after push", "after PR creation"} {
		t.Run(interruption, func(t *testing.T) {
			f := newFinalizationFixture(t, "op-race")
			branch, dir := f.expectedTarget("op-race")
			f.completion("op-race", branch, dir, "feat: review once", "Bind each consent to actual input.", taskstate.RunStatusSucceeded)
			if interruption == "before push" {
				var diagnostics bytes.Buffer
				writer := &publicationConsentMutation{Writer: &diagnostics, mutate: func() {
					selectOperatorCommit(f, dir, "operator-race", "operator race output\n", "amended or rewritten history")
				}}
				err := f.runWithWriters("a\np\n", io.Discard, writer, "task", "run", "op-race")
				require.ErrorContains(t, err, "not at the reviewed commit")
				f.assertUnpublished("op-race")
			} else {
				if interruption == "after push" {
					f.publication.losePush = true
				} else {
					f.pr.statusError = errors.New("lost PR observation")
				}
				_, _, err := f.runError("a\np\n", "task", "run", "op-race")
				require.ErrorContains(t, err, "observation")
				f.publication.losePush = false
				f.publication.observeError = nil
				f.pr.statusError = nil
				selectOperatorCommit(f, dir, "operator-race", "operator race output\n", "amended or rewritten history")
			}
			accepted, _ := f.loadFinalTask("op-race")
			old := accepted.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
			require.NotNil(t, old.Consent)
			assert.Equal(t, "output-1", old.Plan.Input.Code.Commit)
			f.run("b\n", "task", "run", "op-race")
			pending := f.inspectWorkflow("op-race")
			require.NotNil(t, pending.Pending)
			assert.Equal(t, "operator-race", pending.Publication.Input.Code.Commit)
			assert.False(t, pending.Publication.Consented)
			retained, _ := f.loadFinalTask("op-race")
			previous := retained.Work.Workflows[0].Phases[len(accepted.Work.Workflows[0].Phases)-1].(*work.PublicationPhase)
			assert.Equal(t, old.Plan, previous.Plan)
			assert.Equal(t, old.Consent, previous.Consent)
			assert.Equal(t, old.Operation, previous.Operation)
			if interruption == "before push" {
				assert.Equal(t, old, previous)
			} else {
				require.NotNil(t, previous.Delivery)
				assert.Equal(t, "public-output-1", previous.Delivery.(*work.PullRequestDelivery).Head)
				assert.Equal(t, "public-output-1", pending.Publication.ExpectedHead)
				assert.Equal(t, "public-output-1", pending.Published.PublicCommit)
				assert.Equal(t, "public-output-1", f.publication.remote[branch], "abort leaves newly selected input unpublished")
				assert.Len(t, f.pr.created, 1, "reconcile the old effect before replacing its plan")
			}
			f.run("p\n", "task", "run", "op-race")
			f.assertPublishedPR("op-race")
			assert.Equal(t, "public-operator-race", f.publication.remote[branch])
			assert.Len(t, f.pr.created, 1, "new input updates the same PR instead of creating another")
			final, _ := f.loadFinalTask("op-race")
			assert.Len(t, final.Reviews, 1)
			assert.Len(t, final.Runs, 1)
		})
	}
}

type publicationConsentMutation struct {
	io.Writer
	mutate func()
}

func (w *publicationConsentMutation) Write(p []byte) (int, error) {
	if w.mutate != nil && strings.Contains(string(p), "Recorded for this unchanged plan") {
		w.mutate()
		w.mutate = nil
	}
	return w.Writer.Write(p)
}

func TestIntegrationWorkflowReviewedPRUpdateRetainsPublicAndLocalRevisionMappings(t *testing.T) {
	for _, inputKind := range []string{"reviewed", "amended", "rewritten"} {
		for _, continuation := range []string{"unchanged input", "new selected input"} {
			t.Run(inputKind+"/"+continuation, func(t *testing.T) {
				f := newFeatureFinalizationFixture(t, "op-update", false)
				f.run("", "task", "done", "op-update")
				initial, _ := f.loadFinalTask("op-update")
				first := initial.Work.Workflows[0].Phases[3].(*work.PublicationPhase)
				prior := *first.Delivery.(*work.PullRequestDelivery)
				branch, dir := f.expectedTarget("op-update")
				f.candidate.contents = "new reviewed output\n"
				// Arrange a subsequent controller handoff; routing external findings into
				// implementation is outside publication's scope. Real capture/review follows.
				f.completion("op-update", branch, dir, "fix: narrower repair rationale", "Do not replace the feature narrative.", taskstate.RunStatusSucceeded)
				f.run("a\nb\n", "task", "run", "op-update")
				pending := f.inspectWorkflow("op-update")
				assert.Equal(t, "update-pr", pending.Publication.Operation)
				selectedPlan, _ := f.loadFinalTask("op-update")
				assert.Equal(t, &work.OutputRef{Phase: 1}, selectedPlan.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase).Plan.FeatureCompletion)
				assert.Equal(t, prior.Head, pending.Publication.ExpectedHead)
				assert.Equal(t, prior.PublicCommit, pending.Published.PublicCommit)
				assert.Contains(t, pending.Publication.Content, "Ready for PR")
				assert.NotContains(t, strings.Split(pending.Publication.Content, "\n\n")[0], "narrower repair")
				assert.Equal(t, "output-2", pending.Revision.Code.Commit)
				before, _ := f.loadFinalTask("op-update")
				assert.Empty(t, taskstate.FinalizationFacts(before).Commit, "older delivery does not mark newer output pushed")
				if inputKind != "reviewed" {
					content := "rewritten operator output\n"
					if inputKind == "amended" {
						content = f.candidate.contents
					}
					selectOperatorCommit(f, dir, "operator-"+inputKind, content, "amended or rewritten history")
					f.run("b\n", "task", "run", "op-update")
				}
				selected := f.inspectWorkflow("op-update")
				actual := selected.Publication.Input.Code
				f.publication.losePush = true
				_, _, err := f.runError("p\n", "task", "run", "op-update")
				require.ErrorContains(t, err, "lost push observation")
				interrupted, _ := f.loadFinalTask("op-update")
				op := interrupted.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				require.NotNil(t, op.Operation.Public)
				assert.Nil(t, op.Delivery)
				grant := *op.Consent
				assert.Equal(t, prior.Head, op.Operation.Parent)
				assert.Equal(t, "public-"+actual.Commit, f.publication.remote[branch])
				assert.Len(t, f.pr.created, 1)
				f.publication.losePush = false
				f.publication.observeError = nil
				f.candidate.destinationHeads = map[string]string{"main": "destination-advanced-after-push"}
				oldActual := actual
				if continuation == "new selected input" {
					selectOperatorCommit(f, dir, "next-operator-"+inputKind, "next operator input\n", "amended or rewritten history")
					_, notice := f.run("b\n", "task", "run", "op-update")
					assert.Contains(t, notice, "next-operator-"+inputKind)
					replacement := f.inspectWorkflow("op-update")
					require.NotNil(t, replacement.Pending)
					assert.Equal(t, "public-"+oldActual.Commit, replacement.Publication.ExpectedHead)
					assert.Equal(t, "public-"+oldActual.Commit, replacement.Published.PublicCommit)
					reconciled, _ := f.loadFinalTask("op-update")
					previous := reconciled.Work.Workflows[0].Phases[selected.Publication.Plan.Phase].(*work.PublicationPhase)
					require.NotNil(t, previous.Delivery)
					assert.Equal(t, grant, *previous.Consent)
					assert.Equal(t, op.Operation, previous.Operation)
					assert.Equal(t, oldActual, previous.Plan.Input.Code)
					assert.Equal(t, "public-"+oldActual.Commit, f.publication.remote[branch], "abort must not publish newly selected input")
					actual = replacement.Publication.Input.Code
					f.run("p\n", "task", "run", "op-update")
				} else {
					f.run("", "task", "run", "op-update")
				}
				final, _ := f.loadFinalTask("op-update")
				delivered := final.Work.Workflows[0].Phases[selected.Publication.Plan.Phase].(*work.PublicationPhase)
				assert.Equal(t, grant, *delivered.Consent, "unchanged interrupted delivery retains consent despite destination advancement")
				assert.Equal(t, first, final.Work.Workflows[0].Phases[3], "earlier publication remains inspectable")
				update := delivered.Delivery.(*work.PullRequestDelivery)
				assert.Equal(t, prior.PullRequest, update.PullRequest)
				assert.Equal(t, prior.Head, update.PreviousHead)
				assert.Equal(t, "public-"+oldActual.Commit, update.PublicCommit)
				assert.Equal(t, oldActual.Tree, update.Tree)
				if continuation == "new selected input" {
					latest := f.inspectWorkflow("op-update")
					published := final.Work.Workflows[0].Phases[latest.Publication.Plan.Phase].(*work.PublicationPhase)
					assert.NotEqual(t, grant, *published.Consent)
					assert.Equal(t, "public-"+oldActual.Commit, published.Operation.Parent)
					assert.Equal(t, "public-"+actual.Commit, published.Delivery.(*work.PullRequestDelivery).PublicCommit)
					assert.Equal(t, actual.Tree, published.Delivery.(*work.PullRequestDelivery).Tree)
				}
				assert.Equal(t, "output-2", final.Runs[1].Completion.Commit)
				assert.Equal(t, before.Reviews, final.Reviews, "operator input is never reported as reviewed")
				assert.Len(t, f.pr.created, 1)
				assert.Equal(t, "Ready for PR", f.pr.existing.Title)
				if inputKind != "reviewed" {
					assert.Contains(t, f.pr.existing.Body, "Publication review gap")
					assert.Contains(t, f.pr.existing.Body, actual.Commit)
				}
			})
		}
	}
}

func TestIntegrationWorkflowPRUpdateRefusesRemoteAndLifecycleChangesBeforePackaging(t *testing.T) {
	for _, change := range []string{"remote head", "merged", "closed", "destination"} {
		t.Run(change, func(t *testing.T) {
			f := newFeatureFinalizationFixture(t, "op-safe", false)
			f.run("", "task", "done", "op-safe")
			branch, dir := f.expectedTarget("op-safe")
			f.candidate.contents = "reviewed update\n"
			f.completion("op-safe", branch, dir, "fix: update", "Update existing PR.", taskstate.RunStatusSucceeded)
			f.run("a\nb\n", "task", "run", "op-safe")
			prior := f.publication.remote[branch]
			switch change {
			case "remote head":
				f.publication.remote[branch] = "unexpected-remote"
			case "merged":
				f.pr.state = "merged"
			case "closed":
				f.pr.state = "closed"
			case "destination":
				f.pr.existing.BaseBranch = "other"
			}
			_, _, err := f.runError("p\n", "task", "run", "op-safe")
			require.ErrorContains(t, err, "reconcile")
			saved, _ := f.loadFinalTask("op-safe")
			p := saved.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
			assert.Nil(t, p.Delivery)
			assert.Nil(t, p.Operation.Public, "provider and remote checks precede commit effects")
			assert.Len(t, f.publication.packages, 1, "only the initial public commit exists")
			assert.Len(t, f.pr.created, 1)
			assert.Empty(t, f.tasks.closed)
			if change == "remote head" {
				assert.Equal(t, "unexpected-remote", f.publication.remote[branch])
			} else {
				assert.Equal(t, prior, f.publication.remote[branch])
			}
		})
	}
}

func TestIntegrationWorkflowPackagingTreeChangesRequireActualInputAndRenewedConsent(t *testing.T) {
	f := newFeatureFinalizationFixture(t, "op-hook", false)
	f.publication.packageTree = "hook-tree"
	_, _, err := f.runError("", "task", "done", "op-hook")
	require.ErrorContains(t, err, "packaging changed authorized tree")
	saved, _ := f.loadFinalTask("op-hook")
	p := saved.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
	assert.Equal(t, "hook-tree", p.Operation.Public.Tree)
	assert.Nil(t, p.Delivery)
	assert.Empty(t, f.publication.remote)
	assert.Empty(t, f.pr.created)
	grant := *p.Consent
	// The adapter retained the hook-produced commit. An operator can select it;
	// no automatic checkout rewrite or fabricated review occurs.
	_, dir := f.expectedTarget("op-hook")
	selectOperatorCommit(f, dir, "hook-committed-input", "actual hook output\n", "amended or rewritten history")
	f.publication.packageTree = ""
	f.run("b\n", "task", "run", "op-hook")
	pending := f.inspectWorkflow("op-hook")
	require.NotNil(t, pending.Pending)
	assert.Equal(t, "hook-committed-input", pending.Publication.Input.Code.Commit)
	assert.Equal(t, work.InputOperator, pending.Publication.Input.Provenance)
	f.run("p\n", "task", "run", "op-hook")
	final, _ := f.loadFinalTask("op-hook")
	assert.Equal(t, grant, *final.Work.Workflows[0].Phases[3].(*work.PublicationPhase).Consent)
	assert.Len(t, final.Reviews, 1)
	assert.Contains(t, f.pr.existing.Body, "not reported as reviewed")
}
