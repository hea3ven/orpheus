// Package status projects local task snapshots into operator-facing status groups.
package status

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

// GroupID identifies an M4 local status projection group.
type GroupID string

const (
	// GroupNeedsAttention contains items or repo diagnostics that need human correction or retry.
	GroupNeedsAttention GroupID = "needs_attention"

	// GroupWorking contains in-progress items with a currently running attached run.
	GroupWorking GroupID = "working"

	// GroupIdle contains in-progress items with no active attached run.
	GroupIdle GroupID = "idle"

	// GroupInReview contains non-closed items ready for human review.
	GroupInReview GroupID = "in_review"

	// GroupReadyToRun contains items Orpheus' local readiness policy considers ready.
	GroupReadyToRun GroupID = "ready_to_run"

	// GroupBlocked contains items with locally visible open blocking dependencies.
	GroupBlocked GroupID = "blocked"

	// GroupDoneClosed contains closed backend items.
	GroupDoneClosed GroupID = "done_closed"
)

// EntryKind identifies whether a projected status entry is a task or a repository diagnostic.
type EntryKind string

const (
	// EntryTask is a projected backend item.
	EntryTask EntryKind = "task"

	// EntryRepoFailure is a per-repository query diagnostic.
	EntryRepoFailure EntryKind = "repo_failure"
)

// Entry is one row in a status group.
type Entry struct {
	Kind       EntryKind
	Repository task.Repository
	Task       task.Task
	Failure    error
	Source     string
	Operation  string
	Detail     string
}

// Group is an ordered collection of status entries.
type Group struct {
	ID      GroupID
	Title   string
	Entries []Entry
}

// Projection is the M4 local action-queue view.
type Projection struct {
	Groups []Group
}

// RunStateIndex contains the latest Orpheus run attempt by repository/task key.
type RunStateIndex map[string]taskstate.RunAttempt

// LocalTaskState contains local Orpheus facts used by status projection.
type LocalTaskState struct {
	LatestRun       *taskstate.RunAttempt
	Finalization    taskstate.Finalization
	ExpectedTargets *workflow.ExpectedTargets
}

// LocalTaskStateIndex contains local Orpheus facts by repository/task key.
type LocalTaskStateIndex map[string]LocalTaskState

type readinessState string

const (
	readinessReady     readinessState = "ready"
	readinessAttention readinessState = "needs_attention"
	readinessWorking   readinessState = "working"
	readinessIdle      readinessState = "idle"
	readinessBlocked   readinessState = "blocked"
	readinessReview    readinessState = "in_review"
	readinessDone      readinessState = "done"
)

type policyResult struct {
	state  readinessState
	detail string
}

// Project builds the local-only status projection from task aggregation snapshots.
func Project(snapshot task.SnapshotResult) Projection {
	return ProjectWithRunStates(snapshot, nil)
}

// ProjectWithRunStates builds the status projection using latest run attempts.
func ProjectWithRunStates(snapshot task.SnapshotResult, runStates RunStateIndex) Projection {
	return ProjectWithLocalTaskStates(snapshot, localTaskStatesFromRunStates(runStates))
}

// ProjectWithLocalTaskStates builds the status projection using local Orpheus task-state facts.
func ProjectWithLocalTaskStates(snapshot task.SnapshotResult, localStates LocalTaskStateIndex) Projection {
	projection := Projection{Groups: []Group{
		{ID: GroupNeedsAttention, Title: "Needs attention"},
		{ID: GroupInReview, Title: "Reviewing"},
		{ID: GroupWorking, Title: "Working"},
		{ID: GroupIdle, Title: "Idle"},
		{ID: GroupReadyToRun, Title: "Ready to run"},
		{ID: GroupBlocked, Title: "Blocked"},
		{ID: GroupDoneClosed, Title: "Done / closed"},
	}}

	for _, repoSnapshot := range snapshot.Repositories {
		projectRepository(&projection, repoSnapshot, localStates)
	}
	for _, failure := range snapshot.Failures {
		projection.add(GroupNeedsAttention, failureEntry(failure))
	}
	return projection
}

// ReadyRows returns rows selected by the canonical Orpheus MVP readiness policy.
func ReadyRows(snapshot task.SnapshotResult) []task.RepoTask {
	return ReadyRowsWithRunStates(snapshot, nil)
}

// ReadyRowsWithRunStates returns ready rows while respecting local run history.
func ReadyRowsWithRunStates(snapshot task.SnapshotResult, runStates RunStateIndex) []task.RepoTask {
	return ReadyRowsWithLocalTaskStates(snapshot, localTaskStatesFromRunStates(runStates))
}

