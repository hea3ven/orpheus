// Package status projects local task snapshots into operator-facing status groups.
package status

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/readiness"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
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
	Kind           EntryKind
	Repository     task.Repository
	Task           task.Task
	Failure        error
	Source         string
	Operation      string
	Detail         string
	SemanticDetail Detail
	EpicProgress   Detail
}

// DetailKind identifies the machine-readable reason behind an entry detail.
type DetailKind string

const (
	DetailNone DetailKind = ""

	DetailClosed                   DetailKind = "closed"
	DetailPullRequest              DetailKind = "pull_request"
	DetailLocalReview              DetailKind = "local_review"
	DetailReviewRunning            DetailKind = "review_running"
	DetailReviewManualStep         DetailKind = "review_manual_step"
	DetailReviewDecisionLost       DetailKind = "review_decision_lost"
	DetailReviewDecisionRequired   DetailKind = "review_decision_required"
	DetailReviewFollowUpReady      DetailKind = "review_follow_up_ready"
	DetailReviewBudgetSpent        DetailKind = "review_budget_spent"
	DetailReviewFindings           DetailKind = "review_findings"
	DetailReviewAborted            DetailKind = "review_aborted"
	DetailReviewFailed             DetailKind = "review_failed"
	DetailReviewPassed             DetailKind = "review_passed"
	DetailReviewPublishFailed      DetailKind = "review_publish_failed"
	DetailReviewUnknownState       DetailKind = "review_unknown_state"
	DetailNoRun                    DetailKind = "no_run"
	DetailRunRunning               DetailKind = "run_running"
	DetailRunFailed                DetailKind = "run_failed"
	DetailRunIncomplete            DetailKind = "run_incomplete"
	DetailRunUnknownState          DetailKind = "run_unknown_state"
	DetailOpenTaskRunHistory       DetailKind = "open_task_run_history"
	DetailParentMissing            DetailKind = "parent_missing"
	DetailParentNotEpic            DetailKind = "parent_not_epic"
	DetailParentNotReady           DetailKind = "parent_not_ready"
	DetailMissingExternalRef       DetailKind = "missing_external_ref"
	DetailWrongPRTarget            DetailKind = "wrong_pr_target"
	DetailWrongLocalTarget         DetailKind = "wrong_local_target"
	DetailFinalizedButOpen         DetailKind = "finalized_but_open"
	DetailMissingDependency        DetailKind = "missing_dependency"
	DetailMissingDependencies      DetailKind = "missing_dependencies"
	DetailDependencyDetailsMissing DetailKind = "dependency_details_missing"
	DetailBlockedDependency        DetailKind = "blocked_dependency"
	DetailBlockedDependencies      DetailKind = "blocked_dependencies"
	DetailUnknownTaskStatus        DetailKind = "unknown_task_status"
	DetailRepoFailure              DetailKind = "repo_failure"
	DetailEpicProgress             DetailKind = "epic_progress"
)

// Detail carries compact-rendering inputs without requiring CLI prose parsing.
type Detail struct {
	Kind      DetailKind
	URL       string
	ID        string
	IDs       []string
	Attempt   int
	State     string
	Step      string
	Count     int
	Completed int
	Total     int
	Source    string
	Operation string
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
	LatestRun                 *taskstate.RunAttempt
	Runs                      []taskstate.RunAttempt
	Target                    *taskstate.TaskTarget
	LatestReview              *taskstate.ReviewAttempt
	LatestFinalizationFailure *taskstate.Event
	Finalization              taskstate.Finalization
	ExpectedTargets           *tasktarget.ExpectedTargets
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
	state          readinessState
	detail         string
	semanticDetail Detail
}

type epicChildProgress struct {
	Completed     int
	ObservedTotal int
	DeclaredTotal int
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
	progressByEpic := epicChildProgressByParent(repoSnapshot.Tasks)
	for _, taskItem := range repoSnapshot.Tasks {
		result := classify(
			repoSnapshot.Repository,
			taskItem,
			index,
			localTaskStateFor(localStates, repoSnapshot.Repository.ID, taskItem.ID),
		)
		epicProgress := Detail{}
		if taskItem.IssueType == task.IssueTypeEpic {
			epicProgress = epicProgressDetail(progressByEpic[strings.TrimSpace(taskItem.ID)])
		}
		projection.add(groupForState(result.state), taskEntry(repoSnapshot.Repository, taskItem, result, epicProgress))
	}
}

