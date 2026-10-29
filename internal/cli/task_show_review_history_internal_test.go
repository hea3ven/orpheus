package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskShowReviewScopesHistoryAttemptAndFinding(t *testing.T) {
	state := taskstate.TaskState{
		Reviews: []taskstate.ReviewAttempt{
			{
				Attempt:  1,
				Status:   taskstate.ReviewStatusBlocked,
				Pipeline: "standard",
				Step:     "ai-review",
				Findings: []taskstate.ReviewFinding{
					{Type: taskstate.FindingTypeBlocking, Step: "ai-review", Title: "Waived blocker", Description: "The old behavior is accepted.", SuggestedAction: "Retain the guard.", Waiver: "Accepted for this release."},
					{Type: taskstate.FindingTypeBlocking, Step: "ai-review", Title: "Repaired blocker", Description: "The old repair was attempted.", SuggestedAction: "Repair the race.", TargetedByRunAttempt: 1},
					{Type: taskstate.FindingTypeAdvisory, Step: "ai-review", Title: "Advisory note", Description: "Non-blocking note."},
					{Type: taskstate.FindingTypeSeparateTask, Step: "ai-review", Title: "Extract helper", Description: "Track cleanup separately.", TaskProposal: taskstate.ReviewTaskProposal{Title: "Extract helper", Description: "Extract the duplicated helper.", AcceptanceCriteria: "The helper has focused tests."}, CreatedTaskID: "op-42"},
				},
			},
			{Attempt: 2, Status: taskstate.ReviewStatusRunning, Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Title: "Current finding", Description: "Must not enter prior history."}}},
		},
		Runs: []taskstate.RunAttempt{
			{Attempt: 1, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{Summary: "Repair completed"}, ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{1}, AdvisoryFindingIndexes: []int{2}}},
			{Attempt: 2, Status: taskstate.RunStatusFailed, ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{1}, AdvisoryFindingIndexes: []int{2}}},
		},
	}

	var history bytes.Buffer
	if err := renderTaskShowReview(&history, "alpha", "op-history", state, reviewShowScope{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Attempt 1: blocked (4 authoritative finding(s))",
		"1/1 · ai-review · blocking · waived · Waived blocker",
		"1/2 · ai-review · blocking · follow-up run 1 succeeded · Repaired blocker",
		"1/4 · ai-review · separate_task · created task op-42 · Extract helper",
		"Attempt 2: running (1 authoritative finding(s))",
		"2/1 · - · blocking · open · Current finding",
		"orpheus task show review <task-id> <review-attempt> <finding-number>",
	} {
		if !strings.Contains(history.String(), want) {
			t.Fatalf("history missing %q:\n%s", want, history.String())
		}
	}
	if strings.Contains(history.String(), "The old behavior is accepted.") {
		t.Fatalf("history must remain compact:\n%s", history.String())
	}

	var attempt bytes.Buffer
	if err := renderTaskShowReview(&attempt, "alpha", "op-history", state, reviewShowScope{reviewAttempt: 1}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Authoritative review attempt 1:", "Findings by step:", "Follow-up runs:", "Run attempt 1: succeeded (required blocking findings 2; advisory opportunities 3)", "Created follow-up tasks:", "op-42 (finding 4, step ai-review): Extract helper"} {
		if !strings.Contains(attempt.String(), want) {
			t.Fatalf("attempt detail missing %q:\n%s", want, attempt.String())
		}
	}

	var finding bytes.Buffer
	if err := renderTaskShowReview(&finding, "alpha", "op-history", state, reviewShowScope{reviewAttempt: 1, findingNumber: 4}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Authoritative finding 1/4:", "Description: Track cleanup separately.", "Disposition: converted/created task op-42", "Proposed task title: Extract helper", "Proposed task description: Extract the duplicated helper.", "Proposed task acceptance criteria: The helper has focused tests.", "Created follow-up task: op-42", "Associated follow-up runs:\n  (none recorded)"} {
		if !strings.Contains(finding.String(), want) {
			t.Fatalf("finding detail missing %q:\n%s", want, finding.String())
		}
	}

	finding.Reset()
	if err := renderTaskShowReview(&finding, "alpha", "op-history", state, reviewShowScope{reviewAttempt: 1, findingNumber: 2}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Suggested action: Repair the race.", "Associated follow-up runs:", "Run attempt 1: succeeded (completion recorded)", "Run attempt 2: failed (no completion)"} {
		if !strings.Contains(finding.String(), want) {
			t.Fatalf("finding follow-up detail missing %q:\n%s", want, finding.String())
		}
	}

	finding.Reset()
	if err := renderTaskShowReview(&finding, "alpha", "op-history", state, reviewShowScope{reviewAttempt: 1, findingNumber: 3}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(finding.String(), "Run attempt 1: succeeded (completion recorded)") || !strings.Contains(finding.String(), "Run attempt 2: failed (no completion)") {
		t.Fatalf("advisory follow-up audit missing:\n%s", finding.String())
	}
	// Reused human ordinals after reset must never select older review/repair
	// ownership. The current attempt also exposes its immutable committed scope.
	snapshot := testutil.ReviewableWork("alpha", "op-snapshot", "/fixture/current", "task-branch")
	old := testutil.ReviewableWork("alpha", "op-snapshot", "/fixture/old", "old-task-branch").Workflows[0]
	snapshot.Workflows = append([]work.Workflow{old}, snapshot.Workflows...)
	for wi := range snapshot.Workflows {
		w := &snapshot.Workflows[wi]
		w.ReviewContracts = []work.ReviewContract{{PipelineKey: "manual", Slots: []work.ReviewSlot{{Kind: work.ReviewManual, ConfigKey: "approval"}}}}
		at := time.Date(2026, 1, 1, 0, 0, 10, 0, time.UTC)
		finding := work.Finding{Classification: work.FindingSeparateTask, Title: "old task receipt", Description: "Archived task receipt", Proposal: &work.FollowUpProposal{Title: "Old follow-up", Description: "Old task body", AcceptanceCriteria: "Old acceptance"}, CreatedTask: &work.CreatedFollowUp{TaskID: "old-followup", ObservedAt: at}}
		if wi == 1 {
			finding.Title = "current task receipt"
			finding.CreatedTask = &work.CreatedFollowUp{TaskID: "current-followup", ObservedAt: at}
		}
		w.Phases = append(w.Phases, &work.ReviewPhase{Contract: 0, StartedAt: at, Scope: work.ReviewScope{Requirements: fmt.Sprintf("Workflow %d requirements", wi), Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}}, Steps: []work.ReviewStep{&work.ManualReviewStep{Slot: 0, Findings: []work.Finding{finding}, Decision: &work.ApprovedReview{At: at}}}})
	}
	current, err := taskstate.Project(snapshot)
	require.NoError(t, err)
	// An adversarial old repair uses the same display ordinals.
	current.Runs = append(current.Runs, taskstate.RunAttempt{Address: work.AttemptRef{Workflow: 0, Phase: 3}, Attempt: 99, Status: taskstate.RunStatusFailed, ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}}})
	attempt.Reset()
	require.NoError(t, renderTaskShowReview(&attempt, "alpha", "op-snapshot", current, reviewShowScope{reviewAttempt: 1}))
	assert.Contains(t, attempt.String(), "Committed scope (workflow 1, phase 2)")
	assert.Contains(t, attempt.String(), "Full-task comparison: initial-commit -> fixture-output")
	assert.Contains(t, attempt.String(), "Captured requirements: Workflow 1 requirements")
	assert.Contains(t, attempt.String(), "current-followup")
	assert.NotContains(t, attempt.String(), "old-followup")
	assert.NotContains(t, attempt.String(), "Run attempt 99")
	var allWorkflows bytes.Buffer
	require.NoError(t, renderTaskShowReview(&allWorkflows, "alpha", "op-snapshot", current, reviewShowScope{}))
	assert.Contains(t, allWorkflows.String(), "Workflow 0, phase 2")
	assert.Contains(t, allWorkflows.String(), "Workflow 1, phase 2")
	assert.Contains(t, allWorkflows.String(), "old task receipt")
	assert.Contains(t, allWorkflows.String(), "current task receipt")

}

