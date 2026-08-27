//go:build integration

package review_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

type failingReviewerHeaderWriter struct {
	output bytes.Buffer
}

func (w *failingReviewerHeaderWriter) Write(p []byte) (int, error) {
	if strings.Contains(string(p), "== Reviewer:") {
		return 0, errors.New("reviewer header output unavailable")
	}
	return w.output.Write(p)
}

func TestIntegrationRunPipelinePairedReviewerAdmitsAlternateFindingAfterPrimary(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	h := newAgentReviewPipelineHarness(t)
	calls := []string{}
	launcher := fakeReviewLauncherFunc(func(_ context.Context, command agentexec.Command, _ agentexec.LaunchOptions) error {
		calls = append(calls, command.Name)
		switch command.Name {
		case "reviewer":
			_, err := h.store.RecordReviewFinding("alpha", "op-1", h.attempt.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "primary", Description: "primary finding", Step: "ai-review", Reviewer: "primary"})
			return err
		case "alternate":
			_, err := h.store.RecordAlternateReviewFinding("alpha", "op-1", h.attempt.Attempt, "ai-review", taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "alternate", Description: "alternate finding", Step: "ai-review"})
			return err
		default:
			return errors.New("unexpected reviewer")
		}
	})
	outcome, err := runPairedPipeline(t, h, launcher, func(comparison review.AlternateReviewComparison) ([]review.AlternateFindingDecision, error) {
		if len(comparison.Primary) != 1 || len(comparison.Alternate) != 1 {
			t.Fatalf("comparison = %#v", comparison)
		}
		return []review.AlternateFindingDecision{{FindingIndex: 0, Classification: taskstate.AlternateFindingAdmitted}}, nil
	})
	if err != nil || outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	if got := strings.Join(calls, ","); got != "reviewer,alternate" {
		t.Fatalf("calls = %q", got)
	}
	state, err := h.store.Load("alpha", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := taskstate.LatestReview(state)
	if len(latest.Findings) != 2 || latest.Findings[1].Reviewer != "alternate" {
		t.Fatalf("authoritative findings = %#v", latest.Findings)
	}
	comparison := latest.Steps[0].Comparison
	if comparison == nil || comparison.AlternateExecution.Status != taskstate.RunStatusSucceeded || comparison.AlternateFindings[0].Classification != taskstate.AlternateFindingAdmitted {
		t.Fatalf("comparison = %#v", comparison)
	}
}

