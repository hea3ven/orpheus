//go:build integration

package cli_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestIntegrationWorkflowTaskReviewUsageWriteErrorPreservesFollowUpOutcomeAndTerminalAttempts(t *testing.T) {
	for _, test := range []struct {
		name         string
		runErr       error
		failTerminal bool
	}{
		{name: "successful repair"},
		{name: "failed repair", runErr: errors.New("repair failed")},
		{name: "failed repair and terminal write", runErr: errors.New("repair failed"), failTerminal: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newReviewWorkflowFixture(t, "op-usage-fault", "Review usage fault", "Repair blockers even if usage persistence fails.")
			paths := fixture.paths
			fixture.pipelines("standard", map[string][]map[string]any{
				"standard": {{"kind": "check", "name": "status", "command": fixture.candidateCheck()}},
			})
			fixture.budget(4)
			fixture.completingRepairs("repairer", 1, true)
			if test.runErr != nil {
				fixture.agent.outcomes[0] = semanticAgentOutcome{err: test.runErr}
			}
			fixture.options.Dependencies.CaptureUsage = func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
				is.Contains(opts.SessionName, "op-usage-fault")
				return taskstate.RecordRunUsageOptions{
					Session: &taskstate.AgentSession{ID: "follow-up-session", LogPath: "/fixture/follow-up-session.jsonl"},
					Usage: &taskstate.AgentUsage{
						InputTokens: 111, OutputTokens: 222, TotalTokens: 333,
					},
					UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "semantic_follow_up_usage"},
				}
			}
			usageWriteErr := errors.New("injected usage write failure")
			failOnce := false
			terminalAttempted := false
			terminalWriteErr := errors.New("injected terminal write failure")
			must.NoError(state.SetMemoryDataWriteError(paths, func(relativeDataPath string, proposedYAML []byte) error {
				if filepath.ToSlash(relativeDataPath) != "repos/alpha/tasks/op-usage-fault.yaml" {
					return nil
				}
				var proposed taskstate.TaskState
				if err := yaml.Unmarshal(proposedYAML, &proposed); err != nil {
					return err
				}
				if len(proposed.Runs) == 2 && proposed.Runs[1].Status == taskstate.RunStatusFailed {
					terminalAttempted = true
					if test.failTerminal {
						return terminalWriteErr
					}
				}
				for _, run := range proposed.Runs {
					if !failOnce && run.Execution.Usage != nil && run.Execution.Usage.TotalTokens == 333 &&
						run.Execution.UsageCapture.Reason == "semantic_follow_up_usage" {
						failOnce = true
						return usageWriteErr
					}
				}
				return nil
			}))
			t.Cleanup(func() { require.NoError(t, state.SetMemoryDataWriteError(paths, nil)) })

			stdout, stderr, err := fixture.runError("k\n", "task", "run", "op-usage-fault")

			must.Error(err)
			is.ErrorIs(err, usageWriteErr)
			is.Contains(err.Error(), "record implementation usage")
			if test.runErr == nil {
				is.Contains(stdout, "Recorded completion for op-usage-fault")
			} else {
				is.ErrorIs(err, test.runErr)
				is.NotContains(stdout, "Recorded completion for op-usage-fault")
				is.True(terminalAttempted, "usage persistence failure must not skip terminal bookkeeping")
				if test.failTerminal {
					is.ErrorIs(err, terminalWriteErr)
				}
			}
			is.NotContains(stdout, "Review passed for op-usage-fault")
			is.Contains(stderr, "Review blocked for op-usage-fault by check \"status\".")
			is.Contains(stderr, "Autonomous review follow-up for op-usage-fault targets review attempt 1 finding(s) 1.")
			must.True(failOnce, "usage write fault was not consumed")
			must.Len(fixture.agent.launches, 1)

			var taskState taskstate.TaskState
			must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-usage-fault.yaml"), &taskState))
			must.Len(taskState.Runs, 2)
			latestRun, ok := taskstate.LatestRun(taskState)
			must.True(ok)
			is.Equal(2, latestRun.Attempt)
			switch {
			case test.failTerminal:
				is.Equal(taskstate.RunStatusRunning, latestRun.Status)
				is.Nil(latestRun.Completion)
			case test.runErr != nil:
				is.Equal(taskstate.RunStatusFailed, latestRun.Status)
				is.Nil(latestRun.Completion)
			default:
				is.Equal(taskstate.RunStatusSucceeded, latestRun.Status)
				must.NotNil(latestRun.Completion)
			}
			is.Nil(latestRun.Execution.Usage)
			is.Empty(latestRun.Execution.UsageCapture.Reason)
			must.NotNil(latestRun.ReviewFollowUp)
			is.Equal(1, latestRun.ReviewFollowUp.ReviewAttempt)
			is.Equal([]int{0}, latestRun.ReviewFollowUp.FindingIndexes)
			must.Len(taskState.Reviews, 1)
			latestReview, ok := taskstate.LatestReview(taskState)
			must.True(ok)
			is.Equal(taskstate.ReviewStatusBlocked, latestReview.Status)
			must.Len(latestReview.Findings, 1)
			is.Equal(2, latestReview.Findings[0].TargetedByRunAttempt)
			is.Empty(taskstate.FinalizationFacts(taskState).Commit)
			is.Nil(taskState.Finalization)
		})
	}
}

