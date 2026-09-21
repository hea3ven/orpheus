//go:build integration

package cli_test

import (
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/require"
)

func recordTaskShowReviewAttempt(
	t *testing.T,
	store taskstate.Store,
	now *time.Time,
	status taskstate.ReviewStatus,
) {
	t.Helper()

	must := require.New(t)
	reviewAttempt, err := store.StartReviewWithOptions("alpha", "op-review", taskstate.StartReviewOptions{
		Pipeline: "local",
		Step:     "manual",
	})
	must.NoError(err)
	*now = now.Add(time.Minute)
	_, err = store.FinishReview(
		"alpha",
		"op-review",
		reviewAttempt.Attempt,
		status,
	)
	must.NoError(err)
	*now = now.Add(time.Minute)
}

func seedTaskShowReviewState(t *testing.T, paths state.Paths, repoPath string) {
	t.Helper()

	must := require.New(t)
	runStore := taskstate.NewStore(paths)
	oldReview := recordCreatedReviewFollowUp(t, runStore)
	_, err := runStore.FinishReview("alpha", "op-main", oldReview.Attempt, taskstate.ReviewStatusPassed)
	must.NoError(err)

	latestReview := recordMixedReviewFindings(t, runStore)
	latestReview, err = runStore.MarkReviewAutomatedBlockerDecisionKept("alpha", "op-main", latestReview.Attempt)
	must.NoError(err)
	_, err = runStore.FinishReview("alpha", "op-main", latestReview.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)
	followUpRun, err := runStore.StartRun("alpha", "op-main", taskstate.StartRunOptions{
		Agent:    "codex",
		Branch:   "main",
		Worktree: repoPath,
	})
	must.NoError(err)
	_, err = runStore.TargetReviewFindings("alpha", "op-main", latestReview.Attempt, []int{1}, followUpRun.Attempt)
	must.NoError(err)
}

func recordCreatedReviewFollowUp(t *testing.T, runStore taskstate.Store) taskstate.ReviewAttempt {
	t.Helper()

	must := require.New(t)
	oldReview, err := runStore.StartReviewWithOptions("alpha", "op-main", taskstate.StartReviewOptions{
		Pipeline: "manual",
		Step:     "manual",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewStep("alpha", "op-main", oldReview.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "manual",
		Name: "manual",
	})
	must.NoError(err)
	_, err = runStore.RecordReviewFinding("alpha", "op-main", oldReview.Attempt, taskstate.ReviewFinding{
		Type:        taskstate.FindingTypeSeparateTask,
		Title:       "Older cleanup",
		Description: "Track old cleanup separately.",
		Step:        "manual",
		TaskProposal: taskstate.ReviewTaskProposal{
			Title:              "Older cleanup",
			Description:        "Clean up old code.",
			AcceptanceCriteria: "Cleanup is tested.",
		},
	})
	must.NoError(err)
	_, err = runStore.RecordReviewFindingCreatedTask("alpha", "op-main", oldReview.Attempt, 0, "op-41")
	must.NoError(err)
	return oldReview
}

func recordMixedReviewFindings(t *testing.T, runStore taskstate.Store) taskstate.ReviewAttempt {
	t.Helper()

	must := require.New(t)
	latestReview, err := runStore.StartReviewWithOptions("alpha", "op-main", taskstate.StartReviewOptions{
		Pipeline: "quality",
		Step:     "unit-tests",
	})
	must.NoError(err)
	exitCode := 1
	_, err = runStore.RecordReviewStep("alpha", "op-main", latestReview.Attempt, taskstate.RecordReviewStepOptions{
		Kind:     "check",
		Name:     "unit-tests",
		ExitCode: &exitCode,
	})
	must.NoError(err)
	_, err = runStore.RecordReviewStep("alpha", "op-main", latestReview.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "agent_review",
		Name: "ai-review",
	})
	must.NoError(err)
	for _, finding := range mixedReviewFindings() {
		_, err = runStore.RecordReviewFinding("alpha", "op-main", latestReview.Attempt, finding)
		must.NoError(err)
	}
	_, err = runStore.RecordReviewFindingCreatedTask("alpha", "op-main", latestReview.Attempt, 3, "op-42")
	must.NoError(err)
	return latestReview
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
