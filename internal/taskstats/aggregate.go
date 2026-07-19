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
	GroupWeek  Group = "week"
	GroupMonth Group = "month"
)

// View identifies the analytical aggregate stats view.
type View string

const (
	ViewThroughput          View = "throughput"
	ViewImplementation      View = "implementation"
	ViewReview              View = "review"
	ViewConsumption         View = "consumption"
	ViewImplementationModel View = "implementation-model"
	ViewReviewerModel       View = "reviewer-model"
	ViewModelPair           View = "model-pair"
)

const (
	modelCohortMixed      = "mixed"
	modelCohortManualOnly = "manual-only"
	modelCohortUnknown    = "unknown"
)

type modelCohortKey struct {
	implementationModel string
	reviewerModel       string
}

// StateLoader loads local Orpheus task state for aggregate projection.
type StateLoader interface {
	Load(repoID, taskID string) (taskstate.TaskState, error)
}

// AggregateReportOptions controls time-based aggregate projection.
type AggregateReportOptions struct {
	Group        Group
	View         View
	From         *time.Time
	To           *time.Time
	Repositories []string
}

// AggregateReport is the non-rendering projection for time-grouped task stats.
type AggregateReport struct {
	Group Group
	View  View
	From  *time.Time
	To    *time.Time
	Repos []string

	Periods []AggregatePeriod
	Cohorts []AggregateModelCohort

	TasksWithoutAnchor            int
	TasksWithoutResolvedTimestamp int
	TasksWithoutImplementation    int
	TasksWithoutReviewActivity    int
	ExecutionsWithoutStartedAt    int
}

// AggregatePeriod contains metrics for one time period.
type AggregatePeriod struct {
	Key   string
	Tasks int

	Resolved int

	WorkflowTime            DurationCohort
	ImplementationAgentTime DurationCohort
	ReviewTime              DurationCohort
	RepairCycles            IntCohort
	Tokens                  IntCohort
	Cost                    CostCohort

	Executions          int
	ImplementationFails int
	FirstPassApprovals  int
	RepairTasks         int
	BlockedReviews      int
	BlockingFindings    int
	OperationalFailures int
	AbortedReviews      int
	PausedReviews       int

	// Legacy fields retained for callers/tests that still inspect the pre-view projection.
	FullTaskTime        time.Duration
	FullTaskTimeCount   int
	UnknownFullTaskTime int

	ImplementationTime        time.Duration
	ImplementationTimeCount   int
	UnknownImplementationTime int

	Totals          AggregateTotals
	TotalsByPurpose map[taskstate.AgentExecutionPurpose]AggregateTotals
}

// AggregateModelCohort contains metrics for one implementation/reviewer model cohort.
type AggregateModelCohort struct {
	Key                 string
	ImplementationModel string
	ReviewerModel       string
	Tasks               int

	CompletionTime DurationCohort
	WorkflowTime   DurationCohort
	RepairCycles   IntCohort
	Tokens         IntCohort
	Cost           CostCohort

	FirstPassApprovals  int
	RepairTasks         int
	BlockedReviews      int
	BlockingFindings    int
	OperationalFailures int
}

// DurationCohort stores per-task duration distribution and known-data coverage.
type DurationCohort struct {
	Samples int
	Known   int
	Median  time.Duration
	P75     time.Duration

	values []time.Duration
}

// Unknown returns the number of samples without known data.
func (c DurationCohort) Unknown() int { return c.Samples - c.Known }

// IntCohort stores per-task integer distribution and known-data coverage.
type IntCohort struct {
	Samples int
	Known   int
	Median  int
	Total   int

	values []int
}

// Unknown returns the number of samples without known data.
func (c IntCohort) Unknown() int { return c.Samples - c.Known }

// CostCohort stores per-task cost distribution, totals, and known-data coverage.
type CostCohort struct {
	Samples        int
	Known          int
	MedianMicroUSD int64
	TotalMicroUSD  int64

	values []int64
}

// Unknown returns the number of samples without known data.
func (c CostCohort) Unknown() int { return c.Samples - c.Known }

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
	case "week", "weekly":
		return GroupWeek, nil
	case "month", "monthly":
		return GroupMonth, nil
	default:
		return "", fmt.Errorf("unsupported stats group %q; expected day, week, or month", group)
	}
}

