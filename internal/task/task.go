// Package task defines Orpheus' backend-neutral task model and read contracts.
package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrNotFound indicates a task backend could not find a matching active task.
	ErrNotFound = errors.New("task not found")

	// ErrMutationConflict indicates a backend mutation found task state that needs operator attention.
	ErrMutationConflict = errors.New("task mutation conflict")
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
	ExternalRef        string
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

// SessionName returns the implementation agent session name for a task run.
func (t Task) SessionName() string {
	return t.sessionName("Implementing")
}

// FollowUpSessionName returns the implementation agent session name for a
// review follow-up run.
func (t Task) FollowUpSessionName() string {
	return t.sessionName("Resolving issues in")
}

// ReviewSessionName returns the review agent session name for a task review.
func (t Task) ReviewSessionName() string {
	return t.sessionName("Reviewing")
}

func (t Task) sessionName(prefix string) string {
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Sprintf("%s %s", prefix, t.ID)
	}
	return fmt.Sprintf("%s %s %s", prefix, t.ID, t.Title)
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

// DispatchMutator is the narrow backend-neutral mutation used before launching an attached agent.
//
// MarkInProgress means: mark taskID as being worked through the native backend and persist
// Orpheus' deterministic branch/worktree pointers for the task.
type DispatchMutator interface {
	MarkInProgress(ctx context.Context, taskID string, branch string, worktree string) error
}

// DispatchBackend is the backend capability set needed by task dispatch orchestration.
type DispatchBackend interface {
	Getter
	DispatchMutator
}

// GitFactsMutator updates evolving Git facts after publication materializes a
// task branch while retaining the task's fixed work directory.
type GitFactsMutator interface {
	UpdateGitFacts(ctx context.Context, taskID string, branch string, worktree string) error
}

// PRURLMutator is the narrow backend-neutral mutation used when a task enters PR review.
type PRURLMutator interface {
	SetPRURL(ctx context.Context, taskID string, prURL string) error
}

// EpicStartMutator is the narrow backend-neutral mutation used to activate an
// eligible epic. Callers are responsible for applying epic lifecycle policy
// before invoking it.
type EpicStartMutator interface {
	StartEpic(ctx context.Context, taskID string) error
}

// SyncBackend is the backend capability set needed by task sync orchestration.
type SyncBackend interface {
	Getter
	PRURLMutator
	CloseMutator
}

// UpdateOptions describes a backend-neutral request to update a task item.
//
// Pointer fields distinguish "not specified" (nil) from "explicitly cleared" (empty string).
// Use the NormalizeUpdateOptions helper to apply validation and convert to UpdateOptions
// with nil pointers for unspecified fields.
type UpdateOptions struct {
	ID                 string
	Title              *string
	Description        *string
	Design             *string
	AcceptanceCriteria *string
	ExternalRef        *string
	ParentID           *string
	AddBlockingIDs     []string
	RemoveBlockingIDs  []string
}

// UpdateMutator is the narrow backend-neutral mutation used to update task items.
type UpdateMutator interface {
	Update(ctx context.Context, opts UpdateOptions) (Task, error)
}

// UpdateBackend is the backend capability set needed for task update orchestration.
type UpdateBackend interface {
	Getter
	UpdateMutator
}

// UpdateBackendFactory constructs an update backend for one repository source.
type UpdateBackendFactory func(RepositorySource) (UpdateBackend, error)

// UpdateService applies Orpheus update policy independently of Cobra and any
// concrete task source.
type UpdateService struct {
	Sources        []RepositorySource
	BackendFactory UpdateBackendFactory
}

