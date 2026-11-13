//go:build integration

package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/cli"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowPublicationFlowEditsAndInterruptedConsentNeverDeliverAnotherIntent(t *testing.T) {
	f := newFinalizationFixture(t, "op-plan")
	branch, dir := f.expectedTarget("op-plan")
	f.completion("op-plan", branch, dir, "feat: reviewed intent", "Retain the reviewed implementation.", taskstate.RunStatusSucceeded)
	f.run("a\nq\n", "task", "run", "op-plan")
	original := f.inspectWorkflow("op-plan")
	require.NotNil(t, original.Pending)
	assert.Equal(t, "create-pr", original.Publication.Operation)
	assert.False(t, original.Publication.PRDraft, "unconfigured publication is ready for review")
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
	f.run("i\ndirect-merge\ni\npull-request\nd\nq\n", "task", "run", "op-plan")
	edited := f.inspectWorkflow("op-plan")
	require.NotNil(t, edited.Pending)
	assert.NotEqual(t, original.Pending.ID.Owner, edited.Pending.ID.Owner)
	assert.Equal(t, original.Publication.Review, edited.Publication.Review)
	assert.Equal(t, original.Revision, edited.Revision)
	assert.Equal(t, original.PublicationContent, edited.PublicationContent)
	assert.False(t, edited.Publication.Consented)
	assert.True(t, edited.Publication.PRDraft, "explicit draft selection survives pause")
	f.run("i\npull-request\nq\n", "task", "run", "op-plan")
	assert.Equal(t, edited.Pending.ID, f.inspectWorkflow("op-plan").Pending.ID, "unchanged intent does not create a new operation")

	before, _ := f.loadFinalTask("op-plan")
	f.candidate.destinationHeads = map[string]string{"main": "advanced-destination"}
	_, notice := f.run("b\n", "task", "run", "op-plan")
	assert.Contains(t, notice, "advanced-destination")
	refreshed := f.inspectWorkflow("op-plan")
	assert.Equal(t, "advanced-destination", refreshed.Publication.Destination.Commit)
	assert.True(t, refreshed.Publication.PRDraft, "selection survives refreshed input context")
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
	assert.True(t, authorized.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase).Target.(*work.PullRequestTarget).Draft)
	assert.Nil(t, authorized.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase).Delivery)
	assert.Len(t, authorized.Reviews, 1, "flow edits do not repeat review")
	require.Len(t, f.pr.created, 1)
	assert.True(t, f.pr.created[0].Draft)
	assert.Empty(t, f.tasks.closed)
	f.pr.statusError = nil
	for _, lifecycle := range []pullrequest.State{pullrequest.StateClosed, pullrequest.StateMerged} {
		f.pr.state = lifecycle
		_, _, err = f.runError("", "task", "run", "op-plan")
		require.ErrorContains(t, err, "reconcile lifecycle")
		assert.Len(t, f.pr.created, 1, "a recovered closed/merged PR must not produce duplicate creation")
	}
	f.pr.state = pullrequest.StateOpen
	f.pr.crossRepository = true
	_, _, err = f.runError("", "task", "run", "op-plan")
	require.ErrorContains(t, err, "repository, destination")
	assert.Len(t, f.pr.created, 1, "a matching fork head is not this operation's owned PR")
	f.pr.crossRepository = false
	f.pr.existing.BaseBranch = "retargeted"
	_, _, err = f.runError("", "task", "run", "op-plan")
	require.ErrorContains(t, err, "destination, branch or public head changed")
	assert.Len(t, f.pr.created, 1, "retargeted PR is not absence")
	f.pr.existing.BaseBranch = "main"
	f.setConfig("publication", map[string]any{"integration_flow": "direct-merge", "title_template": "Changed {{summary}}", "pr_draft": false})
	f.pipelines("new-policy", map[string][]map[string]any{"new-policy": {{"kind": "manual", "name": "new-earlier-requirement"}}})
	f.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return false }
	f.run("", "task", "run", "op-plan")
	f.assertPublishedPR("op-plan")
	final, _ := f.loadFinalTask("op-plan")
	assert.Equal(t, grant, final.Work.Workflows[0].Phases[refreshed.Publication.Phase.Phase].(*work.PublicationPhase).Consent)
	assert.Len(t, final.Reviews, 1, "configuration edits do not retrospectively invalidate reviewed evidence")
	assert.Equal(t, authorized.Reviews, final.Reviews, "configuration edits preserve actual review results and findings")
	assert.Equal(t, authorized.Work.Workflows[0].ReviewContracts, final.Work.Workflows[0].ReviewContracts, "delivery does not reconcile earlier review requirements")
	assert.Len(t, f.pr.created, 1, "retry observes the existing PR instead of creating another")
	assert.Equal(t, "feat: reviewed intent", f.pr.created[0].Title)
	assert.True(t, f.inspectWorkflow("op-plan").Publication.PRDraft)

	// A confirmed restart preserves this delivered workflow without lending its
	// review pass or publication consent to the new work, or touching the PR.
	f.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return true }
	remoteHead := f.publication.remote[branch]
	f.run("y\n", "task", "reset", "op-plan")
	restarted, _ := f.loadFinalTask("op-plan")
	require.Len(t, restarted.Work.Workflows, 2)
	assert.Equal(t, final.Work.Workflows[0], restarted.Work.Workflows[0])
	assert.NotNil(t, restarted.Work.ActiveWorkflow().ResetRequestedAt)
	assert.Empty(t, restarted.Work.ActiveWorkflow().Phases)
	assert.Nil(t, restarted.Work.ActiveWorkflow().Publication)
	assert.Empty(t, restarted.Work.ActiveWorkflow().ReviewContracts)
	assert.Nil(t, restarted.Finalization)
	_, hasReview := taskstate.LatestReview(restarted)
	assert.False(t, hasReview)
	assert.Len(t, f.pr.created, 1)
	assert.Equal(t, remoteHead, f.publication.remote[branch])
	assert.Empty(t, f.tasks.closed)

	// Preparation retains recorded public output before moving the task branch;
	// released private revision refs are not reconstructed from historical SHAs.
	stopped := errors.New("stop reset after branch move")
	f.configureImplementer("restart", agent.Profile{Command: "unused-implementer"})
	f.git.resetApplyError = stopped
	f.git.startingCommit, f.git.trees["restart-default"] = "restart-default", "restart-tree"
	_, _, err = f.runError("", "task", "run", "op-plan")
	require.ErrorIs(t, err, stopped)
	require.Len(t, f.git.resetApplications, 1)
	assert.Contains(t, f.git.resetApplications[0].RetainCommits, final.Finalization.Commit)
	assert.NotContains(t, f.git.resetApplications[0].RetainCommits, "output-1")
	assert.Equal(t, remoteHead, f.publication.remote[branch])
	assert.Len(t, f.pr.created, 1)
}

