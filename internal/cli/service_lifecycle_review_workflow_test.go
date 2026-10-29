//go:build integration

package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"context"
	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"time"
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
	seedReviewBeforeCapturedContinuation(t, fixture)

	stdout, stderr := fixture.run("a\nVerified direct repair.\nw\nAccepted compatibility risk.\n", "task", "run", "op-main")

	is.Contains(stdout, "fresh pipeline")
	is.Contains(stdout, "Review passed for op-main")
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
	assertUnconsentedPublication(t, state)
}

func TestIntegrationWorkflowTaskReviewAutonomousFollowUpResumesCompatiblePiSession(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewDispatchFixture(t, "op-resume-session")
	paths := fixture.paths
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

	is.Contains(reviewStdout, "Review passed for op-resume-session")
	is.Contains(reviewStderr, "Autonomous review follow-up for op-resume-session targets review attempt 1 finding(s) 1.")
	must.Len(fixture.agent.launches, 2)
	must.Len(fixture.agent.contexts, 2)
	is.Contains(fixture.agent.contexts[0], "- Run attempt: 1")
	is.Contains(fixture.agent.contexts[1], "- Run attempt: 2")
	is.Contains(fixture.agent.contexts[1], "Required blocking findings:")
	is.Contains(fixture.agent.contexts[1], "call `orpheus agent done` exactly once for the current attempt")
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
	is.Nil(state.Runs[0].Execution.Launch, "initial implementations must not select a prior session")
	must.NotNil(state.Runs[0].Completion)
	is.Equal(implementation.Summary, state.Runs[0].Completion.Summary)
	must.NotNil(state.Runs[1].Completion)
	is.Equal(repair.Summary, state.Runs[1].Completion.Summary)
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

func seedReviewBeforeCapturedContinuation(t *testing.T, f *reviewWorkflowFixture) {
	t.Helper()
	capture := workflow.ImplementationService{Paths: f.paths, Store: workstate.NewStore(f.paths), Git: f.candidate}
	view, _ := f.loadFinalTask("op-main")
	_, err := capture.Capture(context.Background(), view.Runs[0].Address)
	require.NoError(t, err)
	prior, err := f.taskStore.StartReviewWithOptions("alpha", "op-main", taskstate.StartReviewOptions{Pipeline: "prior", Step: "lint", Contract: work.ReviewContract{PipelineKey: "prior", Slots: []work.ReviewSlot{{Kind: work.ReviewCheck, ConfigKey: "lint"}}}, Scope: work.ReviewScope{Requirements: "Prior task requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}}})
	require.NoError(t, err)
	_, err = f.taskStore.RecordReviewStep("alpha", "op-main", prior.Attempt, taskstate.RecordReviewStepOptions{Address: prior.Address, Kind: taskstate.ReviewStepKindCheck, Name: "lint", ExitCode: fixtureExitCode(7), ObservedAt: time.Now().UTC(), Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Step: "lint", Title: "Lint failure", Description: "Fix the lint failure."}, {Type: taskstate.FindingTypeBlocking, Step: "lint", Title: "Second blocker", Description: "Waive with an explicit reason."}}})
	require.NoError(t, err)
	continuation, err := f.taskStore.StartRun("alpha", "op-main", taskstate.StartRunOptions{Agent: "fixture-manual-continuation"})
	require.NoError(t, err)
	_, err = f.taskStore.CompleteImplementation(continuation.Address, taskstate.CompleteRunOptions{Summary: "fix: direct operator repair", Description: "An independently completed continuation before fresh review.", DetailedDescription: "Original feature content remains authoritative.", TechnicalExplanation: "Explicit synthetic continuation history."})
	require.NoError(t, err)
	_, err = f.taskStore.FinishObservedRun(continuation.Address, &work.ExitedProcess{Code: fixtureExitCode(0), At: time.Now().UTC()}, 0)
	require.NoError(t, err)
	_, err = capture.Capture(context.Background(), continuation.Address)
	require.NoError(t, err)
}