// ReadyRowsWithLocalTaskStates returns ready rows while respecting local Orpheus task state.
func ReadyRowsWithLocalTaskStates(snapshot task.SnapshotResult, localStates LocalTaskStateIndex) []task.RepoTask {
	rows := make([]task.RepoTask, 0)
	for _, repoSnapshot := range snapshot.Repositories {
		index := newRepositoryIndex(repoSnapshot.Tasks)
		for _, taskItem := range repoSnapshot.Tasks {
			localState := localTaskStateFor(localStates, repoSnapshot.Repository.ID, taskItem.ID)
			if classify(repoSnapshot.Repository, taskItem, index, localState).state != readinessReady {
				continue
			}
			rows = append(rows, task.RepoTask{
				Repository: repoSnapshot.Repository,
				Task:       taskItem.Clone(),
			})
		}
	}
	return rows
}

// RunStateKey returns the stable lookup key for RunStateIndex.
func RunStateKey(repoID, taskID string) string {
	return repoID + "\x00" + taskID
}

func projectRepository(projection *Projection, repoSnapshot task.RepositorySnapshot, localStates LocalTaskStateIndex) {
	index := newRepositoryIndex(repoSnapshot.Tasks)
	for _, taskItem := range repoSnapshot.Tasks {
		result := classify(
			repoSnapshot.Repository,
			taskItem,
			index,
			localTaskStateFor(localStates, repoSnapshot.Repository.ID, taskItem.ID),
		)
		projection.add(groupForState(result.state), taskEntry(repoSnapshot.Repository, taskItem, result.detail))
	}
}

func classify(repository task.Repository, taskItem task.Task, index map[string]task.Task, localState *LocalTaskState) policyResult {
	metadata := taskItem.OrpheusMetadata()
	latestRun := latestRunFrom(localState)
	if taskItem.Status == task.StatusClosed {
		return policyResult{state: readinessDone, detail: "closed"}
	}
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return policyResult{state: readinessReview, detail: metadata.PRURL}
	}

	expectedTargets := expectedTargetsFrom(localState)
	if expectedTargets != nil {
		if _, ok := workflow.ClassifyExpectedPRReviewReady(*expectedTargets, taskItem, latestRun); ok {
			if strings.TrimSpace(latestRun.Completion.Commit) != "" {
				return policyResult{state: readinessAttention, detail: "needs PR"}
			}
			return policyResult{
				state:  readinessAttention,
				detail: "completion recorded but commit failed; needs manual correction",
			}
		}
	}
	if expectedTargets != nil {
		if _, ok := workflow.ClassifyExpectedLocalReviewReady(*expectedTargets, taskItem, latestRun); ok {
			if localState == nil || localState.Finalization.ClosedAt == nil {
				return policyResult{state: readinessReview, detail: "local review; run task done"}
			}
			return policyResult{
				state:  readinessAttention,
				detail: "finalization recorded but backend task is not closed",
			}
		}
	}

	if _, ok := workflow.ClassifyPRReviewReady(repository, taskItem, latestRun); ok {
		if strings.TrimSpace(latestRun.Completion.Commit) != "" {
			return policyResult{
				state:  readinessAttention,
				detail: "completion target is not the deterministic Orpheus worktree/team target",
			}
		}
		return policyResult{
			state:  readinessAttention,
			detail: "completion recorded but commit failed; needs manual correction",
		}
	}
	if _, ok := workflow.ClassifyLocalReviewReady(repository, taskItem, latestRun); ok {
		return policyResult{
			state:  readinessAttention,
			detail: "completion target is not the deterministic Orpheus main/solo target",
		}
	}

	if taskItem.Status == task.StatusInProgress {
		return classifyInProgress(latestRun)
	}
	if taskItem.Status == task.StatusOpen && latestRun != nil {
		return policyResult{state: readinessAttention, detail: openTaskRunHistoryDetail(*latestRun)}
	}
	deps := dependencyIDs(taskItem)
	missingDetail := missingDependencyDetail(taskItem, deps, index)
	if missingDetail != "" {
		return policyResult{state: readinessAttention, detail: missingDetail}
	}
	openDeps := openDependencyIDs(deps, index)
	if len(openDeps) > 0 {
		return policyResult{state: readinessBlocked, detail: "blocked by " + strings.Join(openDeps, ", ")}
	}

	if taskItem.Status == task.StatusOpen {
		return policyResult{state: readinessReady, detail: "-"}
	}
	return policyResult{
		state:  readinessAttention,
		detail: fmt.Sprintf("status %s is not locally actionable", formatStatus(taskItem.Status)),
	}
}

func classifyInProgress(latestRun *taskstate.RunAttempt) policyResult {
	if latestRun == nil {
		return policyResult{state: readinessIdle, detail: "no attached run recorded"}
	}

	switch latestRun.Status {
	case taskstate.RunStatusRunning:
		return policyResult{state: readinessWorking, detail: runAttemptDetail(*latestRun)}
	case taskstate.RunStatusFailed:
		return policyResult{state: readinessAttention, detail: runAttemptDetail(*latestRun)}
	case taskstate.RunStatusSucceeded:
		return policyResult{
			state:  readinessIdle,
			detail: fmt.Sprintf("%s; agent exited without completion", runAttemptDetail(*latestRun)),
		}
	default:
		return policyResult{state: readinessAttention, detail: runAttemptDetail(*latestRun)}
	}
}