func classify(repository task.Repository, taskItem task.Task, index map[string]task.Task, localState *LocalTaskState) policyResult {
	metadata := taskItem.OrpheusMetadata()
	latestRun := latestRunFrom(localState)
	if taskItem.Status == task.StatusClosed {
		return newPolicyResult(readinessDone, "closed", Detail{Kind: DetailClosed})
	}
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return newPolicyResult(
			readinessReview,
			metadata.PRURL,
			Detail{Kind: DetailPullRequest, URL: metadata.PRURL},
		)
	}
	expectedTargets := expectedTargetsFrom(localState)
	taskTarget := taskTargetFrom(localState)
	if result, ok := classifyExpectedReviewReady(expectedTargets, taskItem, taskTarget, latestRun, localState); ok {
		return result
	}
	if taskItem.Status == task.StatusInProgress && latestRun != nil && latestRun.Status == taskstate.RunStatusRunning {
		return classifyInProgress(latestRun)
	}
	if result, ok := classifyParentEpicGate(taskItem, index); ok {
		return result
	}
	if publication.RequiresExternalRef(repository.TitleTemplate) && strings.TrimSpace(taskItem.ExternalRef) == "" {
		return newPolicyResult(
			readinessAttention,
			missingExternalRefDetail(taskItem.ID),
			Detail{Kind: DetailMissingExternalRef, ID: taskItem.ID},
		)
	}

	if result, ok := classifyUnexpectedCompletionTarget(repository, taskItem, taskTarget, latestRun); ok {
		return result
	}

	if taskItem.Status == task.StatusInProgress {
		return classifyInProgress(latestRun)
	}
	return classifyOpenOrUnknownTask(taskItem, index, latestRun)
}

func classifyOpenOrUnknownTask(
	taskItem task.Task,
	index map[string]task.Task,
	latestRun *taskstate.RunAttempt,
) policyResult {
	if taskItem.Status == task.StatusOpen && latestRun != nil {
		return newPolicyResult(
			readinessAttention,
			openTaskRunHistoryDetail(*latestRun),
			runHistoryDetail(*latestRun),
		)
	}
	deps := dependencyIDs(taskItem)
	missingDetail, missingSemanticDetail := missingDependencyDetail(taskItem, deps, index)
	if missingDetail != "" {
		return newPolicyResult(readinessAttention, missingDetail, missingSemanticDetail)
	}
	openDeps := openDependencyIDs(deps, index)
	if len(openDeps) > 0 {
		return newPolicyResult(
			readinessBlocked,
			"blocked by "+strings.Join(openDeps, ", "),
			blockedDependencyDetail(openDeps),
		)
	}

	if taskItem.Status == task.StatusOpen {
		return newPolicyResult(readinessReady, "-", Detail{})
	}
	statusText := formatStatus(taskItem.Status)
	return newPolicyResult(
		readinessAttention,
		fmt.Sprintf("status %s is not locally actionable", statusText),
		Detail{Kind: DetailUnknownTaskStatus, State: statusText},
	)
}

func classifyUnexpectedCompletionTarget(
	repository task.Repository,
	taskItem task.Task,
	taskTarget *taskstate.TaskTarget,
	latestRun *taskstate.RunAttempt,
) (policyResult, bool) {
	if taskTarget == nil {
		return policyResult{}, false
	}
	if _, ok := workflow.ClassifyPRReviewReady(repository, taskItem, *taskTarget, latestRun); ok {
		return newPolicyResult(
			readinessAttention,
			"completion target is not the deterministic Orpheus worktree/team target",
			Detail{Kind: DetailWrongPRTarget},
		), true
	}
	if _, ok := workflow.ClassifyLocalReviewReady(repository, taskItem, *taskTarget, latestRun); ok {
		return newPolicyResult(
			readinessAttention,
			"completion target is not the deterministic Orpheus main/solo target",
			Detail{Kind: DetailWrongLocalTarget},
		), true
	}
	return policyResult{}, false
}

