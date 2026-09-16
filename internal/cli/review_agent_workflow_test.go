//go:build integration

package cli_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationTaskReviewAgentReviewStepLaunchesReviewerAndPassesWithoutFindings(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review agent", "Run attached reviewer.")
	paths := fixture.paths
	fixture.reviewers(semanticAgentOutcome{})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}}})
	stdout, _ := fixture.run("", "task", "run", "op-main")

	is.Contains(stdout, "Finalized op-main")
	must.Len(fixture.agent.launches, 1)
	launch := fixture.agent.launches[0]
	is.Equal(taskWorkflowRepoRoot, launch.dir)
	is.Equal("review", launch.environment["ORPHEUS_AGENT_PURPOSE"])
	is.Equal("1", launch.environment["ORPHEUS_REVIEW_ATTEMPT"])
	is.Equal("ai-review", launch.environment["ORPHEUS_REVIEW_STEP"])
	is.Contains(launch.environment["ORPHEUS_AGENT_PROMPT"], "You are an agent dispatched by Orpheus.")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Steps, 1)
	is.Equal("agent_review", latest.Steps[0].Kind)
	is.Equal("ai-review", latest.Steps[0].Name)
	must.NotNil(latest.Steps[0].Execution)
	is.Equal(taskstate.RunStatusSucceeded, latest.Steps[0].Execution.Status)
	is.Equal("review-agent", latest.Steps[0].Execution.Command)
	must.NotNil(latest.Steps[0].Execution.FinishedAt)
	is.GreaterOrEqual(latest.Steps[0].Execution.DurationMillis, int64(0))
	is.Equal(4242, latest.Steps[0].Execution.ChildPID)
	is.Equal(taskstate.UsageCaptureUnknown, latest.Steps[0].Execution.UsageCapture.Status)
	is.Equal("usage capture is not supported for harness -", latest.Steps[0].Execution.UsageCapture.Reason)
	must.Len(latest.Steps[0].Execution.Args, 1)
	is.Contains(latest.Steps[0].Execution.Args[0], "Reviewing op-main Ready for task done - ")
	is.Contains(latest.Steps[0].Execution.Args[0], "You are an agent dispatched by Orpheus.")
	is.Empty(latest.Findings)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationTaskReviewAgentReviewBlockingFindingStopsPipeline(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review agent blocker", "Record a blocker.")
	paths := fixture.paths
	fixture.reviewers(semanticAgentOutcome{findings: []taskstate.ReviewFinding{{Type: "blocking", Title: "Generated blocker", Description: "The review agent found a blocker.", SuggestedAction: "Fix the blocker."}}})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}, {"kind": "manual", "name": "approval"}}})
	headBefore := fixture.candidate.head
	stdout, stderr := fixture.run("", "task", "run", "op-main")

	is.Contains(stdout, "Recorded blocking review finding 1 for op-main.")
	is.Contains(stderr, "== Review step: ai-review (agent_review) ==")
	is.Contains(stderr, "Review blocked for op-main by agent_review \"ai-review\".")
	is.NotContains(stderr, "Review action")
	is.Equal(headBefore, fixture.candidate.head)

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Equal("ai-review", latest.Findings[0].Step)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationTaskReviewAgentReviewMixedAutomatedBlockerDecisions(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review agent blockers", "Record blockers.")
	paths := fixture.paths
	fixture.reviewers(semanticAgentOutcome{findings: []taskstate.ReviewFinding{{Type: "blocking", Title: "Keep blocker", Description: "Still blocks.", SuggestedAction: "Fix kept."}, {Type: "blocking", Title: "Downgrade blocker", Description: "Can be advisory.", SuggestedAction: "Document it."}, {Type: "blocking", Title: "Cancel blocker", Description: "False positive.", SuggestedAction: "Ignore it."}}})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}, {"kind": "manual", "name": "approval"}}})
	fixture.budget(1)
	stdout, stderr := fixture.run("k\nd\nNot required for this task.\nw\nFalse positive from reviewer.\n", "task", "run", "--pipeline", "standard", "op-main")

	is.Contains(stdout, "Recorded blocking review finding 1 for op-main.")
	is.Contains(stdout, "Recorded blocking review finding 2 for op-main.")
	is.Contains(stdout, "Recorded blocking review finding 3 for op-main.")
	is.Contains(stderr, "Automated blocking findings from step \"ai-review\"")
	is.Contains(stderr, "Review blocked for op-main by agent_review \"ai-review\".")
	is.NotContains(stderr, "◆ REVIEW STEP · approval (manual)")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.Len(latest.Findings, 3)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Empty(latest.Findings[0].Waiver)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[1].Type)
	is.Equal("Not required for this task.", latest.Findings[1].DowngradeReason)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[2].Type)
	is.Equal("False positive from reviewer.", latest.Findings[2].Waiver)
	showStdout, showStderr := fixture.run("", "task", "show", "review", "op-main", "1")
	is.Empty(showStderr)
	is.Contains(showStdout, "Title: Keep blocker")
	is.Contains(showStdout, "Resolution: open")
	is.Contains(showStdout, "Title: Downgrade blocker")
	is.Contains(showStdout, "Resolution: downgraded to advisory: Not required for this task.")
	is.Contains(showStdout, "Title: Cancel blocker")
	is.Contains(showStdout, "Resolution: waived: False positive from reviewer.")
	fixture.budget(2)
	fixture.repairExitsWithoutCompletion()

	_, runStderr := fixture.run("", "task", "run", "op-main")
	is.Contains(runStderr, "exited without completion")
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	must.Len(state.Runs, 2)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal([]int{0}, state.Runs[1].ReviewFollowUp.FindingIndexes)
	latest, ok = taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(2, latest.Findings[0].TargetedByRunAttempt)
	is.Zero(latest.Findings[1].TargetedByRunAttempt)
	is.Zero(latest.Findings[2].TargetedByRunAttempt)
}

