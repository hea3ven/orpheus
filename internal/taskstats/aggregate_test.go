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

func TestAggregateReportFromSnapshotExcludesEpicsBeforeStateLoading(t *testing.T) {
	closedAt := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	snapshot := taskmodel.SnapshotResult{
		Repositories: []taskmodel.RepositorySnapshot{{
			Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
			Tasks: []taskmodel.Task{
				{ID: "op-task", IssueType: taskmodel.IssueTypeTask, ClosedAt: &closedAt},
				{ID: "op-bug", IssueType: taskmodel.IssueTypeBug, ClosedAt: &closedAt},
				{ID: "op-chore", IssueType: taskmodel.IssueTypeChore, ClosedAt: &closedAt},
				{ID: "op-custom", IssueType: taskmodel.IssueType("custom"), ClosedAt: &closedAt},
				{ID: "op-unknown", ClosedAt: &closedAt},
				{ID: "op-epic-resolved", IssueType: taskmodel.IssueTypeEpic, ClosedAt: &closedAt},
				{ID: "op-epic-open", IssueType: taskmodel.IssueTypeEpic},
			},
		}},
	}

	for _, tc := range []struct {
		name    string
		group   taskstats.Group
		wantKey string
	}{
		{name: "day", group: taskstats.GroupDay, wantKey: "2026-07-02"},
		{name: "month", group: taskstats.GroupMonth, wantKey: "2026-07"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			loader := &recordingStateLoader{}

			got, failures := taskstats.AggregateReportFromSnapshot(snapshot, loader, tc.group)

			must.Empty(failures)
			is.Zero(got.TasksWithoutResolvedTimestamp)
			must.Len(got.Periods, 1)
			period := got.Periods[0]
			is.Equal(tc.wantKey, period.Key)
			is.Equal(5, period.Resolved)
			is.Equal(5, period.UnknownFullTaskTime)
			is.Equal(5, period.UnknownImplementationTime)
			is.Equal(5, period.Totals.UnknownUsage)
			is.Equal(5, period.Totals.UnknownCost)
			is.ElementsMatch([]string{
				"alpha/op-task",
				"alpha/op-bug",
				"alpha/op-chore",
				"alpha/op-custom",
				"alpha/op-unknown",
			}, loader.loaded)
		})
	}
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

//nolint:funlen // The fixture documents completed, follow-up, incomplete, and filtered runs together.
func TestAggregateImplementationViewUsesAgentDoneDurationAndKeepsKnownUsage(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(5 * time.Minute)
	followUpStartedAt := startedAt.Add(time.Hour)
	followUpCompletedAt := followUpStartedAt.Add(10 * time.Minute)
	processFinishedAt := startedAt.Add(30 * time.Minute)
	incompleteStartedAt := startedAt.Add(2 * time.Hour)
	incompleteFinishedAt := incompleteStartedAt.Add(20 * time.Minute)
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{
		{
			Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
			Tasks: []taskmodel.Task{
				{ID: "op-complete"},
				{ID: "op-incomplete"},
			},
		},
		{
			Repository: taskmodel.Repository{ID: "beta", Name: "Beta"},
			Tasks:      []taskmodel.Task{{ID: "op-filtered"}},
		},
	}}
	loader := fakeStateLoader{states: map[string]taskstate.TaskState{
		"alpha/op-complete": {
			Runs: []taskstate.RunAttempt{
				{
					Execution: taskstate.AgentExecution{
						Model:      "gpt-5",
						StartedAt:  startedAt,
						FinishedAt: &processFinishedAt,
						Usage:      &taskstate.AgentUsage{TotalTokens: 100},
					},
					Completion: &taskstate.Completion{CompletedAt: completedAt},
				},
				{
					Execution: taskstate.AgentExecution{
						Model:     "gpt-5",
						StartedAt: followUpStartedAt,
						Usage:     &taskstate.AgentUsage{TotalTokens: 200},
					},
					Completion: &taskstate.Completion{CompletedAt: followUpCompletedAt},
					ReviewFollowUp: &taskstate.ReviewFollowUp{
						ReviewAttempt:  1,
						FindingIndexes: []int{0},
					},
				},
			},
		},
		"alpha/op-incomplete": {
			Runs: []taskstate.RunAttempt{{
				Execution: taskstate.AgentExecution{
					Model:      "gpt-5",
					StartedAt:  incompleteStartedAt,
					FinishedAt: &incompleteFinishedAt,
					Usage:      &taskstate.AgentUsage{TotalTokens: 300},
				},
			}},
		},
		"beta/op-filtered": {
			Runs: []taskstate.RunAttempt{{Execution: taskstate.AgentExecution{StartedAt: startedAt}}},
		},
	}}

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group:        taskstats.GroupWeek,
		View:         taskstats.ViewImplementation,
		Repositories: []string{"alpha"},
	})

	must.Empty(failures)
	must.Len(got.Periods, 1)
	period := got.Periods[0]
	is.Equal("2026-W27", period.Key)
	is.Equal(2, period.Tasks)
	is.Equal(15*time.Minute, period.ImplementationAgentTime.Median)
	is.Equal(15*time.Minute, period.ImplementationAgentTime.P75)
	is.Equal(1, period.ImplementationAgentTime.Known)
	is.Equal(2, period.ImplementationAgentTime.Samples)
	is.Equal(300, period.Tokens.Median)
	is.Equal(2, period.Tokens.Known)
	is.Equal(2, period.Tokens.Samples)
}

