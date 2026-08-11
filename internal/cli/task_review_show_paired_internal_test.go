package cli

import (
	"bufio"
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/spf13/cobra"
)

func TestRenderTaskReviewShowRendersPairedReviewerComparison(t *testing.T) {
	started := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	finished := started.Add(time.Minute)
	var output bytes.Buffer
	state := taskstate.TaskState{Reviews: []taskstate.ReviewAttempt{{
		Attempt: 1, Status: taskstate.ReviewStatusPassed, Pipeline: "standard", Step: "ai-review", StartedAt: started, FinishedAt: &finished,
		Steps: []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusSucceeded, Profile: "primary", Harness: "pi", Model: "primary-model", StartedAt: started, FinishedAt: &finished}, Comparison: &taskstate.ReviewComparison{
			AlternateExecution: &taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusSucceeded, Profile: "alternate", Harness: "codex", Model: "alternate-model", StartedAt: started, FinishedAt: &finished, Usage: &taskstate.AgentUsage{TotalTokens: 42}},
			AlternateFindings:  []taskstate.AlternateReviewFinding{{Finding: taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "alternate raw", Description: "kept for comparison", Reviewer: "alternate"}, Classification: taskstate.AlternateFindingExcluded}},
		}}},
		Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeAdvisory, Title: "primary raw", Description: "authoritative", Step: "ai-review", Reviewer: "primary"}},
	}}}
	err := renderTaskReviewShow(&output, "alpha", "op-paired", state, reviewShowScope{reviewAttempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Primary execution:", "Profile: primary", "Alternate execution:", "Profile: alternate", "Usage: input=0 cached_input=0 output=0 reasoning_output=0 total=42", "Raw alternate findings:", "Alternate finding 1 [excluded]", "Reviewer: primary"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, output.String())
		}
	}
}

//nolint:funlen // The fixture and scoped assertions document the inspection hierarchy together.
func TestTaskReviewShowScopesHistoryAttemptAndFinding(t *testing.T) {
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
	if err := renderTaskReviewShow(&history, "alpha", "op-history", state, reviewShowScope{}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Attempt 1: blocked (4 authoritative finding(s))",
		"1/1 · ai-review · blocking · waived · Waived blocker",
		"1/2 · ai-review · blocking · follow-up run 1 succeeded · Repaired blocker",
		"1/4 · ai-review · separate_task · created task op-42 · Extract helper",
		"Attempt 2: running (1 authoritative finding(s))",
		"2/1 · - · blocking · open · Current finding",
		"orpheus task review show <task-id> <review-attempt> <finding-number>",
	} {
		if !strings.Contains(history.String(), want) {
			t.Fatalf("history missing %q:\n%s", want, history.String())
		}
	}
	if strings.Contains(history.String(), "The old behavior is accepted.") {
		t.Fatalf("history must remain compact:\n%s", history.String())
	}

	var attempt bytes.Buffer
	if err := renderTaskReviewShow(&attempt, "alpha", "op-history", state, reviewShowScope{reviewAttempt: 1}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Authoritative review attempt 1:", "Findings by step:", "Follow-up runs:", "Run attempt 1: succeeded (required blocking findings 2; advisory opportunities 3)", "Created follow-up Beads:", "op-42 (finding 4, step ai-review): Extract helper"} {
		if !strings.Contains(attempt.String(), want) {
			t.Fatalf("attempt detail missing %q:\n%s", want, attempt.String())
		}
	}

	var finding bytes.Buffer
	if err := renderTaskReviewShow(&finding, "alpha", "op-history", state, reviewShowScope{reviewAttempt: 1, findingNumber: 4}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Authoritative finding 1/4:", "Description: Track cleanup separately.", "Disposition: converted/created task op-42", "Proposed task title: Extract helper", "Proposed task description: Extract the duplicated helper.", "Proposed task acceptance criteria: The helper has focused tests.", "Created follow-up task: op-42", "Associated follow-up runs:\n  (none recorded)"} {
		if !strings.Contains(finding.String(), want) {
			t.Fatalf("finding detail missing %q:\n%s", want, finding.String())
		}
	}

	finding.Reset()
	if err := renderTaskReviewShow(&finding, "alpha", "op-history", state, reviewShowScope{reviewAttempt: 1, findingNumber: 2}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Suggested action: Repair the race.", "Associated follow-up runs:", "Run attempt 1: succeeded (completion recorded)", "Run attempt 2: failed (no completion)"} {
		if !strings.Contains(finding.String(), want) {
			t.Fatalf("finding follow-up detail missing %q:\n%s", want, finding.String())
		}
	}

	finding.Reset()
	if err := renderTaskReviewShow(&finding, "alpha", "op-history", state, reviewShowScope{reviewAttempt: 1, findingNumber: 3}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(finding.String(), "Run attempt 1: succeeded (completion recorded)") || !strings.Contains(finding.String(), "Run attempt 2: failed (no completion)") {
		t.Fatalf("advisory follow-up audit missing:\n%s", finding.String())
	}
}

func TestReviewShowRejectsInvalidScopes(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"op-1", "0"},
		{"op-1", "one"},
		{"op-1", "1", "0"},
		{"op-1", "1", "two"},
		{"op-1", "1", "1", "extra"},
	} {
		if _, err := parseReviewShowScope(args); err == nil {
			t.Fatalf("parseReviewShowScope(%q) succeeded", args)
		}
	}
}