func classifyParentEpicGate(taskItem task.Task, index map[string]task.Task) (policyResult, bool) {
	parentGate := readiness.EvaluateParentEpicGateFromIndex(taskItem, index)
	if parentGate.State == readiness.ParentEpicGateAllowed {
		return policyResult{}, false
	}
	state := readinessAttention
	if parentGate.State == readiness.ParentEpicGateBlocked {
		state = readinessBlocked
	}
	return newPolicyResult(state, parentGate.Detail(), parentEpicDetail(parentGate)), true
}

func classifyExpectedReviewReady(
	expectedTargets *tasktarget.ExpectedTargets,
	taskItem task.Task,
	taskTarget *taskstate.TaskTarget,
	latestRun *taskstate.RunAttempt,
	localState *LocalTaskState,
) (policyResult, bool) {
	if expectedTargets == nil || taskTarget == nil {
		return policyResult{}, false
	}
	if _, ok := workflow.ClassifyExpectedPRReviewReady(*expectedTargets, taskItem, *taskTarget, latestRun); ok {
		if localState != nil {
			if result, ok := classifyLatestReview(localState.Runs, localState.LatestReview, localState.LatestFinalizationFailure); ok {
				return result, true
			}
		}
		return localReviewPolicyResult(), true
	}
	if _, ok := workflow.ClassifyExpectedLocalReviewReady(*expectedTargets, taskItem, *taskTarget, latestRun); !ok {
		return policyResult{}, false
	}
	if localState != nil {
		if result, ok := classifyLatestReview(localState.Runs, localState.LatestReview, localState.LatestFinalizationFailure); ok {
			return result, true
		}
	}
	if localState == nil || localState.Finalization.ClosedAt == nil {
		return localReviewPolicyResult(), true
	}
	return newPolicyResult(
		readinessAttention,
		"finalization recorded but backend task is not closed",
		Detail{Kind: DetailFinalizedButOpen},
	), true
}

func classifyLatestReview(
	runs []taskstate.RunAttempt,
	latestReview *taskstate.ReviewAttempt,
	latestFinalizationFailure *taskstate.Event,
) (policyResult, bool) {
	if latestReview == nil {
		return policyResult{}, false
	}

	switch latestReview.Status {
	case taskstate.ReviewStatusRunning:
		return newPolicyResult(
			readinessReview,
			"review running",
			Detail{Kind: DetailReviewRunning},
		), true
	case taskstate.ReviewStatusWaitingForManual:
		step := valueOrUnknown(latestReview.Step)
		return newPolicyResult(
			readinessReview,
			fmt.Sprintf("local review; run task review (waiting for manual step %s)", step),
			Detail{Kind: DetailReviewManualStep, Step: step},
		), true
	case taskstate.ReviewStatusBlocked:
		return classifyBlockedReview(taskstate.TaskState{Runs: runs}, *latestReview), true
	case taskstate.ReviewStatusAborted:
		return newPolicyResult(
			readinessReview,
			"review aborted; run task review",
			Detail{Kind: DetailReviewAborted},
		), true
	case taskstate.ReviewStatusFailed:
		return newPolicyResult(
			readinessAttention,
			"review failed operationally; run task review",
			Detail{Kind: DetailReviewFailed},
		), true
	case taskstate.ReviewStatusPassed:
		if latestFinalizationFailure != nil {
			return newPolicyResult(
				readinessAttention,
				"review passed; publication failed; fix publication issue, then run task done",
				Detail{Kind: DetailReviewPublishFailed},
			), true
		}
		return newPolicyResult(
			readinessReview,
			"review passed; run task done",
			Detail{Kind: DetailReviewPassed},
		), true
	default:
		state := valueOrUnknown(string(latestReview.Status))
		return newPolicyResult(
			readinessAttention,
			fmt.Sprintf("review attempt %d has status %s", latestReview.Attempt, state),
			Detail{Kind: DetailReviewUnknownState, Attempt: latestReview.Attempt, State: state},
		), true
	}
}