// ParseView normalizes a user-facing aggregate stats view value.
func ParseView(view string) (View, error) {
	switch strings.ToLower(strings.TrimSpace(view)) {
	case "", "throughput":
		return ViewThroughput, nil
	case "implementation", "implementer", "speed":
		return ViewImplementation, nil
	case "review", "reviewer":
		return ViewReview, nil
	case "consumption", "usage", "cost":
		return ViewConsumption, nil
	case "implementation-model", "implementer-model", "implementation_model", "implementer_model":
		return ViewImplementationModel, nil
	case "reviewer-model", "review-model", "reviewer_model", "review_model":
		return ViewReviewerModel, nil
	case "model-pair", "pair", "model_pair", "implementation-reviewer-pair", "implementer-reviewer-pair":
		return ViewModelPair, nil
	default:
		return "", fmt.Errorf(
			"unsupported stats view %q; expected throughput, implementation, review, consumption, implementation-model, reviewer-model, or model-pair",
			view,
		)
	}
}

// AggregateReportFromSnapshot projects aggregate stats from task snapshots and local state.
func AggregateReportFromSnapshot(
	snapshot taskmodel.SnapshotResult,
	stateLoader StateLoader,
	group Group,
) (AggregateReport, []taskmodel.RepoFailure) {
	return AggregateReportFromSnapshotWithOptions(snapshot, stateLoader, AggregateReportOptions{
		Group: group,
		View:  ViewThroughput,
	})
}

// AggregateReportFromSnapshotWithOptions projects focused time-based analytical stats.
//
//nolint:funlen // The projection loop keeps loading, filtering, and view dispatch together.
func AggregateReportFromSnapshotWithOptions(
	snapshot taskmodel.SnapshotResult,
	stateLoader StateLoader,
	opts AggregateReportOptions,
) (AggregateReport, []taskmodel.RepoFailure) {
	group := opts.Group
	if group == "" {
		group = GroupDay
	}
	view := opts.View
	if view == "" {
		view = ViewThroughput
	}

	report := AggregateReport{
		Group: group,
		View:  view,
		From:  cloneTime(opts.From),
		To:    cloneTime(opts.To),
		Repos: append([]string(nil), opts.Repositories...),
	}
	periods := map[string]*AggregatePeriod{}
	cohorts := map[modelCohortKey]*AggregateModelCohort{}
	failures := make([]taskmodel.RepoFailure, 0)
	repoFilter := newRepositoryFilter(opts.Repositories)
	consumptionBuckets := map[string]map[string][]executionRecord{}

	for _, repoSnapshot := range snapshot.Repositories {
		if !repoFilter.matches(repoSnapshot.Repository) {
			continue
		}
		for _, taskItem := range repoSnapshot.Tasks {
			if taskItem.IssueType == taskmodel.IssueTypeEpic {
				continue
			}

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

			switch view {
			case ViewImplementation:
				addImplementationTask(&report, periods, group, opts, repoSnapshot.Repository, taskItem, taskState)
			case ViewReview:
				addReviewTask(&report, periods, group, opts, repoSnapshot.Repository, taskItem, taskState)
			case ViewConsumption:
				collectConsumptionTask(&report, consumptionBuckets, group, opts, repoSnapshot.Repository, taskItem, taskState)
			case ViewImplementationModel, ViewReviewerModel, ViewModelPair:
				addModelComparisonTask(&report, cohorts, opts, taskItem, taskState)
			default:
				addThroughputTask(&report, periods, group, opts, taskItem, taskState)
			}
		}
	}

	if view == ViewConsumption {
		for key, taskRecords := range consumptionBuckets {
			period := ensurePeriod(periods, key)
			for _, records := range taskRecords {
				period.addConsumptionRecords(records)
			}
		}
	}

	report.Periods = sortedPeriods(periods)
	for i := range report.Periods {
		report.Periods[i].finish()
	}
	report.Cohorts = sortedModelCohorts(cohorts)
	for i := range report.Cohorts {
		report.Cohorts[i].finish()
	}
	return report, failures
}

func addThroughputTask(
	report *AggregateReport,
	periods map[string]*AggregatePeriod,
	group Group,
	opts AggregateReportOptions,
	taskItem taskmodel.Task,
	state taskstate.TaskState,
) {
	resolvedAt, ok := resolvedAt(taskItem, state)
	if !ok {
		report.TasksWithoutAnchor++
		report.TasksWithoutResolvedTimestamp++
		return
	}
	if !inDateRange(resolvedAt, opts) {
		return
	}

	period := ensurePeriod(periods, periodKey(resolvedAt, group))
	period.addThroughputTask(taskItem, state, resolvedAt)
}