func TestReviewShowRejectsInvalidScopes(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{args: []string{}, want: "accepts a task ID, optional positive review attempt, and optional positive finding number"},
		{args: []string{"op-1", "0"}, want: `review attempt must be a positive integer, got "0"`},
		{args: []string{"op-1", "one"}, want: `review attempt must be a positive integer, got "one"`},
		{args: []string{"op-1", "1", "0"}, want: `finding number must be a positive integer, got "0"`},
		{args: []string{"op-1", "1", "two"}, want: `finding number must be a positive integer, got "two"`},
		{args: []string{"op-1", "1", "1", "extra"}, want: "accepts a task ID, optional positive review attempt, and optional positive finding number"},
	}
	for _, test := range tests {
		if _, err := parseReviewShowScope(test.args); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("parseReviewShowScope(%q) error = %v, want %q", test.args, err, test.want)
		}
	}
}

func TestTaskShowReviewRejectsUnknownAttemptAndFinding(t *testing.T) {
	state := taskstate.TaskState{Reviews: []taskstate.ReviewAttempt{{
		Attempt: 2,
		Findings: []taskstate.ReviewFinding{{
			Type:  taskstate.FindingTypeBlocking,
			Title: "Only finding",
		}},
	}}}
	tests := []struct {
		empty bool
		scope reviewShowScope
		want  string
	}{
		{empty: true, scope: reviewShowScope{reviewAttempt: 1}, want: "review attempt 1 was not found for task op-1: no review attempts are recorded"},
		{empty: true, scope: reviewShowScope{reviewAttempt: 1, findingNumber: 1}, want: "review attempt 1 was not found for task op-1: no review attempts are recorded"},
		{scope: reviewShowScope{reviewAttempt: 1}, want: "review attempt 1 was not found for task op-1"},
		{scope: reviewShowScope{reviewAttempt: 2, findingNumber: 2}, want: "authoritative finding number 2 is out of range for review attempt 2 (has 1 authoritative finding(s))"},
	}
	for _, test := range tests {
		var output bytes.Buffer
		current := state
		if test.empty {
			current = taskstate.TaskState{}
		}
		err := renderTaskShowReview(&output, "alpha", "op-1", current, test.scope)
		if strings.Contains(output.String(), "No review attempts recorded") {
			t.Fatalf("scoped lookup must fail: %s", output.String())
		}
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("renderTaskShowReview(scope=%+v) error = %v, want %q", test.scope, err, test.want)
		}
	}
}