func classifyBlockedReview(state taskstate.TaskState, review taskstate.ReviewAttempt) policyResult {
	count := untargetedBlockingFindingCount(state, review)
	if review.AutomatedBlockerDecisionInterrupted {
		return newPolicyResult(
			readinessReview,
			"review blocker decision interrupted; run task review",
			Detail{Kind: DetailReviewDecisionLost},
		)
	}
	if taskstate.HasUnkeptAutomatedBlockingFindingsInState(state, review) {
		return newPolicyResult(
			readinessReview,
			"review blocker decision required; run task review",
			Detail{Kind: DetailReviewDecisionRequired},
		)
	}
	if review.AutonomousBudgetExhausted {
		return newPolicyResult(
			readinessIdle,
			fmt.Sprintf(
				"review blocked after autonomous attempt budget by %d finding(s); run task run to continue",
				count,
			),
			Detail{Kind: DetailReviewBudgetSpent, Count: count},
		)
	}
	if taskstate.HasFailedReviewFollowUpTargets(state, review) {
		return newPolicyResult(
			readinessIdle,
			"review follow-up failed; retry task run",
			Detail{Kind: DetailReviewFindings, Count: count},
		)
	}
	if count == 0 {
		return newPolicyResult(
			readinessReview,
			"review blockers targeted; run task review",
			Detail{Kind: DetailReviewFollowUpReady},
		)
	}
	return newPolicyResult(
		readinessIdle,
		fmt.Sprintf("review blocked by %d finding(s); run task run", count),
		Detail{Kind: DetailReviewFindings, Count: count},
	)
}

func untargetedBlockingFindingCount(state taskstate.TaskState, review taskstate.ReviewAttempt) int {
	return len(taskstate.UntargetedBlockingFindingIndexesInState(state, review))
}

func classifyInProgress(latestRun *taskstate.RunAttempt) policyResult {
	if latestRun == nil {
		return newPolicyResult(readinessIdle, "no attached run recorded", Detail{Kind: DetailNoRun})
	}

	switch latestRun.Status {
	case taskstate.RunStatusRunning:
		return newPolicyResult(readinessWorking, runAttemptDetail(*latestRun), runDetail(*latestRun))
	case taskstate.RunStatusFailed:
		return newPolicyResult(readinessAttention, runAttemptDetail(*latestRun), runDetail(*latestRun))
	case taskstate.RunStatusSucceeded:
		return newPolicyResult(
			readinessIdle,
			fmt.Sprintf("%s; agent exited without completion", runAttemptDetail(*latestRun)),
			Detail{Kind: DetailRunIncomplete, Attempt: latestRun.Attempt},
		)
	default:
		return newPolicyResult(readinessAttention, runAttemptDetail(*latestRun), runDetail(*latestRun))
	}
}

func openTaskRunHistoryDetail(latestRun taskstate.RunAttempt) string {
	return fmt.Sprintf("backend status is open but local %s", runAttemptDetail(latestRun))
}

func newPolicyResult(state readinessState, detail string, semanticDetail Detail) policyResult {
	return policyResult{
		state:          state,
		detail:         detail,
		semanticDetail: semanticDetail,
	}
}

func localReviewPolicyResult() policyResult {
	return newPolicyResult(
		readinessReview,
		"local review; run task review",
		Detail{Kind: DetailLocalReview},
	)
}

func missingExternalRefDetail(taskID string) string {
	return fmt.Sprintf(
		"missing required external reference; set it with `bd update %s --external-ref <reference>`",
		taskID,
	)
}

func runHistoryDetail(run taskstate.RunAttempt) Detail {
	detail := runDetail(run)
	detail.Kind = DetailOpenTaskRunHistory
	return detail
}