func TestIntegrationWorkflowPublicationPromptPausesOnEOFAndContinuesDeliveryWithRedirectedOutput(t *testing.T) {
	f := newFinalizationFixture(t, "op-prompt")
	branch, dir := f.expectedTarget("op-prompt")
	f.completion("op-prompt", branch, dir, "feat: terminal decision", "Separate assessment and delivery.", taskstate.RunStatusSucceeded)
	f.options.Dependencies.Terminal = cli.TerminalCapabilities{InputIsTerminal: func(io.Reader) bool { return true }, OutputIsTerminal: func(io.Writer) bool { return false }}
	f.setConfig("publication", map[string]any{"pr_draft": true})
	_, output := f.run("a\nd\nauthorize", "task", "run", "op-prompt")
	for _, fact := range []string{"PUBLICATION", "publication-decision (manual)", "PUBLICATION COMMIT", "feat: terminal decision", "FLOW", "DESTINATION", "EFFECTS", "PR MODE", "ready for review", "d=change draft/ready", "Publication decision"} {
		assert.Contains(t, output, fact)
	}
	assert.NotContains(t, output, "\x1b")
	paused := f.inspectWorkflow("op-prompt")
	require.NotNil(t, paused.Pending, "an unterminated answer at EOF is not consent")
	assert.False(t, paused.Publication.PRDraft, "explicit ready selection overrides global draft default")
	f.assertUnpublished("op-prompt")

	_, output = f.run("i\ndirect-merge\nq\n", "task", "run", "op-prompt")
	assert.Contains(t, output, "Push destination")
	// The last displayed decision belongs to direct merge, which has no PR mode editor.
	require.Contains(t, output, "Publication decision")
	directPrompt := output[strings.LastIndex(output, "Publication decision"):]
	assert.NotContains(t, directPrompt, "draft")
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
	assert.Equal(t, taskmodel.StatusInProgress, item.Status, "authorization delivers but source closure waits for sync")
	assert.Nil(t, taskstate.FinalizationFacts(final).ClosedAt)
	assert.NotNil(t, taskstate.FinalizationFacts(final).PushedAt, "failed result output cannot erase verified delivery")
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
	f.publication.taskHead = commit
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
						assert.Equal(t, grant, *recovered.Work.Workflows[0].Phases[pending.Publication.Phase.Phase].(*work.PublicationPhase).Consent)
					} else {
						f.run("p\n", "task", "run", "op-operator")
					}
					final, item := f.loadFinalTask("op-operator")
					assert.Equal(t, before.Reviews, final.Reviews, "operator input must not replay or fabricate review")
					assert.Equal(t, before.Runs, final.Runs, "operator commits must not manufacture an agent effort or completion")
					assert.Len(t, f.candidate.commits, 1, "only the original implementation was committed by Orpheus")
					plan := final.Work.Workflows[0].Phases[pending.Publication.Phase.Phase].(*work.PublicationPhase)
					require.NotNil(t, plan.Consent)
					assert.Equal(t, actual, plan.Input.Code.Commit)
					assert.Equal(t, "output-1", final.Runs[0].Completion.Commit)
					assert.Equal(t, "public-"+actual, taskstate.FinalizationFacts(final).Commit)
					if flow == "pull-request" {
						assert.Equal(t, taskmodel.StatusInProgress, item.Status)
						assert.Equal(t, "public-"+actual, f.publication.remote[branch])
						assert.Equal(t, "main", f.pr.created[0].BaseBranch)
						assert.Equal(t, f.candidate.trees[actual], plan.Delivery.(*work.PullRequestDelivery).Tree)
					} else {
						assert.Equal(t, taskmodel.StatusInProgress, item.Status)
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
	f := newFinalizationFixture(t, "op-race")
	branch, dir := f.expectedTarget("op-race")
	f.completion("op-race", branch, dir, "feat: review once", "Bind each consent to actual input.", taskstate.RunStatusSucceeded)
	var diagnostics bytes.Buffer
	writer := &publicationConsentMutation{Writer: &diagnostics, mutate: func() {
		selectOperatorCommit(f, dir, "operator-race", "operator race output\n", "amended or rewritten history")
	}}
	err := f.runWithWriters("a\np\n", io.Discard, writer, "task", "run", "op-race")
	require.ErrorContains(t, err, "not at the reviewed commit")
	f.assertUnpublished("op-race")
	accepted, _ := f.loadFinalTask("op-race")
	old := accepted.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
	require.NotNil(t, old.Consent)
	assert.Equal(t, "output-1", old.Input.Code.Commit)
	f.run("b\n", "task", "run", "op-race")
	pending := f.inspectWorkflow("op-race")
	require.NotNil(t, pending.Pending)
	assert.Equal(t, "operator-race", pending.Publication.Input.Code.Commit)
	assert.False(t, pending.Publication.Consented)
	retained, _ := f.loadFinalTask("op-race")
	assert.Equal(t, old, retained.Work.Workflows[0].Phases[len(accepted.Work.Workflows[0].Phases)-1])
	f.run("p\n", "task", "run", "op-race")
	f.assertPublishedPR("op-race")
	assert.Equal(t, "public-operator-race", f.publication.remote[branch])
	final, _ := f.loadFinalTask("op-race")
	assert.Len(t, final.Reviews, 1)
	assert.Len(t, final.Runs, 1)
}