func TestIntegrationRunPipelineRestartedPairedReviewDiscardsPriorExecution(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	h := newAgentReviewPipelineHarness(t)
	primaryRuns := 0
	alternateRuns := 0
	comparisonPrompts := 0

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context: context.Background(), Store: h.store, RepoID: "alpha", TaskID: "op-1", Branch: "main", Workdir: h.workdir,
		Attempt: h.attempt, Pipeline: agentReviewPipeline(), Stdout: io.Discard, Stderr: io.Discard, AgentConfig: pairedReviewAgentConfig(),
		AgentLauncher: fakeReviewLauncherFunc(func(_ context.Context, command agentexec.Command, _ agentexec.LaunchOptions) error {
			switch command.Name {
			case "reviewer":
				primaryRuns++
				if primaryRuns == 1 {
					_, err := h.store.RecordReviewFinding("alpha", "op-1", h.attempt.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "restarted blocker", Description: "restart after fixing the environment", Step: "ai-review", Reviewer: "primary"})
					return err
				}
				return nil
			case "alternate":
				alternateRuns++
				title := "discarded alternate finding"
				if alternateRuns == 2 {
					title = "new alternate finding"
				}
				_, err := h.store.RecordAlternateReviewFinding("alpha", "op-1", h.attempt.Attempt, "ai-review", taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: title, Description: "paired result", Step: "ai-review"})
				return err
			default:
				return fmt.Errorf("unexpected reviewer %q", command.Name)
			}
		}),
		PromptAutomatedBlockers: func(blockers review.AutomatedBlockerReview) ([]review.AutomatedBlockerDecision, error) {
			if len(blockers.Blockers) != 1 || blockers.Blockers[0].Finding.Title != "restarted blocker" {
				return nil, fmt.Errorf("blockers = %#v", blockers.Blockers)
			}
			return []review.AutomatedBlockerDecision{{FindingIndex: blockers.Blockers[0].Index, Action: review.AutomatedBlockerActionRestart}}, nil
		},
		PromptAlternateFindings: func(comparison review.AlternateReviewComparison) ([]review.AlternateFindingDecision, error) {
			comparisonPrompts++
			if len(comparison.Alternate) != 1 {
				t.Fatalf("comparison = %#v, want one alternate finding", comparison)
			}
			if comparison.Alternate[0].Finding.Title == "discarded alternate finding" {
				return []review.AlternateFindingDecision{{FindingIndex: comparison.Alternate[0].Index, Classification: taskstate.AlternateFindingExcluded}}, nil
			}
			if len(comparison.Primary) != 0 || comparison.Alternate[0].Finding.Title != "new alternate finding" {
				t.Fatalf("comparison = %#v, want only the rerun alternate finding", comparison)
			}
			return []review.AlternateFindingDecision{{FindingIndex: comparison.Alternate[0].Index, Classification: taskstate.AlternateFindingAdmitted}}, nil
		},
	})
	if err != nil || outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	if primaryRuns != 2 || alternateRuns != 2 || comparisonPrompts != 2 {
		t.Fatalf("runs primary=%d alternate=%d prompts=%d, want 2/2/2", primaryRuns, alternateRuns, comparisonPrompts)
	}

	state, err := h.store.Load("alpha", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	latest, _ := taskstate.LatestReview(state)
	if len(latest.Steps) != 1 || latest.Steps[0].Comparison == nil {
		t.Fatalf("steps = %#v, want only the rerun paired execution", latest.Steps)
	}
	if len(latest.Findings) != 1 || latest.Findings[0].Reviewer != "alternate" {
		t.Fatalf("findings = %#v, want only the rerun alternate finding", latest.Findings)
	}
	if len(latest.Steps[0].Comparison.AlternateFindings) != 1 || latest.Steps[0].Comparison.AlternateFindings[0].Finding.Title != "new alternate finding" {
		t.Fatalf("comparison = %#v, want only the rerun alternate result", latest.Steps[0].Comparison)
	}
}

func TestIntegrationRunPipelinePairedReviewerKeepsDuplicateAndExcludedFindingsNonAuthoritative(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	h := newAgentReviewPipelineHarness(t)
	launcher := fakeReviewLauncherFunc(func(_ context.Context, command agentexec.Command, _ agentexec.LaunchOptions) error {
		if command.Name == "reviewer" {
			_, err := h.store.RecordReviewFinding("alpha", "op-1", h.attempt.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "primary", Description: "primary finding", Step: "ai-review", Reviewer: "primary"})
			return err
		}
		for _, title := range []string{"duplicate", "excluded"} {
			if _, err := h.store.RecordAlternateReviewFinding("alpha", "op-1", h.attempt.Attempt, "ai-review", taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: title, Description: title + " finding", Step: "ai-review"}); err != nil {
				return err
			}
		}
		return nil
	})
	outcome, err := runPairedPipeline(t, h, launcher, func(review.AlternateReviewComparison) ([]review.AlternateFindingDecision, error) {
		return []review.AlternateFindingDecision{
			{FindingIndex: 0, Classification: taskstate.AlternateFindingDuplicate, DuplicateOf: 0},
			{FindingIndex: 1, Classification: taskstate.AlternateFindingExcluded},
		}, nil
	})
	if err != nil || outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	state, _ := h.store.Load("alpha", "op-1")
	latest, _ := taskstate.LatestReview(state)
	if len(latest.Findings) != 1 || len(latest.Steps[0].Comparison.AlternateFindings) != 2 {
		t.Fatalf("review = %#v", latest)
	}
	if latest.Steps[0].Comparison.AlternateFindings[0].Classification != taskstate.AlternateFindingDuplicate || latest.Steps[0].Comparison.AlternateFindings[1].Classification != taskstate.AlternateFindingExcluded {
		t.Fatalf("comparison = %#v", latest.Steps[0].Comparison)
	}
	if _, err := h.store.ClassifyAlternateReviewFindings("alpha", "op-1", h.attempt.Attempt, "ai-review", []taskstate.AlternateReviewFindingDecision{{FindingIndex: 0, Classification: taskstate.AlternateFindingAdmitted}}); err == nil {
		t.Fatal("reclassification succeeded, want rejection")
	}
	state, _ = h.store.Load("alpha", "op-1")
	latest, _ = taskstate.LatestReview(state)
	if len(latest.Findings) != 1 {
		t.Fatalf("invalid classification changed findings: %#v", latest.Findings)
	}
}

