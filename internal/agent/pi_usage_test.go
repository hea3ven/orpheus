package agent_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCapturePiUsageCorrelatesSessionAssistantUsageAndReportedCost(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "worktree")
	must.NoError(os.MkdirAll(workdir, 0o755))
	sessionPath := filepath.Join(root, "project", "2026-07-07T10-00-00-000Z_pi-session.jsonl")
	writePiSessionLog(t, sessionPath, piSessionLogFixture{
		cwd:         workdir,
		sessionID:   "pi-session",
		sessionName: "(op-1) Stats",
		startedAt:   time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	})

	got := agent.CapturePiUsage(agent.PiUsageCaptureOptions{
		ExecutionDir: workdir,
		SessionName:  "(op-1) Stats",
		StartedAt:    time.Date(2026, 7, 7, 9, 59, 30, 0, time.UTC),
		Env:          map[string]string{"PI_CODING_AGENT_SESSION_DIR": root},
	})

	must.NotNil(got.Session)
	must.NotNil(got.Usage)
	must.NotNil(got.UsageCost)
	is.Equal("pi-session", got.Session.ID)
	is.Equal(sessionPath, got.Session.LogPath)
	is.Equal("openai-codex/gpt-5.5", got.Model)
	is.Equal(taskstate.UsageCaptureCaptured, got.UsageCapture.Status)
	is.Equal("matched_pi_session", got.UsageCapture.Reason)
	is.Equal(150, got.Usage.InputTokens)
	is.Equal(20, got.Usage.CachedInputTokens)
	is.Equal(30, got.Usage.OutputTokens)
	is.Equal(5, got.Usage.ReasoningOutputTokens)
	is.Equal(180, got.Usage.TotalTokens)
	is.Equal(agent.UsageCostKindPiReportedEstimated, got.UsageCost.Kind)
	is.Equal(int64(1240), got.UsageCost.AmountMicroUSD)
}

func TestCapturePiUsageRejectsMismatchedRecordedSessionName(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "worktree")
	must.NoError(os.MkdirAll(workdir, 0o755))
	writePiSessionLog(t, filepath.Join(root, "one.jsonl"), piSessionLogFixture{
		cwd:         workdir,
		sessionID:   "pi-session",
		sessionName: "other session",
		startedAt:   time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	})

	got := agent.CapturePiUsage(agent.PiUsageCaptureOptions{
		ExecutionDir: workdir,
		SessionName:  "(op-1) Stats",
		StartedAt:    time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		Env:          map[string]string{"PI_CODING_AGENT_SESSION_DIR": root},
	})

	is.Nil(got.Session)
	is.Nil(got.Usage)
	is.Equal(taskstate.UsageCaptureUnknown, got.UsageCapture.Status)
	is.Equal("no_matching_pi_session", got.UsageCapture.Reason)
}

func TestCapturePiUsageReportsAmbiguousMatches(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "worktree")
	must.NoError(os.MkdirAll(workdir, 0o755))
	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	onePath := filepath.Join(root, "one.jsonl")
	twoPath := filepath.Join(root, "two.jsonl")
	writePiSessionLog(t, onePath, piSessionLogFixture{
		cwd:         workdir,
		sessionID:   "one",
		sessionName: "Pi one",
		startedAt:   startedAt.Add(time.Second),
	})
	writePiSessionLog(t, twoPath, piSessionLogFixture{
		cwd:         workdir,
		sessionID:   "two",
		sessionName: "Pi two",
		startedAt:   startedAt.Add(6 * time.Second),
	})

	got := agent.CapturePiUsage(agent.PiUsageCaptureOptions{
		ExecutionDir: workdir,
		StartedAt:    startedAt,
		Env:          map[string]string{"PI_CODING_AGENT_SESSION_DIR": root},
	})

	is.Nil(got.Session)
	is.Nil(got.Usage)
	is.Equal(taskstate.UsageCaptureAmbiguous, got.UsageCapture.Status)
	is.Equal("multiple_matching_pi_sessions", got.UsageCapture.Reason)
	is.Equal(2, got.UsageCapture.CandidateCount)
	must.Len(got.Candidates, 2)
	is.Equal(taskstate.UsageCaptureCandidate{
		SessionID:         "one",
		SessionName:       "Pi one",
		LogPath:           onePath,
		CWD:               workdir,
		Model:             "openai-codex/gpt-5.5",
		StartedAt:         startedAt.Add(time.Second),
		StartOffsetMillis: 1000,
	}, got.Candidates[0])
	is.Equal(taskstate.UsageCaptureCandidate{
		SessionID:         "two",
		SessionName:       "Pi two",
		LogPath:           twoPath,
		CWD:               workdir,
		Model:             "openai-codex/gpt-5.5",
		StartedAt:         startedAt.Add(6 * time.Second),
		StartOffsetMillis: 6000,
	}, got.Candidates[1])
}

func TestCapturePiUsagePreservesNoAssistantUsageReasonForClosestSession(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "worktree")
	must.NoError(os.MkdirAll(workdir, 0o755))
	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	writePiSessionLog(t, filepath.Join(root, "closest.jsonl"), piSessionLogFixture{
		cwd:                workdir,
		sessionID:          "closest",
		startedAt:          startedAt.Add(time.Second),
		omitAssistantUsage: true,
	})
	writePiSessionLog(t, filepath.Join(root, "farther.jsonl"), piSessionLogFixture{
		cwd:       workdir,
		sessionID: "farther",
		startedAt: startedAt.Add(time.Minute),
	})

	got := agent.CapturePiUsage(agent.PiUsageCaptureOptions{
		ExecutionDir: workdir,
		StartedAt:    startedAt,
		Env:          map[string]string{"PI_CODING_AGENT_SESSION_DIR": root},
	})

	must.NotNil(got.Session)
	is.Nil(got.Usage)
	is.Equal("closest", got.Session.ID)
	is.Equal(taskstate.UsageCaptureUnknown, got.UsageCapture.Status)
	is.Equal("matching_pi_session_has_no_assistant_usage", got.UsageCapture.Reason)
	is.Equal(2, got.UsageCapture.CandidateCount)
}

type piSessionLogFixture struct {
	cwd                string
	sessionID          string
	sessionName        string
	startedAt          time.Time
	omitAssistantUsage bool
}

func writePiSessionLog(t *testing.T, path string, fixture piSessionLogFixture) {
	t.Helper()

	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	timestamp := fixture.startedAt.UTC().Format(time.RFC3339Nano)
	nameField := ""
	if fixture.sessionName != "" {
		nameField = `,"name":"` + fixture.sessionName + `"`
	}
	content := `{"type":"session","version":3,"id":"` + fixture.sessionID + `","timestamp":"` + timestamp + `","cwd":"` + fixture.cwd + `"` + nameField + `}
{"type":"model_change","id":"model","timestamp":"` + timestamp + `","provider":"openai-codex","modelId":"gpt-5.5"}
{"type":"message","id":"user","timestamp":"` + timestamp + `","message":{"role":"user"}}
`
	if !fixture.omitAssistantUsage {
		content += `
{"type":"message","id":"assistant-1","timestamp":"` + timestamp + `","message":{"role":"assistant"},"usage":{"input":100,"output":20,"cacheRead":10,"cacheWrite":3,"reasoning":5,"totalTokens":120,"cost":{"total":0.001234}}}
{"type":"message","id":"assistant-2","timestamp":"` + timestamp + `","message":{"role":"assistant"},"usage":{"input":50,"output":10,"cacheRead":7,"cacheWrite":0,"reasoning":0,"totalTokens":60,"cost":{"total":0.000006}}}
`
	}
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