func runDetail(run taskstate.RunAttempt) Detail {
	switch run.Status {
	case taskstate.RunStatusRunning:
		return Detail{Kind: DetailRunRunning, Attempt: run.Attempt}
	case taskstate.RunStatusFailed:
		return Detail{Kind: DetailRunFailed, Attempt: run.Attempt}
	case taskstate.RunStatusSucceeded:
		return Detail{Kind: DetailRunIncomplete, Attempt: run.Attempt}
	default:
		return Detail{
			Kind:    DetailRunUnknownState,
			Attempt: run.Attempt,
			State:   valueOrUnknown(string(run.Status)),
		}
	}
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

func parentEpicDetail(parentGate readiness.ParentEpicGate) Detail {
	if parentGate.Parent == nil {
		return Detail{Kind: DetailParentMissing, ID: parentGate.ParentID}
	}
	if parentGate.Parent.IssueType != task.IssueTypeEpic {
		return Detail{
			Kind:  DetailParentNotEpic,
			ID:    parentGate.ParentID,
			State: valueOrUnknown(string(parentGate.Parent.IssueType)),
		}
	}
	return Detail{
		Kind:  DetailParentNotReady,
		ID:    parentGate.ParentID,
		State: formatStatus(parentGate.Parent.Status),
	}
}

func blockedDependencyDetail(ids []string) Detail {
	detail := Detail{Kind: DetailBlockedDependencies, IDs: cloneStrings(ids), Count: len(ids)}
	if len(ids) == 1 {
		detail.Kind = DetailBlockedDependency
		detail.ID = ids[0]
	}
	return detail
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	clone := make([]string, len(values))
	copy(clone, values)
	return clone
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

func taskTargetFrom(localState *LocalTaskState) *taskstate.TaskTarget {
	if localState == nil {
		return nil
	}
	return localState.Target
}

func expectedTargetsFrom(localState *LocalTaskState) *tasktarget.ExpectedTargets {
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

func epicChildProgressByParent(tasks []task.Task) map[string]epicChildProgress {
	progressByEpic := make(map[string]epicChildProgress)
	for _, taskItem := range tasks {
		if taskItem.IssueType == task.IssueTypeEpic {
			id := strings.TrimSpace(taskItem.ID)
			if id != "" {
				progress := progressByEpic[id]
				if taskItem.Relations.ChildCount > progress.DeclaredTotal {
					progress.DeclaredTotal = taskItem.Relations.ChildCount
				}
				progressByEpic[id] = progress
			}
		}

		parentID := strings.TrimSpace(taskItem.Relations.ParentID)
		if parentID == "" {
			continue
		}
		progress := progressByEpic[parentID]
		progress.ObservedTotal++
		if taskItem.Status == task.StatusClosed {
			progress.Completed++
		}
		progressByEpic[parentID] = progress
	}
	return progressByEpic
}

func epicProgressDetail(progress epicChildProgress) Detail {
	total := epicProgressTotal(progress)
	return Detail{
		Kind:      DetailEpicProgress,
		Completed: progress.Completed,
		Total:     total,
	}
}

func epicProgressTotal(progress epicChildProgress) int {
	return max(progress.ObservedTotal, progress.DeclaredTotal)
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

func missingDependencyDetail(taskItem task.Task, deps []string, index map[string]task.Task) (string, Detail) {
	missing := make([]string, 0)
	for _, id := range deps {
		if _, ok := index[id]; !ok {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		detail := Detail{Kind: DetailMissingDependencies, IDs: cloneStrings(missing), Count: len(missing)}
		if len(missing) == 1 {
			detail.Kind = DetailMissingDependency
			detail.ID = missing[0]
		}
		return "missing dependency " + strings.Join(missing, ", "), detail
	}
	if taskItem.Relations.BlockedByCount > len(deps) {
		count := taskItem.Relations.BlockedByCount - len(deps)
		return fmt.Sprintf("dependency details missing for %d blocker(s)", count), Detail{
			Kind:  DetailDependencyDetailsMissing,
			Count: count,
		}
	}
	return "", Detail{}
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

func taskEntry(repository task.Repository, taskItem task.Task, result policyResult, epicProgress Detail) Entry {
	return Entry{
		Kind:           EntryTask,
		Repository:     repository,
		Task:           taskItem.Clone(),
		Detail:         detailOrDash(result.detail),
		SemanticDetail: result.semanticDetail,
		EpicProgress:   epicProgress,
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
		SemanticDetail: Detail{
			Kind:      DetailRepoFailure,
			Source:    valueOrUnknown(failure.Source),
			Operation: valueOrUnknown(failure.Operation),
		},
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
