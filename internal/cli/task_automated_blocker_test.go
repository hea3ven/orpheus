//nolint:testpackage // Exercises unexported automated-blocker presentation helpers.
package cli

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/spf13/cobra"
)

func TestRenderAuthoritativeFindingUsesPersistedIndexForDuplicateFindings(t *testing.T) {
	finding := taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "duplicate", Description: "same fields"}
	reviewAttempt := taskstate.ReviewAttempt{Attempt: 1, Findings: []taskstate.ReviewFinding{finding, finding}}
	state := taskstate.TaskState{Runs: []taskstate.RunAttempt{
		{Attempt: 1, ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}}},
		{Attempt: 2, ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{1}}},
	}}
	var output bytes.Buffer

	if err := renderAuthoritativeFinding(&output, state, reviewAttempt, 2); err != nil {
		t.Fatalf("renderAuthoritativeFinding() error = %v", err)
	}
	if !strings.Contains(output.String(), "Run attempt 2") {
		t.Errorf("finding output = %q, want follow-up for the second persisted finding", output.String())
	}
	if strings.Contains(output.String(), "Run attempt 1") {
		t.Errorf("finding output = %q, must not include follow-up for the first persisted finding", output.String())
	}
}

func TestPromptAutomatedBlockerUsesAuthoritativeNumber(t *testing.T) {
	var output bytes.Buffer
	command := &cobra.Command{}
	command.SetErr(&output)

	decisions, err := promptAutomatedBlockerDecisions(command, bufio.NewReader(strings.NewReader("p\n")), review.AutomatedBlockerReview{
		Step: review.Step{Name: "unit"},
		Blockers: []review.AutomatedBlocker{{
			Index:   4,
			Number:  1,
			Finding: taskstate.ReviewFinding{Title: "failed check", Description: "the check failed"},
		}},
	})
	if err != nil {
		t.Fatalf("promptAutomatedBlockerDecisions() error = %v", err)
	}
	if len(decisions) != 1 || decisions[0].FindingIndex != 4 || decisions[0].Action != review.AutomatedBlockerActionPause {
		t.Fatalf("decisions = %#v, want pause for persisted finding 4", decisions)
	}
	for _, want := range []string{"Finding 1:", "Decision for finding 1"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("prompt output = %q, want %q", output.String(), want)
		}
	}
	if strings.Contains(output.String(), "finding 5") {
		t.Errorf("prompt output = %q, must not use persisted finding index", output.String())
	}
}