func TestAggregateImplementationViewKeepsPartialKnownConsumptionTotals(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	snapshot, loader := partialKnownConsumptionFixture()

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group: taskstats.GroupDay,
		View:  taskstats.ViewImplementation,
	})

	must.Empty(failures)
	must.Len(got.Periods, 1)
	period := got.Periods[0]
	is.Equal(150, period.Tokens.Total)
	is.Zero(period.Tokens.Known)
	is.Equal(1, period.Tokens.Samples)
	is.Equal(int64(625), period.Cost.TotalMicroUSD)
	is.Zero(period.Cost.Known)
	is.Equal(1, period.Cost.Samples)
}

func TestAggregateConsumptionViewKeepsPartialKnownConsumptionTotals(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	snapshot, loader := partialKnownConsumptionFixture()

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group: taskstats.GroupDay,
		View:  taskstats.ViewConsumption,
	})

	must.Empty(failures)
	must.Len(got.Periods, 1)
	period := got.Periods[0]
	is.Equal(1, period.Tasks)
	is.Equal(2, period.Executions)
	is.Equal(150, period.Tokens.Total)
	is.Zero(period.Tokens.Known)
	is.Equal(1, period.Tokens.Samples)
	is.Equal(int64(625), period.Cost.TotalMicroUSD)
	is.Zero(period.Cost.Known)
	is.Equal(1, period.Cost.Samples)
}

func TestAggregateReviewViewSumsReviewActivityWithoutFollowUpGap(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	blockedFinishedAt := startedAt.Add(10 * time.Minute)
	followUpStartedAt := startedAt.Add(20 * time.Minute)
	followUpCompletedAt := startedAt.Add(2 * time.Hour)
	passedStartedAt := followUpCompletedAt
	passedFinishedAt := passedStartedAt.Add(15 * time.Minute)
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{{
		Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
		Tasks:      []taskmodel.Task{{ID: "op-review-gap"}},
	}}}
	loader := fakeStateLoader{states: map[string]taskstate.TaskState{
		"alpha/op-review-gap": {
			Runs: []taskstate.RunAttempt{{
				Execution: taskstate.AgentExecution{StartedAt: followUpStartedAt},
				Completion: &taskstate.Completion{
					CompletedAt: followUpCompletedAt,
				},
				ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}},
			}},
			Reviews: []taskstate.ReviewAttempt{
				{
					Attempt:    1,
					Status:     taskstate.ReviewStatusBlocked,
					StartedAt:  startedAt,
					FinishedAt: &blockedFinishedAt,
					Findings:   []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking}},
				},
				{
					Attempt:    2,
					Status:     taskstate.ReviewStatusPassed,
					StartedAt:  passedStartedAt,
					FinishedAt: &passedFinishedAt,
				},
			},
		},
	}}

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group: taskstats.GroupDay,
		View:  taskstats.ViewReview,
	})

	must.Empty(failures)
	must.Len(got.Periods, 1)
	period := got.Periods[0]
	is.Equal(1, period.Tasks)
	is.Equal(25*time.Minute, period.ReviewTime.Median)
	is.Equal(25*time.Minute, period.ReviewTime.P75)
	is.Equal(1, period.ReviewTime.Known)
	is.Equal(1, period.ReviewTime.Samples)
	is.Equal(1, period.RepairCycles.Median)
	is.Equal(1, period.RepairTasks)
	is.Equal(1, period.BlockedReviews)
	is.Equal(1, period.BlockingFindings)
}

func TestAggregateReviewViewSeparatesRepairCyclesAndOperationalOutcomes(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	blockedFinishedAt := startedAt.Add(10 * time.Minute)
	passedStartedAt := startedAt.Add(2 * time.Hour)
	passedFinishedAt := passedStartedAt.Add(15 * time.Minute)
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{{
		Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
		Tasks:      []taskmodel.Task{{ID: "op-review"}},
	}}}
	loader := fakeStateLoader{states: map[string]taskstate.TaskState{
		"alpha/op-review": {
			Runs: []taskstate.RunAttempt{{
				ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}},
			}},
			Reviews: []taskstate.ReviewAttempt{
				{
					Attempt:    1,
					Status:     taskstate.ReviewStatusBlocked,
					StartedAt:  startedAt,
					FinishedAt: &blockedFinishedAt,
					Findings:   []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking}},
				},
				{
					Attempt:    2,
					Status:     taskstate.ReviewStatusFailed,
					StartedAt:  passedStartedAt,
					FinishedAt: &passedFinishedAt,
				},
				{Attempt: 3, Status: taskstate.ReviewStatusAborted, StartedAt: passedStartedAt, FinishedAt: &passedFinishedAt},
				{Attempt: 4, Status: taskstate.ReviewStatusWaitingForManual, StartedAt: passedStartedAt},
			},
		},
	}}

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group: taskstats.GroupDay,
		View:  taskstats.ViewReview,
	})

	must.Empty(failures)
	must.Len(got.Periods, 1)
	period := got.Periods[0]
	is.Equal(1, period.Tasks)
	is.Equal(1, period.RepairCycles.Median)
	is.Equal(1, period.RepairTasks)
	is.Equal(1, period.BlockedReviews)
	is.Equal(1, period.BlockingFindings)
	is.Equal(1, period.OperationalFailures)
	is.Equal(1, period.AbortedReviews)
	is.Equal(1, period.PausedReviews)
	is.Equal(0, period.ReviewTime.Known)
	is.Equal(1, period.ReviewTime.Samples)
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