func addImplementationTask(
	report *AggregateReport,
	periods map[string]*AggregatePeriod,
	group Group,
	opts AggregateReportOptions,
	_ taskmodel.Repository,
	_ taskmodel.Task,
	state taskstate.TaskState,
) {
	anchor, ok := firstImplementationDispatchAt(state)
	if !ok {
		report.TasksWithoutAnchor++
		report.TasksWithoutImplementation++
		return
	}
	if !inDateRange(anchor, opts) {
		return
	}

	period := ensurePeriod(periods, periodKey(anchor, group))
	period.addImplementationTask(state)
}

func addReviewTask(
	report *AggregateReport,
	periods map[string]*AggregatePeriod,
	group Group,
	opts AggregateReportOptions,
	_ taskmodel.Repository,
	_ taskmodel.Task,
	state taskstate.TaskState,
) {
	anchor, ok := firstReviewActivityAt(state)
	if !ok {
		report.TasksWithoutAnchor++
		report.TasksWithoutReviewActivity++
		return
	}
	if !inDateRange(anchor, opts) {
		return
	}

	period := ensurePeriod(periods, periodKey(anchor, group))
	period.addReviewTask(state)
}

func collectConsumptionTask(
	report *AggregateReport,
	buckets map[string]map[string][]executionRecord,
	group Group,
	opts AggregateReportOptions,
	repository taskmodel.Repository,
	taskItem taskmodel.Task,
	state taskstate.TaskState,
) {
	taskKey := repository.ID + "/" + taskItem.ID
	for _, record := range executionRecords(state) {
		anchor := record.execution.StartedAt
		if anchor.IsZero() {
			report.ExecutionsWithoutStartedAt++
			continue
		}
		if !inDateRange(anchor, opts) {
			continue
		}
		key := periodKey(anchor, group)
		if buckets[key] == nil {
			buckets[key] = map[string][]executionRecord{}
		}
		buckets[key][taskKey] = append(buckets[key][taskKey], record)
	}
}

func addModelComparisonTask(
	report *AggregateReport,
	cohorts map[modelCohortKey]*AggregateModelCohort,
	opts AggregateReportOptions,
	taskItem taskmodel.Task,
	state taskstate.TaskState,
) {
	anchor, ok := modelComparisonAnchor(report.View, state)
	if !ok {
		report.TasksWithoutAnchor++
		report.addMissingModelComparisonAnchor()
		return
	}
	if !inDateRange(anchor, opts) {
		return
	}

	implementationCohort := implementationModelCohort(state)
	reviewerCohort := reviewerModelCohort(state)
	outcomeCohort := ensureModelCohort(cohorts, modelOutcomeKey(report.View, implementationCohort, reviewerCohort))
	outcomeCohort.addModelOutcomeTask(taskItem, state)
	addModelComparisonUsage(cohorts, report.View, implementationCohort, reviewerCohort, state)
}

func modelComparisonAnchor(view View, state taskstate.TaskState) (time.Time, bool) {
	switch view {
	case ViewReviewerModel:
		return firstReviewActivityAt(state)
	default:
		return firstImplementationDispatchAt(state)
	}
}

func (r *AggregateReport) addMissingModelComparisonAnchor() {
	switch r.View {
	case ViewReviewerModel:
		r.TasksWithoutReviewActivity++
	default:
		r.TasksWithoutImplementation++
	}
}

func modelOutcomeKey(view View, implementationCohort string, reviewerCohort string) modelCohortKey {
	switch view {
	case ViewImplementationModel:
		return newImplementationModelCohortKey(implementationCohort)
	case ViewReviewerModel:
		return newReviewerModelCohortKey(reviewerCohort)
	default:
		return newModelPairCohortKey(implementationCohort, reviewerCohort)
	}
}

func addModelComparisonUsage(
	cohorts map[modelCohortKey]*AggregateModelCohort,
	view View,
	implementationCohort string,
	reviewerCohort string,
	state taskstate.TaskState,
) {
	switch view {
	case ViewImplementationModel:
		addUsageByModel(cohorts, implementationRecords(state), newImplementationModelCohortKey)
	case ViewReviewerModel:
		addUsageByModel(cohorts, reviewRecords(state), newReviewerModelCohortKey)
	case ViewModelPair:
		addUsageByModelPair(cohorts, implementationCohort, reviewerCohort, state)
	}
}