type publicationConsentMutation struct {
	trigger string
	io.Writer
	mutate func()
}

func (w *publicationConsentMutation) Write(p []byte) (int, error) {
	trigger := w.trigger
	if trigger == "" {
		trigger = "Recorded for this unchanged plan"
	}
	if w.mutate != nil && strings.Contains(string(p), trigger) {
		w.mutate()
		w.mutate = nil
	}
	return w.Writer.Write(p)
}

func TestIntegrationWorkflowInitialPublicationRefusesUnrelatedRemoteHistory(t *testing.T) {
	for _, remoteChange := range []string{"branch exists", "PR appears after push"} {
		t.Run(remoteChange, func(t *testing.T) {
			f := newFeatureFinalizationFixture(t, "op-safe", false)
			branch, _ := f.expectedTarget("op-safe")
			if remoteChange == "branch exists" {
				f.publication.remote[branch] = "unexpected-remote"
			} else {
				f.publication.beforePush = func(gitmeta.PublicCommitRequest) {
					f.pr.existing = &pullrequest.CreateRequest{RepositoryPath: taskWorkflowRepoRoot, HeadBranch: branch, BaseBranch: "main", Title: "Unrelated PR", Body: "Not created by this operation"}
				}
			}
			_, _, err := f.runError("", "task", "done", "op-safe")
			require.Error(t, err)
			if remoteChange == "branch exists" {
				require.ErrorContains(t, err, "unexpected public head")
				assert.Equal(t, "unexpected-remote", f.publication.remote[branch])
				assert.Empty(t, f.publication.packages)
				assert.Equal(t, "output-1", f.candidate.head)
			} else {
				require.ErrorContains(t, err, "without this operation's creation intent")
				saved, _ := f.loadFinalTask("op-safe")
				p := saved.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				assert.Nil(t, p.Operation.PRCreationStartedAt)
				assert.Nil(t, p.Delivery)
				_, _, err = f.runError("", "task", "done", "op-safe")
				require.ErrorContains(t, err, "cannot import")
				assert.Equal(t, "Unrelated PR", f.pr.existing.Title)
			}
			assert.Empty(t, f.pr.created)
			assert.Empty(t, f.tasks.closed)
		})
	}
}