func TestIntegrationTaskReviewPromotesAgentReviewAdvisoryAndTargetsFollowUp(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review agent advisory", "Promote advisory if needed.")
	paths := fixture.paths
	fixture.reviewers(semanticAgentOutcome{findings: []taskstate.ReviewFinding{{Type: "advisory", Title: "Generated advisory", Description: "The review agent found a risky edge case.", SuggestedAction: "Handle the edge case before publishing."}, {Type: "advisory", Title: "Second generated advisory", Description: "The review agent found an issue that needs attention.", SuggestedAction: "Address it before publishing."}}})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}, {"kind": "manual", "name": "approval"}}})
	fixture.budget(1)
	headBefore := fixture.candidate.head
	firstInput := strings.Join([]string{
		"p",
		"k",
		"p",
		"",
	}, "\n")
	firstStdout, firstStderr, err := fixture.runError(firstInput, "task", "run", "op-main")
	must.NoError(err, "execute task run\nstderr: %s", firstStderr)

	is.Contains(firstStdout, "Recorded advisory review finding 1 for op-main.")
	is.Contains(firstStdout, "Recorded advisory review finding 2 for op-main.")
	is.Contains(firstStderr, "Promoted advisory finding 2 to blocking.")
	is.Contains(firstStderr, "Review for op-main is waiting for manual step \"approval\" because manual review input is unavailable.")
	var waiting taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &waiting))
	paused, ok := taskstate.LatestReview(waiting)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusWaitingForManual, paused.Status)
	must.Len(paused.Findings, 2)
	is.Equal(taskstate.FindingTypeBlocking, paused.Findings[1].Type)

	input := strings.Join([]string{
		"v",
		"Manual advisory",
		"The human reviewer added a non-blocking note.",
		"Keep this in mind later.",
		"f",
		"",
	}, "\n")
	stdout, stderr := fixture.run(input, "task", "run", "op-main")

	is.Empty(stdout)
	priorStart := strings.Index(firstStderr, "▲ PRIOR UNRESOLVED ADVISORIES")
	must.NotEqual(-1, priorStart)
	menuStart := strings.Index(firstStderr[priorStart:], "Review action [")
	must.NotEqual(-1, menuStart)
	priorSummary := firstStderr[priorStart : priorStart+menuStart]
	is.Contains(priorSummary, "Finding 1 · ai-review · advisory\nGenerated advisory")
	is.Contains(priorSummary, "Finding 2 · ai-review · advisory\nSecond generated advisory")
	is.Contains(priorSummary, "DESCRIPTION\nThe review agent found a risky edge case.")
	is.Contains(priorSummary, "SUGGESTED ACTION\nHandle the edge case before publishing.")
	is.Contains(firstStderr, "Review action [a=approve, b=block, p=review advisories")
	is.Contains(firstStderr, "Advisory finding 1 (ai-review): Generated advisory")
	is.Contains(firstStderr, "Description: The review agent found a risky edge case.")
	is.Contains(firstStderr, "Suggested action: Handle the edge case before publishing.")
	is.Contains(firstStderr, "Advisory finding 1 [k=keep advisory, p=promote, q=return]")
	is.Contains(firstStderr, "Advisory finding 2 (ai-review): Second generated advisory")
	is.Contains(firstStderr, "Description: The review agent found an issue that needs attention.")
	is.Contains(firstStderr, "Suggested action: Address it before publishing.")
	is.Contains(stderr, "Resuming review attempt 1 at manual step \"approval\".")
	is.Contains(stderr, "▲ OPEN BLOCKERS FROM EARLIER STEPS")
	is.Contains(stderr, "Finding 2 · ai-review · blocking\nSecond generated advisory")
	is.Contains(stderr, "DESCRIPTION\nThe review agent found an issue that needs attention.")
	is.Contains(stderr, "SUGGESTED ACTION\nAddress it before publishing.")
	is.Contains(stderr, "Review action [f=finish/block, b=block, p=review advisories, v=advisory, t=task, q=abort]")
	is.NotContains(stderr, "Finding 3 · approval · advisory\nManual advisory")
	is.Contains(stderr, "Review blocked for op-main.")
	is.Equal(headBefore, fixture.candidate.head)

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.Len(latest.Findings, 3)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[0].Type)
	is.Equal("Generated advisory", latest.Findings[0].Title)
	is.Equal("ai-review", latest.Findings[0].Step)
	is.Empty(latest.Findings[0].DowngradeReason)
	is.Empty(latest.Findings[0].Waiver)
	is.Zero(latest.Findings[0].TargetedByRunAttempt)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[1].Type)
	is.Equal("Second generated advisory", latest.Findings[1].Title)
	is.Equal("ai-review", latest.Findings[1].Step)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[2].Type)
	is.Equal("approval", latest.Findings[2].Step)
	is.Empty(taskstate.FinalizationFacts(state).Commit)

	showStdout, showStderr := fixture.run("", "task", "show", "review", "op-main", "1")
	is.Empty(showStderr)
	is.Contains(showStdout, "Status: blocked")
	is.Contains(showStdout, "Type: blocking")
	is.Contains(showStdout, "Title: Generated advisory")
	is.Contains(showStdout, "Resolution: open")
	is.Contains(showStdout, "Next step: autonomous review attempts are exhausted")

	fixture.budget(2)
	fixture.repairExitsWithoutCompletion()
	_, runStderr := fixture.run("", "task", "run", "op-main")
	is.Contains(runStderr, "exited without completion")
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok = taskstate.LatestReview(state)
	must.True(ok)
	must.Len(latest.Findings, 3)
	is.Zero(latest.Findings[0].TargetedByRunAttempt)
	is.Equal(2, latest.Findings[1].TargetedByRunAttempt)
	is.Zero(latest.Findings[2].TargetedByRunAttempt)
	must.Len(state.Runs, 2)
	must.NotNil(state.Runs[1].ReviewFollowUp)
	is.Equal(latest.Attempt, state.Runs[1].ReviewFollowUp.ReviewAttempt)
	is.Equal([]int{1}, state.Runs[1].ReviewFollowUp.FindingIndexes)
}