func addUsageByModelPair(
	cohorts map[modelCohortKey]*AggregateModelCohort,
	implementationCohort string,
	reviewerCohort string,
	state taskstate.TaskState,
) {
	recordsByPair := map[modelCohortKey][]executionRecord{}

	for _, record := range implementationRecords(state) {
		pairKey := newModelPairCohortKey(executionModelCohort(record.execution), reviewerCohort)
		recordsByPair[pairKey] = append(recordsByPair[pairKey], record)
	}
	for _, record := range reviewRecords(state) {
		pairKey := newModelPairCohortKey(implementationCohort, executionModelCohort(record.execution))
		recordsByPair[pairKey] = append(recordsByPair[pairKey], record)
	}

	for pairKey, records := range recordsByPair {
		cohort := ensureModelCohort(cohorts, pairKey)
		cohort.addUsageRecords(records)
	}
}

func addUsageByModel(
	cohorts map[modelCohortKey]*AggregateModelCohort,
	records []executionRecord,
	keyForModel func(string) modelCohortKey,
) {
	recordsByModel := map[modelCohortKey][]executionRecord{}
	for _, record := range records {
		model := executionModelCohort(record.execution)
		key := keyForModel(model)
		recordsByModel[key] = append(recordsByModel[key], record)
	}
	for key, records := range recordsByModel {
		cohort := ensureModelCohort(cohorts, key)
		cohort.addUsageRecords(records)
	}
}

func newImplementationModelCohortKey(model string) modelCohortKey {
	return modelCohortKey{implementationModel: model}
}

func newReviewerModelCohortKey(model string) modelCohortKey {
	return modelCohortKey{reviewerModel: model}
}

func newModelPairCohortKey(implementationModel string, reviewerModel string) modelCohortKey {
	return modelCohortKey{
		implementationModel: implementationModel,
		reviewerModel:       reviewerModel,
	}
}

func (k modelCohortKey) toCohort() AggregateModelCohort {
	return AggregateModelCohort{
		Key:                 k.displayKey(),
		ImplementationModel: k.implementationModel,
		ReviewerModel:       k.reviewerModel,
	}
}

func (k modelCohortKey) displayKey() string {
	if k.implementationModel == "" {
		return k.reviewerModel
	}
	if k.reviewerModel == "" {
		return k.implementationModel
	}
	return k.implementationModel + "/" + k.reviewerModel
}

func ensurePeriod(periods map[string]*AggregatePeriod, key string) *AggregatePeriod {
	period := periods[key]
	if period == nil {
		period = &AggregatePeriod{Key: key}
		periods[key] = period
	}
	return period
}

func ensureModelCohort(cohorts map[modelCohortKey]*AggregateModelCohort, key modelCohortKey) *AggregateModelCohort {
	cohort := cohorts[key]
	if cohort == nil {
		newCohort := key.toCohort()
		cohort = &newCohort
		cohorts[key] = cohort
	}
	return cohort
}

func sortedPeriods(periods map[string]*AggregatePeriod) []AggregatePeriod {
	result := make([]AggregatePeriod, 0, len(periods))
	for _, period := range periods {
		result = append(result, *period)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Key < result[j].Key
	})
	return result
}

func sortedModelCohorts(cohorts map[modelCohortKey]*AggregateModelCohort) []AggregateModelCohort {
	result := make([]AggregateModelCohort, 0, len(cohorts))
	for _, cohort := range cohorts {
		result = append(result, *cohort)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Key != result[j].Key {
			return result[i].Key < result[j].Key
		}
		if result[i].ImplementationModel != result[j].ImplementationModel {
			return result[i].ImplementationModel < result[j].ImplementationModel
		}
		return result[i].ReviewerModel < result[j].ReviewerModel
	})
	return result
}

func (p *AggregatePeriod) finish() {
	p.WorkflowTime.finish()
	p.ImplementationAgentTime.finish()
	p.ReviewTime.finish()
	p.RepairCycles.finish()
	p.Tokens.finish()
	p.Cost.finish()
}

func (c *AggregateModelCohort) finish() {
	c.CompletionTime.finish()
	c.WorkflowTime.finish()
	c.RepairCycles.finish()
	c.Tokens.finish()
	c.Cost.finish()
}

func (p *AggregatePeriod) addThroughputTask(
	taskItem taskmodel.Task,
	state taskstate.TaskState,
	resolvedAt time.Time,
) {
	p.Tasks++
	p.Resolved++
	if workflowDuration, ok := workflowDuration(state, resolvedAt); ok {
		p.WorkflowTime.addKnown(workflowDuration)
	} else {
		p.WorkflowTime.addUnknown()
	}

	// Legacy aggregate semantics.
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
	records := executionRecords(state)
	p.Totals.addTask(records)
	p.addTaskByPurpose(records)
}

