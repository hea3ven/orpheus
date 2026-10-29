package taskstate_test

import (
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReviewFindingResolutionContract(t *testing.T) {
	for _, test := range reviewFindingResolutionContractCases {
		t.Run(test.name, func(t *testing.T) {
			if got := taskstate.ResolveReviewFinding(test.finding); got != test.resolution {
				t.Fatalf("ResolveReviewFinding() = %q, want %q", got, test.resolution)
			}
			if got := taskstate.ReviewFindingResolved(test.finding); got != test.resolved {
				t.Fatalf("ReviewFindingResolved() = %v, want %v", got, test.resolved)
			}
			if got := taskstate.IsOpenBlockingReviewFinding(test.finding); got != test.openBlocking {
				t.Fatalf("IsOpenBlockingReviewFinding() = %v, want %v", got, test.openBlocking)
			}
			if got := taskstate.IsOpenAdvisoryReviewFinding(test.finding); got != test.openAdvisory {
				t.Fatalf("IsOpenAdvisoryReviewFinding() = %v, want %v", got, test.openAdvisory)
			}
		})
	}
}

type reviewFindingResolutionContractCase struct {
	name         string
	finding      taskstate.ReviewFinding
	resolution   taskstate.ReviewFindingResolution
	resolved     bool
	openBlocking bool
	openAdvisory bool
}

var reviewFindingResolutionContractCases = []reviewFindingResolutionContractCase{
	{
		name: "open blocking",
		finding: taskstate.ReviewFinding{
			Type:        taskstate.FindingTypeBlocking,
			Title:       "Blocker",
			Description: "Still open.",
		},
		resolution:   taskstate.ReviewFindingResolutionOpen,
		openBlocking: true,
	},
	{
		name: "manually addressed blocking",
		finding: taskstate.ReviewFinding{
			Type:              taskstate.FindingTypeBlocking,
			Title:             "Manually addressed",
			Description:       "Fixed outside Orpheus.",
			AddressedManually: "Verified direct fix.",
		},
		resolution: taskstate.ReviewFindingResolutionAddressedManually,
		resolved:   true,
	},
	{
		name: "waived blocking",
		finding: taskstate.ReviewFinding{
			Type:        taskstate.FindingTypeBlocking,
			Title:       "Waived",
			Description: "Accepted.",
			Waiver:      "Accepted risk.",
		},
		resolution: taskstate.ReviewFindingResolutionWaived,
		resolved:   true,
	},
	{
		name: "downgraded advisory",
		finding: taskstate.ReviewFinding{
			Type:            taskstate.FindingTypeAdvisory,
			Title:           "Downgraded",
			Description:     "No longer blocking.",
			DowngradeReason: "False positive.",
		},
		resolution: taskstate.ReviewFindingResolutionDowngraded,
		resolved:   true,
	},
	{
		name: "created follow-up task",
		finding: taskstate.ReviewFinding{
			Type:          taskstate.FindingTypeSeparateTask,
			Title:         "Follow-up",
			Description:   "Track later.",
			CreatedTaskID: "op-2",
		},
		resolution: taskstate.ReviewFindingResolutionCreatedTask,
		resolved:   true,
	},
	{
		name: "targeted blocker",
		finding: taskstate.ReviewFinding{
			Type:                 taskstate.FindingTypeBlocking,
			Title:                "Targeted",
			Description:          "Follow-up in progress.",
			TargetedByRunAttempt: 2,
		},
		resolution: taskstate.ReviewFindingResolutionTargetedByRun,
		resolved:   true,
	},
	{
		name: "open advisory",
		finding: taskstate.ReviewFinding{
			Type:        taskstate.FindingTypeAdvisory,
			Title:       "Advisory",
			Description: "Could be promoted.",
		},
		resolution:   taskstate.ReviewFindingResolutionNonBlocking,
		openAdvisory: true,
	},
}

func TestEligibleAdvisoryFindingIndexes(t *testing.T) {
	review := taskstate.ReviewAttempt{
		Steps: []taskstate.ReviewStep{
			{Kind: taskstate.ReviewStepKindAgentReview, Name: "completed", Execution: &taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusSucceeded}},
			{Kind: taskstate.ReviewStepKindAgentReview, Name: "interrupted", Execution: &taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusInterrupted, InterruptionReason: "supervisor disappeared"}},
		},
		Findings: []taskstate.ReviewFinding{
			{Type: taskstate.FindingTypeBlocking, Step: "completed", Title: "Blocker", Description: "Must fix."},
			{Type: taskstate.FindingTypeAdvisory, Step: "completed", Title: "Ordinary", Description: "Could improve."},
			{Type: taskstate.FindingTypeAdvisory, Step: "completed", Title: "Downgraded", Description: "No longer required.", DowngradeReason: "Safe to defer."},
			{Type: taskstate.FindingTypeAdvisory, Step: "completed", Title: "Waived", Description: "Do not reopen.", Waiver: "Accepted risk."},
			{Type: taskstate.FindingTypeAdvisory, Step: "completed", Title: "Manual", Description: "Already fixed.", AddressedManually: "Verified."},
			{Type: taskstate.FindingTypeSeparateTask, Step: "completed", Title: "Separate", Description: "Track later."},
			{Type: taskstate.FindingTypeAdvisory, Step: "interrupted", Title: "Audit-only", Description: "Must not be actionable."},
		},
	}

	indexes := taskstate.EligibleAdvisoryFindingIndexes(review)
	if !reflect.DeepEqual(indexes, []int{1, 2}) {
		t.Fatalf("eligible advisory indexes = %#v, want []int{1, 2}", indexes)
	}
}