func TestIntegrationRunPipelinePairedReviewerLabelsAndIsolatesRollingOutput(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	h := newAgentReviewPipelineHarness(t)
	var stdout bytes.Buffer
	terminal := newVisualTerminal()

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context:           context.Background(),
		Store:             h.store,
		RepoID:            "alpha",
		TaskID:            "op-1",
		Branch:            "main",
		Workdir:           h.workdir,
		Attempt:           h.attempt,
		Pipeline:          agentReviewPipeline(),
		Stdout:            &stdout,
		Stderr:            terminal,
		InteractiveOutput: true,
		AgentConfig:       pairedReviewAgentConfig(),
		AgentLauncher: fakeReviewLauncherFunc(func(_ context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
			switch command.Name {
			case "reviewer":
				_, err := fmt.Fprintln(opts.Stdout, "primary rolling output")
				return err
			case "alternate":
				if _, err := fmt.Fprintln(opts.Stdout, "alternate rolling output"); err != nil {
					return err
				}
				_, err := h.store.RecordAlternateReviewFinding("alpha", "op-1", h.attempt.Attempt, "ai-review", taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "alternate", Description: "alternate finding", Step: "ai-review"})
				return err
			default:
				return errors.New("unexpected reviewer")
			}
		}),
		PromptAlternateFindings: func(review.AlternateReviewComparison) ([]review.AlternateFindingDecision, error) {
			return []review.AlternateFindingDecision{{FindingIndex: 0, Classification: taskstate.AlternateFindingAdmitted}}, nil
		},
	})
	if err != nil || outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want rolling output captured away from stdout", stdout.String())
	}

	assertPairedRollingOutput(t, terminal)
}

func assertPairedRollingOutput(t *testing.T, terminal *visualTerminal) {
	t.Helper()

	primaryHeader := "== Reviewer: primary (profile: reviewer; model: -) =="
	alternateHeader := "== Reviewer: alternate (profile: alternate; model: -) =="
	raw := terminal.raw.String()
	primaryHeaderIndex := strings.Index(raw, primaryHeader)
	primaryOutputIndex := strings.Index(raw, "primary rolling output")
	alternateHeaderIndex := strings.Index(raw, alternateHeader)
	alternateOutputIndex := strings.Index(raw, "alternate rolling output")
	if primaryHeaderIndex < 0 || primaryOutputIndex < primaryHeaderIndex || alternateHeaderIndex < primaryOutputIndex || alternateOutputIndex < alternateHeaderIndex {
		t.Fatalf("reviewer output ordering is not labeled and sequential:\n%s", raw)
	}
	if afterAlternate := raw[alternateHeaderIndex:]; strings.Contains(afterAlternate, "\x1b[1A") {
		t.Fatalf("terminal output after alternate heading = %q, want no later primary cursor movement", afterAlternate)
	}
	visible := terminal.Visible()
	for _, want := range []string{primaryHeader, alternateHeader, "alternate rolling output"} {
		if !strings.Contains(visible, want) {
			t.Fatalf("visible terminal = %q, want %q", visible, want)
		}
	}
	if strings.Contains(visible, "primary rolling output") {
		t.Fatalf("visible terminal = %q, want cleared primary output isolated from alternate tail", visible)
	}
}