func (p *AggregatePeriod) addImplementationTask(state taskstate.TaskState) {
	p.Tasks++
	if duration, ok := implementationAgentDuration(state); ok {
		p.ImplementationAgentTime.addKnown(duration)
	} else {
		p.ImplementationAgentTime.addUnknown()
	}
	records := implementationRecords(state)
	usage, knownUsage := taskUsage(records)
	p.Tokens.add(usage.TotalTokens, knownUsage)
	cost, knownCost := taskCost(records)
	p.Cost.add(cost, knownCost)
	p.ImplementationFails += implementationFailureCount(state)
}

func (p *AggregatePeriod) addReviewTask(state taskstate.TaskState) {
	p.Tasks++
	if duration, ok := reviewActivityDuration(state); ok {
		p.ReviewTime.addKnown(duration)
	} else {
		p.ReviewTime.addUnknown()
	}
	repairCycles := repairCycleCount(state)
	p.RepairCycles.addKnown(repairCycles)
	if repairCycles > 0 {
		p.RepairTasks++
	}
	if firstReviewPassed(state) {
		p.FirstPassApprovals++
	}
	p.BlockedReviews += reviewStatusCount(state, taskstate.ReviewStatusBlocked)
	p.OperationalFailures += reviewStatusCount(state, taskstate.ReviewStatusFailed)
	p.AbortedReviews += reviewStatusCount(state, taskstate.ReviewStatusAborted)
	p.PausedReviews += reviewStatusCount(state, taskstate.ReviewStatusWaitingForManual)
	p.BlockingFindings += blockingFindingCount(state)
}

func (p *AggregatePeriod) addConsumptionRecords(records []executionRecord) {
	p.Tasks++
	p.Executions += len(records)
	usage, knownUsage := taskUsage(records)
	p.Tokens.add(usage.TotalTokens, knownUsage)
	cost, knownCost := taskCost(records)
	p.Cost.add(cost, knownCost)
}

func (c *AggregateModelCohort) addModelOutcomeTask(taskItem taskmodel.Task, state taskstate.TaskState) {
	c.Tasks++
	if completionTime, ok := implementationAgentDuration(state); ok {
		c.CompletionTime.addKnown(completionTime)
	} else {
		c.CompletionTime.addUnknown()
	}
	if resolvedAt, ok := resolvedAt(taskItem, state); ok {
		if workflowTime, ok := workflowDuration(state, resolvedAt); ok {
			c.WorkflowTime.addKnown(workflowTime)
		} else {
			c.WorkflowTime.addUnknown()
		}
	} else {
		c.WorkflowTime.addUnknown()
	}
	repairCycles := repairCycleCount(state)
	c.RepairCycles.addKnown(repairCycles)
	if repairCycles > 0 {
		c.RepairTasks++
	}
	if firstReviewPassed(state) {
		c.FirstPassApprovals++
	}
	c.BlockedReviews += reviewStatusCount(state, taskstate.ReviewStatusBlocked)
	c.OperationalFailures += reviewStatusCount(state, taskstate.ReviewStatusFailed)
	c.BlockingFindings += blockingFindingCount(state)
}

func (c *AggregateModelCohort) addUsageRecords(records []executionRecord) {
	usage, knownUsage := taskUsage(records)
	c.Tokens.add(usage.TotalTokens, knownUsage)
	cost, knownCost := taskCost(records)
	c.Cost.add(cost, knownCost)
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
	return first.UTC(), !first.IsZero()
}

func firstReviewActivityAt(state taskstate.TaskState) (time.Time, bool) {
	var first time.Time
	for _, review := range state.Reviews {
		if !review.StartedAt.IsZero() && (first.IsZero() || review.StartedAt.Before(first)) {
			first = review.StartedAt
		}
		for _, step := range review.Steps {
			if step.Execution == nil || step.Execution.StartedAt.IsZero() {
				continue
			}
			if first.IsZero() || step.Execution.StartedAt.Before(first) {
				first = step.Execution.StartedAt
			}
		}
	}
	return first.UTC(), !first.IsZero()
}

func periodKey(value time.Time, group Group) string {
	value = value.UTC()
	switch group {
	case GroupMonth:
		return value.Format("2006-01")
	case GroupWeek:
		year, week := value.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", year, week)
	default:
		return value.Format("2006-01-02")
	}
}

type executionRecord struct {
	purpose   taskstate.AgentExecutionPurpose
	execution taskstate.AgentExecution
}