func TestIntegrationWorkflowPackagingTreeChangesRequireActualInputAndRenewedConsent(t *testing.T) {
	f := newFeatureFinalizationFixture(t, "op-hook", false)
	actualContents := "actual hook output\n"
	f.publication.packageTree = fmt.Sprintf("tree-%x", sha256.Sum256([]byte(actualContents)))
	f.publication.packageContents = actualContents
	_, _, err := f.runError("", "task", "done", "op-hook")
	require.ErrorContains(t, err, "packaging changed authorized tree")
	saved, _ := f.loadFinalTask("op-hook")
	p := saved.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
	assert.Equal(t, f.publication.packageTree, p.Operation.Public.Tree)
	assert.Equal(t, p.Operation.Public.Commit, f.candidate.head, "actual hook commit stays in the task checkout")
	assert.Nil(t, p.Delivery)
	assert.Empty(t, f.publication.remote)
	assert.Empty(t, f.pr.created)
	grant := *p.Consent
	f.publication.packageTree, f.publication.packageContents = "", ""
	f.run("b\n", "task", "run", "op-hook")
	pending := f.inspectWorkflow("op-hook")
	require.NotNil(t, pending.Pending)
	assert.Equal(t, "public-output-1", pending.Publication.Input.Code.Commit)
	assert.Equal(t, work.InputOperator, pending.Publication.Input.Provenance)
	f.run("p\n", "task", "run", "op-hook")
	final, _ := f.loadFinalTask("op-hook")
	assert.Equal(t, grant, *final.Work.Workflows[0].Phases[3].(*work.PublicationPhase).Consent)
	assert.Len(t, final.Reviews, 1)
	assert.Contains(t, f.pr.existing.Body, "not reported as reviewed")
	assert.Equal(t, "public-public-output-1", f.candidate.head)
}