//nolint:funlen // The fixture documents model outcome and execution-usage attribution together.
func TestAggregateImplementationModelViewSeparatesOutcomesAndUsageAttribution(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	singleDoneAt := startedAt.Add(20 * time.Minute)
	singleClosedAt := startedAt.Add(3 * time.Hour)
	mixedGPTDoneAt := startedAt.Add(2*time.Hour + 10*time.Minute)
	mixedClaudeStartedAt := startedAt.Add(3 * time.Hour)
	mixedClaudeDoneAt := mixedClaudeStartedAt.Add(5 * time.Minute)
	mixedClosedAt := startedAt.Add(5 * time.Hour)
	blockedFinishedAt := startedAt.Add(time.Hour + 15*time.Minute)
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{{
		Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
		Tasks: []taskmodel.Task{
			{ID: "op-single", ClosedAt: &singleClosedAt},
			{ID: "op-mixed", ClosedAt: &mixedClosedAt},
			{ID: "op-unknown", ClosedAt: &singleClosedAt},
		},
	}}}
	loader := fakeStateLoader{states: map[string]taskstate.TaskState{
		"alpha/op-single": {
			Runs: []taskstate.RunAttempt{{
				Execution: taskstate.AgentExecution{
					Model:     "gpt-5",
					StartedAt: startedAt,
					Usage:     &taskstate.AgentUsage{TotalTokens: 100},
				},
				Completion: &taskstate.Completion{CompletedAt: singleDoneAt},
			}},
			Reviews: []taskstate.ReviewAttempt{{Status: taskstate.ReviewStatusPassed, StartedAt: startedAt.Add(time.Hour)}},
		},
		"alpha/op-unknown": {
			Runs: []taskstate.RunAttempt{{
				Execution: taskstate.AgentExecution{
					StartedAt: startedAt.Add(30 * time.Minute),
					Usage:     &taskstate.AgentUsage{TotalTokens: 25},
				},
				Completion: &taskstate.Completion{CompletedAt: startedAt.Add(35 * time.Minute)},
			}},
		},
		"alpha/op-mixed": {
			Runs: []taskstate.RunAttempt{
				{
					Execution: taskstate.AgentExecution{
						Model:     "gpt-5",
						StartedAt: startedAt.Add(2 * time.Hour),
						Usage:     &taskstate.AgentUsage{TotalTokens: 150},
					},
					Completion: &taskstate.Completion{CompletedAt: mixedGPTDoneAt},
				},
				{
					Execution: taskstate.AgentExecution{
						Model:     "claude-sonnet",
						StartedAt: mixedClaudeStartedAt,
						Usage:     &taskstate.AgentUsage{TotalTokens: 90},
					},
					Completion:     &taskstate.Completion{CompletedAt: mixedClaudeDoneAt},
					ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1},
				},
			},
			Reviews: []taskstate.ReviewAttempt{{
				Attempt:    1,
				Status:     taskstate.ReviewStatusBlocked,
				StartedAt:  startedAt.Add(time.Hour),
				FinishedAt: &blockedFinishedAt,
				Findings:   []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking}},
			}},
		},
	}}

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group: taskstats.GroupDay,
		View:  taskstats.ViewImplementationModel,
	})

	must.Empty(failures)
	gpt := modelCohortByKey(t, got.Cohorts, "gpt-5")
	is.Equal(1, gpt.Tasks)
	is.Equal(20*time.Minute, gpt.CompletionTime.Median)
	is.Equal(1, gpt.FirstPassApprovals)
	is.Equal(250, gpt.Tokens.Total)
	is.Equal(2, gpt.Tokens.Known)
	is.Equal(2, gpt.Tokens.Samples)

	mixed := modelCohortByKey(t, got.Cohorts, "mixed")
	is.Equal(1, mixed.Tasks)
	is.Equal(1, mixed.RepairCycles.Median)
	is.Equal(1, mixed.RepairTasks)
	is.Equal(1, mixed.BlockingFindings)
	is.Zero(mixed.Tokens.Samples)

	claude := modelCohortByKey(t, got.Cohorts, "claude-sonnet")
	is.Zero(claude.Tasks)
	is.Equal(90, claude.Tokens.Total)
	is.Equal(1, claude.Tokens.Known)

	unknown := modelCohortByKey(t, got.Cohorts, "unknown")
	is.Equal(1, unknown.Tasks)
	is.Equal(25, unknown.Tokens.Total)
}

