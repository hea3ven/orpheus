//go:build integration

package cli_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/implementation"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	implementationCommit = "output-1"
	selectedReviewCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestIntegrationWorkflowFreshReviewSelectsBranchTipAndPreparesPublication(t *testing.T) {
	f := reviewFixtureWithOperatorCommit(t)
	f.reviewers(semanticAgentOutcome{captureContext: true})
	f.git.diffStats = map[[2]string]string{{"initial-commit", selectedReviewCommit}: " operator-change.txt | 1 +"}
	f.pipelines("standard", map[string][]map[string]any{"standard": {
		{"kind": "agent_review", "name": "reviewer"},
		{"kind": "manual", "name": "inspect"},
	}})

	stdout, stderr := f.run("a\n", "task", "run", "op-selected")

	assert.Contains(t, stderr, "Implementation commit: "+implementationCommit)
	assert.Contains(t, stderr, "Selected review commit: "+selectedReviewCommit)
	assert.Contains(t, stderr, "\n  0123456 Extend coverage\n")
	assert.Contains(t, stderr, "\n  bbbbbbb Update operator behavior\n")
	assert.Contains(t, stderr, "operator-change.txt | 1 +")
	require.Len(t, f.agent.contexts, 1)
	assert.Contains(t, f.agent.contexts[0], "- Reviewed output: "+selectedReviewCommit)
	assert.Contains(t, f.agent.contexts[0], "Technical explanation.")
	assert.Contains(t, stdout, "publication awaits explicit authorization")
	state, item := f.loadFinalTask("op-selected")
	require.Len(t, state.Runs, 1)
	assert.Equal(t, implementationCommit, state.Runs[0].Completion.Commit)
	assert.Equal(t, "Document implementation only.", state.Runs[0].Completion.CommitBody)
	require.Len(t, state.Reviews, 1)
	assert.Equal(t, taskstate.ReviewStatusPassed, state.Reviews[0].Status)
	assertUnconsentedPublication(t, state)
	assert.Empty(t, item.OrpheusMetadata().PRURL)
	assert.Empty(t, f.candidate.pushes)
	plan := state.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
	assert.Equal(t, work.InputReviewed, plan.Input.Provenance)
	assert.Equal(t, selectedReviewCommit, plan.Input.Code.Commit)
	assert.Equal(t, selectedReviewCommit, f.candidate.retained[plan.Input.Code.RetainedRef].Commit)
	shown, _ := f.run("", "task", "show", "review", "op-selected", "1")
	assert.Contains(t, shown, "Full-task comparison: initial-commit -> "+selectedReviewCommit)
	view := f.inspectWorkflow("op-selected")
	require.NotNil(t, view.Revision)
	assert.Equal(t, selectedReviewCommit, view.Revision.Code.Commit)

	_, prompt := f.run("q\n", "task", "run", "op-selected")
	assert.Contains(t, prompt, "Publication decision [")
	assert.NotContains(t, prompt, "publication input differs from reviewed output")
	paused, _ := f.loadFinalTask("op-selected")
	assertUnconsentedPublication(t, paused)
	assert.Empty(t, f.candidate.pushes)
}

func TestIntegrationWorkflowFreshReviewReportsSelectionBeforeReservation(t *testing.T) {
	f := reviewFixtureWithOperatorCommit(t)
	f.pipelines("standard", map[string][]map[string]any{"standard": {
		{"kind": "check", "name": "check", "command": f.check("check", checkResult{})},
	}})
	before, _ := f.loadFinalTask("op-selected")

	err := f.runWithWriters("", io.Discard, interruptedSelectionOutput{}, "task", "run", "op-selected")

	require.ErrorIs(t, err, io.ErrClosedPipe)
	interrupted, item := f.loadFinalTask("op-selected")
	assert.Equal(t, before, interrupted, "an unreported selection must not leave a reserved review")
	assertTaskNotPublished(t, interrupted, item)
	assert.Empty(t, f.checkCalls)
	assert.Equal(t, selectedReviewCommit, f.git.heads[taskWorkflowRepoRoot])

	stdout, stderr := f.run("", "task", "run", "op-selected")

	assert.Contains(t, stderr, "Implementation commit: "+implementationCommit)
	assert.Contains(t, stderr, "Selected review commit: "+selectedReviewCommit)
	assert.Contains(t, stderr, "\n  0123456 Extend coverage\n")
	assert.Contains(t, stderr, "\n  bbbbbbb Update operator behavior\n")
	assert.Contains(t, stdout, "Review passed for op-selected")
	assert.Equal(t, []string{"check"}, f.checkCalls)
}