func TestIntegrationRunPipelinePairedReviewerKeepsExpandedPrimaryBlockerTailBeforeAlternate(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	h := newAgentReviewPipelineHarness(t)
	terminal := newVisualTerminal()

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context: context.Background(), Store: h.store, RepoID: "alpha", TaskID: "op-1", Branch: "main", Workdir: h.workdir,
		Attempt: h.attempt, Pipeline: agentReviewPipeline(), Stdout: io.Discard, Stderr: terminal, InteractiveOutput: true,
		AgentConfig: pairedReviewAgentConfig(),
		AgentLauncher: fakeReviewLauncherFunc(func(_ context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
			switch command.Name {
			case "reviewer":
				for i := 1; i <= 35; i++ {
					if _, err := fmt.Fprintf(opts.Stdout, "primary blocker output %02d\n", i); err != nil {
						return err
					}
				}
				_, err := h.store.RecordReviewFinding("alpha", "op-1", h.attempt.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "primary blocker", Description: "primary blocker finding", Step: "ai-review", Reviewer: "primary"})
				return err
			case "alternate":
				_, err := fmt.Fprintln(opts.Stdout, "alternate rolling output")
				return err
			default:
				return errors.New("unexpected reviewer")
			}
		}),
	})
	if err != nil || outcome.Status != taskstate.ReviewStatusBlocked {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}

	visible := terminal.Visible()
	primaryTailStart := strings.Index(visible, "primary blocker output 06")
	alternateHeader := strings.Index(visible, "== Reviewer: alternate")
	alternateOutput := strings.Index(visible, "alternate rolling output")
	if primaryTailStart < 0 || alternateHeader < primaryTailStart || alternateOutput < alternateHeader {
		t.Fatalf("visible terminal = %q, want expanded primary tail before alternate output", visible)
	}
	if !strings.Contains(visible, "primary blocker output 35") || strings.Contains(visible, "primary blocker output 05") {
		t.Fatalf("visible terminal = %q, want latest 30 primary blocker lines", visible)
	}
}

func TestIntegrationRunPipelinePairedReviewerLabelsAttachedOutput(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	h := newAgentReviewPipelineHarness(t)
	config := pairedReviewAgentConfig()
	primary := config.Agents["reviewer"]
	primary.Interactive = true
	config.Agents["reviewer"] = primary
	alternate := config.Agents["alternate"]
	alternate.Interactive = true
	config.Agents["alternate"] = alternate
	var stdout, stderr bytes.Buffer

	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context: context.Background(), Store: h.store, RepoID: "alpha", TaskID: "op-1", Branch: "main", Workdir: h.workdir,
		Attempt: h.attempt, Pipeline: agentReviewPipeline(), Stdout: &stdout, Stderr: &stderr, InteractiveOutput: true, AgentConfig: config,
		AgentLauncher: fakeReviewLauncherFunc(func(_ context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
			_, err := fmt.Fprintf(opts.Stdout, "%s attached output\n", command.Name)
			return err
		}),
	})
	if err != nil || outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}

	primaryHeader := "== Reviewer: primary (profile: reviewer; model: -) =="
	alternateHeader := "== Reviewer: alternate (profile: alternate; model: -) =="
	if got := stderr.String(); !strings.Contains(got, primaryHeader) || !strings.Contains(got, alternateHeader) || strings.Index(got, alternateHeader) < strings.Index(got, primaryHeader) {
		t.Fatalf("reviewer headings = %q, want primary then alternate", got)
	}
	if got, want := stdout.String(), "reviewer attached output\nalternate attached output\n"; got != want {
		t.Fatalf("attached output = %q, want %q", got, want)
	}
}

