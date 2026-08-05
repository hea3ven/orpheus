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
	err := renderTaskReviewShow(&output, "alpha", "op-paired", taskstate.TaskState{Reviews: []taskstate.ReviewAttempt{{
		Attempt: 1, Status: taskstate.ReviewStatusPassed, Pipeline: "standard", Step: "ai-review", StartedAt: started, FinishedAt: &finished,
		Steps: []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusSucceeded, Profile: "primary", Harness: "pi", Model: "primary-model", StartedAt: started, FinishedAt: &finished}, Comparison: &taskstate.ReviewComparison{
			AlternateExecution: &taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusSucceeded, Profile: "alternate", Harness: "codex", Model: "alternate-model", StartedAt: started, FinishedAt: &finished, Usage: &taskstate.AgentUsage{TotalTokens: 42}},
			AlternateFindings:  []taskstate.AlternateReviewFinding{{Finding: taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "alternate raw", Description: "kept for comparison", Reviewer: "alternate"}, Classification: taskstate.AlternateFindingExcluded}},
		}}},
		Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeAdvisory, Title: "primary raw", Description: "authoritative", Step: "ai-review", Reviewer: "primary"}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Primary execution:", "Profile: primary", "Alternate execution:", "Profile: alternate", "Usage: input=0 cached_input=0 output=0 reasoning_output=0 total=42", "Raw alternate findings:", "Alternate finding 1 [excluded]", "Reviewer: primary"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, output.String())
		}
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
