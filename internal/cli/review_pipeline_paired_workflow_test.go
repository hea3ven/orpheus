//go:build integration

package cli_test

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowReviewPipelineAdmitsAlternateFindingAfterPrimaryThroughCLI(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	is := assert.New(t)
	must := require.New(t)
	fixture := newPairedReviewWorkflowFixture(t, "op-paired")
	fixture.reviewLauncher.outcomes = append(fixture.reviewLauncher.outcomes,
		reviewOutcome(taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "primary", Description: "primary finding"}),
		reviewOutcome(taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "alternate", Description: "alternate finding"}),
	)

	stdout, stderr := fixture.run("a\n", "task", "run", "op-paired")

	is.Contains(stdout, "Recorded advisory review finding 1 for op-paired.")
	is.Contains(stderr, "Paired reviewer comparison for step \"ai-review\"")
	is.Contains(stderr, "Primary reviewer: profile=reviewer")
	is.Contains(stderr, "Alternate reviewer: profile=alternate")
	must.Len(fixture.reviewLauncher.launches, 2)
	is.Equal("primary", fixture.reviewLauncher.launches[0].environment["ORPHEUS_REVIEWER_ROLE"])
	is.Equal("alternate", fixture.reviewLauncher.launches[1].environment["ORPHEUS_REVIEWER_ROLE"])
	is.Equal("reviewer", fixture.reviewLauncher.launches[0].command.Name)
	is.Equal("alternate", fixture.reviewLauncher.launches[1].command.Name)

	latest := loadLatestReview(t, fixture, "op-paired")
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 2)
	is.Equal("primary", latest.Findings[0].Title)
	is.Equal("primary", latest.Findings[0].Reviewer)
	is.Equal("alternate", latest.Findings[1].Title)
	is.Equal("alternate", latest.Findings[1].Reviewer)
	comparison := latest.Steps[0].Comparison
	must.NotNil(comparison)
	is.Equal(taskstate.RunStatusSucceeded, comparison.AlternateExecution.Status)
	must.Len(comparison.AlternateFindings, 1)
	is.Equal(taskstate.AlternateFindingAdmitted, comparison.AlternateFindings[0].Classification)
}

func TestIntegrationWorkflowReviewPipelineClassifiesDuplicateAndExcludedAlternateFindingsThroughCLI(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	is := assert.New(t)
	must := require.New(t)
	fixture := newPairedReviewWorkflowFixture(t, "op-dupe")
	fixture.reviewLauncher.outcomes = append(fixture.reviewLauncher.outcomes,
		reviewOutcome(taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "primary", Description: "primary finding"}),
		reviewOutcome(
			taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "duplicate", Description: "duplicate finding"},
			taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "excluded", Description: "excluded finding"},
		),
	)

	fixture.run("d\n1\ne\n", "task", "run", "op-dupe")

	latest := loadLatestReview(t, fixture, "op-dupe")
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal("primary", latest.Findings[0].Title)
	comparison := latest.Steps[0].Comparison
	must.NotNil(comparison)
	must.Len(comparison.AlternateFindings, 2)
	is.Equal(taskstate.AlternateFindingDuplicate, comparison.AlternateFindings[0].Classification)
	is.Equal(0, comparison.AlternateFindings[0].DuplicateOf)
	is.Equal(taskstate.AlternateFindingExcluded, comparison.AlternateFindings[1].Classification)
}