func TestIntegrationWorkflowReviewerUsageWriteFailureRetainsProcessAndTerminalErrors(t *testing.T) {
	for _, failTerminal := range []bool{false, true} {
		name := "review failure recorded"
		if failTerminal {
			name = "review failure write also fails"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newReviewWorkflowFixture(t, "op-review-fault", "Candidate", "Review this work.")
			processErr := errors.New("reviewer failed")
			usageErr := errors.New("execution usage write failed")
			terminalErr := errors.New("review terminal write failed")
			fixture.reviewers(semanticAgentOutcome{err: processErr})
			fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "review"}}})
			captures := 0
			fixture.options.Dependencies.CaptureUsage = func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
				captures++
				return aCapturedPiUsage()
			}
			terminalAttempted := false
			require.NoError(t, state.SetMemoryDataWriteError(fixture.paths, func(path string, data []byte) error {
				if filepath.ToSlash(path) != "repos/alpha/tasks/op-review-fault.yaml" {
					return nil
				}
				var proposed taskstate.TaskState
				if err := yaml.Unmarshal(data, &proposed); err != nil {
					return err
				}
				latest, ok := taskstate.LatestReview(proposed)
				if !ok {
					return nil
				}
				if latest.Status == taskstate.ReviewStatusFailed {
					terminalAttempted = true
					if failTerminal {
						return terminalErr
					}
				}
				if len(latest.Steps) == 1 && latest.Steps[0].Execution != nil && latest.Steps[0].Execution.Usage != nil {
					return usageErr
				}
				return nil
			}))
			t.Cleanup(func() { require.NoError(t, state.SetMemoryDataWriteError(fixture.paths, nil)) })

			_, _, err := fixture.runError("", "task", "run", "op-review-fault")

			require.ErrorIs(t, err, processErr)
			require.ErrorIs(t, err, usageErr)
			assert.True(t, terminalAttempted)
			assert.Equal(t, 1, captures)
			if failTerminal {
				assert.ErrorIs(t, err, terminalErr)
			}
			loaded, task := fixture.loadFinalTask("op-review-fault")
			require.Len(t, loaded.Reviews, 1)
			if failTerminal {
				assert.Equal(t, taskstate.ReviewStatusRunning, loaded.Reviews[0].Status)
			} else {
				assert.Equal(t, taskstate.ReviewStatusFailed, loaded.Reviews[0].Status)
			}
			assertTaskNotPublished(t, loaded, task)
		})
	}
}