func TestIntegrationWorkflowInPlacePublicationOrdersPersistenceRecoveryAndPrivateRefRelease(t *testing.T) {
	for _, failure := range []string{"intent save", "packaging", "public commit save", "delivery save", "private ref cleanup", "cleanup record save"} {
		t.Run(failure, func(t *testing.T) {
			f := newFeatureFinalizationFixture(t, "op-inplace", false)
			branch, dir := f.expectedTarget("op-inplace")
			initial, _ := f.loadFinalTask("op-inplace")
			original := initial.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
			grant := *original.Consent
			persistenceFailure := errors.New("injected publication persistence failure")
			if failure == "packaging" {
				f.publication.packageError = errors.New("ordinary commit failed")
			}
			if failure == "private ref cleanup" {
				f.publication.releaseError = errors.New("private ref deletion unavailable")
			}
			require.NoError(t, state.SetMemoryDataWriteError(f.paths, func(rel string, data []byte) error {
				if !strings.HasSuffix(rel, "/op-inplace.yaml") {
					return nil
				}
				snapshot, err := workstate.Decode(data)
				if err != nil {
					return nil
				}
				p, ok := snapshot.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				if !ok {
					return nil
				}
				if failure == "intent save" && p.Operation != nil && p.Operation.Public == nil {
					return persistenceFailure
				}
				if failure == "public commit save" && p.Operation != nil && p.Operation.Public != nil && p.Delivery == nil {
					return persistenceFailure
				}
				if failure == "cleanup record save" && p.PrivateRefsReleasedAt != nil {
					return persistenceFailure
				}
				if failure == "delivery save" && p.Delivery != nil {
					return persistenceFailure
				}
				return nil
			}))
			f.publication.beforePackage = func(req gitmeta.PublicCommitRequest) {
				persisted, _ := f.loadFinalTask("op-inplace")
				p := persisted.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				require.NotNil(t, p.Operation, "intent must be durable before branch rewriting")
				assert.Nil(t, p.Operation.Public)
				assert.Equal(t, req.Input.Commit, f.candidate.retained[req.Ref+"/input"].Commit)
			}
			f.publication.beforePush = func(req gitmeta.PublicCommitRequest) {
				persisted, _ := f.loadFinalTask("op-inplace")
				p := persisted.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				require.NotNil(t, p.Operation.Public, "public commit must be durable before push")
				assert.Equal(t, f.candidate.head, p.Operation.Public.Commit)
				assert.Nil(t, p.Delivery)
				assert.Nil(t, p.PrivateRefsReleasedAt)
				assert.Contains(t, f.candidate.retained, req.Ref+"/input")
			}
			f.pr.beforeCreate = func() {
				persisted, _ := f.loadFinalTask("op-inplace")
				p := persisted.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				require.NotNil(t, p.Operation.Public)
				require.NotNil(t, p.Operation.PRCreationStartedAt, "creation intent must be durable before the provider write")
			}
			f.publication.beforeRelease = func() {
				persisted, _ := f.loadFinalTask("op-inplace")
				p := persisted.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
				require.NotNil(t, p.Delivery, "verified delivery must be durable before private ref deletion")
				assert.Equal(t, grant, *p.Consent)
			}
			_, _, err := f.runError("", "task", "done", "op-inplace")
			require.Error(t, err)
			interrupted, _ := f.loadFinalTask("op-inplace")
			p := interrupted.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
			assert.Equal(t, grant, *p.Consent)
			assert.Nil(t, p.PrivateRefsReleasedAt)
			if failure == "cleanup record save" {
				assert.NotNil(t, p.Delivery)
			} else {
				assert.NotEmpty(t, f.candidate.retained, "incomplete delivery must not release private history")
			}
			if failure == "intent save" {
				assert.Equal(t, "output-1", f.candidate.head)
				assert.Zero(t, f.publication.packageCalls)
			}
			require.NoError(t, state.SetMemoryDataWriteError(f.paths, nil))
			f.publication.packageError, f.publication.releaseError = nil, nil
			requiresRecovery := failure == "packaging" || failure == "public commit save"
			if requiresRecovery {
				pending := f.inspectWorkflow("op-inplace")
				require.NotNil(t, pending.Pending)
				assert.Equal(t, workflow.InteractionPublicationRecovery, pending.Pending.ID.Kind)
				beforeHead, beforeContents := f.candidate.head, f.candidate.contents
				_, notice := f.run("m\n", "task", "run", "op-inplace")
				assert.Contains(t, notice, "Saved input: output-1")
				assert.Contains(t, notice, "Discard checkout and index changes")
				assert.Zero(t, f.publication.restores)
				assert.Equal(t, beforeHead, f.candidate.head)
				assert.Equal(t, beforeContents, f.candidate.contents)
				f.run("r", "task", "run", "op-inplace")
				assert.Zero(t, f.publication.restores, "partial EOF is not reset consent")
				f.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return false }
				piped := &countedDecisionInput{Reader: strings.NewReader("r\n")}
				command := cli.NewRootCommandWithOptions(f.options)
				command.SetIn(piped)
				command.SetOut(io.Discard)
				command.SetErr(io.Discard)
				command.SetArgs([]string{"task", "run", "op-inplace"})
				err = command.Execute()
				require.ErrorIs(t, err, review.ErrInteractionRequired)
				assert.Zero(t, piped.reads)
				assert.Zero(t, f.publication.restores)
				preserved, _ := f.loadFinalTask("op-inplace")
				assert.Equal(t, interrupted, preserved, "manual repair, EOF and noninteractive input preserve pending recovery")
				f.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return true }
				canceled, cancel := context.WithCancel(context.Background())
				var canceledOutput bytes.Buffer
				command = cli.NewRootCommandWithOptions(f.options)
				command.SetContext(canceled)
				command.SetIn(strings.NewReader("r\n"))
				command.SetOut(io.Discard)
				command.SetErr(&publicationConsentMutation{Writer: &canceledOutput, trigger: "Recovery [", mutate: cancel})
				command.SetArgs([]string{"task", "run", "op-inplace"})
				err = command.Execute()
				cancel()
				require.ErrorIs(t, err, context.Canceled)
				assert.Zero(t, f.publication.restores, "cancellation cannot confirm a reset")
				f.options.Dependencies.Terminal.InputIsTerminal = func(io.Reader) bool { return true }
				f.run("r\n", "task", "run", "op-inplace")
				assert.Equal(t, 1, f.publication.restores)
			} else {
				f.run("", "task", "done", "op-inplace")
			}
			final, _ := f.loadFinalTask("op-inplace")
			delivered := final.Work.Workflows[0].Phases[3].(*work.PublicationPhase)
			require.NotNil(t, delivered.Delivery)
			require.NotNil(t, delivered.PrivateRefsReleasedAt)
			assert.Equal(t, grant, *delivered.Consent)
			assert.Equal(t, "output-1", delivered.Input.Code.Commit)
			assert.Equal(t, "public-output-1", f.candidate.head)
			assert.Equal(t, f.candidate.head, f.publication.remote[branch])
			assert.Equal(t, initial.Runs, final.Runs)
			assert.Equal(t, initial.Reviews, final.Reviews)
			assert.Len(t, f.pr.created, 1)
			assert.Len(t, f.candidate.pushes, 1)
			expectedPackages := 1
			if requiresRecovery {
				expectedPackages = 2
			}
			assert.Equal(t, expectedPackages, f.publication.packageCalls)
			expectedReleases := 1
			if failure == "cleanup record save" {
				expectedReleases = 2
			}
			assert.Equal(t, expectedReleases, f.publication.releases)
			for ref := range f.candidate.retained {
				assert.NotContains(t, ref, "refs/orpheus/revisions/616c706861/6f702d696e706c616365/0/")
				assert.NotContains(t, ref, "refs/orpheus/publication-inputs/616c706861/6f702d696e706c616365/0/")
			}
			delete(f.candidate.trees, "output-1")
			inspection, _ := f.run("", "task", "show", "workflow", "op-inplace")
			assert.Contains(t, inspection, "output-1")
			assert.Contains(t, inspection, "public-output-1")
			f.run("", "task", "done", "op-inplace")
			f.run("", "task", "sync", "op-inplace")
			selectOperatorCommit(f, dir, "later-public-fix", "ordinary later public work\n", "additional commits after review")
			f.run("", "task", "run", "op-inplace")
			assert.Equal(t, "later-public-fix", f.candidate.head)
			assert.Equal(t, "public-output-1", f.publication.remote[branch], "later local commits are not implicitly delivered")
			assert.Equal(t, expectedPackages, f.publication.packageCalls, "recorded delivery and later public work must never re-squash")
			assert.Len(t, f.pr.created, 1)
		})
	}
}