//nolint:funlen // The fixture documents harness/thinking cohort selection and sparse usage rows.
func TestAggregateImplementationModelViewIncludesHarnessAndThinkingInCohorts(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	doneAt := startedAt.Add(10 * time.Minute)
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{{
		Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
		Tasks: []taskmodel.Task{
			{ID: "op-codex-high"},
			{ID: "op-pi-high"},
			{ID: "op-codex-default"},
			{ID: "op-mixed-thinking"},
		},
	}}}
	loader := fakeStateLoader{states: map[string]taskstate.TaskState{
		"alpha/op-codex-high": {
			Runs: []taskstate.RunAttempt{{
				Execution: taskstate.AgentExecution{
					Harness:   "codex",
					Model:     "gpt-5",
					Thinking:  "high",
					StartedAt: startedAt,
					Usage:     &taskstate.AgentUsage{TotalTokens: 100},
				},
				Completion: &taskstate.Completion{CompletedAt: doneAt},
			}},
		},
		"alpha/op-pi-high": {
			Runs: []taskstate.RunAttempt{{
				Execution: taskstate.AgentExecution{
					Harness:   "pi",
					Model:     "gpt-5",
					Thinking:  "high",
					StartedAt: startedAt.Add(time.Hour),
					Usage:     &taskstate.AgentUsage{TotalTokens: 200},
				},
				Completion: &taskstate.Completion{CompletedAt: doneAt.Add(time.Hour)},
			}},
		},
		"alpha/op-codex-default": {
			Runs: []taskstate.RunAttempt{{
				Execution: taskstate.AgentExecution{
					Harness:   "codex",
					Model:     "gpt-5",
					Args:      []string{"exec", "--model", "gpt-5"},
					StartedAt: startedAt.Add(2 * time.Hour),
					Usage:     &taskstate.AgentUsage{TotalTokens: 300},
				},
				Completion: &taskstate.Completion{CompletedAt: doneAt.Add(2 * time.Hour)},
			}},
		},
		"alpha/op-mixed-thinking": {
			Runs: []taskstate.RunAttempt{
				{
					Execution: taskstate.AgentExecution{
						Harness:   "codex",
						Model:     "gpt-5",
						Thinking:  "high",
						StartedAt: startedAt.Add(3 * time.Hour),
						Usage:     &taskstate.AgentUsage{TotalTokens: 400},
					},
					Completion: &taskstate.Completion{CompletedAt: doneAt.Add(3 * time.Hour)},
				},
				{
					Execution: taskstate.AgentExecution{
						Harness:   "codex",
						Model:     "gpt-5",
						Thinking:  "medium",
						StartedAt: startedAt.Add(4 * time.Hour),
						Usage:     &taskstate.AgentUsage{TotalTokens: 500},
					},
					Completion: &taskstate.Completion{CompletedAt: doneAt.Add(4 * time.Hour)},
				},
			},
		},
	}}

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group: taskstats.GroupDay,
		View:  taskstats.ViewImplementationModel,
	})

	must.Empty(failures)
	codexHigh := modelCohortByKey(t, got.Cohorts, "gpt-5 (harness=codex, thinking=high)")
	is.Equal(1, codexHigh.Tasks)
	is.Equal(500, codexHigh.Tokens.Total)
	piHigh := modelCohortByKey(t, got.Cohorts, "gpt-5 (harness=pi, thinking=high)")
	is.Equal(1, piHigh.Tasks)
	is.Equal(200, piHigh.Tokens.Total)
	codexDefault := modelCohortByKey(t, got.Cohorts, "gpt-5 (harness=codex, thinking=default)")
	is.Equal(1, codexDefault.Tasks)
	is.Equal(300, codexDefault.Tokens.Total)
	codexMedium := modelCohortByKey(t, got.Cohorts, "gpt-5 (harness=codex, thinking=medium)")
	is.Zero(codexMedium.Tasks)
	is.Equal(500, codexMedium.Tokens.Total)
	mixed := modelCohortByKey(t, got.Cohorts, "mixed")
	is.Equal(1, mixed.Tasks)
	is.Zero(mixed.Tokens.Samples)
}

