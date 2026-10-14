package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestReviewInspectionGivesActionableGuidanceForEachReviewState(t *testing.T) {
	blockedFinding := taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "Blocker"}
	tests := []struct {
		name                          string
		state                         taskstate.TaskState
		review                        taskstate.ReviewAttempt
		want, wantHistory, wantDetail []string
	}{
		{
			name: "waiting for automated decision", want: []string{"paused", "resume step unit"},
			wantDetail: []string{"Automated blocker decisions: paused"},
			review:     taskstate.ReviewAttempt{Status: taskstate.ReviewStatusWaitingForAutomatedDecision, Step: "unit"},
		},
		{
			name: "waiting for manual step", want: []string{"resume manual step approval"},
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusWaitingForManual, Step: "approval"},
		},
		{
			name: "blocked comparison input interrupted", want: []string{"alternate comparison input was interrupted", "start a fresh review"},
			review: taskstate.ReviewAttempt{
				Status: taskstate.ReviewStatusBlocked,
				Steps:  []taskstate.ReviewStep{{Comparison: &taskstate.ReviewComparison{InputInterrupted: true}}},
			},
		},
		{
			name: "blocked automated decision interrupted", want: []string{"automated blocker decisions were interrupted", "start a fresh review"},
			wantDetail: []string{"Automated blocker decisions: interrupted"},
			review:     taskstate.ReviewAttempt{Status: taskstate.ReviewStatusBlocked, AutomatedBlockerDecisionInterrupted: true},
		},
		{
			name: "blocked unkept automated findings", want: []string{"automated blockers need operator decisions", "start a fresh review"},
			review: taskstate.ReviewAttempt{
				Status:   taskstate.ReviewStatusBlocked,
				Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Step: "unit", Title: "Blocker"}},
				Steps:    []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindCheck, Name: "unit"}},
			},
		},
		{
			name: "blocked autonomous budget exhausted", want: []string{"attempts are exhausted", "fresh budget"},
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusBlocked, AutonomousBudgetExhausted: true},
		},
		{
			name: "blocked open findings", want: []string{"address open blocking findings", "fresh review after repair"},
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusBlocked, Findings: []taskstate.ReviewFinding{blockedFinding}},
		},
		{
			name: "blocked failed follow-up", want: []string{"retry", "address open blocking findings"},
			wantHistory: []string{"follow-up run 1 failed"},
			state: taskstate.TaskState{Runs: []taskstate.RunAttempt{{
				Attempt:        1,
				Status:         taskstate.RunStatusFailed,
				ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}},
			}}},
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusBlocked, Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Title: "Blocker", TargetedByRunAttempt: 1}}},
		},
		{
			name: "blocked targeted findings", want: []string{"after targeted follow-up work completes"},
			state: taskstate.TaskState{Runs: []taskstate.RunAttempt{{
				Attempt:        1,
				Status:         taskstate.RunStatusRunning,
				ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}},
			}}},
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusBlocked, Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Title: "Blocker", TargetedByRunAttempt: 1}}},
		},
		{
			name: "failed", want: []string{"when ready"},
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusFailed},
		},
		{
			name: "aborted", want: []string{"when ready"},
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusAborted},
		},
		{
			name: "running", want: nil,
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusRunning},
		},
		{
			name: "passed", want: nil,
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusPassed},
		},
		{name: "no review attempts", want: []string{"No review attempts recorded", "after task work is ready"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := tt.state
			scopes := []reviewShowScope{{}}
			if tt.review.Status != "" {
				tt.review.Attempt = 1
				state.Reviews = []taskstate.ReviewAttempt{tt.review}
				scopes = append(scopes, reviewShowScope{reviewAttempt: 1})
			}
			for _, scope := range scopes {
				scopeWant := tt.wantHistory
				if scope.reviewAttempt > 0 {
					scopeWant = tt.wantDetail
				}
				assertReviewInspectionGuidance(t, state, scope, tt.want, scopeWant)
			}
		})
	}
}

func assertReviewInspectionGuidance(t *testing.T, state taskstate.TaskState, scope reviewShowScope, want, scopeWant []string) {
	t.Helper()
	var output bytes.Buffer

	if err := renderTaskShowReview(&output, "alpha", "op-1", state, scope); err != nil {
		t.Fatal(err)
	}

	got := output.String()
	if len(want) == 0 {
		if strings.Contains(got, "Next step:") {
			t.Fatalf("scope %+v has unexpected next action: %s", scope, got)
		}
		return
	}
	fragments := append([]string{"orpheus task run op-1"}, want...)
	fragments = append(fragments, scopeWant...)
	for _, fragment := range fragments {
		if !strings.Contains(got, fragment) {
			t.Fatalf("scope %+v missing %q: %s", scope, fragment, got)
		}
	}
}