// Update validates one request and updates it in source.
func (s UpdateService) Update(ctx context.Context, source RepositorySource, request UpdateOptions) (Task, error) {
	if s.BackendFactory == nil {
		return Task{}, errors.New("update task: backend factory is required")
	}
	if err := sourceIsConfigured(s.Sources, source); err != nil {
		return Task{}, err
	}

	opts, err := NormalizeUpdateOptions(request)
	if err != nil {
		return Task{}, err
	}

	backend, err := s.BackendFactory(source)
	if err != nil {
		return Task{}, updateFailure{message: fmt.Sprintf("cannot prepare task update in repository %s", source.Repository.ID), cause: err}
	}

	// Load current task to validate state and get current values
	current, err := backend.Get(ctx, opts.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Task{}, fmt.Errorf("task %q not found in repository %s", opts.ID, source.Repository.ID)
		}
		return Task{}, updateFailure{message: fmt.Sprintf("cannot inspect task %q in repository %s", opts.ID, source.Repository.ID), cause: err}
	}

	if current.Status == StatusClosed {
		return Task{}, fmt.Errorf("task %q in repository %s is closed", opts.ID, source.Repository.ID)
	}
	if current.IssueType != IssueTypeTask && current.IssueType != IssueTypeEpic {
		return Task{}, fmt.Errorf("task %q in repository %s has unsupported item type %q; expected task or epic", opts.ID, source.Repository.ID, current.IssueType)
	}
	resultingExternalRef := current.ExternalRef
	if opts.ExternalRef != nil {
		resultingExternalRef = *opts.ExternalRef
	}
	if err := validateRequiredExternalRef(source.Repository.TitleTemplate, current.IssueType, resultingExternalRef); err != nil {
		return Task{}, err
	}
	if err := validateRequiredUpdateContent(opts, current); err != nil {
		return Task{}, err
	}

	// Validate all graph changes before invoking the backend mutator, whose
	// individual source operations are not necessarily atomic.
	if err := s.validateRelations(ctx, source, backend, opts, current); err != nil {
		return Task{}, err
	}
	// The underlying task source removes relationships by item pair rather than
	// by relationship type. Do not pass absent blocking dependencies through to
	// it: they are retry-safe no-ops and may instead name an unrelated edge.
	opts.RemoveBlockingIDs = existingBlockingDependencyIDs(current, opts.RemoveBlockingIDs)

	updated, err := backend.Update(ctx, opts)
	if err != nil {
		return Task{}, updateFailure{message: fmt.Sprintf("cannot update task %q in repository %s", opts.ID, source.Repository.ID), cause: err}
	}
	if updated.IssueType == IssueTypeUnknown {
		updated.IssueType = current.IssueType
	}
	return updated, nil
}

func (s UpdateService) validateRelations(
	ctx context.Context,
	source RepositorySource,
	backend UpdateBackend,
	opts UpdateOptions,
	current Task,
) error {
	if err := s.validateParent(ctx, source, backend, opts, current); err != nil {
		return err
	}
	if err := s.validateAddBlockingDependencies(ctx, source, backend, opts, current); err != nil {
		return err
	}
	return s.validateRemoveBlockingDependencies(ctx, source, backend, opts, current)
}

func (s UpdateService) validateParent(
	ctx context.Context,
	source RepositorySource,
	backend UpdateBackend,
	opts UpdateOptions,
	current Task,
) error {
	if opts.ParentID == nil {
		return nil
	}
	parentID := strings.TrimSpace(*opts.ParentID)
	if parentID == "" {
		// Clearing parent is allowed if backend supports it
		return nil
	}
	if err := s.validateReferenceRepository(source, parentID, "parent"); err != nil {
		return err
	}
	parent, err := getUpdateReference(ctx, backend, source, parentID, "parent")
	if err != nil {
		return err
	}
	if parent.IssueType != IssueTypeEpic {
		return fmt.Errorf("parent %q in repository %s must be an epic", parentID, source.Repository.ID)
	}
	if parent.Status == StatusClosed {
		return fmt.Errorf("parent epic %q in repository %s is closed", parentID, source.Repository.ID)
	}
	createsCycle, err := s.wouldCreateParentCycle(ctx, backend, source, current.ID, parentID)
	if err != nil {
		return err
	}
	if createsCycle {
		return fmt.Errorf("setting parent %q would create a parent-child cycle", parentID)
	}
	return nil
}

