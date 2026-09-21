//go:build integration

package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskReviewFreshBlockerDispositionsStartFreshCLIPipeline(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review fresh dispositions", "Resolve old blockers first.")
	paths := fixture.paths
	check := fixture.check("fresh-check", checkResult{stdout: "fresh pipeline\n"})
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": {{"kind": "check", "name": "unit", "command": check}},
	})
	seedReviewWithFindings(t, paths, "op-main", "standard", "lint", taskstate.ReviewStatusBlocked, []taskstate.ReviewFinding{
		{Type: taskstate.FindingTypeBlocking, Step: "lint", Title: "Lint failure", Description: "Fix the lint failure."},
		{Type: taskstate.FindingTypeBlocking, Step: "lint", Title: "Second blocker", Description: "Waive with an explicit reason."},
	})

	stdout, stderr := fixture.run("a\nVerified direct repair.\nw\nAccepted compatibility risk.\n", "task", "run", "op-main")

	is.Contains(stdout, "fresh pipeline")
	is.Contains(stdout, "Finalized op-main")
	is.Contains(stderr, "Open blocking findings from the latest review")
	is.Contains(stderr, "Manual address reason")
	is.Contains(stderr, "Waiver reason")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	must.Len(state.Reviews, 2)
	prior := state.Reviews[0]
	must.Len(prior.Findings, 2)
	is.Equal("Verified direct repair.", prior.Findings[0].AddressedManually)
	is.Equal("Accepted compatibility risk.", prior.Findings[1].Waiver)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(2, latest.Attempt)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	is.Empty(latest.Findings)
	is.NotEmpty(taskstate.FinalizationFacts(state).Commit)
}

func TestIntegrationWorkflowTaskReviewKeepsFailedFreshBlockerEligibleForFollowUp(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-main", "Review failed blocker", "Keep a failed blocker.")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": {{"kind": "check", "name": "unit", "command": "must-not-run"}},
	})
	prior := seedReviewWithFindings(t, paths, "op-main", "standard", "unit", taskstate.ReviewStatusFailed, []taskstate.ReviewFinding{
		{Type: taskstate.FindingTypeBlocking, Step: "unit", Title: "Unit failure", Description: "Fix the failing test."},
	})
	priorFinishedAt := *prior.FinishedAt

	stdout, stderr := fixture.run("k\n", "task", "run", "op-main")

	is.Empty(stdout)
	is.Contains(stderr, "Open blocking findings from the latest review")
	is.Contains(stderr, "Decision for finding 1")
	is.Empty(fixture.agent.launches)
	is.Empty(fixture.checkCalls)
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	must.Len(state.Reviews, 1)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	must.NotNil(latest.FinishedAt)
	is.True(latest.FinishedAt.Equal(priorFinishedAt))
	indexes, eligible := taskstate.UntargetedBlockingFindingIndexesForFollowUp(latest)
	is.True(latest.AutomatedBlockerDecisionKept)
	is.True(eligible)
	is.Equal([]int{0}, indexes)
}

func TestIntegrationWorkflowTaskReviewAutonomousFollowUpResumesCompatiblePiSession(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewDispatchFixture(t, "op-resume-session")
	paths := fixture.paths
	fixture.options.Environment["ORPHEUS_RESUME_SESSIONS"] = "1"
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": {
			{"kind": "manual", "name": "approval"},
			{"kind": "check", "name": "status", "command": fixture.candidateCheck()},
		},
	})
	fixture.budget(4)
	fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "pi-impl"}, map[string]agent.Profile{
		"pi-impl": {Harness: "pi", Model: "gpt-5"},
	})
	sessionPath := filepath.Join(testutil.CanonicalTempDir(t), "pi-session.jsonl")
	sessionContent := `{"type":"session","version":3,"id":"session-1","timestamp":"2026-07-07T10:00:00Z","cwd":"` + taskWorkflowRepoRoot + `"}
{"type":"message","id":"assistant","timestamp":"2026-07-07T10:00:01Z","message":{"role":"assistant","usage":{"input":10,"output":5,"totalTokens":15}}}
`
	must.NoError(os.WriteFile(sessionPath, []byte(sessionContent), 0o644))
	captures := 0
	fixture.options.Dependencies.CaptureUsage = func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		captures++
		if captures == 1 {
			is.Equal("pi", opts.Harness)
			is.Equal(taskWorkflowRepoRoot, opts.ExecutionDir)
			return taskstate.RecordRunUsageOptions{
				Session:      &taskstate.AgentSession{ID: "session-1", LogPath: sessionPath},
				Usage:        &taskstate.AgentUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
				UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched_pi_session"},
			}
		}
		return taskstate.RecordRunUsageOptions{UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureUnknown, Reason: "not checked by scenario"}}
	}
	implementation := aCompletion()
	repair := aRepairCompletion()
	fixture.agent.outcomes = append(fixture.agent.outcomes,
		semanticAgentOutcome{completion: &implementation},
		semanticAgentOutcome{completion: &repair, mutateCandidate: func() { fixture.candidate.contents = "pass\n" }},
	)

	runStdout, runStderr := fixture.run("", "task", "run", "--repo-root", "--agent", "pi-impl", "op-resume-session")
	is.Contains(runStdout, "Recorded completion for op-resume-session")
	is.Contains(runStderr, "Review for op-resume-session is waiting for manual step \"approval\"")
	reviewStdout, reviewStderr := fixture.run("a\nk\na\n", "task", "run", "op-resume-session")

	is.Contains(reviewStdout, "Published op-resume-session")
	is.Contains(reviewStderr, "Autonomous review follow-up for op-resume-session targets review attempt 1 finding(s) 1.")
	must.Len(fixture.agent.launches, 2)
	args := fixture.agent.launches[1].command.Args
	sessionArg := -1
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--session" {
			sessionArg = i
			break
		}
	}
	must.NotEqual(-1, sessionArg, "resumed command args: %#v", args)
	is.Equal(sessionPath, args[sessionArg+1])
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-resume-session.yaml"), &state))
	must.Len(state.Runs, 2)
	launch := state.Runs[1].Execution.Launch
	must.NotNil(launch)
	is.Equal(taskstate.AgentLaunchResumed, launch.Mode)
	is.Equal(1, launch.SourceRunAttempt)
	must.NotNil(launch.SourceSession)
	is.Equal("session-1", launch.SourceSession.ID)
	must.NotNil(launch.UsageBaseline)
	is.Equal(15, launch.UsageBaseline.TotalTokens)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
}

func seedReviewWithFindings(
	t *testing.T,
	paths state.Paths,
	taskID string,
	pipeline string,
	step string,
	status taskstate.ReviewStatus,
	findings []taskstate.ReviewFinding,
) taskstate.ReviewAttempt {
	t.Helper()
	store := taskstate.NewStore(paths)
	attempt, err := store.StartReviewWithOptions("alpha", taskID, taskstate.StartReviewOptions{Pipeline: pipeline, Step: step})
	require.NoError(t, err, "start review")
	_, err = store.RecordReviewStep("alpha", taskID, attempt.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindCheck, Name: step})
	require.NoError(t, err, "record review step")
	for _, finding := range findings {
		_, err = store.RecordReviewFinding("alpha", taskID, attempt.Attempt, finding)
		require.NoError(t, err, "record review finding")
	}
	finished, err := store.FinishReview("alpha", taskID, attempt.Attempt, status)
	require.NoError(t, err, "finish review")
	return finished
}