func TestIntegrationRunPipelinePairedReviewerHeaderWriteFailureDoesNotRecordPrimaryExecution(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	h := newAgentReviewPipelineHarness(t)
	stderr := new(failingReviewerHeaderWriter)
	launcherCalls := 0

	_, err := review.RunPipeline(review.PipelineRunOptions{
		Context: context.Background(), Store: h.store, RepoID: "alpha", TaskID: "op-1", Branch: "main", Workdir: h.workdir,
		Attempt: h.attempt, Pipeline: agentReviewPipeline(), Stdout: io.Discard, Stderr: stderr, AgentConfig: pairedReviewAgentConfig(),
		AgentLauncher: fakeReviewLauncherFunc(func(context.Context, agentexec.Command, agentexec.LaunchOptions) error {
			launcherCalls++
			return nil
		}),
	})
	if err == nil || !strings.Contains(err.Error(), "reviewer header output unavailable") {
		t.Fatalf("RunPipeline error = %v, want reviewer header failure", err)
	}
	if launcherCalls != 0 {
		t.Fatalf("reviewer launches = %d, want 0", launcherCalls)
	}
	if got := stderr.output.String(); !strings.Contains(got, "== Review step: ai-review (agent_review) ==") {
		t.Fatalf("accepted output = %q, want review step header", got)
	}

	state, err := h.store.Load("alpha", "op-1")
	if err != nil {
		t.Fatal(err)
	}
	latest, ok := taskstate.LatestReview(state)
	if !ok {
		t.Fatal("latest review attempt is missing")
	}
	if len(latest.Steps) != 0 {
		t.Fatalf("review steps = %#v, want no stranded primary execution", latest.Steps)
	}
}

func TestIntegrationRunPipelinePairedReviewerResolutionFailureIsPersisted(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "missing")
	h := newAgentReviewPipelineHarness(t)
	var output bytes.Buffer
	outcome, err := review.RunPipeline(review.PipelineRunOptions{
		Context: context.Background(), Store: h.store, RepoID: "alpha", TaskID: "op-1", Branch: "main", Workdir: h.workdir,
		Attempt: h.attempt, Pipeline: agentReviewPipeline(), Stdout: io.Discard, Stderr: &output, AgentConfig: reviewAgentConfig(false), AgentLauncher: fakeReviewLauncher{},
	})
	if err != nil || outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	state, _ := h.store.Load("alpha", "op-1")
	latest, _ := taskstate.LatestReview(state)
	comparison := latest.Steps[0].Comparison
	if comparison == nil || comparison.AlternateExecution.Status != taskstate.RunStatusFailed || !strings.Contains(comparison.Failure, "missing") {
		t.Fatalf("comparison = %#v", comparison)
	}
}

func TestIntegrationRunPipelinePairedReviewerAlternateFailureDoesNotFailPrimary(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	h := newAgentReviewPipelineHarness(t)
	launcher := fakeReviewLauncherFunc(func(_ context.Context, command agentexec.Command, _ agentexec.LaunchOptions) error {
		if command.Name == "alternate" {
			return errors.New("alternate unavailable")
		}
		return nil
	})
	outcome, err := runPairedPipeline(t, h, launcher, nil)
	if err != nil || outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	state, _ := h.store.Load("alpha", "op-1")
	latest, _ := taskstate.LatestReview(state)
	if got := latest.Steps[0].Comparison.AlternateExecution.Status; got != taskstate.RunStatusFailed {
		t.Fatalf("alternate status = %q", got)
	}
	if !strings.Contains(latest.Steps[0].Comparison.Failure, "alternate unavailable") {
		t.Fatalf("failure = %q", latest.Steps[0].Comparison.Failure)
	}
}