//nolint:funlen // The fixture keeps manual-only, mixed reviewer, and usage attribution assertions together.
func TestAggregateReviewerModelViewKeepsManualOnlyAndMixedCohortsVisible(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(10 * time.Minute)
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{{
		Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
		Tasks: []taskmodel.Task{
			{ID: "op-manual"},
			{ID: "op-reviewer"},
			{ID: "op-mixed-reviewer"},
		},
	}}}
	loader := fakeStateLoader{states: map[string]taskstate.TaskState{
		"alpha/op-manual": {
			Reviews: []taskstate.ReviewAttempt{{
				Status:     taskstate.ReviewStatusPassed,
				StartedAt:  startedAt,
				FinishedAt: &finishedAt,
				Steps:      []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindManual, Name: "operator"}},
			}},
		},
		"alpha/op-reviewer": {
			Reviews: []taskstate.ReviewAttempt{{
				Status:     taskstate.ReviewStatusPassed,
				StartedAt:  startedAt,
				FinishedAt: &finishedAt,
				Steps: []taskstate.ReviewStep{{
					Kind: taskstate.ReviewStepKindAgentReview,
					Name: "agent",
					Execution: &taskstate.AgentExecution{
						Model:     "reviewer-gpt",
						StartedAt: startedAt,
						Usage:     &taskstate.AgentUsage{TotalTokens: 100},
					},
				}},
			}},
		},
		"alpha/op-mixed-reviewer": {
			Reviews: []taskstate.ReviewAttempt{{
				Status:     taskstate.ReviewStatusBlocked,
				StartedAt:  startedAt,
				FinishedAt: &finishedAt,
				Findings:   []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking}},
				Steps: []taskstate.ReviewStep{
					{
						Kind: taskstate.ReviewStepKindAgentReview,
						Name: "gpt",
						Execution: &taskstate.AgentExecution{
							Model:     "reviewer-gpt",
							StartedAt: startedAt,
							Usage:     &taskstate.AgentUsage{TotalTokens: 50},
						},
					},
					{
						Kind: taskstate.ReviewStepKindAgentReview,
						Name: "claude",
						Execution: &taskstate.AgentExecution{
							Model:     "reviewer-claude",
							StartedAt: startedAt,
							Usage:     &taskstate.AgentUsage{TotalTokens: 70},
						},
					},
				},
			}},
		},
	}}

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group: taskstats.GroupDay,
		View:  taskstats.ViewReviewerModel,
	})

	must.Empty(failures)
	manual := modelCohortByKey(t, got.Cohorts, "manual-only")
	is.Equal(1, manual.Tasks)
	is.Equal(1, manual.FirstPassApprovals)
	is.Zero(manual.Tokens.Samples)

	gpt := modelCohortByKey(t, got.Cohorts, "reviewer-gpt")
	is.Equal(1, gpt.Tasks)
	is.Equal(150, gpt.Tokens.Total)
	is.Equal(2, gpt.Tokens.Known)

	mixed := modelCohortByKey(t, got.Cohorts, "mixed")
	is.Equal(1, mixed.Tasks)
	is.Equal(1, mixed.BlockingFindings)
	is.Zero(mixed.Tokens.Samples)

	claude := modelCohortByKey(t, got.Cohorts, "reviewer-claude")
	is.Zero(claude.Tasks)
	is.Equal(70, claude.Tokens.Total)
}

func TestAggregateReviewerModelViewTreatsAgentReviewWithoutExecutionAsUnknown(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	snapshot, loader := missingReviewerExecutionFixture()

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group: taskstats.GroupDay,
		View:  taskstats.ViewReviewerModel,
	})

	must.Empty(failures)
	unknown := modelCohortByKey(t, got.Cohorts, "unknown")
	is.Equal(1, unknown.Tasks)
	is.Equal(1, unknown.FirstPassApprovals)
	is.Zero(unknown.Tokens.Samples)

	mixed := modelCohortByKey(t, got.Cohorts, "mixed")
	is.Equal(1, mixed.Tasks)
	is.Equal(1, mixed.BlockingFindings)
	is.Zero(mixed.Tokens.Samples)

	gpt := modelCohortByKey(t, got.Cohorts, "reviewer-gpt")
	is.Zero(gpt.Tasks)
	is.Equal(50, gpt.Tokens.Total)
	assertNoModelCohort(t, got.Cohorts, "manual-only")
}

func TestAggregateModelPairViewTreatsAgentReviewWithoutExecutionAsUnknown(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	snapshot, loader := missingReviewerExecutionFixture()

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group: taskstats.GroupDay,
		View:  taskstats.ViewModelPair,
	})

	must.Empty(failures)
	unknownPair := modelCohortByKey(t, got.Cohorts, "gpt-5/unknown")
	is.Equal(1, unknownPair.Tasks)
	is.Equal(1, unknownPair.FirstPassApprovals)
	is.Equal(100, unknownPair.Tokens.Total)

	mixedPair := modelCohortByKey(t, got.Cohorts, "gpt-5/mixed")
	is.Equal(1, mixedPair.Tasks)
	is.Equal(1, mixedPair.BlockingFindings)
	is.Equal(75, mixedPair.Tokens.Total)

	gptSparse := modelCohortByKey(t, got.Cohorts, "gpt-5/reviewer-gpt")
	is.Zero(gptSparse.Tasks)
	is.Equal(50, gptSparse.Tokens.Total)
	assertNoModelCohort(t, got.Cohorts, "gpt-5/manual-only")
}