func TestIntegrationWorkflowReviewPipelineRestartedPairedReviewKeepsOnlyRerunComparisonThroughCLI(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	is := assert.New(t)
	must := require.New(t)
	fixture := newPairedReviewWorkflowFixture(t, "op-restart")
	fixture.reviewLauncher.outcomes = append(fixture.reviewLauncher.outcomes,
		reviewOutcome(taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "restarted blocker", Description: "restart after fixing the environment", SuggestedAction: "Restart after fixing the environment."}),
		reviewOutcome(taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "discarded alternate finding", Description: "paired result"}),
		reviewOutcome(),
		reviewOutcome(taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "new alternate finding", Description: "paired result"}),
	)

	fixture.run("e\nr\na\n", "task", "run", "op-restart")

	must.Len(fixture.reviewLauncher.launches, 4)
	latest := loadLatestReview(t, fixture, "op-restart")
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Steps, 1)
	must.Len(latest.Findings, 1)
	is.Equal("new alternate finding", latest.Findings[0].Title)
	is.Equal("alternate", latest.Findings[0].Reviewer)
	comparison := latest.Steps[0].Comparison
	must.NotNil(comparison)
	must.Len(comparison.AlternateFindings, 1)
	is.Equal("new alternate finding", comparison.AlternateFindings[0].Finding.Title)
	is.Equal(taskstate.AlternateFindingAdmitted, comparison.AlternateFindings[0].Classification)
}

func TestIntegrationWorkflowReviewPipelineLabelsAndIsolatesPairedRollingOutputThroughCLI(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	is := assert.New(t)
	must := require.New(t)
	fixture := newPairedReviewWorkflowFixture(t, "op-rolling")
	fixture.forceInteractiveReviewOutput(80)
	fixture.reviewLauncher.outcomes = append(fixture.reviewLauncher.outcomes,
		reviewOutcome(),
		reviewOutcome(taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "alternate", Description: "alternate finding"}),
	)
	fixture.reviewLauncher.outputs = append(fixture.reviewLauncher.outputs,
		reviewPipelineLaunchOutput{stdout: "primary rolling output\n"},
		reviewPipelineLaunchOutput{stdout: "alternate rolling output\n"},
	)
	var stdout bytes.Buffer
	stderr := newCLIVisualTerminal(80)

	err := fixture.runWithWriters("a\n", &stdout, stderr, "task", "run", "op-rolling")

	must.NoError(err)
	is.NotContains(stdout.String(), "rolling output")
	raw := stderr.Raw()
	primaryHeader := "== Reviewer: primary (profile: reviewer; model: -) =="
	alternateHeader := "== Reviewer: alternate (profile: alternate; model: -) =="
	primaryHeaderIndex := strings.Index(raw, primaryHeader)
	primaryOutputIndex := strings.Index(raw, "primary rolling output")
	alternateHeaderIndex := strings.Index(raw, alternateHeader)
	alternateOutputIndex := strings.Index(raw, "alternate rolling output")
	is.True(primaryHeaderIndex >= 0 && primaryOutputIndex > primaryHeaderIndex && alternateHeaderIndex > primaryOutputIndex && alternateOutputIndex > alternateHeaderIndex, "reviewer output should be labeled and ordered:\n%s", raw)
	visible := stderr.Visible()
	is.Contains(visible, primaryHeader)
	is.Contains(visible, alternateHeader)
	is.Contains(visible, "alternate rolling output")
	is.NotContains(visible, "primary rolling output")
	latest := loadLatestReview(t, fixture, "op-rolling")
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
}

func TestIntegrationWorkflowReviewPipelineKeepsExpandedPrimaryBlockerTailBeforeAlternateThroughCLI(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	is := assert.New(t)
	must := require.New(t)
	fixture := newPairedReviewWorkflowFixture(t, "op-primary-tail")
	fixture.forceInteractiveReviewOutput(80)
	fixture.budget(1)
	fixture.reviewLauncher.outcomes = append(fixture.reviewLauncher.outcomes,
		reviewOutcome(taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "primary blocker", Description: "primary blocker finding", SuggestedAction: "Fix the blocker."}),
		reviewOutcome(),
	)
	var primaryOutput strings.Builder
	for i := 1; i <= 35; i++ {
		fmt.Fprintf(&primaryOutput, "primary blocker output %02d\n", i)
	}
	fixture.reviewLauncher.outputs = append(fixture.reviewLauncher.outputs,
		reviewPipelineLaunchOutput{stdout: primaryOutput.String()},
		reviewPipelineLaunchOutput{stdout: "alternate rolling output\n"},
	)
	var stdout bytes.Buffer
	stderr := newCLIVisualTerminal(80)

	err := fixture.runWithWriters("k\n", &stdout, stderr, "task", "run", "op-primary-tail")

	must.NoError(err)
	is.NotContains(stdout.String(), "primary blocker output")
	visible := stderr.Visible()
	primaryTailStart := strings.Index(visible, "primary blocker output 07")
	alternateHeader := strings.Index(visible, "== Reviewer: alternate")
	alternateOutput := strings.Index(visible, "alternate rolling output")
	is.True(primaryTailStart >= 0 && alternateHeader > primaryTailStart && alternateOutput > alternateHeader, "visible terminal should keep expanded primary tail before alternate output:\n%s", visible)
	is.Contains(visible, "primary blocker output 35")
	is.NotContains(visible, "primary blocker output 06")
	latest := loadLatestReview(t, fixture, "op-primary-tail")
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
}

