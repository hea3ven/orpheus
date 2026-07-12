package taskstats

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// Group identifies the time period used for aggregate stats.
type Group string

const (
	GroupDay   Group = "day"
	GroupMonth Group = "month"
)

// StateLoader loads local Orpheus task state for aggregate projection.
type StateLoader interface {
	Load(repoID, taskID string) (taskstate.TaskState, error)
}

// AggregateReport is the non-rendering projection for resolved task trends.
type AggregateReport struct {
	Group                         Group
	Periods                       []AggregatePeriod
	TasksWithoutResolvedTimestamp int
}

// AggregatePeriod contains metrics for one resolved task period.
type AggregatePeriod struct {
	Key      string
	Resolved int

	FullTaskTime        time.Duration
	FullTaskTimeCount   int
	UnknownFullTaskTime int

	ImplementationTime        time.Duration
	ImplementationTimeCount   int
	UnknownImplementationTime int

	Totals AggregateTotals
}

// AggregateTotals contains execution, active time, token, and cost totals.
type AggregateTotals struct {
	Executions      int
	Duration        time.Duration
	Usage           taskstate.AgentUsage
	KnownUsageTasks int
	CostMicroUSD    int64
	KnownCostTasks  int
	UnknownUsage    int
	UnknownCost     int

	usageForAverage        taskstate.AgentUsage
	costMicroUSDForAverage int64
}

// AverageFullTaskTime returns creation-to-resolution average for this period.
func (p AggregatePeriod) AverageFullTaskTime() (time.Duration, bool) {
	return averageDuration(p.FullTaskTime, p.FullTaskTimeCount)
}

// AverageImplementationTime returns first-dispatch-to-resolution average for this period.
func (p AggregatePeriod) AverageImplementationTime() (time.Duration, bool) {
	return averageDuration(p.ImplementationTime, p.ImplementationTimeCount)
}

// AverageActiveAgentTime returns average recorded active agent time per resolved task.
func (p AggregatePeriod) AverageActiveAgentTime() (time.Duration, bool) {
	return averageDuration(p.Totals.Duration, p.Resolved)
}

// AverageTokenCount returns average recorded tokens per resolved task.
func (p AggregatePeriod) AverageTokenCount() (int, bool) {
	if p.Totals.KnownUsageTasks <= 0 {
		return 0, false
	}
	return p.Totals.usageForAverage.TotalTokens / p.Totals.KnownUsageTasks, true
}

// AverageCostMicroUSD returns average estimated API-equivalent cost per resolved task.
func (p AggregatePeriod) AverageCostMicroUSD() (int64, bool) {
	if p.Totals.KnownCostTasks <= 0 {
		return 0, false
	}
	return p.Totals.costMicroUSDForAverage / int64(p.Totals.KnownCostTasks), true
}

// ParseGroup normalizes a user-facing aggregate stats group value.
func ParseGroup(group string) (Group, error) {
	switch strings.ToLower(strings.TrimSpace(group)) {
	case "day", "daily":
		return GroupDay, nil
	case "month", "monthly":
		return GroupMonth, nil
	default:
		return "", fmt.Errorf("unsupported stats group %q; expected day or month", group)
	}
}

// AggregateReportFromSnapshot projects aggregate stats from task snapshots and local state.
func AggregateReportFromSnapshot(
	snapshot taskmodel.SnapshotResult,
	stateLoader StateLoader,
	group Group,
) (AggregateReport, []taskmodel.RepoFailure) {
	periods := map[string]*AggregatePeriod{}
	failures := make([]taskmodel.RepoFailure, 0)
	report := AggregateReport{Group: group}

	for _, repoSnapshot := range snapshot.Repositories {
		for _, taskItem := range repoSnapshot.Tasks {
			taskState, err := stateLoader.Load(repoSnapshot.Repository.ID, taskItem.ID)
			if err != nil {
				failures = append(failures, taskmodel.RepoFailure{
					Repository: repoSnapshot.Repository,
					Source:     "task_state",
					Operation:  "load",
					Err:        err,
				})
				continue
			}

			resolvedAt, ok := resolvedAt(taskItem, taskState)
			if !ok {
				report.TasksWithoutResolvedTimestamp++
				continue
			}

			key := periodKey(resolvedAt, group)
			period := periods[key]
			if period == nil {
				period = &AggregatePeriod{Key: key}
				periods[key] = period
			}
			period.addTask(taskItem, taskState, resolvedAt)
		}
	}

	report.Periods = make([]AggregatePeriod, 0, len(periods))
	for _, period := range periods {
		report.Periods = append(report.Periods, *period)
	}
	sort.Slice(report.Periods, func(i, j int) bool {
		return report.Periods[i].Key < report.Periods[j].Key
	})
	return report, failures
}

func (p *AggregatePeriod) addTask(
	taskItem taskmodel.Task,
	state taskstate.TaskState,
	resolvedAt time.Time,
) {
	p.Resolved++
	if taskItem.CreatedAt != nil && !taskItem.CreatedAt.IsZero() && !resolvedAt.Before(*taskItem.CreatedAt) {
		p.FullTaskTime += resolvedAt.Sub(*taskItem.CreatedAt)
		p.FullTaskTimeCount++
	} else {
		p.UnknownFullTaskTime++
	}

	if firstDispatch, ok := firstImplementationDispatchAt(state); ok && !resolvedAt.Before(firstDispatch) {
		p.ImplementationTime += resolvedAt.Sub(firstDispatch)
		p.ImplementationTimeCount++
	} else {
		p.UnknownImplementationTime++
	}

	p.Totals.addTask(executionRecords(state))
}