func TestIntegrationRunPipelinePairedReviewerInterruptedInputBlocksWithoutAdmission(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	h := newAgentReviewPipelineHarness(t)
	launcher := fakeReviewLauncherFunc(func(_ context.Context, command agentexec.Command, _ agentexec.LaunchOptions) error {
		if command.Name != "alternate" {
			return nil
		}
		_, err := h.store.RecordAlternateReviewFinding("alpha", "op-1", h.attempt.Attempt, "ai-review", taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "alternate", Description: "alternate finding", Step: "ai-review"})
		return err
	})
	outcome, err := runPairedPipeline(t, h, launcher, func(review.AlternateReviewComparison) ([]review.AlternateFindingDecision, error) {
		return nil, review.ErrAutomatedBlockerInputUnavailable
	})
	if err != nil || outcome.Status != taskstate.ReviewStatusBlocked {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	state, _ := h.store.Load("alpha", "op-1")
	latest, _ := taskstate.LatestReview(state)
	if len(latest.Findings) != 0 || !latest.Steps[0].Comparison.InputInterrupted {
		t.Fatalf("review = %#v", latest)
	}
}

func TestIntegrationRunPipelinePairedReviewerPrimaryFailureSkipsAlternate(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "alternate")
	h := newAgentReviewPipelineHarness(t)
	calls := 0
	_, err := runPairedPipeline(t, h, fakeReviewLauncherFunc(func(_ context.Context, _ agentexec.Command, _ agentexec.LaunchOptions) error {
		calls++
		return errors.New("primary failed")
	}), nil)
	if err == nil {
		t.Fatal("RunPipeline succeeded, want primary failure")
	}
	if calls != 1 {
		t.Fatalf("reviewer calls = %d, want 1", calls)
	}
	state, _ := h.store.Load("alpha", "op-1")
	latest, _ := taskstate.LatestReview(state)
	if latest.Steps[0].Comparison != nil {
		t.Fatalf("comparison = %#v, want nil", latest.Steps[0].Comparison)
	}
}

func runPairedPipeline(t *testing.T, h agentReviewPipelineHarness, launcher agentexec.Launcher, prompt func(review.AlternateReviewComparison) ([]review.AlternateFindingDecision, error)) (review.PipelineOutcome, error) {
	t.Helper()
	var output bytes.Buffer
	return review.RunPipeline(review.PipelineRunOptions{
		Context: context.Background(), Store: h.store, RepoID: "alpha", TaskID: "op-1", Branch: "main", Workdir: h.workdir,
		Attempt: h.attempt, Pipeline: agentReviewPipeline(), Stdout: io.Discard, Stderr: &output, AgentConfig: pairedReviewAgentConfig(), AgentLauncher: launcher, PromptAlternateFindings: prompt,
	})
}

func pairedReviewAgentConfig() agent.Config {
	return agent.Config{Defaults: agent.AgentDefaults{Implementer: "impl", Reviewer: "reviewer"}, Agents: map[string]agent.Profile{
		"impl": {Command: "impl"}, "reviewer": {Command: "review-agent"}, "alternate": {Command: "alternate-agent"},
	}}
}

func TestIntegrationRunPipelineWithoutAlternatePreservesLegacyReviewerEnvironment(t *testing.T) {
	t.Setenv("ORPHEUS_ALTERNATE_REVIEWER_PROFILE", "")
	h := newAgentReviewPipelineHarness(t)
	launcher := fakeReviewLauncherFunc(func(_ context.Context, _ agentexec.Command, opts agentexec.LaunchOptions) error {
		if got := envValue(opts.Env, "ORPHEUS_REVIEWER_ROLE"); got != "" {
			t.Fatalf("reviewer role = %q, want absent", got)
		}
		_, err := h.store.RecordReviewFinding("alpha", "op-1", h.attempt.Attempt, taskstate.ReviewFinding{Type: taskstate.FindingTypeAdvisory, Title: "legacy", Description: "legacy finding", Step: "ai-review"})
		return err
	})
	outcome, err := runPairedPipeline(t, h, launcher, nil)
	if err != nil || outcome.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("outcome=%#v err=%v", outcome, err)
	}
	state, _ := h.store.Load("alpha", "op-1")
	latest, _ := taskstate.LatestReview(state)
	if latest.Steps[0].Comparison != nil || latest.Findings[0].Reviewer != "" {
		t.Fatalf("legacy review = %#v", latest)
	}
}