func TestIntegrationWorkflowReviewPipelineLabelsAttachedPairedOutputThroughCLI(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	is := assert.New(t)
	must := require.New(t)
	fixture := newPairedReviewWorkflowFixture(t, "op-attached-paired")
	fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "impl", Reviewer: "reviewer"}, map[string]agent.Profile{
		"impl":      {Command: "impl"},
		"reviewer":  {Command: "review-agent", Interactive: true},
		"alternate": {Command: "alternate-agent", Interactive: true},
	})
	fixture.reviewLauncher.outcomes = append(fixture.reviewLauncher.outcomes, reviewOutcome(), reviewOutcome())
	fixture.reviewLauncher.outputs = append(fixture.reviewLauncher.outputs,
		reviewPipelineLaunchOutput{stdout: "reviewer attached output\n"},
		reviewPipelineLaunchOutput{stdout: "alternate attached output\n"},
	)

	stdout, stderr := fixture.run("", "task", "run", "op-attached-paired")

	primaryHeader := "== Reviewer: primary (profile: reviewer; model: -) =="
	alternateHeader := "== Reviewer: alternate (profile: alternate; model: -) =="
	is.Contains(stderr, primaryHeader)
	is.Contains(stderr, alternateHeader)
	is.Less(strings.Index(stderr, primaryHeader), strings.Index(stderr, alternateHeader))
	is.Contains(stdout, "reviewer attached output\nalternate attached output\n")
	is.Contains(stdout, "Finalized op-attached-paired")
	must.Len(fixture.reviewLauncher.launches, 2)
	is.Equal("primary", fixture.reviewLauncher.launches[0].environment["ORPHEUS_REVIEWER_ROLE"])
	is.Equal("alternate", fixture.reviewLauncher.launches[1].environment["ORPHEUS_REVIEWER_ROLE"])
}

func TestIntegrationWorkflowReviewPipelineAlternateFailureKeepsPrimaryAuthoritativeThroughCLI(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	is := assert.New(t)
	must := require.New(t)
	fixture := newPairedReviewWorkflowFixture(t, "op-alt-fails")
	fixture.reviewLauncher.outcomes = append(fixture.reviewLauncher.outcomes,
		reviewOutcome(),
		semanticAgentOutcome{review: true, err: fmt.Errorf("alternate unavailable")},
	)

	fixture.run("", "task", "run", "op-alt-fails")

	latest := loadLatestReview(t, fixture, "op-alt-fails")
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	comparison := latest.Steps[0].Comparison
	must.NotNil(comparison)
	is.Equal(taskstate.RunStatusFailed, comparison.AlternateExecution.Status)
	is.Contains(comparison.Failure, "alternate unavailable")
}

