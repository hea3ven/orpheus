//go:build integration

package cli_test

import (
	workmodel "github.com/hea3ven/orpheus/internal/work"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/require"
)

// The reader owner supplies complete synthetic history rather than pretending
// aggregate statuses are native observations or approvals.
func seedTaskShowReviewState(t *testing.T, paths state.Paths, repoPath string) {
	t.Helper()
	snapshot := testutil.ReviewableWork("alpha", "op-main", repoPath, "main")
	w := &snapshot.Workflows[0]
	at := time.Date(2026, 1, 1, 0, 0, 10, 0, time.UTC)
	old := workmodel.Finding{Title: "Older cleanup", Classification: workmodel.FindingSeparateTask, Description: "Track old cleanup separately.", Proposal: &workmodel.FollowUpProposal{Title: "Older cleanup", Description: "Clean up old code.", AcceptanceCriteria: "Cleanup is tested."}, CreatedTask: &workmodel.CreatedFollowUp{TaskID: "op-41", ObservedAt: at}}
	w.ReviewContracts = []workmodel.ReviewContract{{PipelineKey: "manual", Slots: []workmodel.ReviewSlot{{Kind: workmodel.ReviewManual, ConfigKey: "manual"}}}, {PipelineKey: "quality", Slots: []workmodel.ReviewSlot{{Kind: workmodel.ReviewCheck, ConfigKey: "unit-tests"}, {Kind: workmodel.ReviewAgent, ConfigKey: "ai-review"}}}}
	scope := workmodel.ReviewScope{Requirements: "Synthetic task requirements", Output: workmodel.OutputRef{Phase: 1}, Destination: &workmodel.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}}
	authored := mixedReviewFindings()
	findings := make([]workmodel.Finding, len(authored))
	for i, f := range authored {
		findings[i] = workmodel.Finding{Title: f.Title, Classification: workmodel.FindingClassification(f.Type), Description: f.Description, SuggestedAction: f.SuggestedAction}
		if f.Type == taskstate.FindingTypeBlocking {
			findings[i].Dispositions = []workmodel.FindingDisposition{&workmodel.RepairRequired{}, &workmodel.RepairRequired{At: at.Add(2 * time.Second)}}
		}
		if f.Waiver != "" {
			findings[i].Dispositions = append(findings[i].Dispositions, &workmodel.WaivedFinding{Waiver: workmodel.Waiver{At: at.Add(3 * time.Second), Reason: f.Waiver}})
		}
		if !f.TaskProposal.IsZero() {
			findings[i].Proposal = &workmodel.FollowUpProposal{Title: f.TaskProposal.Title, Description: f.TaskProposal.Description, AcceptanceCriteria: f.TaskProposal.AcceptanceCriteria}
			findings[i].CreatedTask = &workmodel.CreatedFollowUp{TaskID: "op-42", ObservedAt: at.Add(3 * time.Second)}
		}
	}
	code := 0
	checkCode := 1
	w.Phases = append(w.Phases, &workmodel.ReviewPhase{Contract: 0, Scope: scope, StartedAt: at, Steps: []workmodel.ReviewStep{&workmodel.ManualReviewStep{Slot: 0, Findings: []workmodel.Finding{old}, Decision: &workmodel.ApprovedReview{At: at}}}}, &workmodel.ReviewPhase{Contract: 1, Scope: scope, StartedAt: at.Add(time.Second), Steps: []workmodel.ReviewStep{&workmodel.CheckStep{Slot: 0, Result: &workmodel.CheckResult{ExitCode: &checkCode, FinishedAt: at.Add(time.Second)}, Findings: findings[:1]}, &workmodel.AgentReviewStep{Slot: 1, Execution: workmodel.AgentExecution{StartedAt: at.Add(time.Second), Process: &workmodel.ExitedProcess{Code: &code, At: at.Add(2 * time.Second)}}, Report: &workmodel.ReviewReport{Findings: findings[1:]}}}}, &workmodel.ImplementationPhase{Input: &workmodel.OutputRef{Phase: 1}, TargetFindings: []workmodel.FindingRef{{Phase: 3, Step: 1, Finding: 0}}, Execution: workmodel.AgentExecution{Profile: "codex", StartedAt: at.Add(4 * time.Second), Process: &workmodel.RunningProcess{}}})
	_, err := workstate.NewStore(paths).Save(snapshot, 0)
	require.NoError(t, err)
}

func mixedReviewFindings() []taskstate.ReviewFinding {
	return []taskstate.ReviewFinding{
		{
			Type:            taskstate.FindingTypeBlocking,
			Title:           "Tests fail",
			Description:     "make test fails.",
			Step:            "unit-tests",
			SuggestedAction: "Fix failing tests.",
		},
		{
			Type:            taskstate.FindingTypeBlocking,
			Title:           "Race condition",
			Description:     "The update path can race.",
			Step:            "ai-review",
			SuggestedAction: "Guard the shared state.",
		},
		{
			Type:            taskstate.FindingTypeBlocking,
			Title:           "Known limitation",
			Description:     "This is accepted for the MVP.",
			Step:            "ai-review",
			SuggestedAction: "Document the limitation.",
			Waiver:          "Accepted risk for now.",
		},
		{
			Type:        taskstate.FindingTypeSeparateTask,
			Title:       "Extract helper",
			Description: "A helper would reduce duplication.",
			Step:        "ai-review",
			TaskProposal: taskstate.ReviewTaskProposal{
				Title:              "Extract helper",
				Description:        "Extract the repeated helper.",
				AcceptanceCriteria: "Helper has focused tests.",
			},
		},
	}
}

func clockSequence(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if len(times) == 0 {
			return time.Now().UTC()
		}
		if index >= len(times) {
			return times[len(times)-1]
		}
		value := times[index]
		index++
		return value
	}
}