func executionRecords(state taskstate.TaskState) []executionRecord {
	records := make([]executionRecord, 0, len(state.Runs))
	records = append(records, implementationRecords(state)...)
	for _, reviewAttempt := range state.Reviews {
		for _, step := range reviewAttempt.Steps {
			if step.Execution == nil {
				continue
			}
			records = append(records, newExecutionRecord(
				taskstate.AgentExecutionPurposeReview,
				*step.Execution,
			))
		}
	}
	for _, event := range state.Events {
		if !isTerminalSyncConflictResolutionEvent(event) || event.Execution == nil {
			continue
		}
		records = append(records, newExecutionRecord(
			taskstate.AgentExecutionPurposeSyncConflictResolution,
			*event.Execution,
		))
	}
	return records
}

func implementationRecords(state taskstate.TaskState) []executionRecord {
	records := make([]executionRecord, 0, len(state.Runs))
	for _, run := range state.Runs {
		records = append(records, newExecutionRecord(
			taskstate.AgentExecutionPurposeImplementation,
			run.Execution,
		))
	}
	return records
}

func reviewRecords(state taskstate.TaskState) []executionRecord {
	records := make([]executionRecord, 0)
	for _, reviewAttempt := range state.Reviews {
		for _, step := range reviewAttempt.Steps {
			if step.Kind != taskstate.ReviewStepKindAgentReview || step.Execution == nil {
				continue
			}
			records = append(records, newExecutionRecord(
				taskstate.AgentExecutionPurposeReview,
				*step.Execution,
			))
		}
	}
	return records
}

func implementationModelCohort(state taskstate.TaskState) string {
	return recordsModelCohort(implementationRecords(state), false)
}

func reviewerModelCohort(state taskstate.TaskState) string {
	if len(state.Reviews) == 0 {
		return modelCohortUnknown
	}

	models := map[string]struct{}{}
	for _, reviewAttempt := range state.Reviews {
		for _, step := range reviewAttempt.Steps {
			if step.Kind != taskstate.ReviewStepKindAgentReview {
				continue
			}
			if step.Execution == nil {
				models[modelCohortUnknown] = struct{}{}
				continue
			}
			models[executionModelCohort(*step.Execution)] = struct{}{}
		}
	}
	if len(models) == 0 {
		return modelCohortManualOnly
	}
	if len(models) > 1 {
		return modelCohortMixed
	}
	for model := range models {
		return model
	}
	return modelCohortUnknown
}

func recordsModelCohort(records []executionRecord, manualOnlyWhenEmpty bool) string {
	if len(records) == 0 {
		if manualOnlyWhenEmpty {
			return modelCohortManualOnly
		}
		return modelCohortUnknown
	}
	models := map[string]struct{}{}
	for _, record := range records {
		models[executionModelCohort(record.execution)] = struct{}{}
	}
	if len(models) > 1 {
		return modelCohortMixed
	}
	for model := range models {
		return model
	}
	return modelCohortUnknown
}

func executionModelCohort(execution taskstate.AgentExecution) string {
	return execution.AgentSelection().CohortLabel()
}

func newExecutionRecord(
	fallbackPurpose taskstate.AgentExecutionPurpose,
	execution taskstate.AgentExecution,
) executionRecord {
	purpose := execution.Purpose
	if purpose == "" {
		purpose = fallbackPurpose
	}
	return executionRecord{
		purpose:   purpose,
		execution: execution,
	}
}

func isTerminalSyncConflictResolutionEvent(event taskstate.Event) bool {
	return event.Type == taskstate.EventSyncConflictFinished ||
		event.Type == taskstate.EventSyncConflictFailed
}

func (p *AggregatePeriod) addTaskByPurpose(records []executionRecord) {
	if len(records) == 0 {
		return
	}
	if p.TotalsByPurpose == nil {
		p.TotalsByPurpose = make(map[taskstate.AgentExecutionPurpose]AggregateTotals)
	}

	recordsByPurpose := make(map[taskstate.AgentExecutionPurpose][]executionRecord)
	for _, record := range records {
		recordsByPurpose[record.purpose] = append(recordsByPurpose[record.purpose], record)
	}
	for purpose, records := range recordsByPurpose {
		totals := p.TotalsByPurpose[purpose]
		totals.addTask(records)
		p.TotalsByPurpose[purpose] = totals
	}
}

