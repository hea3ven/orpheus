// Package status projects local task snapshots into operator-facing status groups.
package status

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// GroupID identifies an M2 local status projection group.
type GroupID string

const (
	// GroupReadyToRun contains items Orpheus' local readiness policy considers ready.
	GroupReadyToRun GroupID = "ready_to_run"

	// GroupWorking contains non-closed in-progress items without a local PR URL.
	GroupWorking GroupID = "working"

	// GroupBlocked contains items with locally visible open blocking dependencies.
	GroupBlocked GroupID = "blocked"

	// GroupInReview contains non-closed items ready for human review.
	GroupInReview GroupID = "in_review"

	// GroupDoneClosed contains closed backend items.
	GroupDoneClosed GroupID = "done_closed"

	// GroupUnknown contains items or repo diagnostics M2 cannot classify confidently.
	GroupUnknown GroupID = "unknown_needs_attention"
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

// Projection is the M2 local-only action-queue view.
type Projection struct {
	Groups []Group
}

// RunStateIndex contains the latest Orpheus run attempt by repository/task key.
type RunStateIndex map[string]taskstate.RunAttempt

type readinessState string

const (
	readinessReady   readinessState = "ready"
	readinessWorking readinessState = "working"
	readinessBlocked readinessState = "blocked"
	readinessReview  readinessState = "in_review"
	readinessDone    readinessState = "done"
	readinessUnknown readinessState = "unknown"
)

type policyResult struct {
	state  readinessState
	detail string
}

// Project builds the local-only M2 status projection from task aggregation snapshots.
func Project(snapshot task.SnapshotResult) Projection {
	return ProjectWithRunStates(snapshot, nil)
}

// ProjectWithRunStates builds the status projection using latest run attempts for M3 local execution state.
func ProjectWithRunStates(snapshot task.SnapshotResult, runStates RunStateIndex) Projection {
	projection := Projection{Groups: []Group{
		{ID: GroupUnknown, Title: "Unknown / needs attention"},
		{ID: GroupInReview, Title: "In review"},
		{ID: GroupWorking, Title: "Working"},
		{ID: GroupReadyToRun, Title: "Ready to run"},
		{ID: GroupBlocked, Title: "Blocked"},
		{ID: GroupDoneClosed, Title: "Done / closed"},
	}}

	for _, repoSnapshot := range snapshot.Repositories {
		projectRepository(&projection, repoSnapshot, runStates)
	}
	for _, failure := range snapshot.Failures {
		projection.add(GroupUnknown, failureEntry(failure))
	}
	return projection
}

// ReadyRows returns rows selected by the canonical Orpheus MVP readiness policy.
func ReadyRows(snapshot task.SnapshotResult) []task.RepoTask {
	return ReadyRowsWithRunStates(snapshot, nil)
}

// ReadyRowsWithRunStates returns ready rows while respecting active M3 run attempts.
func ReadyRowsWithRunStates(snapshot task.SnapshotResult, runStates RunStateIndex) []task.RepoTask {
	rows := make([]task.RepoTask, 0)
	for _, repoSnapshot := range snapshot.Repositories {
		index := newRepositoryIndex(repoSnapshot.Tasks)
		for _, taskItem := range repoSnapshot.Tasks {
			if classify(repoSnapshot.Repository, taskItem, index, latestRunFor(runStates, repoSnapshot.Repository.ID, taskItem.ID)).state != readinessReady {
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

func projectRepository(projection *Projection, repoSnapshot task.RepositorySnapshot, runStates RunStateIndex) {
	index := newRepositoryIndex(repoSnapshot.Tasks)
	for _, taskItem := range repoSnapshot.Tasks {
		result := classify(repoSnapshot.Repository, taskItem, index, latestRunFor(runStates, repoSnapshot.Repository.ID, taskItem.ID))
		projection.add(groupForState(result.state), taskEntry(repoSnapshot.Repository, taskItem, result.detail))
	}
}

func classify(repository task.Repository, taskItem task.Task, index map[string]task.Task, latestRun *taskstate.RunAttempt) policyResult {
	metadata := taskItem.OrpheusMetadata()
	if taskItem.Status == task.StatusClosed {
		return policyResult{state: readinessDone, detail: "closed"}
	}
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return policyResult{state: readinessReview, detail: metadata.PRURL}
	}
	if latestRun != nil && latestRun.Status == taskstate.RunStatusRunning {
		return policyResult{
			state:  readinessUnknown,
			detail: fmt.Sprintf("run attempt %d is running; M3 cannot verify whether the attached process is still alive", latestRun.Attempt),
		}
	}
	if isSuccessfulRepoRootReview(repository, taskItem, latestRun) {
		return policyResult{state: readinessReview, detail: "local repo-root review (no PR URL)"}
	}
	if taskItem.Status == task.StatusInProgress {
		return policyResult{state: readinessWorking, detail: "-"}
	}

	deps := dependencyIDs(taskItem)
	missingDetail := missingDependencyDetail(taskItem, deps, index)
	if missingDetail != "" {
		return policyResult{state: readinessUnknown, detail: missingDetail}
	}
	openDeps := openDependencyIDs(deps, index)
	if len(openDeps) > 0 {
		return policyResult{state: readinessBlocked, detail: "blocked by " + strings.Join(openDeps, ", ")}
	}

	if taskItem.Status == task.StatusOpen {
		return policyResult{state: readinessReady, detail: "-"}
	}
	return policyResult{state: readinessUnknown, detail: fmt.Sprintf("status %s is not locally actionable in M2", formatStatus(taskItem.Status))}
}

func isSuccessfulRepoRootReview(repository task.Repository, taskItem task.Task, latestRun *taskstate.RunAttempt) bool {
	if latestRun == nil || latestRun.Status != taskstate.RunStatusSucceeded || taskItem.Status != task.StatusInProgress {
		return false
	}

	metadata := taskItem.OrpheusMetadata()
	if !repoRootMetadataMatches(repository, metadata) {
		return false
	}
	return strings.TrimSpace(latestRun.Branch) == strings.TrimSpace(repository.DefaultBranch) &&
		cleanPath(latestRun.Worktree) == cleanPath(repository.Path)
}

func repoRootMetadataMatches(repository task.Repository, metadata task.OrpheusMetadata) bool {
	defaultBranch := strings.TrimSpace(repository.DefaultBranch)
	repoPath := cleanPath(repository.Path)
	if defaultBranch == "" || repoPath == "" {
		return false
	}
	return metadata.HasBranch && strings.TrimSpace(metadata.Branch) == defaultBranch &&
		metadata.HasWorktree && cleanPath(metadata.Worktree) == repoPath
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func latestRunFor(runStates RunStateIndex, repoID string, taskID string) *taskstate.RunAttempt {
	if len(runStates) == 0 {
		return nil
	}
	run, ok := runStates[RunStateKey(repoID, taskID)]
	if !ok {
		return nil
	}
	return &run
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
	case readinessWorking:
		return GroupWorking
	case readinessBlocked:
		return GroupBlocked
	case readinessReview:
		return GroupInReview
	case readinessDone:
		return GroupDoneClosed
	default:
		return GroupUnknown
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