func TestUntargetedBlockingFindingIndexes(t *testing.T) {
	review := taskstate.ReviewAttempt{
		Findings: []taskstate.ReviewFinding{
			{
				Type:        taskstate.FindingTypeBlocking,
				Title:       "Open",
				Description: "Needs follow-up.",
			},
			{
				Type:        taskstate.FindingTypeBlocking,
				Title:       "Waived",
				Description: "Accepted.",
				Waiver:      "Not relevant.",
			},
			{
				Type:            taskstate.FindingTypeAdvisory,
				Title:           "Downgraded",
				Description:     "Not blocking.",
				DowngradeReason: "False positive.",
			},
			{
				Type:                 taskstate.FindingTypeBlocking,
				Title:                "Targeted",
				Description:          "Already has follow-up.",
				TargetedByRunAttempt: 2,
			},
			{
				Type:              taskstate.FindingTypeBlocking,
				Title:             "Manually addressed",
				Description:       "Fixed directly.",
				AddressedManually: "Verified outside Orpheus.",
			},
			{
				Type:        taskstate.FindingTypeBlocking,
				Title:       "Second open",
				Description: "Also needs follow-up.",
			},
		},
	}

	indexes := taskstate.UntargetedBlockingFindingIndexes(review)
	if len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 5 {
		t.Fatalf("UntargetedBlockingFindingIndexes() = %#v, want []int{0, 5}", indexes)
	}
	if !taskstate.ReviewHasOpenBlockers(review) {
		t.Fatal("ReviewHasOpenBlockers() = false, want true")
	}
}

