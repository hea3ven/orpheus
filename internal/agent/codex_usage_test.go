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

func TestCaptureCodexUsageCorrelatesSessionAndTokenCount(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "worktree")
	must.NoError(os.MkdirAll(workdir, 0o755))
	sessionDir := filepath.Join(root, "sessions", "2026", "07", "07")
	must.NoError(os.MkdirAll(sessionDir, 0o755))
	sessionPath := filepath.Join(sessionDir, "rollout-2026-07-07T10-00-00-session-1.jsonl")
	writeCodexSessionLog(t, sessionPath, workdir, "session-1")

	got := agent.CaptureCodexUsage(agent.CodexUsageCaptureOptions{
		ExecutionDir: workdir,
		StartedAt:    time.Date(2026, 7, 7, 9, 59, 0, 0, time.UTC),
		FinishedAt:   time.Date(2026, 7, 7, 10, 10, 0, 0, time.UTC),
		Env:          map[string]string{"CODEX_HOME": root},
	})

	require.NotNil(t, got.Session)
	require.NotNil(t, got.Usage)
	is.Equal("session-1", got.Session.ID)
	is.Equal(sessionPath, got.Session.LogPath)
	is.Equal(taskstate.UsageCaptureCaptured, got.UsageCapture.Status)
	is.Equal(123, got.Usage.InputTokens)
	is.Equal(45, got.Usage.CachedInputTokens)
	is.Equal(67, got.Usage.OutputTokens)
	is.Equal(8, got.Usage.ReasoningOutputTokens)
	is.Equal(190, got.Usage.TotalTokens)
}

func TestCaptureCodexUsageReportsAmbiguousMatches(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "worktree")
	sessionDir := filepath.Join(root, "sessions", "2026", "07", "07")
	must.NoError(os.MkdirAll(sessionDir, 0o755))
	writeCodexSessionLog(t, filepath.Join(sessionDir, "one.jsonl"), workdir, "one")
	writeCodexSessionLog(t, filepath.Join(sessionDir, "two.jsonl"), workdir, "two")

	got := agent.CaptureCodexUsage(agent.CodexUsageCaptureOptions{
		ExecutionDir: workdir,
		StartedAt:    time.Date(2026, 7, 7, 9, 59, 0, 0, time.UTC),
		FinishedAt:   time.Date(2026, 7, 7, 10, 10, 0, 0, time.UTC),
		Env:          map[string]string{"CODEX_HOME": root},
	})

	is.Nil(got.Session)
	is.Nil(got.Usage)
	is.Equal(taskstate.UsageCaptureAmbiguous, got.UsageCapture.Status)
	is.Equal("multiple_matching_codex_sessions", got.UsageCapture.Reason)
	is.Equal(2, got.UsageCapture.CandidateCount)
}

func writeCodexSessionLog(t *testing.T, path string, cwd string, sessionID string) {
	t.Helper()

	content := `{"timestamp":"2026-07-07T10:00:00.000Z","type":"session_meta","payload":{"session_id":"` + sessionID + `","id":"` + sessionID + `","timestamp":"2026-07-07T10:00:00.000Z","cwd":"` + cwd + `","model":"gpt-5"}}
{"timestamp":"2026-07-07T10:01:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":123,"cached_input_tokens":45,"output_tokens":67,"reasoning_output_tokens":8,"total_tokens":190}}}}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