func TestTaskReviewHistoryExcludesInterruptedReviewAuditFindings(t *testing.T) {
	finished := time.Now()
	state := taskstate.TaskState{Reviews: []taskstate.ReviewAttempt{
		{
			Attempt:  1,
			Steps:    []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusInterrupted, StartedAt: finished.Add(-time.Minute), FinishedAt: &finished, InterruptionReason: "supervisor disappeared"}}},
			Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Step: "ai-review", Title: "Interrupted audit finding", Description: "Retained for audit only."}},
		},
		{Attempt: 2, Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Title: "Authoritative finding", Description: "Valid prior finding."}}},
	}}

	var history bytes.Buffer
	if err := renderTaskShowReview(&history, "alpha", "op-audit", state, reviewShowScope{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(history.String(), "Interrupted audit finding") {
		t.Fatalf("history included audit-only finding:\n%s", history.String())
	}
	for _, want := range []string{"Attempt 1: - (0 authoritative finding(s))", "2/1 · - · blocking · open · Authoritative finding"} {
		if !strings.Contains(history.String(), want) {
			t.Fatalf("history missing %q:\n%s", want, history.String())
		}
	}

	var finding bytes.Buffer
	if err := renderTaskShowReview(&finding, "alpha", "op-audit", state, reviewShowScope{reviewAttempt: 1, findingNumber: 1}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Audit-only finding 1/1:", "Interrupted audit finding", "Disposition: audit-only (interrupted reviewer)", "Audit-only: interrupted reviewer"} {
		if !strings.Contains(finding.String(), want) {
			t.Fatalf("exact finding inspection missing %q:\n%s", want, finding.String())
		}
	}
}

func TestTaskReviewHistoryCollapsesMultilineFindingFields(t *testing.T) {
	state := taskstate.TaskState{Reviews: []taskstate.ReviewAttempt{{
		Attempt: 1,
		Findings: []taskstate.ReviewFinding{{
			Type: taskstate.FindingTypeBlocking, Step: "ai-review\nspoofed-step", Title: "Multiline title\n- spoofed finding", Description: "Exact finding keeps its content.", Waiver: "Accepted\nwith context",
		}},
	}}}
	var output bytes.Buffer
	if err := renderTaskShowReview(&output, "alpha", "op-multiline", state, reviewShowScope{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "1/1 · ai-review spoofed-step · blocking · waived · Multiline title - spoofed finding") {
		t.Fatalf("compact history did not collapse fields:\n%s", output.String())
	}
	if strings.Contains(output.String(), "\n- spoofed finding") {
		t.Fatalf("multiline title split compact history:\n%s", output.String())
	}
}