func TestAggregateModelPairViewCombinesSamePairUsagePerTask(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(10 * time.Minute)
	reviewStartedAt := startedAt.Add(20 * time.Minute)
	reviewFinishedAt := startedAt.Add(30 * time.Minute)
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{{
		Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
		Tasks:      []taskmodel.Task{{ID: "op-agent-reviewed"}},
	}}}
	loader := fakeStateLoader{states: map[string]taskstate.TaskState{
		"alpha/op-agent-reviewed": {
			Runs: []taskstate.RunAttempt{{
				Execution: taskstate.AgentExecution{
					Model:     "gpt-5",
					StartedAt: startedAt,
					Usage:     &taskstate.AgentUsage{TotalTokens: 100},
					UsageCost: &taskstate.AgentUsageCost{AmountMicroUSD: 1000},
				},
				Completion: &taskstate.Completion{CompletedAt: completedAt},
			}},
			Reviews: []taskstate.ReviewAttempt{{
				Status:     taskstate.ReviewStatusPassed,
				StartedAt:  reviewStartedAt,
				FinishedAt: &reviewFinishedAt,
				Steps: []taskstate.ReviewStep{{
					Kind: taskstate.ReviewStepKindAgentReview,
					Name: "agent",
					Execution: &taskstate.AgentExecution{
						Model:     "reviewer-gpt",
						StartedAt: reviewStartedAt,
						Usage:     &taskstate.AgentUsage{TotalTokens: 50},
						UsageCost: &taskstate.AgentUsageCost{AmountMicroUSD: 500},
					},
				}},
			}},
		},
	}}

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group: taskstats.GroupDay,
		View:  taskstats.ViewModelPair,
	})

	must.Empty(failures)
	pair := modelCohortByKey(t, got.Cohorts, "gpt-5/reviewer-gpt")
	is.Equal(1, pair.Tasks)
	is.Equal(1, pair.FirstPassApprovals)
	is.Equal(150, pair.Tokens.Total)
	is.Equal(150, pair.Tokens.Median)
	is.Equal(1, pair.Tokens.Known)
	is.Equal(1, pair.Tokens.Samples)
	is.Equal(int64(1500), pair.Cost.TotalMicroUSD)
	is.Equal(int64(1500), pair.Cost.MedianMicroUSD)
	is.Equal(1, pair.Cost.Known)
	is.Equal(1, pair.Cost.Samples)
}

//nolint:funlen // The fixture verifies slash-containing model IDs do not collapse pair cohorts.
func TestAggregateModelPairViewKeepsSlashContainingModelIDsDistinct(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(10 * time.Minute)
	reviewStartedAt := startedAt.Add(20 * time.Minute)
	reviewFinishedAt := startedAt.Add(30 * time.Minute)
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{{
		Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
		Tasks: []taskmodel.Task{
			{ID: "op-implementation-slash"},
			{ID: "op-reviewer-slash"},
		},
	}}}
	loader := fakeStateLoader{states: map[string]taskstate.TaskState{
		"alpha/op-implementation-slash": modelPairState(modelPairStateInput{
			implementationModel:  "openai-codex/gpt-5.5",
			reviewerModel:        "mini",
			startedAt:            startedAt,
			completedAt:          completedAt,
			reviewStartedAt:      reviewStartedAt,
			reviewFinishedAt:     reviewFinishedAt,
			implementationTokens: 100,
			reviewerTokens:       10,
		}),
		"alpha/op-reviewer-slash": modelPairState(modelPairStateInput{
			implementationModel:  "openai-codex",
			reviewerModel:        "gpt-5.5/mini",
			startedAt:            startedAt.Add(time.Hour),
			completedAt:          completedAt.Add(time.Hour),
			reviewStartedAt:      reviewStartedAt.Add(time.Hour),
			reviewFinishedAt:     reviewFinishedAt.Add(time.Hour),
			implementationTokens: 200,
			reviewerTokens:       20,
		}),
	}}

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group: taskstats.GroupDay,
		View:  taskstats.ViewModelPair,
	})

	must.Empty(failures)
	slashImplementation := modelCohortByModels(
		t,
		got.Cohorts,
		"openai-codex/gpt-5.5",
		"mini",
	)
	is.Equal(1, slashImplementation.Tasks)
	is.Equal(1, slashImplementation.FirstPassApprovals)
	is.Equal(110, slashImplementation.Tokens.Total)

	slashReviewer := modelCohortByModels(
		t,
		got.Cohorts,
		"openai-codex",
		"gpt-5.5/mini",
	)
	is.Equal(1, slashReviewer.Tasks)
	is.Equal(1, slashReviewer.FirstPassApprovals)
	is.Equal(220, slashReviewer.Tokens.Total)
}