// wouldCreateParentCycle reports whether setting parentID as taskID's parent
// would make taskID an ancestor of itself. Every reference read is surfaced so
// callers do not mutate after incomplete graph validation.
func (s UpdateService) wouldCreateParentCycle(
	ctx context.Context,
	backend UpdateBackend,
	source RepositorySource,
	taskID string,
	parentID string,
) (bool, error) {
	visited := make(map[string]struct{})
	for currentID := parentID; currentID != ""; {
		if currentID == taskID {
			return true, nil
		}
		if _, seen := visited[currentID]; seen {
			return true, nil
		}
		visited[currentID] = struct{}{}

		item, err := getUpdateReference(ctx, backend, source, currentID, "parent")
		if err != nil {
			return false, err
		}
		currentID = strings.TrimSpace(item.Relations.ParentID)
	}
	return false, nil
}

func (s UpdateService) validateAddBlockingDependencies(
	ctx context.Context,
	source RepositorySource,
	backend UpdateBackend,
	opts UpdateOptions,
	current Task,
) error {
	for _, dependencyID := range opts.AddBlockingIDs {
		if err := s.validateReferenceRepository(source, dependencyID, "blocking dependency"); err != nil {
			return err
		}
		dependency, err := getUpdateReference(ctx, backend, source, dependencyID, "blocking dependency")
		if err != nil {
			return err
		}
		if dependency.IssueType != IssueTypeTask && dependency.IssueType != IssueTypeEpic {
			return fmt.Errorf("blocking dependency %q in repository %s must be a task or epic", dependencyID, source.Repository.ID)
		}
		if dependencyID == current.ID {
			return fmt.Errorf("task %q cannot depend on itself", current.ID)
		}

		// Check for cycle: would adding this dependency create a cycle?
		// We need to check if there's a path from dependencyID to current.ID
		createsCycle, err := s.wouldCreateCycle(ctx, backend, source, current.ID, dependencyID)
		if err != nil {
			return err
		}
		if createsCycle {
			return fmt.Errorf("adding blocking dependency %q would create a dependency cycle", dependencyID)
		}
	}
	return nil
}

// wouldCreateCycle checks if adding a dependency from taskID to dependencyID would create a cycle.
// It does this by checking if there's already a path from dependencyID to taskID.
func (s UpdateService) wouldCreateCycle(
	ctx context.Context,
	backend UpdateBackend,
	source RepositorySource,
	taskID string,
	dependencyID string,
) (bool, error) {
	// Use DFS to check if there's a path from dependencyID to taskID.
	visited := make(map[string]bool)
	var dfs func(string) (bool, error)
	dfs = func(currentID string) (bool, error) {
		if currentID == taskID {
			return true, nil
		}
		if visited[currentID] {
			return false, nil
		}
		visited[currentID] = true

		item, err := getUpdateReference(ctx, backend, source, currentID, "blocking dependency")
		if err != nil {
			return false, err
		}

		for _, dep := range item.Relations.DependencyIDs {
			createsCycle, err := dfs(dep)
			if err != nil {
				return false, err
			}
			if createsCycle {
				return true, nil
			}
		}
		return false, nil
	}

	return dfs(dependencyID)
}

func (s UpdateService) validateRemoveBlockingDependencies(
	ctx context.Context,
	source RepositorySource,
	backend UpdateBackend,
	opts UpdateOptions,
	current Task,
) error {
	for _, dependencyID := range opts.RemoveBlockingIDs {
		if err := s.validateReferenceRepository(source, dependencyID, "blocking dependency"); err != nil {
			return err
		}
		if !s.dependencyExists(current, dependencyID) {
			// Retry-safe no-op: removing an absent dependency succeeds.
			continue
		}
		dependency, err := getUpdateReference(ctx, backend, source, dependencyID, "blocking dependency")
		if err != nil {
			return err
		}
		if dependency.IssueType != IssueTypeTask && dependency.IssueType != IssueTypeEpic {
			return fmt.Errorf("blocking dependency %q in repository %s must be a task or epic", dependencyID, source.Repository.ID)
		}
	}
	return nil
}

func (s UpdateService) dependencyExists(current Task, dependencyID string) bool {
	for _, dep := range current.Relations.DependencyIDs {
		if dep == dependencyID {
			return true
		}
	}
	return false
}