func resolvedAt(taskItem taskmodel.Task, state taskstate.TaskState) (time.Time, bool) {
	finalization := taskstate.FinalizationFacts(state)
	if finalization.ClosedAt != nil && !finalization.ClosedAt.IsZero() {
		return finalization.ClosedAt.UTC(), true
	}
	if closed, ok := latestTaskClosedEventAt(state); ok {
		return closed.UTC(), true
	}
	if taskItem.ClosedAt != nil && !taskItem.ClosedAt.IsZero() {
		return taskItem.ClosedAt.UTC(), true
	}
	if taskItem.CompletedAt != nil && !taskItem.CompletedAt.IsZero() {
		return taskItem.CompletedAt.UTC(), true
	}
	return time.Time{}, false
}

func latestTaskClosedEventAt(state taskstate.TaskState) (time.Time, bool) {
	var latest time.Time
	for _, event := range state.Events {
		if event.Type != taskstate.EventTaskClosed || event.At.IsZero() {
			continue
		}
		if latest.IsZero() || event.At.After(latest) {
			latest = event.At
		}
	}
	return latest, !latest.IsZero()
}

func firstImplementationDispatchAt(state taskstate.TaskState) (time.Time, bool) {
	var first time.Time
	for _, run := range state.Runs {
		startedAt := run.Execution.StartedAt
		if startedAt.IsZero() {
			continue
		}
		if first.IsZero() || startedAt.Before(first) {
			first = startedAt
		}
	}
	return first, !first.IsZero()
}

func periodKey(value time.Time, group Group) string {
	value = value.UTC()
	if group == GroupMonth {
		return value.Format("2006-01")
	}
	return value.Format("2006-01-02")
}

type executionRecord struct {
	execution taskstate.AgentExecution
}

func executionRecords(state taskstate.TaskState) []executionRecord {
	records := make([]executionRecord, 0, len(state.Runs))
	for _, run := range state.Runs {
		records = append(records, executionRecord{execution: run.Execution})
	}
	for _, reviewAttempt := range state.Reviews {
		for _, step := range reviewAttempt.Steps {
			if step.Execution == nil {
				continue
			}
			records = append(records, executionRecord{execution: *step.Execution})
		}
	}
	return records
}

func (t *AggregateTotals) addTask(records []executionRecord) {
	if len(records) == 0 {
		t.UnknownUsage++
		t.UnknownCost++
		return
	}

	knownUsage := true
	knownCost := true
	var taskUsage taskstate.AgentUsage
	var taskCostMicroUSD int64
	for _, record := range records {
		execution := record.execution
		t.Executions++
		if duration, ok := durationValue(execution); ok {
			t.Duration += duration
		}
		if execution.Usage == nil {
			knownUsage = false
			knownCost = false
			continue
		}
		taskUsage.InputTokens += execution.Usage.InputTokens
		taskUsage.CachedInputTokens += execution.Usage.CachedInputTokens
		taskUsage.OutputTokens += execution.Usage.OutputTokens
		taskUsage.ReasoningOutputTokens += execution.Usage.ReasoningOutputTokens
		taskUsage.TotalTokens += execution.Usage.TotalTokens
		t.Usage.InputTokens += execution.Usage.InputTokens
		t.Usage.CachedInputTokens += execution.Usage.CachedInputTokens
		t.Usage.OutputTokens += execution.Usage.OutputTokens
		t.Usage.ReasoningOutputTokens += execution.Usage.ReasoningOutputTokens
		t.Usage.TotalTokens += execution.Usage.TotalTokens
		if cost, ok := executionCost(execution); ok {
			taskCostMicroUSD += cost.AmountMicroUSD
			t.CostMicroUSD += cost.AmountMicroUSD
		} else {
			knownCost = false
		}
	}

	if knownUsage {
		t.KnownUsageTasks++
		t.usageForAverage.InputTokens += taskUsage.InputTokens
		t.usageForAverage.CachedInputTokens += taskUsage.CachedInputTokens
		t.usageForAverage.OutputTokens += taskUsage.OutputTokens
		t.usageForAverage.ReasoningOutputTokens += taskUsage.ReasoningOutputTokens
		t.usageForAverage.TotalTokens += taskUsage.TotalTokens
	} else {
		t.UnknownUsage++
	}
	if knownCost {
		t.KnownCostTasks++
		t.costMicroUSDForAverage += taskCostMicroUSD
	} else {
		t.UnknownCost++
	}
}

func durationValue(execution taskstate.AgentExecution) (time.Duration, bool) {
	if execution.DurationMillis > 0 {
		return time.Duration(execution.DurationMillis) * time.Millisecond, true
	}
	if execution.FinishedAt == nil || execution.StartedAt.IsZero() {
		return 0, false
	}
	duration := execution.FinishedAt.Sub(execution.StartedAt)
	if duration < 0 {
		return 0, false
	}
	return duration, true
}

func executionCost(execution taskstate.AgentExecution) (agent.UsageCost, bool) {
	if execution.Usage == nil {
		return agent.UsageCost{}, false
	}
	return agent.EstimateUsageCost(execution.Model, *execution.Usage)
}

func averageDuration(total time.Duration, count int) (time.Duration, bool) {
	if count <= 0 {
		return 0, false
	}
	return total / time.Duration(count), true
}
