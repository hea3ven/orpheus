package review_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestRunPipelinePairedReviewerAdmitsAlternateFindingAfterPrimary(t *testing.T) {
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

func TestRunPipelinePairedReviewerKeepsDuplicateAndExcludedFindingsNonAuthoritative(t *testing.T) {
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

func TestRunPipelinePairedReviewerResolutionFailureIsPersisted(t *testing.T) {
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

func TestRunPipelinePairedReviewerAlternateFailureDoesNotFailPrimary(t *testing.T) {
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

func TestRunPipelinePairedReviewerInterruptedInputBlocksWithoutAdmission(t *testing.T) {
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

func TestRunPipelinePairedReviewerPrimaryFailureSkipsAlternate(t *testing.T) {
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

func TestRunPipelineWithoutAlternatePreservesLegacyReviewerEnvironment(t *testing.T) {
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