func TestTaskReviewShowRejectsScopesWhenNoReviewsExist(t *testing.T) {
	for _, scope := range []reviewShowScope{{reviewAttempt: 1}, {reviewAttempt: 1, findingNumber: 1}} {
		var output bytes.Buffer
		err := renderTaskReviewShow(&output, "alpha", "op-empty", taskstate.TaskState{}, scope)
		if err == nil || !strings.Contains(err.Error(), "review attempt 1 was not found for task op-empty: no review attempts are recorded") {
			t.Fatalf("renderTaskReviewShow(scope=%+v) error = %v, want missing-attempt error", scope, err)
		}
		if strings.Contains(output.String(), "No review attempts recorded") {
			t.Fatalf("scoped no-history output must not succeed:\n%s", output.String())
		}
	}
}

func TestTaskReviewHistoryExcludesInterruptedPrimaryAuditFindings(t *testing.T) {
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
	if err := renderTaskReviewShow(&history, "alpha", "op-audit", state, reviewShowScope{}); err != nil {
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
	if err := renderTaskReviewShow(&finding, "alpha", "op-audit", state, reviewShowScope{reviewAttempt: 1, findingNumber: 1}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Audit-only finding 1/1:", "Interrupted audit finding", "Disposition: audit-only (interrupted primary reviewer)", "Audit-only: interrupted primary reviewer"} {
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
	if err := renderTaskReviewShow(&output, "alpha", "op-multiline", state, reviewShowScope{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "1/1 · ai-review spoofed-step · blocking · waived · Multiline title - spoofed finding") {
		t.Fatalf("compact history did not collapse fields:\n%s", output.String())
	}
	if strings.Contains(output.String(), "\n- spoofed finding") {
		t.Fatalf("multiline title split compact history:\n%s", output.String())
	}
}

func TestAlternateFindingPromptRendersReviewerProvenance(t *testing.T) {
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetErr(&output)
	prompt := taskReviewAlternateFindingPrompt(command, bufio.NewReader(strings.NewReader("a\n")))
	decisions, err := prompt(review.AlternateReviewComparison{
		Step:               review.Step{Name: "ai-review"},
		PrimaryExecution:   &taskstate.AgentExecution{Profile: "primary", Model: "primary-model", Harness: "pi", Status: taskstate.RunStatusSucceeded},
		AlternateExecution: &taskstate.AgentExecution{Profile: "alternate", Model: "alternate-model", Harness: "codex", Status: taskstate.RunStatusSucceeded},
		Alternate:          []review.AlternateFinding{{Index: 0, Finding: taskstate.ReviewFinding{Title: "alternate", Description: "finding"}}},
	})
	if err != nil || len(decisions) != 1 || decisions[0].Classification != taskstate.AlternateFindingAdmitted {
		t.Fatalf("decisions=%#v err=%v", decisions, err)
	}
	for _, want := range []string{"Primary reviewer: profile=primary model=primary-model harness=pi", "Alternate reviewer: profile=alternate model=alternate-model harness=codex"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, output.String())
		}
	}
}
