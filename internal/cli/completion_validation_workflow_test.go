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
		{"missing description", []string{"--detailed-description", "PR body.", "--technical-explanation", "Technical explanation."}, `required flag(s) "description" not set`},
		{"missing detailed description", []string{"--description", "Commit body.", "--technical-explanation", "Technical explanation."}, "detailed description is required"},
		{"multiple detailed sources", []string{"--description", "Commit body.", "--detailed-description", "Inline body.", "--detailed-description-file", "/fixture/body.md", "--technical-explanation", "Technical explanation."}, "use exactly one of --detailed-description or --detailed-description-file"},
		{"removed details flag", []string{"--description", "Commit body.", "--details", "Old details.", "--detailed-description", "PR body.", "--technical-explanation", "Technical explanation."}, "unknown flag: --details"},
		{"missing technical explanation", []string{"--description", "Commit body.", "--detailed-description", "PR body."}, "technical explanation is required"},
		{"multiple technical sources", []string{"--description", "Commit body.", "--detailed-description", "PR body.", "--technical-explanation", "Inline explanation.", "--technical-explanation-file", "/fixture/technical.md"}, "use exactly one of --technical-explanation or --technical-explanation-file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFinalizationFixture(t, "op-main")
			before := f.backend.tasks["op-main"].Clone()
			args := append([]string{"agent", "done", "--summary", "Completion"}, test.args...)

			stdout, stderr, err := f.execute(args...)

			require.ErrorContains(t, err, test.want)
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
			f.assertUnpublished("op-main")
			state, item := f.loadFinalTask("op-main")
			assert.Empty(t, state.Runs)
			assert.Equal(t, before, item)
		})
	}
}

func completeAgentTestRun(t *testing.T, runStore taskstate.Store) taskstate.RunAttempt {
	t.Helper()

	latest, ok, err := runStore.LatestRun("alpha", "op-main")
	require.NoError(t, err)
	require.True(t, ok)
	completed, err := runStore.CompleteRun("alpha", "op-main", latest.Attempt, taskstate.CompleteRunOptions{
		Summary:              "First summary",
		Description:          "First details.",
		DetailedDescription:  "Detailed PR body.",
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
	is.Contains(stdout, "local diagnostic")
}

func assertRepeatedAgentCompletion(t *testing.T, runStore taskstate.Store, attempt taskstate.RunAttempt) {
	t.Helper()

	is := assert.New(t)
	must := require.New(t)
	latest, ok, err := runStore.LatestRun("alpha", "op-main")
	must.NoError(err)
	must.True(ok)
	must.NotNil(latest.Completion)
	is.Equal("First summary", latest.Completion.Summary)
	is.Equal("First details.", latest.Completion.Description)
	is.Equal("Detailed PR body.", latest.Completion.DetailedDescription)
	is.Equal("Technical explanation.", latest.Completion.TechnicalExplanation)
	events, err := runStore.Events("alpha", "op-main")
	must.NoError(err)
	must.NotEmpty(events)
	last := events[len(events)-1]
	is.Equal(taskstate.EventCompletionRepeated, last.Type)
	is.Equal(attempt.Attempt, last.Attempt)
	is.Equal("Second summary", last.RequestedSummary)
	is.Equal("Second details.", last.RequestedDescription)
	is.Equal("Second detailed PR body.", last.RequestedDetailedDescription)
	is.Equal("Second technical explanation.", last.RequestedTechnicalExplanation)
}