func openTaskRunHistoryDetail(latestRun taskstate.RunAttempt) string {
	return fmt.Sprintf("backend status is open but local %s", runAttemptDetail(latestRun))
}

func runAttemptDetail(run taskstate.RunAttempt) string {
	switch run.Status {
	case taskstate.RunStatusRunning:
		return fmt.Sprintf("run attempt %d is running", run.Attempt)
	case taskstate.RunStatusFailed:
		return fmt.Sprintf("run attempt %d failed", run.Attempt)
	case taskstate.RunStatusSucceeded:
		return fmt.Sprintf("run attempt %d succeeded", run.Attempt)
	default:
		statusText := strings.TrimSpace(string(run.Status))
		if statusText == "" {
			statusText = "unknown"
		}
		return fmt.Sprintf("run attempt %d has status %s", run.Attempt, statusText)
	}
}

func localTaskStatesFromRunStates(runStates RunStateIndex) LocalTaskStateIndex {
	if len(runStates) == 0 {
		return nil
	}
	localStates := make(LocalTaskStateIndex, len(runStates))
	for key, run := range runStates {
		run := run
		localStates[key] = LocalTaskState{LatestRun: &run}
	}
	return localStates
}

func localTaskStateFor(localStates LocalTaskStateIndex, repoID string, taskID string) *LocalTaskState {
	if len(localStates) == 0 {
		return nil
	}
	localState, ok := localStates[RunStateKey(repoID, taskID)]
	if !ok {
		return nil
	}
	return &localState
}

func latestRunFrom(localState *LocalTaskState) *taskstate.RunAttempt {
	if localState == nil {
		return nil
	}
	return localState.LatestRun
}

func expectedTargetsFrom(localState *LocalTaskState) *workflow.ExpectedTargets {
	if localState == nil {
		return nil
	}
	return localState.ExpectedTargets
}

func newRepositoryIndex(tasks []task.Task) map[string]task.Task {
	index := make(map[string]task.Task, len(tasks))
	for _, taskItem := range tasks {
		id := strings.TrimSpace(taskItem.ID)
		if id == "" {
			continue
		}
		index[id] = taskItem
	}
	return index
}

func dependencyIDs(taskItem task.Task) []string {
	seen := make(map[string]struct{}, len(taskItem.Relations.DependencyIDs))
	ids := make([]string, 0, len(taskItem.Relations.DependencyIDs))
	for _, id := range taskItem.Relations.DependencyIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func missingDependencyDetail(taskItem task.Task, deps []string, index map[string]task.Task) string {
	missing := make([]string, 0)
	for _, id := range deps {
		if _, ok := index[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return "missing dependency " + strings.Join(missing, ", ")
	}
	if taskItem.Relations.BlockedByCount > len(deps) {
		return fmt.Sprintf("dependency details missing for %d blocker(s)", taskItem.Relations.BlockedByCount-len(deps))
	}
	return ""
}

func openDependencyIDs(deps []string, index map[string]task.Task) []string {
	openDeps := make([]string, 0, len(deps))
	for _, id := range deps {
		dependency := index[id]
		if dependency.Status != task.StatusClosed {
			openDeps = append(openDeps, id)
		}
	}
	return openDeps
}

func groupForState(state readinessState) GroupID {
	switch state {
	case readinessReady:
		return GroupReadyToRun
	case readinessAttention:
		return GroupNeedsAttention
	case readinessWorking:
		return GroupWorking
	case readinessIdle:
		return GroupIdle
	case readinessBlocked:
		return GroupBlocked
	case readinessReview:
		return GroupInReview
	case readinessDone:
		return GroupDoneClosed
	default:
		return GroupNeedsAttention
	}
}

func taskEntry(repository task.Repository, taskItem task.Task, detail string) Entry {
	return Entry{
		Kind:       EntryTask,
		Repository: repository,
		Task:       taskItem.Clone(),
		Detail:     detailOrDash(detail),
	}
}

func failureEntry(failure task.RepoFailure) Entry {
	detail := fmt.Sprintf("%s/%s: %v", valueOrUnknown(failure.Source), valueOrUnknown(failure.Operation), failure.Err)
	return Entry{
		Kind:       EntryRepoFailure,
		Repository: failure.Repository,
		Failure:    failure.Err,
		Source:     failure.Source,
		Operation:  failure.Operation,
		Detail:     detail,
	}
}

func detailOrDash(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return "-"
	}
	return detail
}

func valueOrUnknown(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	return value
}

func formatStatus(status task.Status) string {
	if strings.TrimSpace(string(status)) == "" {
		return "unknown"
	}
	return string(status)
}

func (p *Projection) add(groupID GroupID, entry Entry) {
	for i := range p.Groups {
		if p.Groups[i].ID == groupID {
			p.Groups[i].Entries = append(p.Groups[i].Entries, entry)
			return
		}
	}
}
