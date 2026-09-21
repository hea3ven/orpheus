//go:build integration

package cli_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/require"
)

var workflowDirectoryNumber atomic.Uint64

func workflowDirectory(t *testing.T) string {
	t.Helper()
	return filepath.Join("/fixture/command-workflows/"+t.Name(), fmt.Sprintf("directory-%d", workflowDirectoryNumber.Add(1)))
}

func withDoctorCapture(t *testing.T, fixture *workflowFixture, capture func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions) {
	t.Helper()
	calls := 0
	fixture.options.Dependencies.CaptureUsage = func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		calls++
		return capture(opts)
	}
	t.Cleanup(func() { require.Positive(t, calls, "doctor must request session usage") })
}

func rejectDoctorCapture(t *testing.T, fixture *workflowFixture) {
	t.Helper()
	fixture.options.Dependencies.CaptureUsage = func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		t.Fatal("stored usage must not require session capture")
		return taskstate.RecordRunUsageOptions{}
	}
}

func supplyDoctorUsage(t *testing.T, fixture *workflowFixture, harness, directory string, result taskstate.RecordRunUsageOptions) {
	t.Helper()
	withDoctorCapture(t, fixture, func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
		require.Equal(t, harness, opts.Harness)
		require.Equal(t, []string{directory}, opts.ExecutionDirs)
		return result
	})
}

func doctorCapturedUsage(harness, session string, total int) taskstate.RecordRunUsageOptions {
	result := taskstate.RecordRunUsageOptions{
		Session:      &taskstate.AgentSession{ID: session, LogPath: "/fixture/sessions/" + session + ".jsonl"},
		Model:        "gpt-5",
		Usage:        &taskstate.AgentUsage{InputTokens: 1, CachedInputTokens: 2, OutputTokens: 3, ReasoningOutputTokens: 4, TotalTokens: total},
		UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched_codex_session", CandidateCount: 1},
		UsageCost:    &taskstate.AgentUsageCost{Currency: "USD", Kind: agent.UsageCostKindEstimatedAPIEquivalent, AmountMicroUSD: 99, Pricing: &taskstate.AgentUsagePricing{Model: "gpt-5", Source: "capture fixture"}},
	}
	if harness == "pi" {
		result.Model = "openai-codex/gpt-5.5"
		result.Usage = &taskstate.AgentUsage{InputTokens: 150, CachedInputTokens: 20, OutputTokens: 30, ReasoningOutputTokens: 5, TotalTokens: total}
		result.UsageCost = agentUsageCost(1240)
		result.UsageCapture.Reason = "matched_pi_session"
	}
	return result
}

func doctorPiSessionLogPath(sessionRoot, cwd, sessionID string) string {
	pathComponent := strings.ReplaceAll(strings.Trim(cwd, string(filepath.Separator)), string(filepath.Separator), "-")
	return filepath.Join(sessionRoot, pathComponent, sessionID+".jsonl")
}