//nolint:funlen // The fixture verifies pair rows and sparse actual-execution attribution rows together.
func TestAggregateModelPairViewComparesImplementationReviewerPairings(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(10 * time.Minute)
	reviewFinishedAt := startedAt.Add(30 * time.Minute)
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{{
		Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
		Tasks: []taskmodel.Task{
			{ID: "op-manual-pair"},
			{ID: "op-unreviewed-pair"},
			{ID: "op-mixed-pair"},
		},
	}}}
	loader := fakeStateLoader{states: map[string]taskstate.TaskState{
		"alpha/op-manual-pair": {
			Runs: []taskstate.RunAttempt{{
				Execution:  taskstate.AgentExecution{Model: "gpt-5", StartedAt: startedAt, Usage: &taskstate.AgentUsage{TotalTokens: 100}},
				Completion: &taskstate.Completion{CompletedAt: completedAt},
			}},
			Reviews: []taskstate.ReviewAttempt{{
				Status:     taskstate.ReviewStatusPassed,
				StartedAt:  startedAt.Add(20 * time.Minute),
				FinishedAt: &reviewFinishedAt,
			}},
		},
		"alpha/op-unreviewed-pair": {
			Runs: []taskstate.RunAttempt{{
				Execution: taskstate.AgentExecution{
					Model:     "gpt-5",
					StartedAt: startedAt.Add(40 * time.Minute),
					Usage:     &taskstate.AgentUsage{TotalTokens: 40},
				},
				Completion: &taskstate.Completion{CompletedAt: startedAt.Add(45 * time.Minute)},
			}},
		},
		"alpha/op-mixed-pair": {
			Runs: []taskstate.RunAttempt{
				{
					Execution:  taskstate.AgentExecution{Model: "gpt-5", StartedAt: startedAt.Add(time.Hour), Usage: &taskstate.AgentUsage{TotalTokens: 50}},
					Completion: &taskstate.Completion{CompletedAt: startedAt.Add(time.Hour + 5*time.Minute)},
				},
				{
					Execution:  taskstate.AgentExecution{Model: "claude-sonnet", StartedAt: startedAt.Add(2 * time.Hour), Usage: &taskstate.AgentUsage{TotalTokens: 70}},
					Completion: &taskstate.Completion{CompletedAt: startedAt.Add(2*time.Hour + 5*time.Minute)},
				},
			},
			Reviews: []taskstate.ReviewAttempt{{
				Status:     taskstate.ReviewStatusPassed,
				StartedAt:  startedAt.Add(3 * time.Hour),
				FinishedAt: &reviewFinishedAt,
				Steps: []taskstate.ReviewStep{{
					Kind: taskstate.ReviewStepKindAgentReview,
					Name: "agent",
					Execution: &taskstate.AgentExecution{
						Model:     "reviewer-gpt",
						StartedAt: startedAt.Add(3 * time.Hour),
						Usage:     &taskstate.AgentUsage{TotalTokens: 30},
					},
				}},
			}},
		},
	}}

	got, failures := taskstats.AggregateReportFromSnapshotWithOptions(snapshot, loader, taskstats.AggregateReportOptions{
		Group: taskstats.GroupDay,
		View:  taskstats.ViewModelPair,
	})

	must.Empty(failures)
	manualPair := modelCohortByKey(t, got.Cohorts, "gpt-5/manual-only")
	is.Equal(1, manualPair.Tasks)
	is.Equal(1, manualPair.FirstPassApprovals)
	is.Equal(100, manualPair.Tokens.Total)

	unknownPair := modelCohortByKey(t, got.Cohorts, "gpt-5/unknown")
	is.Equal(1, unknownPair.Tasks)
	is.Zero(unknownPair.FirstPassApprovals)
	is.Equal(40, unknownPair.Tokens.Total)

	mixedPair := modelCohortByKey(t, got.Cohorts, "mixed/reviewer-gpt")
	is.Equal(1, mixedPair.Tasks)
	is.Equal(1, mixedPair.FirstPassApprovals)
	is.Equal(30, mixedPair.Tokens.Total)

	claudeSparse := modelCohortByKey(t, got.Cohorts, "claude-sonnet/reviewer-gpt")
	is.Zero(claudeSparse.Tasks)
	is.Equal(70, claudeSparse.Tokens.Total)
}

func partialKnownConsumptionFixture() (taskmodel.SnapshotResult, fakeStateLoader) {
	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(30 * time.Minute)
	missingStartedAt := startedAt.Add(time.Hour)
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{{
		Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
		Tasks:      []taskmodel.Task{{ID: "op-partial"}},
	}}}
	loader := fakeStateLoader{states: map[string]taskstate.TaskState{
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
				{Execution: taskstate.AgentExecution{Model: "gpt-5", StartedAt: missingStartedAt}},
			},
		},
	}}
	return snapshot, loader
}