func TestIntegrationWorkflowReviewPipelineInterruptedAlternateDecisionBlocksWithoutAdmissionThroughCLI(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	is := assert.New(t)
	must := require.New(t)
	fixture := newPairedReviewWorkflowFixture(t, "op-interrupt")
	fixture.reviewLauncher.outcomes = append(fixture.reviewLauncher.outcomes,
		reviewOutcome(),
		reviewOutcome(taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "alternate", Description: "alternate finding"}),
	)

	_, stderr := fixture.run("q\n", "task", "run", "op-interrupt")

	is.Contains(stderr, "Alternate reviewer comparison for op-interrupt was interrupted")
	latest := loadLatestReview(t, fixture, "op-interrupt")
	is.Equal(taskstate.ReviewStatusBlocked, latest.Status)
	is.Empty(latest.Findings)
	comparison := latest.Steps[0].Comparison
	must.NotNil(comparison)
	is.True(comparison.InputInterrupted)
}

func TestIntegrationWorkflowReviewPipelinePrimaryFailureSkipsAlternateThroughCLI(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	is := assert.New(t)
	must := require.New(t)
	fixture := newPairedReviewWorkflowFixture(t, "op-primary-fails")
	fixture.reviewLauncher.outcomes = append(fixture.reviewLauncher.outcomes, semanticAgentOutcome{review: true, err: fmt.Errorf("primary failed")})

	_, _, err := fixture.runError("", "task", "run", "op-primary-fails")

	must.Error(err)
	is.ErrorContains(err, "run agent_review step \"ai-review\"")
	must.Len(fixture.reviewLauncher.launches, 1)
	latest := loadLatestReview(t, fixture, "op-primary-fails")
	must.Len(latest.Steps, 1)
	is.Nil(latest.Steps[0].Comparison)
}

func TestIntegrationWorkflowReviewPipelineMissingAlternateProfilePersistsComparisonFailureThroughCLI(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "missing")
	is := assert.New(t)
	must := require.New(t)
	fixture := newReviewWorkflowFixture(t, "op-missing-alt", "Missing alternate", "Missing alternate profile.")
	fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "impl", Reviewer: "reviewer"}, map[string]agent.Profile{
		"impl":     {Command: "impl"},
		"reviewer": {Command: "review-agent"},
	})
	fixture.agent.outcomes = append(fixture.agent.outcomes, reviewOutcome())
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}}})

	fixture.run("", "task", "run", "op-missing-alt")

	latest := loadLatestReview(t, fixture, "op-missing-alt")
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	comparison := latest.Steps[0].Comparison
	must.NotNil(comparison)
	is.Equal(taskstate.RunStatusFailed, comparison.AlternateExecution.Status)
	is.Contains(comparison.Failure, "missing")
}

func TestIntegrationWorkflowReviewPipelineReviewerHeaderFailureDoesNotLaunchOrRecordStepThroughCLI(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	is := assert.New(t)
	must := require.New(t)
	fixture := newPairedReviewWorkflowFixture(t, "op-header-fails")
	stderr := new(failingReviewerHeaderWriter)
	command := cli.NewRootCommandWithOptions(fixture.options)
	var stdout bytes.Buffer
	command.SetIn(strings.NewReader(""))
	command.SetOut(&stdout)
	command.SetErr(stderr)
	command.SetArgs([]string{"task", "run", "op-header-fails"})

	err := command.Execute()

	must.Error(err)
	is.Contains(err.Error(), "reviewer header output unavailable")
	is.Empty(fixture.reviewLauncher.launches)
	is.Contains(stderr.output.String(), "== Review step: ai-review (agent_review) ==")
	latest := loadLatestReview(t, fixture, "op-header-fails")
	is.Empty(latest.Steps)
}

func newPairedReviewWorkflowFixture(t *testing.T, taskID string) *reviewWorkflowFixture {
	t.Helper()
	fixture := newReviewWorkflowFixture(t, taskID, "Paired review", "Run paired reviewers.")
	fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "impl", Reviewer: "reviewer"}, map[string]agent.Profile{
		"impl":      {Command: "impl"},
		"reviewer":  {Command: "review-agent"},
		"alternate": {Command: "alternate-agent"},
	})
	fixture.pipelines("standard", map[string][]map[string]any{"standard": {{"kind": "agent_review", "name": "ai-review"}}})
	launcher := &reviewPipelineAgentLauncher{t: t, options: &fixture.options, nextPID: 4242}
	fixture.reviewLauncher = launcher
	fixture.options.Dependencies.AgentLauncher = launcher
	t.Cleanup(func() {
		require.Empty(t, launcher.outcomes, "all scripted review outcomes must be consumed")
		require.Empty(t, launcher.outputs, "all scripted review output must be consumed")
	})
	return fixture
}

