// Package task defines Orpheus' backend-neutral task model and read contracts.
package task

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNotFound indicates a task backend could not find a matching active task.
	ErrNotFound = errors.New("task not found")
)

const (
	// MetadataBranch is the Orpheus-owned task metadata key for the task branch.
	MetadataBranch = "orpheus.branch"

	// MetadataWorktree is the Orpheus-owned task metadata key for the task worktree.
	MetadataWorktree = "orpheus.worktree"

	// MetadataPRURL is the Orpheus-owned task metadata key for the task pull request URL.
	MetadataPRURL = "orpheus.pr_url"
)

// Status is a backend-neutral task lifecycle status.
//
// Backends may return statuses not listed in the constants; callers should treat
// unknown non-empty statuses as data from the backend rather than a parse error.
type Status string

const (
	// StatusUnknown is the zero value used when a backend does not provide status.
	StatusUnknown Status = ""

	// StatusOpen means the task has not been started or closed.
	StatusOpen Status = "open"

	// StatusInProgress means work on the task has started.
	StatusInProgress Status = "in_progress"

	// StatusClosed means the task is done or otherwise closed in the backend.
	StatusClosed Status = "closed"
)

// IssueType identifies the kind of task-tracker item.
//
// M2 task views keep the field explicit so adapters and diagnostics can preserve
// backend data across all Beads issue types.
type IssueType string

const (
	// IssueTypeUnknown is the zero value used when a backend does not provide a type.
	IssueTypeUnknown IssueType = ""

	// IssueTypeTask is an implementation task item.
	IssueTypeTask IssueType = "task"

	// IssueTypeBug is a bug item.
	IssueTypeBug IssueType = "bug"

	// IssueTypeEpic is a parent/planning item.
	IssueTypeEpic IssueType = "epic"

	// IssueTypeChore is an operational or maintenance item.
	IssueTypeChore IssueType = "chore"
)

// Metadata is backend-provided task metadata normalized to string key/value pairs.
type Metadata map[string]string

// Value returns the metadata value for key.
func (m Metadata) Value(key string) (string, bool) {
	if m == nil {
		return "", false
	}
	value, ok := m[key]
	return value, ok
}

// Clone returns a copy of the metadata map.
func (m Metadata) Clone() Metadata {
	if m == nil {
		return nil
	}

	clone := make(Metadata, len(m))
	for key, value := range m {
		clone[key] = value
	}
	return clone
}

// OrpheusMetadata projects Orpheus-owned metadata keys from a task.
//
// Has* fields distinguish metadata that is absent from metadata that is present
// with an empty value. Absent metadata is normal for tasks that have not reached
// later Orpheus workflow stages.
type OrpheusMetadata struct {
	Branch      string
	HasBranch   bool
	Worktree    string
	HasWorktree bool
	PRURL       string
	HasPRURL    bool
}

// RelationSummary keeps lightweight relation information when a backend provides it.
//
// Count fields are zero when the backend reports no matching relations or when the
// backend did not include a count. ID slices are optional and may be empty even
// when a count is known.
type RelationSummary struct {
	ParentID string

	DependencyIDs []string
	DependentIDs  []string

	DependencyCount int
	DependentCount  int
	BlockedByCount  int
	BlockingCount   int
	ChildCount      int
}

// Clone returns a copy of the relation summary.
func (r RelationSummary) Clone() RelationSummary {
	r.DependencyIDs = cloneStrings(r.DependencyIDs)
	r.DependentIDs = cloneStrings(r.DependentIDs)
	return r
}

// Task is Orpheus' backend-neutral representation of a task item.
//
// The model intentionally contains only read-side data needed by M2 command
// output, status projection, and later agent context. Mutating capabilities such
// as claiming, closing, or metadata writes belong to later milestone interfaces.
type Task struct {
	ID                 string
	Title              string
	Description        string
	Design             string
	AcceptanceCriteria string

	Status    Status
	Priority  int
	IssueType IssueType
	Labels    []string
	Metadata  Metadata

	Assignee  string
	Owner     string
	CreatedBy string

	CreatedAt   *time.Time
	UpdatedAt   *time.Time
	StartedAt   *time.Time
	CompletedAt *time.Time
	ClosedAt    *time.Time

	Relations RelationSummary
}

// Clone returns a deep copy of mutable task fields.
func (t Task) Clone() Task {
	t.Labels = cloneStrings(t.Labels)
	t.Metadata = t.Metadata.Clone()
	t.CreatedAt = cloneTime(t.CreatedAt)
	t.UpdatedAt = cloneTime(t.UpdatedAt)
	t.StartedAt = cloneTime(t.StartedAt)
	t.CompletedAt = cloneTime(t.CompletedAt)
	t.ClosedAt = cloneTime(t.ClosedAt)
	t.Relations = t.Relations.Clone()
	return t
}