func missingReviewerExecutionFixture() (taskmodel.SnapshotResult, fakeStateLoader) {
	startedAt := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	completedAt := startedAt.Add(10 * time.Minute)
	reviewStartedAt := startedAt.Add(20 * time.Minute)
	reviewFinishedAt := startedAt.Add(30 * time.Minute)
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{{
		Repository: taskmodel.Repository{ID: "alpha", Name: "Alpha"},
		Tasks: []taskmodel.Task{
			{ID: "op-unknown-reviewer"},
			{ID: "op-mixed-reviewer"},
		},
	}}}
	loader := fakeStateLoader{states: map[string]taskstate.TaskState{
		"alpha/op-unknown-reviewer": {
			Runs: []taskstate.RunAttempt{{
				Execution:  taskstate.AgentExecution{Model: "gpt-5", StartedAt: startedAt, Usage: &taskstate.AgentUsage{TotalTokens: 100}},
				Completion: &taskstate.Completion{CompletedAt: completedAt},
			}},
			Reviews: []taskstate.ReviewAttempt{{
				Status:     taskstate.ReviewStatusPassed,
				StartedAt:  reviewStartedAt,
				FinishedAt: &reviewFinishedAt,
				Steps:      []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindAgentReview, Name: "agent"}},
			}},
		},
		"alpha/op-mixed-reviewer": {
			Runs: []taskstate.RunAttempt{{
				Execution:  taskstate.AgentExecution{Model: "gpt-5", StartedAt: startedAt.Add(time.Hour), Usage: &taskstate.AgentUsage{TotalTokens: 75}},
				Completion: &taskstate.Completion{CompletedAt: completedAt.Add(time.Hour)},
			}},
			Reviews: []taskstate.ReviewAttempt{{
				Status:     taskstate.ReviewStatusBlocked,
				StartedAt:  reviewStartedAt.Add(time.Hour),
				FinishedAt: &reviewFinishedAt,
				Findings:   []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking}},
				Steps: []taskstate.ReviewStep{
					{Kind: taskstate.ReviewStepKindAgentReview, Name: "agent"},
					{
						Kind: taskstate.ReviewStepKindAgentReview,
						Name: "known-agent",
						Execution: &taskstate.AgentExecution{
							Model:     "reviewer-gpt",
							StartedAt: reviewStartedAt.Add(time.Hour),
							Usage:     &taskstate.AgentUsage{TotalTokens: 50},
						},
					},
				},
			}},
		},
	}}
	return snapshot, loader
}

type modelPairStateInput struct {
	implementationModel  string
	reviewerModel        string
	startedAt            time.Time
	completedAt          time.Time
	reviewStartedAt      time.Time
	reviewFinishedAt     time.Time
	implementationTokens int
	reviewerTokens       int
}

func modelPairState(input modelPairStateInput) taskstate.TaskState {
	return taskstate.TaskState{
		Runs: []taskstate.RunAttempt{{
			Execution: taskstate.AgentExecution{
				Model:     input.implementationModel,
				StartedAt: input.startedAt,
				Usage:     &taskstate.AgentUsage{TotalTokens: input.implementationTokens},
			},
			Completion: &taskstate.Completion{CompletedAt: input.completedAt},
		}},
		Reviews: []taskstate.ReviewAttempt{{
			Status:     taskstate.ReviewStatusPassed,
			StartedAt:  input.reviewStartedAt,
			FinishedAt: &input.reviewFinishedAt,
			Steps: []taskstate.ReviewStep{{
				Kind: taskstate.ReviewStepKindAgentReview,
				Name: "agent",
				Execution: &taskstate.AgentExecution{
					Model:     input.reviewerModel,
					StartedAt: input.reviewStartedAt,
					Usage:     &taskstate.AgentUsage{TotalTokens: input.reviewerTokens},
				},
			}},
		}},
	}
}

func modelCohortByKey(
	t *testing.T,
	cohorts []taskstats.AggregateModelCohort,
	key string,
) taskstats.AggregateModelCohort {
	t.Helper()
	for _, cohort := range cohorts {
		if cohort.Key == key {
			return cohort
		}
	}
	require.Failf(t, "missing model cohort", "key %q not found in %#v", key, cohorts)
	return taskstats.AggregateModelCohort{}
}

func modelCohortByModels(
	t *testing.T,
	cohorts []taskstats.AggregateModelCohort,
	implementationModel string,
	reviewerModel string,
) taskstats.AggregateModelCohort {
	t.Helper()
	for _, cohort := range cohorts {
		matchesImplementation := cohort.ImplementationModel == implementationModel
		matchesReviewer := cohort.ReviewerModel == reviewerModel
		if matchesImplementation && matchesReviewer {
			return cohort
		}
	}
	require.Failf(
		t,
		"missing model cohort",
		"implementation model %q reviewer model %q not found in %#v",
		implementationModel,
		reviewerModel,
		cohorts,
	)
	return taskstats.AggregateModelCohort{}
}

func assertNoModelCohort(t *testing.T, cohorts []taskstats.AggregateModelCohort, key string) {
	t.Helper()
	for _, cohort := range cohorts {
		if cohort.Key == key {
			require.Failf(t, "unexpected model cohort", "key %q found in %#v", key, cohorts)
		}
	}
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

type recordingStateLoader struct {
	loaded []string
}

func (l *recordingStateLoader) Load(repoID, taskID string) (taskstate.TaskState, error) {
	l.loaded = append(l.loaded, repoID+"/"+taskID)
	return taskstate.TaskState{}, nil
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