func (t *AggregateTotals) addTask(records []executionRecord) {
	if len(records) == 0 {
		t.UnknownUsage++
		t.UnknownCost++
		return
	}

	usage, knownUsage := taskUsage(records)
	cost, knownCost := taskCost(records)
	for _, record := range records {
		t.Executions++
		if duration, ok := durationValue(record.execution); ok {
			t.Duration += duration
		}
		if record.execution.Usage != nil {
			t.Usage.InputTokens += record.execution.Usage.InputTokens
			t.Usage.CachedInputTokens += record.execution.Usage.CachedInputTokens
			t.Usage.OutputTokens += record.execution.Usage.OutputTokens
			t.Usage.ReasoningOutputTokens += record.execution.Usage.ReasoningOutputTokens
			t.Usage.TotalTokens += record.execution.Usage.TotalTokens
		}
		if executionCost, ok := executionCost(record.execution); ok {
			t.CostMicroUSD += executionCost.AmountMicroUSD
		}
	}
	if knownUsage {
		t.KnownUsageTasks++
		t.usageForAverage.InputTokens += usage.InputTokens
		t.usageForAverage.CachedInputTokens += usage.CachedInputTokens
		t.usageForAverage.OutputTokens += usage.OutputTokens
		t.usageForAverage.ReasoningOutputTokens += usage.ReasoningOutputTokens
		t.usageForAverage.TotalTokens += usage.TotalTokens
	} else {
		t.UnknownUsage++
	}
	if knownCost {
		t.KnownCostTasks++
		t.costMicroUSDForAverage += cost
	} else {
		t.UnknownCost++
	}
}

func implementationAgentDuration(state taskstate.TaskState) (time.Duration, bool) {
	if len(state.Runs) == 0 {
		return 0, false
	}
	var total time.Duration
	for _, run := range state.Runs {
		if run.Completion == nil || run.Completion.CompletedAt.IsZero() || run.Execution.StartedAt.IsZero() {
			return 0, false
		}
		duration := run.Completion.CompletedAt.Sub(run.Execution.StartedAt)
		if duration < 0 {
			return 0, false
		}
		total += duration
	}
	return total, true
}

func workflowDuration(state taskstate.TaskState, resolvedAt time.Time) (time.Duration, bool) {
	firstLaunch, ok := firstImplementationDispatchAt(state)
	if !ok || resolvedAt.Before(firstLaunch) {
		return 0, false
	}
	return resolvedAt.Sub(firstLaunch), true
}

func reviewActivityDuration(state taskstate.TaskState) (time.Duration, bool) {
	if len(state.Reviews) == 0 {
		return 0, false
	}
	var total time.Duration
	for _, review := range state.Reviews {
		if review.StartedAt.IsZero() || review.FinishedAt == nil || review.FinishedAt.IsZero() {
			return 0, false
		}
		duration := review.FinishedAt.Sub(review.StartedAt)
		if duration < 0 {
			return 0, false
		}
		total += duration
	}
	return total, true
}

func taskUsage(records []executionRecord) (taskstate.AgentUsage, bool) {
	if len(records) == 0 {
		return taskstate.AgentUsage{}, false
	}
	known := true
	var usage taskstate.AgentUsage
	for _, record := range records {
		execution := record.execution
		if execution.Usage == nil {
			known = false
			continue
		}
		usage.InputTokens += execution.Usage.InputTokens
		usage.CachedInputTokens += execution.Usage.CachedInputTokens
		usage.OutputTokens += execution.Usage.OutputTokens
		usage.ReasoningOutputTokens += execution.Usage.ReasoningOutputTokens
		usage.TotalTokens += execution.Usage.TotalTokens
	}
	return usage, known
}

func taskCost(records []executionRecord) (int64, bool) {
	if len(records) == 0 {
		return 0, false
	}
	known := true
	var total int64
	for _, record := range records {
		cost, ok := executionCost(record.execution)
		if !ok {
			known = false
			continue
		}
		total += cost.AmountMicroUSD
	}
	return total, known
}

func implementationFailureCount(state taskstate.TaskState) int {
	count := 0
	for _, run := range state.Runs {
		if run.Status == taskstate.RunStatusFailed || run.Execution.Status == taskstate.RunStatusFailed {
			count++
		}
	}
	return count
}

func repairCycleCount(state taskstate.TaskState) int {
	blockedReviews := map[int]bool{}
	for _, review := range state.Reviews {
		if review.Status == taskstate.ReviewStatusBlocked {
			blockedReviews[review.Attempt] = false
		}
	}
	for _, run := range state.Runs {
		if run.ReviewFollowUp == nil {
			continue
		}
		if _, ok := blockedReviews[run.ReviewFollowUp.ReviewAttempt]; ok {
			blockedReviews[run.ReviewFollowUp.ReviewAttempt] = true
		}
	}
	count := 0
	for _, repaired := range blockedReviews {
		if repaired {
			count++
		}
	}
	return count
}

