//go:build integration

package cli_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowAgentDoneRejectsInvalidCompletionWithoutEffects(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing description", []string{"--pr-description", "PR body.", "--technical-explanation", "Technical explanation."}, "commit body is required"},
		{"missing PR description", []string{"--commit-body", "Commit body.", "--technical-explanation", "Technical explanation."}, "PR description is required"},
		{"multiple PR description sources", []string{"--commit-body", "Commit body.", "--pr-description", "Inline body.", "--pr-description-file", "/fixture/body.md", "--technical-explanation", "Technical explanation."}, "use exactly one of --pr-description or --pr-description-file"},
		{"removed details flag", []string{"--commit-body", "Commit body.", "--details", "Old details.", "--pr-description", "PR body.", "--technical-explanation", "Technical explanation."}, "unknown flag: --details"},
		{"multiple local body sources", []string{"--commit-body", "inline", "--commit-body-file", "/fixture/body.md", "--technical-explanation", "why"}, "use exactly one of --commit-body or --commit-body-file"},
		{"multiple publication body sources", []string{"--commit-body", "local", "--publication-commit-body", "", "--publication-commit-body-file", "/fixture/body.md"}, "use exactly one of --publication-commit-body or --publication-commit-body-file"},
		{"empty publication body", []string{"--commit-body", "local", "--publication-commit-body", " "}, "publication commit body is required"},
		{"empty PR description", []string{"--commit-body", "local", "--pr-description", " "}, "PR description is required"},
		{"empty publication subject", []string{"--commit-body", "local", "--publication-commit-subject", " "}, "publication commit subject is required"},
		{"obsolete summary", []string{"--summary", "old"}, "unknown flag: --summary"},
		{"obsolete description", []string{"--description", "old"}, "unknown flag: --description"},
		{"obsolete PR flag", []string{"--detailed-description", "old"}, "unknown flag: --detailed-description"},
		{"missing technical explanation", []string{"--commit-body", "Commit body.", "--pr-description", "PR body."}, "technical explanation is required"},
		{"multiple technical sources", []string{"--commit-body", "Commit body.", "--pr-description", "PR body.", "--technical-explanation", "Inline explanation.", "--technical-explanation-file", "/fixture/technical.md"}, "use exactly one of --technical-explanation or --technical-explanation-file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newAgentCompletionFixture(t, "op-main", false)
			beforeState, _ := f.loadFinalTask("op-main")
			before := f.backend.tasks["op-main"].Clone()
			args := append([]string{"agent", "done", "--commit-subject", "Completion"}, test.args...)

			stdout, stderr, err := f.execute(args...)

			require.ErrorContains(t, err, test.want)
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
			f.assertUnpublished("op-main")
			state, item := f.loadFinalTask("op-main")
			assert.Equal(t, beforeState, state)
			assert.Equal(t, before, item)
		})
	}
}

func completeAgentTestRun(t *testing.T, runStore taskstate.Store) taskstate.RunAttempt {
	t.Helper()

	latest, ok, err := runStore.LatestRun("alpha", "op-main")
	require.NoError(t, err)
	require.True(t, ok)
	completed, err := seedFixtureHandoff(runStore, "alpha", "op-main", latest.Attempt, taskstate.CompleteRunOptions{
		CommitSubject:        "First summary",
		CommitBody:           "First details.",
		PRDescription:        "Detailed PR body.",
		TechnicalExplanation: "Technical explanation.",
	})
	require.NoError(t, err)
	return completed
}

func assertRepeatedAgentDoneOutput(t *testing.T, stdout string) {
	t.Helper()

	is := assert.New(t)
	is.Contains(stdout, "already recorded")
	is.Contains(stdout, "Do not run `orpheus agent done` again")
	is.Contains(stdout, "first completion remains authoritative")
}

func assertRepeatedAgentCompletion(t *testing.T, runStore taskstate.Store, attempt taskstate.RunAttempt) {
	t.Helper()

	is := assert.New(t)
	must := require.New(t)
	latest, ok, err := runStore.LatestRun("alpha", "op-main")
	must.NoError(err)
	must.True(ok)
	must.NotNil(latest.Completion)
	is.Equal("First summary", latest.Completion.CommitSubject)
	is.Equal("First details.", latest.Completion.CommitBody)
	snapshot, err := runStore.Load("alpha", "op-main")
	must.NoError(err)
	is.Equal("Detailed PR body.", snapshot.Work.Workflows[0].Publication.PRDescription)
	is.Equal("Technical explanation.", latest.Completion.TechnicalExplanation)
	events, err := runStore.Events("alpha", "op-main")
	must.NoError(err)
	must.NotEmpty(events)
	last := events[len(events)-1]
	is.Equal(taskstate.EventCompletionRecorded, last.Type)
	is.Equal(attempt.Attempt, last.Attempt)
	is.Len(events, 3, "preparation, admission and first handoff only; repeats append nothing")
}
