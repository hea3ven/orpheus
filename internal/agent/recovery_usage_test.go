package agent_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCapturePiUsageRetainsTokensWithoutReportedCost(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	const workdir = "/fixture/worktree"
	started := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(root, "pi.jsonl")
	writePiSessionLog(t, path, piSessionLogFixture{cwd: workdir, sessionID: "pi-no-cost", startedAt: started})
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	// Keep the same assistant records but omit their optional reported costs.
	text := strings.ReplaceAll(string(contents), `,"cost":{"total":0.001234}`, "")
	text = strings.ReplaceAll(text, `,"cost":{"total":0.000006}`, "")
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))

	got := agent.CapturePiUsage(agent.PiUsageCaptureOptions{ExecutionDir: workdir, StartedAt: started, Env: map[string]string{"PI_CODING_AGENT_SESSION_DIR": root}})

	require.NotNil(t, got.Session)
	assert.Equal(t, "pi-no-cost", got.Session.ID)
	require.NotNil(t, got.Usage)
	assert.Equal(t, 180, got.Usage.TotalTokens)
	assert.Nil(t, got.UsageCost)
	assert.Equal(t, taskstate.UsageCaptureCaptured, got.UsageCapture.Status)
}

func TestCaptureResumedPiUsageStopsAtLaterLaunchBaseline(t *testing.T) {
	for _, includeCost := range []bool{true, false} {
		name := "complete boundary"
		if !includeCost {
			name = "without cost boundary"
		}
		t.Run(name, func(t *testing.T) {
			const workdir = "/fixture/worktree"
			session := writeResumeTestSession(t, "pi", workdir, "reused-session")
			initial := taskstate.AgentUsage{InputTokens: 150, CachedInputTokens: 20, OutputTokens: 30, ReasoningOutputTokens: 5, TotalTokens: 180}
			initialCost := int64(1240)
			upper := taskstate.AgentUsage{InputTokens: 161, CachedInputTokens: 23, OutputTokens: 37, ReasoningOutputTokens: 7, TotalTokens: 198}
			upperCost := int64(1561)
			boundary := &agent.ResumedUsageBoundary{Session: &session, Usage: &upper}
			if includeCost {
				boundary.CostMicroUSD = &upperCost
			}
			file, err := os.OpenFile(session.LogPath, os.O_APPEND|os.O_WRONLY, 0o600)
			require.NoError(t, err)
			_, err = file.WriteString(`{"type":"message","id":"second","timestamp":"2026-07-07T10:03:00Z","message":{"role":"assistant","usage":{"input":11,"output":7,"cacheRead":3,"reasoning":2,"totalTokens":18,"cost":{"total":0.000321}}}}
{"type":"message","id":"third","timestamp":"2026-07-07T10:05:00Z","message":{"role":"assistant","usage":{"input":20,"output":10,"cacheRead":4,"reasoning":3,"totalTokens":30,"cost":{"total":0.000500}}}}
`)
			require.NoError(t, err)
			require.NoError(t, file.Close())

			got := agent.CaptureUsage(agent.UsageCaptureOptions{Harness: "pi", ExecutionDir: workdir,
				Launch: &taskstate.AgentLaunch{Mode: taskstate.AgentLaunchResumed, SourceSession: &session, UsageBaseline: &initial, CostBaseline: &initialCost}, ResumeBoundary: boundary})

			require.NotNil(t, got.Usage)
			assert.Equal(t, taskstate.AgentUsage{InputTokens: 11, CachedInputTokens: 3, OutputTokens: 7, ReasoningOutputTokens: 2, TotalTokens: 18}, *got.Usage)
			assert.Equal(t, taskstate.UsageCaptureCaptured, got.UsageCapture.Status)
			if includeCost {
				require.NotNil(t, got.UsageCost)
				assert.Equal(t, int64(321), got.UsageCost.AmountMicroUSD)
			} else {
				assert.Nil(t, got.UsageCost)
			}
		})
	}
}

func TestSameCanonicalSessionRequiresMatchingIDAndResolvedFile(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	path := filepath.Join(root, "session.jsonl")
	alias := filepath.Join(root, "alias.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("session"), 0o600))
	require.NoError(t, os.Symlink(path, alias))
	session := &taskstate.AgentSession{ID: "session", LogPath: path}
	same, err := agent.SameCanonicalSession(session, &taskstate.AgentSession{ID: "session", LogPath: alias})
	require.NoError(t, err)
	assert.True(t, same)
	same, err = agent.SameCanonicalSession(session, &taskstate.AgentSession{ID: "different", LogPath: alias})
	require.NoError(t, err)
	assert.False(t, same)
	same, err = agent.SameCanonicalSession(session, &taskstate.AgentSession{ID: "session", LogPath: filepath.Join(root, "missing")})
	require.Error(t, err)
	assert.False(t, same)
}