type interruptedSelectionOutput struct{}

func (interruptedSelectionOutput) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "Selected review commit:") {
		return 0, io.ErrClosedPipe
	}
	return len(p), nil
}

func TestIntegrationWorkflowRepairCapturesOutputOnSelectedReviewRevision(t *testing.T) {
	f := reviewFixtureWithOperatorCommit(t)
	f.pipelines("standard", map[string][]map[string]any{"standard": {
		{"kind": "check", "name": "check", "command": f.check("check", checkResult{code: 7}, checkResult{})},
	}})
	f.budget(2)
	f.completingRepairs("repairer", 1, true)

	stdout, _ := f.run("k\n", "task", "run", "op-selected")

	assert.Contains(t, stdout, "Review passed for op-selected")
	state, _ := f.loadFinalTask("op-selected")
	require.Len(t, state.Runs, 2)
	require.Len(t, state.Reviews, 2)
	require.Len(t, f.git.captured, 2)
	assert.Equal(t, selectedReviewCommit, f.git.captured[1].Input.Commit)
	assert.Equal(t, implementationCommit, state.Runs[0].Completion.Commit)
	assert.Equal(t, "output-2", state.Runs[1].Completion.Commit)
	w := state.Work.ActiveWorkflow()
	fresh := w.Phases[state.Reviews[1].Address.Phase].(*work.ReviewPhase)
	require.NotNil(t, fresh.Scope.DeltaFrom)
	delta, err := w.Output(*fresh.Scope.DeltaFrom)
	require.NoError(t, err)
	assert.Equal(t, selectedReviewCommit, delta.Commit)
}

func reviewFixtureWithOperatorCommit(t *testing.T) *reviewWorkflowFixture {
	t.Helper()
	f := newReviewWorkflowFixture(t, "op-selected", "Implement original change", "Document implementation only.")
	initial, _ := f.loadFinalTask("op-selected")
	run, ok := taskstate.LatestRun(initial)
	require.True(t, ok)
	capture := implementation.Service{Paths: f.paths, Store: workstate.NewStore(f.paths), Git: f.candidate}
	output, err := capture.Capture(context.Background(), run.Address)
	require.NoError(t, err)
	require.Equal(t, implementationCommit, output.Commit)
	moveReviewFixtureHead(f, selectedReviewCommit)
	f.candidate.addedReviewCommits = []string{"0123456 Extend coverage", "bbbbbbb Update operator behavior"}
	return f
}

// Change commit identity without changing the working tree, so a content-only
// checkout check cannot detect the move.
func moveReviewFixtureHead(f *reviewWorkflowFixture, commit string) {
	f.candidate.head = commit
	f.git.heads[taskWorkflowRepoRoot] = commit
	f.git.trees[commit] = fmt.Sprintf("tree-%x", sha256.Sum256([]byte(f.candidate.contents)))
}

func assertSelectedReviewStops(t *testing.T, f *reviewWorkflowFixture) *work.ReviewPhase {
	t.Helper()
	state, item := f.loadFinalTask("op-selected")
	require.Len(t, state.Reviews, 1)
	assert.Equal(t, taskstate.ReviewStatusFailed, state.Reviews[0].Status)
	require.Len(t, state.Reviews[0].Steps, 1)
	assertTaskNotPublished(t, state, item)
	phase := state.Work.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
	require.NotNil(t, phase.Scope.Selected)
	assert.Equal(t, selectedReviewCommit, phase.Scope.Selected.Commit)
	return phase
}
