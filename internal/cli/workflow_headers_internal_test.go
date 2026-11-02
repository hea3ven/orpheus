package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/mattn/go-runewidth"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorkflowHeadersSeparatePhaseAndStepsAndRefreshContextOnResume(t *testing.T) {
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetOut(&output)
	command.SetErr(&output)
	terminal := TerminalCapabilities{OutputIsTerminal: func(io.Writer) bool { return false }, OutputWidth: func(io.Writer) (int, bool) { return 0, false }}
	headers := newWorkflowHeaders(command, terminal)
	view := workflow.View{TaskID: "op-42", Title: "Review the retained output", Phase: workflow.PhaseReview, PhaseAttempt: 2, Revision: &workflow.RevisionView{Number: 2, Code: work.CodeIdentity{Commit: "repair-output"}}, Scope: &workflow.ScopeView{Baseline: "task-baseline", Head: "repair-output"}, Step: &workflow.StepView{Name: "checks", Kind: "check", Attempt: 2}}
	require.NoError(t, headers.Render(workflow.Progress{View: view}))
	view.Step = &workflow.StepView{Name: "architecture", Kind: "agent_review", Attempt: 1}
	require.NoError(t, headers.Render(workflow.Progress{View: view}))
	require.NoError(t, headers.Render(workflow.Progress{View: view}))
	text := output.String()
	assert.Equal(t, 1, strings.Count(text, "== REVIEW"))
	assert.Equal(t, 1, strings.Count(text, "op-42 - Review the retained output"))
	assert.Contains(t, text, "STEP: checks (check) | attempt 2")
	assert.Equal(t, 1, strings.Count(text, "STEP: architecture (agent_review)"))
	assert.Contains(t, text, "R2 | repair-output")
	assert.Contains(t, text, "Full task | task-baseline -> repair-output")
	assert.NotContains(t, text, "WORKFLOW")
	assert.NotContains(t, text, "\x1b")

	output.Reset()
	require.NoError(t, newWorkflowHeaders(command, terminal).Render(workflow.Progress{View: view, Resumed: true}))
	assert.Contains(t, output.String(), "REVIEW | attempt 2 | resumed")
	assert.Contains(t, output.String(), "op-42 - Review the retained output")
	assert.Contains(t, output.String(), "Full task | task-baseline -> repair-output")
	assert.Equal(t, 2, view.PhaseAttempt)
	assert.Equal(t, 1, view.Step.Attempt)

	output.Reset()
	view.Revision.Code.Commit = "new-output"
	view.Scope.Head = "new-output"
	require.NoError(t, headers.Render(workflow.Progress{View: view}))
	assert.Contains(t, output.String(), "op-42 - Review the retained output")
	assert.Contains(t, output.String(), "new-output")
	assert.NotContains(t, output.String(), "repair-output")
}

func TestWorkflowHeadersPresentPhaseFactsAndWrapCompleteIdentitiesAtDisplayWidth(t *testing.T) {
	revision := &workflow.RevisionView{Number: 2, Code: work.CodeIdentity{Commit: "retained-output"}}
	view := workflow.View{TaskID: "op-42", Title: "Deliver output", Workflow: 2, WorkflowCount: 2, PhaseAttempt: 1, Context: &work.WorkContext{Directory: "/fixture/worktree", TaskBranch: "orpheus/op-42", Baseline: work.CodeIdentity{Commit: "task-baseline"}}, Execution: &workflow.ExecutionView{Profile: "repairer"}, Start: revision, Revision: revision, Scope: &workflow.ScopeView{Baseline: "task-baseline", Head: "retained-output"}, Findings: []work.FindingRef{{Phase: 2, Step: 0, Finding: 3}}, Published: &workflow.PublishedView{Revision: revision, PublicCommit: "public-1", PRURL: "https://example.test/pr/42"}, Publication: &workflow.PublicationView{ReviewAttempt: 2, ReviewOutcome: work.ReviewPassed, Flow: "direct-merge", Destination: work.Destination{Remote: "origin", Branch: "main"}, PRURL: "https://example.test/pr/42", PRState: "merged", PublicCommit: "public-2", DeliveryEvidence: "Destination push verified", AcceptanceEvidence: "PR merge verified"}, Closure: &work.TaskClosure{Evidence: "Task source confirmed closed"}}
	for _, tc := range []struct {
		phase workflow.PhaseKind
		facts []string
	}{
		{workflow.PhasePreparation, []string{"WORKDIR", "/fixture/worktree", "BRANCH", "orpheus/op-42", "BASELINE", "task-baseline"}},
		{workflow.PhaseImplementation, []string{"AGENT", "repairer (implementer)", "START", "R2 | retained-output", "FINDINGS", "finding 4"}},
		{workflow.PhaseReview, []string{"REVISION", "Full task | task-baseline -> retained-output", "PUBLISHED", "public-1", "PR", "https://example.test/pr/42"}},
		{workflow.PhasePublication, []string{"REVIEW", "Attempt 2 passed", "FLOW", "direct-merge", "DESTINATION", "origin/main", "CONSENT", "Awaiting publication decision"}},
		{workflow.PhaseExternalReview, []string{"PR", "https://example.test/pr/42 | merged", "PUBLISHED", "public-2", "DESTINATION", "origin/main"}},
		{workflow.PhaseFinalization, []string{"REVISION", "retained-output", "DELIVERY", "PR merge verified", "CLOSURE", "Task source confirmed closed"}},
	} {
		t.Run(string(tc.phase), func(t *testing.T) {
			var output bytes.Buffer
			command := &cobra.Command{}
			command.SetOut(&output)
			command.SetErr(&output)
			headers := newWorkflowHeaders(command, TerminalCapabilities{OutputIsTerminal: func(io.Writer) bool { return false }, OutputWidth: func(io.Writer) (int, bool) { return 0, false }})
			view.Phase = tc.phase
			require.NoError(t, headers.Render(workflow.Progress{View: view}))
			assert.Contains(t, output.String(), "WORKFLOW     2")
			for _, fact := range tc.facts {
				assert.Contains(t, output.String(), fact)
			}
		})
	}

	for _, width := range []int{18, 40} {
		var output bytes.Buffer
		headers := &workflowHeaders{output: &output, terminal: TerminalCapabilities{OutputWidth: func(io.Writer) (int, bool) { return width, true }}}
		value := "/fixture/界界/long-token-without-spaces/e\u0301/retained-identity"
		require.NoError(t, headers.field(workflowHeaderField{label: "WORKDIR", value: value}))
		var reconstructed strings.Builder
		for index, line := range strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n") {
			assert.LessOrEqual(t, runewidth.StringWidth(line), width)
			if width == 18 && index == 0 {
				assert.Equal(t, "WORKDIR", line)
				continue
			}
			if width == 18 {
				reconstructed.WriteString(strings.TrimPrefix(line, "  "))
			} else {
				reconstructed.WriteString(line[13:])
			}
		}
		assert.Equal(t, value, reconstructed.String(), "wrapping must preserve every identity character")
	}
}