func TestUntargetedBlockingFindingIndexesForFollowUp(t *testing.T) {
	tests := []struct {
		name        string
		review      taskstate.ReviewAttempt
		wantIndexes []int
		wantOK      bool
	}{
		{
			name: "manual blocker is eligible",
			review: taskstate.ReviewAttempt{
				Steps: []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindManual, Name: "inspect"}},
				Findings: []taskstate.ReviewFinding{{
					Type:        taskstate.FindingTypeBlocking,
					Step:        "inspect",
					Title:       "Manual issue",
					Description: "Fix it.",
				}},
			},
			wantIndexes: []int{0},
			wantOK:      true,
		},
		{
			name: "unkept automated blocker is ineligible",
			review: taskstate.ReviewAttempt{
				Steps: []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindCheck, Name: "lint"}},
				Findings: []taskstate.ReviewFinding{{
					Type:        taskstate.FindingTypeBlocking,
					Step:        "lint",
					Title:       "Lint failed",
					Description: "Fix it.",
				}},
			},
			wantOK: false,
		},
		{
			name: "kept automated blocker is eligible",
			review: taskstate.ReviewAttempt{
				Step:                         "ai-review",
				AutomatedBlockerDecisionKept: true,
				Steps:                        []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review"}},
				Findings: []taskstate.ReviewFinding{{
					Type:        taskstate.FindingTypeBlocking,
					Step:        "ai-review",
					Title:       "AI blocker",
					Description: "Fix it.",
				}},
			},
			wantIndexes: []int{0},
			wantOK:      true,
		},
		{
			name: "manual gate promoted prior automated advisory is eligible",
			review: taskstate.ReviewAttempt{
				Step: "approval",
				Steps: []taskstate.ReviewStep{
					{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review"},
					{Kind: taskstate.ReviewStepKindManual, Name: "approval"},
				},
				Findings: []taskstate.ReviewFinding{{
					Type:        taskstate.FindingTypeBlocking,
					Step:        "ai-review",
					Title:       "Promoted advisory",
					Description: "Fix it.",
				}},
			},
			wantIndexes: []int{0},
			wantOK:      true,
		},
		{
			name: "targeted automated blocker is ignored",
			review: taskstate.ReviewAttempt{
				Steps: []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindCheck, Name: "lint"}},
				Findings: []taskstate.ReviewFinding{{
					Type:                 taskstate.FindingTypeBlocking,
					Step:                 "lint",
					Title:                "Lint failed",
					Description:          "Fix it.",
					TargetedByRunAttempt: 2,
				}},
			},
			wantIndexes: []int{},
			wantOK:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			indexes, ok := taskstate.UntargetedBlockingFindingIndexesForFollowUp(tt.review)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !reflect.DeepEqual(indexes, tt.wantIndexes) {
				t.Fatalf("indexes = %#v, want %#v", indexes, tt.wantIndexes)
			}
		})
	}
}

func TestFailedAutomatedFollowUpTargetsRemainEligible(t *testing.T) {
	review := taskstate.ReviewAttempt{
		Step:  "lint",
		Steps: []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindCheck, Name: "lint"}},
		Findings: []taskstate.ReviewFinding{{
			Type:                 taskstate.FindingTypeBlocking,
			Step:                 "lint",
			Title:                "Lint failed",
			Description:          "Fix it.",
			TargetedByRunAttempt: 2,
		}},
	}
	state := taskstate.TaskState{Runs: []taskstate.RunAttempt{{
		Attempt:        2,
		Status:         taskstate.RunStatusFailed,
		ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}},
	}}}

	indexes, eligible := taskstate.UntargetedBlockingFindingIndexesForFollowUpInState(state, review)
	if !eligible || !reflect.DeepEqual(indexes, []int{0}) {
		t.Fatalf("indexes = %#v, eligible %v; want failed target eligible", indexes, eligible)
	}
}

func TestStoreValidatesTaskStatePathComponents(t *testing.T) {
	store := newTestStore(t, time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC))

	_, err := store.Path("alpha", "../op-1")
	if err == nil || !strings.Contains(err.Error(), "task id") {
		t.Fatalf("path error = %v, want task id validation", err)
	}
	_, err = store.Load("alpha/other", "op-1")
	if err == nil || !strings.Contains(err.Error(), "repository id") {
		t.Fatalf("load error = %v, want repo id validation", err)
	}
}

func newTestStore(t *testing.T, times ...time.Time) taskstate.Store {
	t.Helper()

	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	index := 0
	clock := func() time.Time {
		if len(times) == 0 {
			return time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
		}
		if index >= len(times) {
			return times[len(times)-1]
		}
		value := times[index]
		index++
		return value
	}
	return taskstate.NewStoreWithClock(paths, clock)
}
