package agent_test

import (
	"os"
	"path/filepath"
	"strconv"
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
		Env:          map[string]string{"CODEX_HOME": root},
	})

	is.Nil(got.Session)
	is.Nil(got.Usage)
	is.Equal(taskstate.UsageCaptureAmbiguous, got.UsageCapture.Status)
	is.Equal("multiple_matching_codex_sessions", got.UsageCapture.Reason)
	is.Equal(2, got.UsageCapture.CandidateCount)
}

func TestCaptureCodexUsageSelectsClearlyClosestSession(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "worktree")
	sessionDir := filepath.Join(root, "sessions", "2026", "07", "07")
	must.NoError(os.MkdirAll(sessionDir, 0o755))
	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	writeCodexSessionLogAt(t, filepath.Join(sessionDir, "closest.jsonl"), workdir, "closest", startedAt.Add(time.Second))
	writeCodexSessionLogAt(t, filepath.Join(sessionDir, "other.jsonl"), workdir, "other", startedAt.Add(111*time.Second))

	got := agent.CaptureCodexUsage(agent.CodexUsageCaptureOptions{
		ExecutionDir: workdir,
		StartedAt:    startedAt,
		Env:          map[string]string{"CODEX_HOME": root},
	})

	must.NotNil(got.Session)
	is.Equal("closest", got.Session.ID)
	is.Equal(taskstate.UsageCaptureCaptured, got.UsageCapture.Status)
	is.Equal("matched_closest_codex_session", got.UsageCapture.Reason)
	is.Equal(2, got.UsageCapture.CandidateCount)
}

func TestCaptureCodexUsageKeepsUnsafeClosestMatchesAmbiguous(t *testing.T) {
	tests := []struct {
		name    string
		offsets []time.Duration
	}{
		{name: "candidates too close together", offsets: []time.Duration{time.Second, 6 * time.Second}},
		{name: "closest candidate too far from start", offsets: []time.Duration{20 * time.Second, 110 * time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			root := t.TempDir()
			workdir := filepath.Join(t.TempDir(), "worktree")
			sessionDir := filepath.Join(root, "sessions", "2026", "07", "07")
			must.NoError(os.MkdirAll(sessionDir, 0o755))
			startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
			for index, offset := range tt.offsets {
				sessionID := strconv.Itoa(index)
				writeCodexSessionLogAt(
					t,
					filepath.Join(sessionDir, sessionID+".jsonl"),
					workdir,
					sessionID,
					startedAt.Add(offset),
				)
			}

			got := agent.CaptureCodexUsage(agent.CodexUsageCaptureOptions{
				ExecutionDir: workdir,
				StartedAt:    startedAt,
				Env:          map[string]string{"CODEX_HOME": root},
			})

			is.Nil(got.Session)
			is.Equal(taskstate.UsageCaptureAmbiguous, got.UsageCapture.Status)
			is.Equal(2, got.UsageCapture.CandidateCount)
		})
	}
}

func TestCaptureCodexUsageIgnoresSessionsNearExecutionFinish(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "worktree")
	sessionDir := filepath.Join(root, "sessions", "2026", "07", "07")
	must.NoError(os.MkdirAll(sessionDir, 0o755))
	writeCodexSessionLogAt(
		t,
		filepath.Join(sessionDir, "started.jsonl"),
		workdir,
		"started",
		time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
	)
	writeCodexSessionLogAt(
		t,
		filepath.Join(sessionDir, "finish-boundary.jsonl"),
		workdir,
		"finish-boundary",
		time.Date(2026, 7, 7, 10, 58, 30, 0, time.UTC),
	)

	got := agent.CaptureCodexUsage(agent.CodexUsageCaptureOptions{
		ExecutionDir: workdir,
		StartedAt:    time.Date(2026, 7, 7, 9, 59, 0, 0, time.UTC),
		Env:          map[string]string{"CODEX_HOME": root},
	})

	require.NotNil(t, got.Session)
	is.Equal(taskstate.UsageCaptureCaptured, got.UsageCapture.Status)
	is.Equal("started", got.Session.ID)
}

func TestCaptureCodexUsageRequiresMatchingSessionCWD(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := t.TempDir()
	workdir := filepath.Join(t.TempDir(), "worktree")
	otherDir := filepath.Join(t.TempDir(), "other-worktree")
	sessionDir := filepath.Join(root, "sessions", "2026", "07", "07")
	must.NoError(os.MkdirAll(sessionDir, 0o755))
	writeCodexSessionLog(t, filepath.Join(sessionDir, "matching.jsonl"), workdir, "matching")
	writeCodexSessionLog(t, filepath.Join(sessionDir, "other.jsonl"), otherDir, "other")

	got := agent.CaptureCodexUsage(agent.CodexUsageCaptureOptions{
		ExecutionDir: workdir,
		StartedAt:    time.Date(2026, 7, 7, 9, 59, 0, 0, time.UTC),
		Env:          map[string]string{"CODEX_HOME": root},
	})

	require.NotNil(t, got.Session)
	is.Equal(taskstate.UsageCaptureCaptured, got.UsageCapture.Status)
	is.Equal("matching", got.Session.ID)
}

func writeCodexSessionLog(t *testing.T, path string, cwd string, sessionID string) {
	t.Helper()

	writeCodexSessionLogAt(t, path, cwd, sessionID, time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC))
}

func writeCodexSessionLogAt(t *testing.T, path string, cwd string, sessionID string, startedAt time.Time) {
	t.Helper()

	timestamp := startedAt.UTC().Format(time.RFC3339Nano)
	tokenTimestamp := startedAt.Add(time.Minute).UTC().Format(time.RFC3339Nano)
	content := `{"timestamp":"` + timestamp + `","type":"session_meta","payload":{"session_id":"` + sessionID + `","id":"` + sessionID + `","timestamp":"` + timestamp + `","cwd":"` + cwd + `","model":"gpt-5"}}
{"timestamp":"` + tokenTimestamp + `","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":123,"cached_input_tokens":45,"output_tokens":67,"reasoning_output_tokens":8,"total_tokens":190}}}}
`
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}