func firstReviewPassed(state taskstate.TaskState) bool {
	if len(state.Reviews) == 0 {
		return false
	}
	first := state.Reviews[0]
	for _, review := range state.Reviews[1:] {
		if review.Attempt > 0 && (first.Attempt == 0 || review.Attempt < first.Attempt) {
			first = review
			continue
		}
		if first.Attempt == 0 && !review.StartedAt.IsZero() && review.StartedAt.Before(first.StartedAt) {
			first = review
		}
	}
	return first.Status == taskstate.ReviewStatusPassed
}

func reviewStatusCount(state taskstate.TaskState, status taskstate.ReviewStatus) int {
	count := 0
	for _, review := range state.Reviews {
		if review.Status == status {
			count++
		}
	}
	return count
}

func blockingFindingCount(state taskstate.TaskState) int {
	count := 0
	for _, review := range state.Reviews {
		for _, finding := range review.Findings {
			if finding.Type == taskstate.FindingTypeBlocking {
				count++
			}
		}
	}
	return count
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
	resolved := agent.ResolveExecutionUsageCost(execution)
	return resolved.Cost, resolved.Known
}

func averageDuration(total time.Duration, count int) (time.Duration, bool) {
	if count <= 0 {
		return 0, false
	}
	return total / time.Duration(count), true
}

func (c *DurationCohort) addKnown(value time.Duration) {
	c.Samples++
	c.Known++
	c.values = append(c.values, value)
}

func (c *DurationCohort) addUnknown() {
	c.Samples++
}

func (c *DurationCohort) finish() {
	if len(c.values) == 0 {
		return
	}
	sort.Slice(c.values, func(i, j int) bool { return c.values[i] < c.values[j] })
	c.Median = medianDuration(c.values)
	c.P75 = percentileDuration(c.values, 75)
}

func (c *IntCohort) add(value int, known bool) {
	if known {
		c.addKnown(value)
		return
	}
	c.addUnknown()
	c.Total += value
}

func (c *IntCohort) addKnown(value int) {
	c.Samples++
	c.Known++
	c.Total += value
	c.values = append(c.values, value)
}

func (c *IntCohort) addUnknown() {
	c.Samples++
}

func (c *IntCohort) finish() {
	if len(c.values) == 0 {
		return
	}
	sort.Ints(c.values)
	c.Median = medianInt(c.values)
}

func (c *CostCohort) add(value int64, known bool) {
	c.Samples++
	c.TotalMicroUSD += value
	if !known {
		return
	}
	c.Known++
	c.values = append(c.values, value)
}

func (c *CostCohort) finish() {
	if len(c.values) == 0 {
		return
	}
	sort.Slice(c.values, func(i, j int) bool { return c.values[i] < c.values[j] })
	c.MedianMicroUSD = medianInt64(c.values)
}

func medianDuration(values []time.Duration) time.Duration {
	mid := len(values) / 2
	if len(values)%2 == 1 {
		return values[mid]
	}
	return (values[mid-1] + values[mid]) / 2
}

func medianInt(values []int) int {
	mid := len(values) / 2
	if len(values)%2 == 1 {
		return values[mid]
	}
	return (values[mid-1] + values[mid]) / 2
}

func medianInt64(values []int64) int64 {
	mid := len(values) / 2
	if len(values)%2 == 1 {
		return values[mid]
	}
	return (values[mid-1] + values[mid]) / 2
}

func percentileDuration(values []time.Duration, percentile int) time.Duration {
	return values[percentileIndex(len(values), percentile)]
}

func percentileIndex(length int, percentile int) int {
	if length <= 1 {
		return 0
	}
	index := (length*percentile + 99) / 100
	if index <= 0 {
		return 0
	}
	if index > length {
		return length - 1
	}
	return index - 1
}

func inDateRange(anchor time.Time, opts AggregateReportOptions) bool {
	anchor = anchor.UTC()
	if opts.From != nil && anchor.Before(opts.From.UTC()) {
		return false
	}
	if opts.To != nil && !anchor.Before(opts.To.UTC()) {
		return false
	}
	return true
}

type repositoryFilter map[string]struct{}

func newRepositoryFilter(values []string) repositoryFilter {
	filter := repositoryFilter{}
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		filter[value] = struct{}{}
	}
	return filter
}

func (f repositoryFilter) matches(repository taskmodel.Repository) bool {
	if len(f) == 0 {
		return true
	}
	_, idOK := f[strings.ToLower(strings.TrimSpace(repository.ID))]
	_, nameOK := f[strings.ToLower(strings.TrimSpace(repository.Name))]
	return idOK || nameOK
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}
