package cli

import (
	"bytes"
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestRenderReviewNextStepPreservesReviewStatusGuidance(t *testing.T) {
	blockedFinding := taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "Blocker"}
	tests := []struct {
		name   string
		state  taskstate.TaskState
		review taskstate.ReviewAttempt
		want   string
	}{
		{
			name:   "waiting for automated decision",
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusWaitingForAutomatedDecision, Step: "unit"},
			want:   "\nNext step: automated blocker decision is paused; run `orpheus task run op-1` to resume step unit.\n",
		},
		{
			name:   "waiting for manual step",
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusWaitingForManual, Step: "approval"},
			want:   "\nNext step: run `orpheus task run op-1` to resume manual step approval.\n",
		},
		{
			name: "blocked comparison input interrupted",
			review: taskstate.ReviewAttempt{
				Status: taskstate.ReviewStatusBlocked,
				Steps:  []taskstate.ReviewStep{{Comparison: &taskstate.ReviewComparison{InputInterrupted: true}}},
			},
			want: "\nNext step: alternate comparison input was interrupted; run `orpheus task run op-1` to start a fresh review.\n",
		},
		{
			name:   "blocked automated decision interrupted",
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusBlocked, AutomatedBlockerDecisionInterrupted: true},
			want:   "\nNext step: automated blocker decisions were interrupted; run `orpheus task run op-1` to start a fresh review.\n",
		},
		{
			name: "blocked unkept automated findings",
			review: taskstate.ReviewAttempt{
				Status:   taskstate.ReviewStatusBlocked,
				Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Step: "unit", Title: "Blocker"}},
				Steps:    []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindCheck, Name: "unit"}},
			},
			want: "\nNext step: automated blockers need operator decisions; run `orpheus task run op-1` to start a fresh review.\n",
		},
		{
			name:   "blocked autonomous budget exhausted",
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusBlocked, AutonomousBudgetExhausted: true},
			want:   "\nNext step: autonomous review attempts are exhausted; run `orpheus task run op-1` to continue with a fresh budget.\n",
		},
		{
			name:   "blocked open findings",
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusBlocked, Findings: []taskstate.ReviewFinding{blockedFinding}},
			want:   "\nNext step: run `orpheus task run op-1` to address open blocking findings; it starts a fresh review after repair.\n",
		},
		{
			name: "blocked failed follow-up",
			state: taskstate.TaskState{Runs: []taskstate.RunAttempt{{
				Attempt:        1,
				Status:         taskstate.RunStatusFailed,
				ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}},
			}}},
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusBlocked, Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Title: "Blocker", TargetedByRunAttempt: 1}}},
			want:   "\nNext step: retry `orpheus task run op-1` to address open blocking findings; it starts a fresh review after repair.\n",
		},
		{
			name: "blocked targeted findings",
			state: taskstate.TaskState{Runs: []taskstate.RunAttempt{{
				Attempt:        1,
				Status:         taskstate.RunStatusRunning,
				ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}},
			}}},
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusBlocked, Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Title: "Blocker", TargetedByRunAttempt: 1}}},
			want:   "\nNext step: run `orpheus task run op-1` after targeted follow-up work completes.\n",
		},
		{
			name:   "failed",
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusFailed},
			want:   "\nNext step: run `orpheus task run op-1` when ready.\n",
		},
		{
			name:   "aborted",
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusAborted},
			want:   "\nNext step: run `orpheus task run op-1` when ready.\n",
		},
		{
			name:   "running",
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusRunning},
		},
		{
			name:   "passed",
			review: taskstate.ReviewAttempt{Status: taskstate.ReviewStatusPassed},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := renderReviewNextStep(&output, "op-1", tt.state, tt.review); err != nil {
				t.Fatalf("renderReviewNextStep() error = %v", err)
			}
			if got := output.String(); got != tt.want {
				t.Errorf("renderReviewNextStep() output = %q, want %q", got, tt.want)
			}
		})
	}
}
