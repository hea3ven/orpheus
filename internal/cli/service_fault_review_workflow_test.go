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

func TestIntegrationWorkflowTaskReviewUsageWriteErrorDoesNotFailSuccessfulAutonomousFollowUp(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-usage-fault", "Review usage fault", "Repair blockers even if usage persistence fails.")
	paths := fixture.paths
	fixture.pipelines("standard", map[string][]map[string]any{
		"standard": {{"kind": "check", "name": "status", "command": fixture.candidateCheck()}},
	})
	fixture.budget(4)
	fixture.completingRepairs("repairer", 1, true)
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
	must.NoError(state.SetMemoryDataWriteError(paths, func(relativeDataPath string, proposedYAML []byte) error {
		if failOnce || filepath.ToSlash(relativeDataPath) != "repos/alpha/tasks/op-usage-fault.yaml" {
			return nil
		}
		var proposed taskstate.TaskState
		if err := yaml.Unmarshal(proposedYAML, &proposed); err != nil {
			return err
		}
		for _, run := range proposed.Runs {
			if run.Execution.Usage != nil && run.Execution.Usage.TotalTokens == 333 &&
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
	is.Contains(err.Error(), "record run usage")
	is.Contains(stdout, "Recorded completion for op-usage-fault")
	is.NotContains(stdout, "Published op-usage-fault")
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
	is.Equal(taskstate.RunStatusSucceeded, latestRun.Status)
	must.NotNil(latestRun.Completion)
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
}
