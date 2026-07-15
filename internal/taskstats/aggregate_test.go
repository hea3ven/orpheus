package taskstats_test

import (
	"testing"
	"time"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/taskstats"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//nolint:funlen // The fixture keeps resolved timestamp, lifecycle, and usage assertions together.
func TestAggregateReportFromSnapshotProjectsResolvedTaskMetrics(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	createdAt := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	closedAt := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	completedAt := time.Date(2026, 7, 3, 11, 0, 0, 0, time.UTC)
	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	finishedAt := time.Date(2026, 7, 2, 10, 30, 0, 0, time.UTC)

	snapshot := taskmodel.SnapshotResult{
		Repositories: []taskmodel.RepositorySnapshot{{
			Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
			Tasks: []taskmodel.Task{
				{ID: "op-1", CreatedAt: &createdAt, ClosedAt: &closedAt},
				{ID: "op-2", CompletedAt: &completedAt},
				{ID: "op-open"},
			},
		}},
	}
	loader := fakeStateLoader{
		states: map[string]taskstate.TaskState{
			"alpha/op-1": {
				Runs: []taskstate.RunAttempt{{
					Execution: taskstate.AgentExecution{
						Model:      "gpt-5",
						StartedAt:  startedAt,
						FinishedAt: &finishedAt,
						Usage: &taskstate.AgentUsage{
							InputTokens:  100,
							OutputTokens: 50,
							TotalTokens:  150,
						},
					},
				}},
			},
			"alpha/op-2":    {},
			"alpha/op-open": {},
		},
	}

	got, failures := taskstats.AggregateReportFromSnapshot(snapshot, loader, taskstats.GroupDay)

	must.Empty(failures)
	is.Equal(taskstats.GroupDay, got.Group)
	is.Equal(1, got.TasksWithoutResolvedTimestamp)
	must.Len(got.Periods, 2)

	july2 := got.Periods[0]
	is.Equal("2026-07-02", july2.Key)
	is.Equal(1, july2.Resolved)
	is.Equal(36*time.Hour, july2.FullTaskTime)
	is.Equal(1, july2.FullTaskTimeCount)
	averageFullTaskTime, ok := july2.AverageFullTaskTime()
	assertDurationValue(t, averageFullTaskTime, ok, 36*time.Hour)
	is.Equal(2*time.Hour, july2.ImplementationTime)
	is.Equal(1, july2.ImplementationTimeCount)
	averageImplementationTime, ok := july2.AverageImplementationTime()
	assertDurationValue(t, averageImplementationTime, ok, 2*time.Hour)
	is.Equal(1, july2.Totals.Executions)
	is.Equal(30*time.Minute, july2.Totals.Duration)
	averageActiveAgentTime, ok := july2.AverageActiveAgentTime()
	assertDurationValue(t, averageActiveAgentTime, ok, 30*time.Minute)
	is.Equal(150, july2.Totals.Usage.TotalTokens)
	averageTokenCount, ok := july2.AverageTokenCount()
	assertIntValue(t, averageTokenCount, ok, 150)
	is.Zero(july2.Totals.UnknownUsage)
	is.Zero(july2.Totals.UnknownCost)

	july3 := got.Periods[1]
	is.Equal("2026-07-03", july3.Key)
	is.Equal(1, july3.Resolved)
	is.Equal(1, july3.UnknownFullTaskTime)
	is.Equal(1, july3.UnknownImplementationTime)
	is.Equal(1, july3.Totals.UnknownUsage)
	is.Equal(1, july3.Totals.UnknownCost)
	_, ok = july3.AverageTokenCount()
	is.False(ok)
	_, ok = july3.AverageCostMicroUSD()
	is.False(ok)
}

//nolint:funlen // The fixture captures mixed and all-unknown average semantics together.
func TestAggregateReportFromSnapshotAveragesKnownUsageAndCostOnly(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	closedAt := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	allUnknownClosedAt := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	finishedAt := time.Date(2026, 7, 2, 10, 30, 0, 0, time.UTC)

	snapshot := taskmodel.SnapshotResult{
		Repositories: []taskmodel.RepositorySnapshot{{
			Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
			Tasks: []taskmodel.Task{
				{ID: "op-priced", ClosedAt: &closedAt},
				{ID: "op-unpriced", ClosedAt: &closedAt},
				{ID: "op-missing-usage", ClosedAt: &closedAt},
				{ID: "op-all-unknown", ClosedAt: &allUnknownClosedAt},
			},
		}},
	}
	loader := fakeStateLoader{
		states: map[string]taskstate.TaskState{
			"alpha/op-priced": {
				Runs: []taskstate.RunAttempt{{
					Execution: taskstate.AgentExecution{
						Model:      "gpt-5",
						StartedAt:  startedAt,
						FinishedAt: &finishedAt,
						Usage: &taskstate.AgentUsage{
							InputTokens:  100,
							OutputTokens: 50,
							TotalTokens:  150,
						},
					},
				}},
			},
			"alpha/op-unpriced": {
				Runs: []taskstate.RunAttempt{{
					Execution: taskstate.AgentExecution{
						Model:      "vendor-model",
						StartedAt:  startedAt,
						FinishedAt: &finishedAt,
						Usage: &taskstate.AgentUsage{
							InputTokens:  200,
							OutputTokens: 100,
							TotalTokens:  300,
						},
					},
				}},
			},
			"alpha/op-missing-usage": {
				Runs: []taskstate.RunAttempt{{
					Execution: taskstate.AgentExecution{
						Model:      "gpt-5",
						StartedAt:  startedAt,
						FinishedAt: &finishedAt,
					},
				}},
			},
			"alpha/op-all-unknown": {
				Runs: []taskstate.RunAttempt{{
					Execution: taskstate.AgentExecution{
						Model:      "gpt-5",
						StartedAt:  startedAt,
						FinishedAt: &finishedAt,
					},
				}},
			},
		},
	}

	got, failures := taskstats.AggregateReportFromSnapshot(snapshot, loader, taskstats.GroupDay)

	must.Empty(failures)
	must.Len(got.Periods, 2)

	mixed := got.Periods[0]
	is.Equal("2026-07-02", mixed.Key)
	is.Equal(3, mixed.Resolved)
	is.Equal(450, mixed.Totals.Usage.TotalTokens)
	is.Equal(2, mixed.Totals.KnownUsageTasks)
	is.Equal(1, mixed.Totals.UnknownUsage)
	averageTokenCount, ok := mixed.AverageTokenCount()
	assertIntValue(t, averageTokenCount, ok, 225)
	is.Equal(int64(625), mixed.Totals.CostMicroUSD)
	is.Equal(1, mixed.Totals.KnownCostTasks)
	is.Equal(2, mixed.Totals.UnknownCost)
	averageCostMicroUSD, ok := mixed.AverageCostMicroUSD()
	assertInt64Value(t, averageCostMicroUSD, ok, 625)

	allUnknown := got.Periods[1]
	is.Equal("2026-07-03", allUnknown.Key)
	is.Equal(1, allUnknown.Resolved)
	is.Zero(allUnknown.Totals.KnownUsageTasks)
	is.Equal(1, allUnknown.Totals.UnknownUsage)
	_, ok = allUnknown.AverageTokenCount()
	is.False(ok)
	is.Zero(allUnknown.Totals.KnownCostTasks)
	is.Equal(1, allUnknown.Totals.UnknownCost)
	_, ok = allUnknown.AverageCostMicroUSD()
	is.False(ok)
}

func TestAggregateReportUsesStoredPiReportedCost(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	closedAt := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	finishedAt := time.Date(2026, 7, 2, 10, 30, 0, 0, time.UTC)
	snapshot := taskmodel.SnapshotResult{
		Repositories: []taskmodel.RepositorySnapshot{{
			Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
			Tasks:      []taskmodel.Task{{ID: "op-pi", ClosedAt: &closedAt}},
		}},
	}
	loader := fakeStateLoader{
		states: map[string]taskstate.TaskState{
			"alpha/op-pi": {
				Runs: []taskstate.RunAttempt{{
					Execution: taskstate.AgentExecution{
						Harness:    "pi",
						Model:      "openai-codex/gpt-5.5",
						StartedAt:  startedAt,
						FinishedAt: &finishedAt,
						Usage: &taskstate.AgentUsage{
							InputTokens:  100,
							OutputTokens: 50,
							TotalTokens:  150,
						},
						UsageCost: &taskstate.AgentUsageCost{
							Kind:           "pi_reported_estimated",
							Currency:       "USD",
							AmountMicroUSD: 1240,
							Source:         "Pi usage.cost.total",
						},
					},
				}},
			},
		},
	}

	got, failures := taskstats.AggregateReportFromSnapshot(snapshot, loader, taskstats.GroupDay)

	must.Empty(failures)
	must.Len(got.Periods, 1)
	period := got.Periods[0]
	is.Equal(int64(1240), period.Totals.CostMicroUSD)
	is.Equal(1, period.Totals.KnownCostTasks)
	is.Zero(period.Totals.UnknownCost)
	averageCostMicroUSD, ok := period.AverageCostMicroUSD()
	assertInt64Value(t, averageCostMicroUSD, ok, 1240)
}

//nolint:funlen // The fixture keeps combined and per-purpose aggregate assertions together.
func TestAggregateReportIncludesTerminalSyncConflictResolutionExecutions(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	closedAt := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	implementationStartedAt := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	implementationFinishedAt := time.Date(2026, 7, 2, 9, 6, 0, 0, time.UTC)
	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	finishedAt := time.Date(2026, 7, 2, 10, 12, 0, 0, time.UTC)
	snapshot := taskmodel.SnapshotResult{
		Repositories: []taskmodel.RepositorySnapshot{{
			Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
			Tasks:      []taskmodel.Task{{ID: "op-conflict", ClosedAt: &closedAt}},
		}},
	}
	loader := fakeStateLoader{
		states: map[string]taskstate.TaskState{
			"alpha/op-conflict": {
				Runs: []taskstate.RunAttempt{{
					Execution: taskstate.AgentExecution{
						Purpose:    taskstate.AgentExecutionPurposeImplementation,
						Status:     taskstate.RunStatusSucceeded,
						Model:      "gpt-5",
						StartedAt:  implementationStartedAt,
						FinishedAt: &implementationFinishedAt,
						Usage: &taskstate.AgentUsage{
							InputTokens:  50,
							OutputTokens: 10,
							TotalTokens:  60,
						},
					},
				}},
				Events: []taskstate.Event{
					{
						Type: taskstate.EventSyncConflictStarted,
						Execution: &taskstate.AgentExecution{
							Purpose:   taskstate.AgentExecutionPurposeSyncConflictResolution,
							Status:    taskstate.RunStatusRunning,
							Model:     "gpt-5",
							StartedAt: startedAt,
						},
					},
					{
						Type: taskstate.EventSyncConflictFinished,
						Execution: &taskstate.AgentExecution{
							Purpose:    taskstate.AgentExecutionPurposeSyncConflictResolution,
							Status:     taskstate.RunStatusSucceeded,
							Model:      "gpt-5",
							StartedAt:  startedAt,
							FinishedAt: &finishedAt,
							Usage: &taskstate.AgentUsage{
								InputTokens:  120,
								OutputTokens: 30,
								TotalTokens:  150,
							},
						},
					},
				},
			},
		},
	}

	got, failures := taskstats.AggregateReportFromSnapshot(snapshot, loader, taskstats.GroupDay)

	must.Empty(failures)
	must.Len(got.Periods, 1)
	period := got.Periods[0]
	is.Equal(2, period.Totals.Executions)
	is.Equal(18*time.Minute, period.Totals.Duration)
	is.Equal(210, period.Totals.Usage.TotalTokens)
	is.Equal(1, period.Totals.KnownUsageTasks)
	is.Zero(period.Totals.UnknownUsage)
	must.Len(period.TotalsByPurpose, 2)

	implementationTotals := period.TotalsByPurpose[taskstate.AgentExecutionPurposeImplementation]
	is.Equal(1, implementationTotals.Executions)
	is.Equal(6*time.Minute, implementationTotals.Duration)
	is.Equal(60, implementationTotals.Usage.TotalTokens)
	is.Equal(1, implementationTotals.KnownUsageTasks)
	is.Zero(implementationTotals.UnknownUsage)

	conflictTotals := period.TotalsByPurpose[taskstate.AgentExecutionPurposeSyncConflictResolution]
	is.Equal(1, conflictTotals.Executions)
	is.Equal(12*time.Minute, conflictTotals.Duration)
	is.Equal(150, conflictTotals.Usage.TotalTokens)
	is.Equal(1, conflictTotals.KnownUsageTasks)
	is.Zero(conflictTotals.UnknownUsage)
}

func TestAggregateReportFromSnapshotExcludesPartialUnknownTaskFromAverages(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	closedAt := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	finishedAt := time.Date(2026, 7, 2, 10, 30, 0, 0, time.UTC)
	secondFinishedAt := time.Date(2026, 7, 2, 11, 30, 0, 0, time.UTC)

	snapshot := taskmodel.SnapshotResult{
		Repositories: []taskmodel.RepositorySnapshot{{
			Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
			Tasks: []taskmodel.Task{
				{ID: "op-partial", ClosedAt: &closedAt},
			},
		}},
	}
	loader := fakeStateLoader{
		states: map[string]taskstate.TaskState{
			"alpha/op-partial": {
				Runs: []taskstate.RunAttempt{
					{
						Execution: taskstate.AgentExecution{
							Model:      "gpt-5",
							StartedAt:  startedAt,
							FinishedAt: &finishedAt,
							Usage: &taskstate.AgentUsage{
								InputTokens:  100,
								OutputTokens: 50,
								TotalTokens:  150,
							},
						},
					},
					{
						Execution: taskstate.AgentExecution{
							Model:      "gpt-5",
							StartedAt:  startedAt.Add(time.Hour),
							FinishedAt: &secondFinishedAt,
						},
					},
				},
			},
		},
	}

	got, failures := taskstats.AggregateReportFromSnapshot(snapshot, loader, taskstats.GroupDay)

	must.Empty(failures)
	must.Len(got.Periods, 1)

	period := got.Periods[0]
	is.Equal(150, period.Totals.Usage.TotalTokens)
	is.Zero(period.Totals.KnownUsageTasks)
	is.Equal(1, period.Totals.UnknownUsage)
	_, ok := period.AverageTokenCount()
	is.False(ok)
	is.Equal(int64(625), period.Totals.CostMicroUSD)
	is.Zero(period.Totals.KnownCostTasks)
	is.Equal(1, period.Totals.UnknownCost)
	_, ok = period.AverageCostMicroUSD()
	is.False(ok)
}

func assertDurationValue(t *testing.T, got time.Duration, ok bool, want time.Duration) {
	t.Helper()
	require.True(t, ok)
	assert.Equal(t, want, got)
}

func assertIntValue(t *testing.T, got int, ok bool, want int) {
	t.Helper()
	require.True(t, ok)
	assert.Equal(t, want, got)
}

func assertInt64Value(t *testing.T, got int64, ok bool, want int64) {
	t.Helper()
	require.True(t, ok)
	assert.Equal(t, want, got)
}

type fakeStateLoader struct {
	states map[string]taskstate.TaskState
}

func (l fakeStateLoader) Load(repoID, taskID string) (taskstate.TaskState, error) {
	state, ok := l.states[repoID+"/"+taskID]
	if !ok {
		return taskstate.TaskState{}, nil
	}
	return state, nil
}