func existingBlockingDependencyIDs(current Task, requested []string) []string {
	if len(requested) == 0 {
		return nil
	}

	existing := make(map[string]struct{}, len(current.Relations.DependencyIDs))
	for _, id := range current.Relations.DependencyIDs {
		existing[id] = struct{}{}
	}

	filtered := make([]string, 0, len(requested))
	for _, id := range requested {
		if _, ok := existing[id]; ok {
			filtered = append(filtered, id)
		}
	}
	return filtered
}

func (s UpdateService) validateReferenceRepository(source RepositorySource, id string, relation string) error {
	var owner RepositorySource
	longestPrefix := 0
	for _, candidate := range s.Sources {
		prefix := strings.TrimSpace(candidate.Repository.TaskIDPrefix)
		if prefix == "" || !strings.HasPrefix(id, prefix+"-") || len(prefix) <= longestPrefix {
			continue
		}
		owner = candidate
		longestPrefix = len(prefix)
	}
	if owner.Repository.ID != "" && owner.Repository.ID != source.Repository.ID {
		return fmt.Errorf("%s %q belongs to repository %s, not selected repository %s", relation, id, owner.Repository.ID, source.Repository.ID)
	}
	return nil
}

func validateRequiredUpdateContent(opts UpdateOptions, current Task) error {
	title := current.Title
	if opts.Title != nil {
		title = *opts.Title
	}
	if strings.TrimSpace(title) == "" {
		return errors.New("title is required")
	}

	description := current.Description
	if opts.Description != nil {
		description = *opts.Description
	}
	if strings.TrimSpace(description) == "" {
		return errors.New("description is required")
	}

	acceptance := current.AcceptanceCriteria
	if opts.AcceptanceCriteria != nil {
		acceptance = *opts.AcceptanceCriteria
	}
	if strings.TrimSpace(acceptance) == "" {
		return errors.New("acceptance criteria is required")
	}
	return nil
}

func getUpdateReference(ctx context.Context, backend Getter, source RepositorySource, id string, relation string) (Task, error) {
	item, err := backend.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Task{}, fmt.Errorf("%s %q was not found in repository %s", relation, id, source.Repository.ID)
		}
		return Task{}, updateFailure{message: fmt.Sprintf("cannot inspect %s %q in repository %s", relation, id, source.Repository.ID), cause: err}
	}
	return item, nil
}

type updateFailure struct {
	message string
	cause   error
}

func (e updateFailure) Error() string { return e.message }

func (e updateFailure) Unwrap() error { return e.cause }

// CreateOptions describes one backend task item to create.
//
// ParentID and BlockingIDs express the new item's graph relationships without
// exposing backend-specific command syntax.
type CreateOptions struct {
	Title              string
	Description        string
	Design             string
	AcceptanceCriteria string
	ExternalRef        string
	IssueType          IssueType
	ParentID           string
	BlockingIDs        []string
}

// CreateMutator is the narrow backend-neutral mutation used to create items,
// including standalone review follow-up work discovered during review.
type CreateMutator interface {
	Create(ctx context.Context, opts CreateOptions) (Task, error)
}

// CloseMutator is the narrow backend-neutral mutation used when finalizing a
// reviewed main/solo task.
type CloseMutator interface {
	Close(ctx context.Context, taskID string) error
}

// MutationConflictError reports backend task state that prevents a semantic mutation.
type MutationConflictError struct {
	TaskID string
	Reason string
}

// Error describes the task mutation conflict.
func (e MutationConflictError) Error() string {
	if e.TaskID == "" {
		return ErrMutationConflict.Error()
	}
	if e.Reason == "" {
		return fmt.Sprintf("%s: task %s needs attention", ErrMutationConflict, e.TaskID)
	}
	return fmt.Sprintf("%s: task %s needs attention: %s", ErrMutationConflict, e.TaskID, e.Reason)
}

// Unwrap allows callers to match ErrMutationConflict with errors.Is.
func (e MutationConflictError) Unwrap() error {
	return ErrMutationConflict
}

// Repository identifies the registered repository that produced a task row or failure.
type Repository struct {
	ID                     string
	Name                   string
	TaskIDPrefix           string
	Path                   string
	DefaultBranch          string
	TitleTemplate          string
	IntegrationFlow        string
	IncludePRReviewProcess *bool
	ReviewPipeline         string
	ReviewPipelineAliases  map[string]string
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
