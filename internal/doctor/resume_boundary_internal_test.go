package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextResumedUsageBoundaryFailsClosedWithoutLaterUsageBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("session\n"), 0o644))
	session := &taskstate.AgentSession{ID: "session-1", LogPath: path}
	initialUsage := &taskstate.AgentUsage{TotalTokens: 10}
	initialCost := int64(100)
	laterCost := int64(200)
	runs := resumedPiBoundaryRuns(session, initialUsage, &initialCost, nil, &laterCost)

	boundary, reason := nextResumedUsageBoundary(runs, 0)

	assert.Nil(t, boundary)
	assert.Contains(t, reason, "without_usage_upper_bound")
}

func TestNextResumedUsageBoundaryAllowsMissingLaterCostBaseline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("session\n"), 0o644))
	session := &taskstate.AgentSession{ID: "session-1", LogPath: path}
	initialUsage := &taskstate.AgentUsage{TotalTokens: 10}
	initialCost := int64(100)
	laterUsage := &taskstate.AgentUsage{TotalTokens: 20}
	runs := resumedPiBoundaryRuns(session, initialUsage, &initialCost, laterUsage, nil)

	boundary, reason := nextResumedUsageBoundary(runs, 0)

	require.NotNil(t, boundary)
	assert.Same(t, laterUsage, boundary.Usage)
	assert.Nil(t, boundary.CostMicroUSD)
	assert.Empty(t, reason)
}

func resumedPiBoundaryRuns(
	session *taskstate.AgentSession,
	initialUsage *taskstate.AgentUsage,
	initialCost *int64,
	laterUsage *taskstate.AgentUsage,
	laterCost *int64,
) []taskstate.RunAttempt {
	return []taskstate.RunAttempt{
		{
			Attempt: 2,
			Execution: taskstate.AgentExecution{
				Harness: "pi",
				Launch: &taskstate.AgentLaunch{
					Mode: taskstate.AgentLaunchResumed, SourceRunAttempt: 1, SourceSession: session,
					UsageBaseline: initialUsage, CostBaseline: initialCost,
				},
			},
		},
		{
			Attempt: 3,
			Execution: taskstate.AgentExecution{
				Harness: "pi",
				Launch: &taskstate.AgentLaunch{
					Mode: taskstate.AgentLaunchResumed, SourceRunAttempt: 2, SourceSession: session,
					UsageBaseline: laterUsage, CostBaseline: laterCost,
				},
			},
		},
	}
}