func TestIntegrationTaskReviewAgentReviewNonZeroExitMarksOperationalFailure(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review agent failure", "Fail operationally.")
	paths := fixture.paths
	fixture.reviewers(semanticAgentOutcome{err: errors.New("exit status 7")})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}}})
	_, _, err := fixture.runError("", "task", "run", "op-main")

	must.Error(err)
	is.ErrorContains(err, "run agent_review step \"ai-review\"")

	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusFailed, latest.Status)
	must.Len(latest.Steps, 1)
	is.Equal("agent_review", latest.Steps[0].Kind)
	is.Empty(latest.Findings)
	is.Empty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationReviewMutationFailsBeforePublication(t *testing.T) {
	fixture := newReviewWorkflowFixture(t, "op-mutated", "Candidate", "Keep these changes.")
	fixture.reviewers(semanticAgentOutcome{mutateCandidate: func() { fixture.candidate.contents = "reviewer mutation" }})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}}})

	_, _, err := fixture.runError("", "task", "run", "op-mutated")

	require.ErrorContains(t, err, "restored the pre-step snapshot")
	loaded, task := fixture.loadFinalTask("op-mutated")
	require.Len(t, loaded.Reviews, 1)
	assert.Equal(t, taskstate.ReviewStatusFailed, loaded.Reviews[0].Status)
	assert.Equal(t, "reviewed\n", fixture.candidate.contents)
	assert.Empty(t, fixture.candidate.commits)
	assert.Empty(t, fixture.candidate.pushes)
	assertTaskNotPublished(t, loaded, task)
}