// ProjectOrpheusMetadata projects Orpheus-owned metadata keys into named fields.
func ProjectOrpheusMetadata(metadata Metadata) OrpheusMetadata {
	branch, hasBranch := metadata.Value(MetadataBranch)
	worktree, hasWorktree := metadata.Value(MetadataWorktree)
	prURL, hasPRURL := metadata.Value(MetadataPRURL)

	return OrpheusMetadata{
		Branch:      branch,
		HasBranch:   hasBranch,
		Worktree:    worktree,
		HasWorktree: hasWorktree,
		PRURL:       prURL,
		HasPRURL:    hasPRURL,
	}
}

// OrpheusMetadata returns Orpheus-owned metadata projected into named fields.
func (t Task) OrpheusMetadata() OrpheusMetadata {
	return ProjectOrpheusMetadata(t.Metadata)
}

// Getter fetches one task-tracker item by id for task show/get commands.
//
// Callers that implement M2 task views should use IsM2TaskViewItem to reject
// closed items with a clear out-of-scope message.
type Getter interface {
	Get(ctx context.Context, id string) (Task, error)
}

// Lister lists visible task-backend items for local read models.
type Lister interface {
	List(ctx context.Context) ([]Task, error)
}

// ReadBackend is the complete read-only M2 task backend contract.
//
// It intentionally excludes mutating operations such as claim, metadata writes,
// and close; later milestones should introduce separate, narrower mutating
// interfaces where those operations are consumed.
type ReadBackend interface {
	Getter
	Lister
}

// Repository identifies the registered repository that produced a task row or failure.
type Repository struct {
	ID           string
	Name         string
	TaskIDPrefix string
}

// RepoTask is one task row with repository context preserved for global views.
type RepoTask struct {
	Repository Repository
	Task       Task
}

// Clone returns a copy of the repo task row.
func (r RepoTask) Clone() RepoTask {
	r.Task = r.Task.Clone()
	return r
}

// RepoFailure is a per-repository query failure for partial global results.
type RepoFailure struct {
	Repository Repository
	Source     string
	Operation  string
	Err        error
}

// RepositorySnapshot is the local read state for one repository used by status projections.
type RepositorySnapshot struct {
	Repository Repository
	Tasks      []Task
}

// Clone returns a copy of the repository snapshot and mutable task fields.
func (s RepositorySnapshot) Clone() RepositorySnapshot {
	s.Tasks = cloneTasks(s.Tasks)
	return s
}

// SnapshotResult represents a cross-repository read of active and ready task snapshots.
type SnapshotResult struct {
	Repositories []RepositorySnapshot
	Failures     []RepoFailure
}

// HasFailures reports whether at least one repository snapshot query failed.
func (r SnapshotResult) HasFailures() bool {
	return len(r.Failures) > 0
}

// Clone returns a copy of the snapshot result and its mutable task fields.
func (r SnapshotResult) Clone() SnapshotResult {
	clone := SnapshotResult{
		Repositories: cloneSnapshots(r.Repositories),
		Failures:     cloneFailures(r.Failures),
	}
	return clone
}

// QueryResult represents a cross-repository read with successful rows and failures.
type QueryResult struct {
	Rows     []RepoTask
	Failures []RepoFailure
}

// HasFailures reports whether at least one repository query failed.
func (r QueryResult) HasFailures() bool {
	return len(r.Failures) > 0
}

// Clone returns a copy of the query result and its mutable task fields.
func (r QueryResult) Clone() QueryResult {
	clone := QueryResult{
		Rows:     cloneRows(r.Rows),
		Failures: cloneFailures(r.Failures),
	}
	return clone
}

// IsM2TaskViewItem reports whether taskItem is in scope for M2 task views.
//
// Milestone 2 views are intentionally read-only and scoped to active backend
// items. Closed tasks may be visible to backends for status projection, but
// task-list and task-show views should report them as out of scope rather than
// acting on them.
func IsM2TaskViewItem(taskItem Task) bool {
	return taskItem.Status != StatusClosed
}

func cloneRows(rows []RepoTask) []RepoTask {
	if rows == nil {
		return nil
	}

	clone := make([]RepoTask, len(rows))
	for i, row := range rows {
		clone[i] = row.Clone()
	}
	return clone
}

func cloneSnapshots(snapshots []RepositorySnapshot) []RepositorySnapshot {
	if snapshots == nil {
		return nil
	}

	clone := make([]RepositorySnapshot, len(snapshots))
	for i, snapshot := range snapshots {
		clone[i] = snapshot.Clone()
	}
	return clone
}

func cloneFailures(failures []RepoFailure) []RepoFailure {
	if failures == nil {
		return nil
	}

	clone := make([]RepoFailure, len(failures))
	copy(clone, failures)
	return clone
}

func cloneTasks(tasks []Task) []Task {
	if tasks == nil {
		return nil
	}

	clone := make([]Task, len(tasks))
	for i, taskItem := range tasks {
		clone[i] = taskItem.Clone()
	}
	return clone
}

func cloneActiveTasks(tasks []Task) []Task {
	active := make([]Task, 0, len(tasks))
	for _, taskItem := range tasks {
		if !IsM2TaskViewItem(taskItem) {
			continue
		}
		active = append(active, taskItem.Clone())
	}
	return active
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	clone := make([]string, len(values))
	copy(clone, values)
	return clone
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
