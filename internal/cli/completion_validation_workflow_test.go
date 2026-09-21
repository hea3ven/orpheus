//go:build integration

package cli_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowAgentDoneRejectsMissingDescription(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	f := newFinalizationFixture(t, "op-main")
	stdout, stderr, err := f.execute(
		"agent",
		"done",
		"--summary",
		"Missing description",
		"--detailed-description",
		"Detailed PR body.",
		"--technical-explanation",
		"Technical explanation.",
	)

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), `required flag(s) "description" not set`)
	f.assertUnpublished("op-main")
	state, _ := f.loadFinalTask("op-main")
	assert.Empty(t, state.Runs)
}

func TestIntegrationWorkflowAgentDoneRejectsMissingDetailedDescription(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	f := newFinalizationFixture(t, "op-main")
	stdout, stderr, err := f.execute(
		"agent",
		"done",
		"--summary",
		"Missing detailed description",
		"--description",
		"Commit body.",
		"--technical-explanation",
		"Technical explanation.",
	)

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "detailed description is required")
	f.assertUnpublished("op-main")
	state, _ := f.loadFinalTask("op-main")
	assert.Empty(t, state.Runs)
}

func TestIntegrationWorkflowAgentDoneRejectsMultipleDetailedDescriptionSources(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	detailedPath := "/fixture/body.md"

	f := newFinalizationFixture(t, "op-main")
	stdout, stderr, err := f.execute(
		"agent",
		"done",
		"--summary",
		"Multiple detailed sources",
		"--description",
		"Commit body.",
		"--detailed-description",
		"Inline PR body.",
		"--detailed-description-file",
		detailedPath,
		"--technical-explanation",
		"Technical explanation.",
	)

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "use exactly one of --detailed-description or --detailed-description-file")
	f.assertUnpublished("op-main")
	state, _ := f.loadFinalTask("op-main")
	assert.Empty(t, state.Runs)
}

func TestIntegrationWorkflowAgentDoneRejectsRemovedDetailsFlag(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	f := newFinalizationFixture(t, "op-main")
	stdout, stderr, err := f.execute(
		"agent",
		"done",
		"--summary",
		"Removed flag",
		"--description",
		"Commit body.",
		"--details",
		"Old details.",
		"--detailed-description",
		"Detailed PR body.",
		"--technical-explanation",
		"Technical explanation.",
	)

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "unknown flag: --details")
	f.assertUnpublished("op-main")
	state, _ := f.loadFinalTask("op-main")
	assert.Empty(t, state.Runs)
}

func TestIntegrationWorkflowAgentDoneRejectsMissingTechnicalExplanation(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	f := newFinalizationFixture(t, "op-main")
	stdout, stderr, err := f.execute(
		"agent",
		"done",
		"--summary",
		"Missing technical explanation",
		"--description",
		"Commit body.",
		"--detailed-description",
		"Detailed PR body.",
	)

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "technical explanation is required")
	f.assertUnpublished("op-main")
	state, _ := f.loadFinalTask("op-main")
	assert.Empty(t, state.Runs)
}

func TestIntegrationWorkflowAgentDoneRejectsMultipleTechnicalExplanationSources(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	technicalPath := "/fixture/technical.md"

	f := newFinalizationFixture(t, "op-main")
	stdout, stderr, err := f.execute(
		"agent",
		"done",
		"--summary",
		"Multiple technical sources",
		"--description",
		"Commit body.",
		"--detailed-description",
		"Detailed PR body.",
		"--technical-explanation",
		"Inline technical explanation.",
		"--technical-explanation-file",
		technicalPath,
	)

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "use exactly one of --technical-explanation or --technical-explanation-file")
	f.assertUnpublished("op-main")
	state, _ := f.loadFinalTask("op-main")
	assert.Empty(t, state.Runs)
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