func reviewOutcome(findings ...taskstate.ReviewFinding) semanticAgentOutcome {
	return semanticAgentOutcome{review: true, findings: findings}
}

type reviewPipelineAgentLauncher struct {
	t        *testing.T
	options  *cli.CommandOptions
	outcomes []semanticAgentOutcome
	outputs  []reviewPipelineLaunchOutput
	launches []semanticAgentLaunch
	nextPID  int
}

type reviewPipelineLaunchOutput struct {
	stdout string
	stderr string
}

func (l *reviewPipelineAgentLauncher) Run(_ context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
	l.t.Helper()
	if len(l.outcomes) == 0 {
		return fmt.Errorf("unexpected review agent launch: %s", command.Name)
	}
	outcome := l.outcomes[0]
	l.outcomes = l.outcomes[1:]
	environment := parseEnvironment(opts.Env)
	if outcome.review != (environment["ORPHEUS_AGENT_PURPOSE"] == "review") {
		return fmt.Errorf("scripted review outcome does not match launch purpose")
	}
	l.launches = append(l.launches, semanticAgentLaunch{command: command, dir: opts.Dir, environment: environment})
	if outcome.startFailure != nil {
		return outcome.startFailure
	}
	if opts.OnStart != nil {
		if err := opts.OnStart(l.nextPID); err != nil {
			return err
		}
		l.nextPID++
	}
	if len(l.outputs) > 0 {
		output := l.outputs[0]
		l.outputs = l.outputs[1:]
		if _, err := fmt.Fprint(opts.Stdout, output.stdout); err != nil {
			return err
		}
		if _, err := fmt.Fprint(opts.Stderr, output.stderr); err != nil {
			return err
		}
	}
	if outcome.err != nil {
		return outcome.err
	}
	if outcome.review {
		for _, finding := range outcome.findings {
			if err := l.recordFinding(opts, environment, finding); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *reviewPipelineAgentLauncher) recordFinding(opts agentexec.LaunchOptions, environment map[string]string, finding taskstate.ReviewFinding) error {
	command := cli.NewRootCommandWithOptions(l.childOptions(opts.Dir, environment))
	command.SetIn(opts.Stdin)
	command.SetOut(opts.Stdout)
	command.SetErr(opts.Stderr)
	args := []string{"agent", "review", "add", "--type", string(finding.Type), "--title", finding.Title, "--description", finding.Description}
	if strings.TrimSpace(finding.SuggestedAction) != "" {
		args = append(args, "--suggested-action", finding.SuggestedAction)
	}
	command.SetArgs(args)
	return command.Execute()
}

func (l *reviewPipelineAgentLauncher) childOptions(dir string, environment map[string]string) cli.CommandOptions {
	options := *l.options
	options.Environment = maps.Clone(l.options.Environment)
	for key, value := range environment {
		options.Environment[key] = value
	}
	options.AgentWorkingDirectory = dir
	return options
}

func loadLatestReview(t *testing.T, fixture *reviewWorkflowFixture, taskID string) taskstate.ReviewAttempt {
	t.Helper()
	var state taskstate.TaskState
	require.NoError(t, fixture.paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", taskID+".yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	require.True(t, ok)
	return latest
}

type failingReviewerHeaderWriter struct {
	output bytes.Buffer
}

func (w *failingReviewerHeaderWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "== Reviewer:") {
		return 0, fmt.Errorf("reviewer header output unavailable")
	}
	return w.output.Write(p)
}
