package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/task"
)

const crossTypeBlockingDependencyType = "orpheus-blocks"

var (
	_ task.ReadBackend      = TaskBackend{}
	_ task.DispatchMutator  = TaskBackend{}
	_ task.PRURLMutator     = TaskBackend{}
	_ task.EpicStartMutator = TaskBackend{}
	_ task.CloseMutator     = TaskBackend{}
	_ task.CreateMutator    = TaskBackend{}
	_ task.UpdateMutator    = TaskBackend{}
)

// TaskBackend reads task-source items from one explicit Beads workspace.
//
// List returns only task and epic items, including closed items. Get returns
// only those types and rejects other Beads items at the source boundary. Use
// NewTaskBackend or NewTaskBackendWithRunner to construct a valid value.
type TaskBackend struct {
	dir              string
	runner           Runner
	maintenanceOwned bool
	logger           *slog.Logger
}

type diagnosticRunner interface {
	Runner
	WithDiagnosticAttrs(attrs ...slog.Attr) Runner
}

// NewTaskBackend returns a Beads-backed task reader using the bd binary.
func NewTaskBackend(dir string) (TaskBackend, error) {
	return NewTaskBackendWithLogger(dir, nil)
}

// NewTaskBackendWithLogger returns a Beads-backed task reader with diagnostics.
func NewTaskBackendWithLogger(dir string, logger *slog.Logger) (TaskBackend, error) {
	return newTaskBackendWithRunner(dir, false, CommandRunner{Logger: logger}, logger)
}

// NewTaskBackendWithRunner returns a Beads-backed task reader using runner.
// It does not grant maintenance ownership; callers constructing a backend for
// a registered source should use NewTaskBackendForSourceWithRunner.
func NewTaskBackendWithRunner(dir string, runner Runner) (TaskBackend, error) {
	return newTaskBackendWithRunner(dir, false, runner, nil)
}

// NewTaskBackendForSourceWithRunner returns a Beads backend for source.
// Maintenance ownership comes only from source, never from its backend path.
func NewTaskBackendForSourceWithRunner(source task.RepositorySource, runner Runner, logger *slog.Logger) (TaskBackend, error) {
	return newTaskBackendWithRunner(source.BackendDir, source.MaintenanceOwned, runner, logger)
}

func newTaskBackendWithRunner(dir string, maintenanceOwned bool, runner Runner, logger *slog.Logger) (TaskBackend, error) {
	if runner == nil {
		return TaskBackend{}, errors.New("create Beads task backend: runner is required")
	}

	normalizedDir, err := normalizeTaskBackendDir(dir)
	if err != nil {
		return TaskBackend{}, err
	}

	return TaskBackend{
		dir:              normalizedDir,
		runner:           runner,
		maintenanceOwned: maintenanceOwned,
		logger:           logger,
	}, nil
}

// Get fetches one Beads item by id.
func (b TaskBackend) Get(ctx context.Context, id string) (task.Task, error) {
	rawTask, err := b.getRawTask(ctx, id)
	if err != nil {
		return task.Task{}, err
	}

	taskItem, err := rawTask.toTask()
	if err != nil {
		return task.Task{}, fmt.Errorf("get Beads task %q in %q: parse bd show JSON: %w", rawTask.ID, b.dir, err)
	}
	if err := task.ValidateTaskSourceItem(taskItem); err != nil {
		return task.Task{}, fmt.Errorf("get Beads task %q in %q: %w", taskItem.ID, b.dir, err)
	}
	return taskItem, nil
}

// getRawTask reads one Beads item while retaining concrete relationship types
// that are intentionally not exposed by task.Task.
func (b TaskBackend) getRawTask(ctx context.Context, id string) (bdTask, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return bdTask{}, fmt.Errorf("get Beads task in %q: task id is required", b.dir)
	}

	result, err := b.runWithAttrs(ctx, "get", []slog.Attr{slog.String("task_id", id)}, "show", "--id", id)
	if err != nil {
		if isNotFoundResult(result) {
			return bdTask{}, fmt.Errorf("get Beads task %q in %q: %w%s", id, b.dir, task.ErrNotFound, formattedOutput(result))
		}
		return bdTask{}, err
	}

	rawTasks, err := parseRawTaskArray(result.Stdout)
	if err != nil {
		return bdTask{}, fmt.Errorf("get Beads task %q in %q: parse bd show JSON: %w%s", id, b.dir, err, formattedOutput(result))
	}
	for _, rawTask := range rawTasks {
		if rawTask.ID == id {
			return rawTask, nil
		}
	}

	return bdTask{}, fmt.Errorf("get Beads task %q in %q: %w", id, b.dir, task.ErrNotFound)
}

// List lists task and epic Beads items, including closed items.
func (b TaskBackend) List(ctx context.Context) ([]task.Task, error) {
	tasks := make([]task.Task, 0)
	for _, issueType := range []task.IssueType{task.IssueTypeTask, task.IssueTypeEpic} {
		listed, err := b.listIssueType(ctx, issueType)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, listed...)
	}
	return task.FilterTaskSourceItems(tasks), nil
}

func (b TaskBackend) listIssueType(ctx context.Context, issueType task.IssueType) ([]task.Task, error) {
	result, err := b.run(ctx, "list", "list", "--all", "--limit", "0", "--type", string(issueType))
	if err != nil {
		return nil, err
	}

	tasks, err := parseTaskArray(result.Stdout)
	if err != nil {
		return nil, fmt.Errorf("list Beads %s items in %q: parse bd list JSON: %w%s", issueType, b.dir, err, formattedOutput(result))
	}
	return tasks, nil
}

// Create creates a standalone Beads task.
func (b TaskBackend) Create(ctx context.Context, opts task.CreateOptions) (task.Task, error) {
	createOpts, err := task.NormalizeCreateOptions(opts)
	if err != nil {
		return task.Task{}, fmt.Errorf("create Beads task in %q: %w", b.dir, err)
	}

	args := []string{
		"create",
		createOpts.Title,
		"--description", createOpts.Description,
		"--acceptance", createOpts.AcceptanceCriteria,
		"--type", string(createOpts.IssueType),
	}
	if createOpts.Design != "" {
		args = append(args, "--design", createOpts.Design)
	}
	if createOpts.ExternalRef != "" {
		args = append(args, "--external-ref", createOpts.ExternalRef)
	}
	if createOpts.ParentID != "" {
		args = append(args, "--parent", createOpts.ParentID)
	}
	for _, dependencyID := range createOpts.BlockingIDs {
		args = append(args, "--deps", dependencyID)
	}

	result, err := b.runWrite(ctx, "create", args...)
	if err != nil {
		return task.Task{}, err
	}

	created, err := parseCreatedTask(result.Stdout)
	if err != nil {
		return task.Task{}, fmt.Errorf("create Beads task in %q: parse bd create JSON: %w%s", b.dir, err, formattedOutput(result))
	}
	if strings.TrimSpace(created.ID) == "" {
		return task.Task{}, fmt.Errorf("create Beads task in %q: bd create response did not include task id%s", b.dir, formattedOutput(result))
	}
	return created, nil
}

// MarkInProgress marks a Beads task in progress and stores Orpheus dispatch pointers.
func (b TaskBackend) MarkInProgress(ctx context.Context, id string, branch string, worktree string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("mark Beads task in progress in %q: task id is required", b.dir)
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("mark Beads task %q in progress in %q: branch is required", id, b.dir)
	}
	worktree = strings.TrimSpace(worktree)
	if worktree == "" {
		return fmt.Errorf("mark Beads task %q in progress in %q: worktree is required", id, b.dir)
	}

	current, err := b.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("mark Beads task %q in progress in %q: inspect task: %w", id, b.dir, err)
	}
	if err := validateMarkInProgressState(current, branch, worktree); err != nil {
		return fmt.Errorf("mark Beads task %q in progress in %q: %w", id, b.dir, err)
	}
	if current.Status == task.StatusInProgress {
		return nil
	}

	result, err := b.runWriteWithAttrs(
		ctx,
		"mark in-progress",
		[]slog.Attr{slog.String("task_id", id)},
		"update",
		id,
		"--status",
		string(task.StatusInProgress),
		"--set-metadata",
		task.MetadataBranch+"="+branch,
		"--set-metadata",
		task.MetadataWorktree+"="+worktree,
	)
	if err != nil {
		if isNotFoundResult(result) {
			return fmt.Errorf("mark Beads task %q in progress in %q: %w%s", id, b.dir, task.ErrNotFound, formattedOutput(result))
		}
		return err
	}
	return nil
}

// StartEpic marks an eligible Beads epic in progress. Existing in-progress
// epics are treated as a successful no-op.
func (b TaskBackend) StartEpic(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("start Beads epic in %q: task id is required", b.dir)
	}

	current, err := b.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("start Beads epic %q in %q: inspect item: %w", id, b.dir, err)
	}
	if err := validateStartEpicState(current); err != nil {
		return fmt.Errorf("start Beads epic %q in %q: %w", id, b.dir, err)
	}
	if current.Status == task.StatusInProgress {
		return nil
	}

	result, err := b.runWriteWithAttrs(
		ctx,
		"start epic",
		[]slog.Attr{slog.String("task_id", id)},
		"update",
		id,
		"--status",
		string(task.StatusInProgress),
	)
	if err != nil {
		if isNotFoundResult(result) {
			return fmt.Errorf("start Beads epic %q in %q: %w%s", id, b.dir, task.ErrNotFound, formattedOutput(result))
		}
		return err
	}
	return nil
}

// UpdateGitFacts records the current task branch after repository-root work
// is reviewed and publication materializes the deterministic task branch.
func (b TaskBackend) UpdateGitFacts(ctx context.Context, id string, branch string, worktree string) error {
	id = strings.TrimSpace(id)
	branch = strings.TrimSpace(branch)
	worktree = strings.TrimSpace(worktree)
	if id == "" || branch == "" || worktree == "" {
		return fmt.Errorf("update Beads task Git facts in %q: task id, branch, and worktree are required", b.dir)
	}
	current, err := b.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("update Beads task %q Git facts in %q: inspect task: %w", id, b.dir, err)
	}
	if current.Status != task.StatusInProgress {
		return task.MutationConflictError{TaskID: id, Reason: "task is not in progress"}
	}
	metadata := current.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return task.MutationConflictError{TaskID: id, Reason: fmt.Sprintf("%s is already set", task.MetadataPRURL)}
	}
	result, err := b.runWriteWithAttrs(ctx, "update Git facts", []slog.Attr{slog.String("task_id", id)}, "update", id,
		"--set-metadata", task.MetadataBranch+"="+branch,
		"--set-metadata", task.MetadataWorktree+"="+worktree,
	)
	if err != nil {
		if isNotFoundResult(result) {
			return fmt.Errorf("update Beads task %q Git facts in %q: %w%s", id, b.dir, task.ErrNotFound, formattedOutput(result))
		}
		return err
	}
	return nil
}

// SetPRURL stores the task pull request URL in Beads metadata.
func (b TaskBackend) SetPRURL(ctx context.Context, id string, prURL string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("set Beads task PR URL in %q: task id is required", b.dir)
	}
	prURL = strings.TrimSpace(prURL)
	if prURL == "" {
		return fmt.Errorf("set Beads task %q PR URL in %q: PR URL is required", id, b.dir)
	}

	result, err := b.runWriteWithAttrs(
		ctx,
		"set PR URL",
		[]slog.Attr{slog.String("task_id", id)},
		"update",
		id,
		"--set-metadata",
		task.MetadataPRURL+"="+prURL,
	)
	if err != nil {
		if isNotFoundResult(result) {
			return fmt.Errorf("set Beads task %q PR URL in %q: %w%s", id, b.dir, task.ErrNotFound, formattedOutput(result))
		}
		return err
	}
	return nil
}

// Close closes a Beads task. If the task is already closed, Close treats it as
// success so Orpheus finalization retries do not duplicate backend mutations.
func (b TaskBackend) Close(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("close Beads task in %q: task id is required", b.dir)
	}

	current, err := b.Get(ctx, id)
	if err != nil {
		return fmt.Errorf("close Beads task %q in %q: inspect task: %w", id, b.dir, err)
	}
	if current.Status == task.StatusClosed {
		return nil
	}

	result, err := b.runWriteWithAttrs(ctx, "close", []slog.Attr{slog.String("task_id", id)}, "close", id)
	if err != nil {
		if isNotFoundResult(result) {
			return fmt.Errorf("close Beads task %q in %q: %w%s", id, b.dir, task.ErrNotFound, formattedOutput(result))
		}
		return err
	}
	return nil
}

// Update updates a Beads task or epic with new content and relationships.
func (b TaskBackend) Update(ctx context.Context, opts task.UpdateOptions) (task.Task, error) {
	updateOpts, err := task.NormalizeUpdateOptions(opts)
	if err != nil {
		return task.Task{}, fmt.Errorf("update Beads task in %q: %w", b.dir, err)
	}

	id := updateOpts.ID
	rawCurrent, err := b.getRawTask(ctx, id)
	if err != nil {
		return task.Task{}, fmt.Errorf("update Beads task %q in %q: inspect task: %w", id, b.dir, err)
	}
	current, err := rawCurrent.toTask()
	if err != nil {
		return task.Task{}, fmt.Errorf("update Beads task %q in %q: parse current task: %w", id, b.dir, err)
	}
	if current.Status == task.StatusClosed {
		return task.Task{}, task.MutationConflictError{TaskID: id, Reason: "task is closed"}
	}
	if err := preflightBlockingDependencyAdditions(rawCurrent, updateOpts.AddBlockingIDs); err != nil {
		return task.Task{}, fmt.Errorf("update Beads task %q in %q: %w", id, b.dir, err)
	}

	if err := b.updateContentFields(ctx, id, updateOpts); err != nil {
		return task.Task{}, err
	}

	if err := b.updateParent(ctx, id, updateOpts); err != nil {
		return task.Task{}, err
	}

	if err := b.addBlockingDependencies(ctx, current, updateOpts.AddBlockingIDs); err != nil {
		return task.Task{}, err
	}

	// bd dep remove removes a relationship by pair without a type selector.
	// Limit it to blocking edges that were present when this update began so a
	// retry-safe absent removal cannot delete an unrelated relationship.
	removeIDs := existingBlockingDependencyIDs(current, updateOpts.RemoveBlockingIDs)
	if err := b.removeBlockingDependencies(ctx, id, removeIDs); err != nil {
		return task.Task{}, err
	}

	updated, err := b.Get(ctx, id)
	if err != nil {
		return task.Task{}, fmt.Errorf("update Beads task %q in %q: fetch updated task: %w", id, b.dir, err)
	}

	return updated, nil
}

func (b TaskBackend) updateContentFields(ctx context.Context, id string, opts task.UpdateOptions) error {
	// Skip content update when no content fields are specified
	hasContentFields := opts.Title != nil || opts.Description != nil || opts.Design != nil ||
		opts.AcceptanceCriteria != nil || opts.ExternalRef != nil
	if !hasContentFields {
		return nil
	}

	args := []string{"update", id}

	if opts.Title != nil {
		args = append(args, "--title", *opts.Title)
	}
	if opts.Description != nil {
		args = append(args, "--description", *opts.Description)
	}
	if opts.Design != nil {
		args = append(args, "--design", *opts.Design)
	}
	if opts.AcceptanceCriteria != nil {
		args = append(args, "--acceptance", *opts.AcceptanceCriteria)
	}
	if opts.ExternalRef != nil {
		args = append(args, "--external-ref", *opts.ExternalRef)
	}

	result, err := b.runWriteWithAttrs(
		ctx,
		"update content",
		[]slog.Attr{slog.String("task_id", id)},
		args...,
	)
	if err != nil {
		if isNotFoundResult(result) {
			return fmt.Errorf("update Beads task %q in %q: %w%s", id, b.dir, task.ErrNotFound, formattedOutput(result))
		}
		return err
	}
	return nil
}

func (b TaskBackend) updateParent(ctx context.Context, id string, opts task.UpdateOptions) error {
	if opts.ParentID == nil {
		return nil
	}
	parentID := strings.TrimSpace(*opts.ParentID)
	var args []string
	if parentID == "" {
		// Clear parent
		args = []string{"update", id, "--parent", ""}
	} else {
		args = []string{"update", id, "--parent", parentID}
	}

	result, err := b.runWriteWithAttrs(
		ctx,
		"update parent",
		[]slog.Attr{slog.String("task_id", id)},
		args...,
	)
	if err != nil {
		if isNotFoundResult(result) {
			return fmt.Errorf("update Beads task %q in %q: %w%s", id, b.dir, task.ErrNotFound, formattedOutput(result))
		}
		return err
	}
	return nil
}

// preflightBlockingDependencyAdditions rejects additions that Beads cannot
// perform because the item pair is already occupied by a different relation
// type. This must run before any write, since Beads has no atomic multi-field
// update operation and task.Task intentionally hides non-blocking relations.
func preflightBlockingDependencyAdditions(current bdTask, depIDs []string) error {
	for _, depID := range depIDs {
		for _, relation := range current.Dependencies {
			if relation.dependencyID() != depID || isBlockingDependencyType(relation.relationType()) {
				continue
			}
			return fmt.Errorf("cannot add blocking dependency %q: an existing relationship has non-blocking type %q", depID, relation.relationType())
		}
	}
	return nil
}

func (b TaskBackend) addBlockingDependencies(ctx context.Context, current task.Task, depIDs []string) error {
	for _, depID := range depIDs {
		dependency, err := b.Get(ctx, depID)
		if err != nil {
			return fmt.Errorf("add blocking dependency %q to task %q in %q: inspect dependency: %w", depID, current.ID, b.dir, err)
		}

		args := []string{"dep", "add", current.ID, depID}
		if current.IssueType != dependency.IssueType {
			// Beads rejects blocks edges between tasks and epics. Retain the
			// Orpheus blocking relationship with a dedicated non-conflicting
			// edge type, which is parsed alongside native blocks edges below.
			args = append(args, "--type", crossTypeBlockingDependencyType)
		}
		result, err := b.runWriteWithAttrs(
			ctx,
			"add blocking dependency",
			[]slog.Attr{slog.String("task_id", current.ID), slog.String("dependency", depID)},
			args...,
		)
		if err != nil {
			if isNotFoundResult(result) {
				return fmt.Errorf("add blocking dependency %q to task %q in %q: %w%s", depID, current.ID, b.dir, task.ErrNotFound, formattedOutput(result))
			}
			return err
		}
	}
	return nil
}

func existingBlockingDependencyIDs(current task.Task, requested []string) []string {
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

func (b TaskBackend) removeBlockingDependencies(ctx context.Context, id string, depIDs []string) error {
	for _, depID := range depIDs {
		result, err := b.runWriteWithAttrs(
			ctx,
			"remove blocking dependency",
			[]slog.Attr{slog.String("task_id", id), slog.String("dependency", depID)},
			"dep", "remove", id, depID,
		)
		if err != nil {
			if isNotFoundResult(result) {
				return fmt.Errorf("remove blocking dependency %q from task %q in %q: %w%s", depID, id, b.dir, task.ErrNotFound, formattedOutput(result))
			}
			// Check if error is because dependency doesn't exist (retry-safe no-op)
			if strings.Contains(strings.ToLower(result.Stderr), "no such dependency") ||
				strings.Contains(strings.ToLower(result.Stdout), "no such dependency") {
				// Dependency was already absent, treat as success
				continue
			}
			return err
		}
	}
	return nil
}

func validateStartEpicState(taskItem task.Task) error {
	if taskItem.IssueType != task.IssueTypeEpic {
		return task.MutationConflictError{TaskID: taskItem.ID, Reason: "item is not an epic"}
	}
	switch taskItem.Status {
	case task.StatusOpen, task.StatusInProgress:
		return nil
	case task.StatusClosed:
		return task.MutationConflictError{TaskID: taskItem.ID, Reason: "epic is closed"}
	default:
		return task.MutationConflictError{
			TaskID: taskItem.ID,
			Reason: fmt.Sprintf("status %s is not eligible to start", formatTaskStatus(taskItem.Status)),
		}
	}
}

func validateMarkInProgressState(taskItem task.Task, branch string, worktree string) error {
	metadata := taskItem.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return task.MutationConflictError{
			TaskID: taskItem.ID,
			Reason: fmt.Sprintf("%s is already set", task.MetadataPRURL),
		}
	}

	switch taskItem.Status {
	case task.StatusOpen:
		return nil
	case task.StatusInProgress:
		if markInProgressMetadataMatches(metadata, branch, worktree) {
			return nil
		}
		return task.MutationConflictError{
			TaskID: taskItem.ID,
			Reason: inProgressMetadataConflictReason(metadata, branch, worktree),
		}
	case task.StatusClosed:
		return task.MutationConflictError{TaskID: taskItem.ID, Reason: "task is closed"}
	default:
		return task.MutationConflictError{
			TaskID: taskItem.ID,
			Reason: fmt.Sprintf("status %s is not eligible for dispatch", formatTaskStatus(taskItem.Status)),
		}
	}
}

func markInProgressMetadataMatches(metadata task.OrpheusMetadata, branch string, worktree string) bool {
	return metadata.HasBranch && strings.TrimSpace(metadata.Branch) == branch &&
		metadata.HasWorktree && strings.TrimSpace(metadata.Worktree) == worktree
}

func inProgressMetadataConflictReason(metadata task.OrpheusMetadata, branch string, worktree string) string {
	problems := make([]string, 0, 2)
	if !metadata.HasBranch {
		problems = append(problems, task.MetadataBranch+" is missing")
	} else if strings.TrimSpace(metadata.Branch) != branch {
		problems = append(problems, fmt.Sprintf("%s is %q, expected %q", task.MetadataBranch, metadata.Branch, branch))
	}

	if !metadata.HasWorktree {
		problems = append(problems, task.MetadataWorktree+" is missing")
	} else if strings.TrimSpace(metadata.Worktree) != worktree {
		problems = append(problems, fmt.Sprintf("%s is %q, expected %q", task.MetadataWorktree, metadata.Worktree, worktree))
	}

	if len(problems) == 0 {
		return "in-progress task metadata does not match deterministic branch/worktree"
	}
	return "in-progress task metadata does not match deterministic branch/worktree: " + strings.Join(problems, "; ")
}

func formatTaskStatus(status task.Status) string {
	if strings.TrimSpace(string(status)) == "" {
		return "unknown"
	}
	return string(status)
}

func normalizeTaskBackendDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("create Beads task backend: directory is required")
	}
	return normalizePath(dir)
}

func (b TaskBackend) run(ctx context.Context, operation string, args ...string) (Result, error) {
	return b.runWithAttrs(ctx, operation, nil, args...)
}

func (b TaskBackend) runWithAttrs(ctx context.Context, operation string, attrs []slog.Attr, args ...string) (Result, error) {
	return b.runBD(ctx, operation, []string{"--json", "--readonly", "--sandbox"}, attrs, args...)
}

func (b TaskBackend) runWrite(ctx context.Context, operation string, args ...string) (Result, error) {
	return b.runWriteWithAttrs(ctx, operation, nil, args...)
}

func (b TaskBackend) runWriteWithAttrs(ctx context.Context, operation string, attrs []slog.Attr, args ...string) (Result, error) {
	return b.runBD(ctx, operation, []string{"--json", "--sandbox"}, attrs, args...)
}

func (b TaskBackend) runBD(ctx context.Context, operation string, globalArgs []string, attrs []slog.Attr, args ...string) (Result, error) {
	if b.runner == nil {
		return Result{}, fmt.Errorf("%s Beads tasks in %q: runner is required", operation, b.dir)
	}
	if strings.TrimSpace(b.dir) == "" {
		return Result{}, fmt.Errorf("%s Beads tasks: directory is required", operation)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("%s Beads tasks in %q: %w", operation, b.dir, err)
	}

	allArgs := append(append([]string{}, globalArgs...), args...)
	runner := b.runner
	if diagnostics, ok := runner.(diagnosticRunner); ok && len(attrs) > 0 {
		runner = diagnostics.WithDiagnosticAttrs(attrs...)
	}
	result, err := runner.Run(b.dir, allArgs...)
	if err == nil {
		return result, nil
	}

	originalErr := b.commandError(operation, allArgs, result, err)
	if !b.maintenanceOwned || !isReadOnlyCommand(globalArgs) || !isBehindSchemaReadOnlyError(result) {
		return result, originalErr
	}

	b.logSchemaRecovery(ctx, "detected", operation)
	_, migrationErr := b.runSchemaMigration(ctx)
	if migrationErr != nil {
		b.logSchemaRecovery(ctx, "migration_failed", operation)
		return result, fmt.Errorf(
			"%s Beads tasks in %q: schema recovery failed after original read-only operation: %w; run bd migrate schema: %w",
			operation, b.dir, originalErr, migrationErr,
		)
	}

	b.logSchemaRecovery(ctx, "retrying", operation)
	result, err = runner.Run(b.dir, allArgs...)
	if err != nil {
		b.logSchemaRecovery(ctx, "retry_failed", operation)
		return result, fmt.Errorf(
			"%s Beads tasks in %q: schema recovery completed but retry of original operation failed: %w",
			operation, b.dir, b.commandError(operation, allArgs, result, err),
		)
	}
	b.logSchemaRecovery(ctx, "recovered", operation)
	return result, nil
}

func (b TaskBackend) runSchemaMigration(ctx context.Context) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	result, err := b.runner.Run(b.dir, "--json", "--sandbox", "migrate", "schema")
	if err != nil {
		return result, b.commandError("migrate schema", []string{"--json", "--sandbox", "migrate", "schema"}, result, err)
	}
	return result, nil
}

func (b TaskBackend) commandError(operation string, args []string, result Result, err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("%s Beads tasks in %q: bd executable not found; install Beads or ensure bd is on PATH: %w", operation, b.dir, err)
	}
	return fmt.Errorf("%s Beads tasks in %q: run bd %s: %w%s", operation, b.dir, strings.Join(args, " "), err, formattedOutput(result))
}

func isReadOnlyCommand(globalArgs []string) bool {
	for _, arg := range globalArgs {
		if arg == "--readonly" {
			return true
		}
	}
	return false
}

func isBehindSchemaReadOnlyError(result Result) bool {
	message := strings.ToLower(strings.Join([]string{result.Stdout, result.Stderr}, "\n"))
	return strings.Contains(message, "schema version mismatch:") &&
		strings.Contains(message, "database is at v") &&
		strings.Contains(message, "binary expects v") &&
		strings.Contains(message, "read-only open cannot migrate")
}

func (b TaskBackend) logSchemaRecovery(ctx context.Context, event string, operation string) {
	if b.logger == nil || !b.logger.Enabled(ctx, slog.LevelDebug) {
		return
	}
	b.logger.DebugContext(ctx, "Beads task schema recovery",
		slog.String("component", "beads"),
		slog.String("operation", "schema_recovery"),
		slog.String("recovery_event", event),
		slog.String("original_operation", operation),
		slog.String("cwd", b.dir),
	)
}

type bdTask struct {
	ID                 string          `json:"id"`
	Title              string          `json:"title"`
	ExternalRef        string          `json:"external_ref"`
	Description        string          `json:"description"`
	Design             string          `json:"design"`
	AcceptanceCriteria string          `json:"acceptance_criteria"`
	Status             task.Status     `json:"status"`
	Priority           int             `json:"priority"`
	IssueType          task.IssueType  `json:"issue_type"`
	Labels             []string        `json:"labels"`
	Metadata           json.RawMessage `json:"metadata"`
	Assignee           string          `json:"assignee"`
	Owner              string          `json:"owner"`
	CreatedBy          string          `json:"created_by"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	StartedAt          string          `json:"started_at"`
	CompletedAt        string          `json:"completed_at"`
	ClosedAt           string          `json:"closed_at"`
	Parent             string          `json:"parent"`
	Dependencies       []bdRelation    `json:"dependencies"`
	Dependents         []bdRelation    `json:"dependents"`
	DependencyCount    int             `json:"dependency_count"`
	DependentCount     int             `json:"dependent_count"`
	BlockedByCount     int             `json:"blocked_by_count"`
	BlockingCount      int             `json:"blocking_count"`
	ChildCount         int             `json:"child_count"`
}

type bdRelation struct {
	ID          string `json:"id"`
	IssueID     string `json:"issue_id"`
	DependsOnID string `json:"depends_on_id"`
	Type        string `json:"type"`
	DepType     string `json:"dependency_type"`
}

type bdErrorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func parseRawTaskArray(output string) ([]bdTask, error) {
	var rawTasks []bdTask
	if err := json.Unmarshal([]byte(output), &rawTasks); err != nil {
		return nil, err
	}
	return rawTasks, nil
}

func parseTaskArray(output string) ([]task.Task, error) {
	rawTasks, err := parseRawTaskArray(output)
	if err != nil {
		return nil, err
	}

	tasks := make([]task.Task, 0, len(rawTasks))
	for _, rawTask := range rawTasks {
		taskItem, err := rawTask.toTask()
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, taskItem)
	}
	return tasks, nil
}

func parseCreatedTask(output string) (task.Task, error) {
	var rawTask bdTask
	if err := json.Unmarshal([]byte(output), &rawTask); err == nil && strings.TrimSpace(rawTask.ID) != "" {
		return rawTask.toTask()
	}

	tasks, err := parseTaskArray(output)
	if err != nil {
		return task.Task{}, err
	}
	if len(tasks) != 1 {
		return task.Task{}, fmt.Errorf("expected exactly one created task, got %d", len(tasks))
	}
	return tasks[0], nil
}

func (t bdTask) toTask() (task.Task, error) {
	createdAt, err := parseOptionalTime(t.ID, "created_at", t.CreatedAt)
	if err != nil {
		return task.Task{}, err
	}
	updatedAt, err := parseOptionalTime(t.ID, "updated_at", t.UpdatedAt)
	if err != nil {
		return task.Task{}, err
	}
	startedAt, err := parseOptionalTime(t.ID, "started_at", t.StartedAt)
	if err != nil {
		return task.Task{}, err
	}
	completedAt, err := parseOptionalTime(t.ID, "completed_at", t.CompletedAt)
	if err != nil {
		return task.Task{}, err
	}
	closedAt, err := parseOptionalTime(t.ID, "closed_at", t.ClosedAt)
	if err != nil {
		return task.Task{}, err
	}

	metadata, err := parseMetadata(t.ID, t.Metadata)
	if err != nil {
		return task.Task{}, err
	}

	labels := t.Labels
	if labels == nil {
		labels = []string{}
	}

	return task.Task{
		ID:                 t.ID,
		Title:              t.Title,
		ExternalRef:        t.ExternalRef,
		Description:        t.Description,
		Design:             t.Design,
		AcceptanceCriteria: t.AcceptanceCriteria,
		Status:             t.Status,
		Priority:           t.Priority,
		IssueType:          t.IssueType,
		Labels:             labels,
		Metadata:           metadata,
		Assignee:           t.Assignee,
		Owner:              t.Owner,
		CreatedBy:          t.CreatedBy,
		CreatedAt:          createdAt,
		UpdatedAt:          updatedAt,
		StartedAt:          startedAt,
		CompletedAt:        completedAt,
		ClosedAt:           closedAt,
		Relations:          t.relations(),
	}, nil
}

func (t bdTask) relations() task.RelationSummary {
	relations := task.RelationSummary{
		ParentID:        strings.TrimSpace(t.Parent),
		DependencyIDs:   []string{},
		DependentIDs:    []string{},
		DependencyCount: t.DependencyCount,
		DependentCount:  t.DependentCount,
		BlockedByCount:  t.BlockedByCount,
		BlockingCount:   t.BlockingCount,
		ChildCount:      t.ChildCount,
	}

	for _, dependency := range t.Dependencies {
		relationType := dependency.relationType()
		if relationType == "parent-child" && relations.ParentID == "" {
			relations.ParentID = dependency.dependencyID()
			continue
		}
		if isBlockingDependencyType(relationType) {
			relations.DependencyIDs = appendID(relations.DependencyIDs, dependency.dependencyID())
		}
	}

	for _, dependent := range t.Dependents {
		relations.DependentIDs = appendID(relations.DependentIDs, dependent.dependentID())
	}

	if relations.DependencyCount == 0 {
		relations.DependencyCount = len(relations.DependencyIDs)
	}
	if relations.DependentCount == 0 {
		relations.DependentCount = len(relations.DependentIDs)
	}
	return relations
}

func isBlockingDependencyType(relationType string) bool {
	return relationType == "blocks" || relationType == crossTypeBlockingDependencyType
}

func (r bdRelation) relationType() string {
	if r.Type != "" {
		return r.Type
	}
	return r.DepType
}

func (r bdRelation) dependencyID() string {
	if r.DependsOnID != "" {
		return r.DependsOnID
	}
	if r.ID != "" {
		return r.ID
	}
	return r.IssueID
}

func (r bdRelation) dependentID() string {
	if r.ID != "" {
		return r.ID
	}
	if r.IssueID != "" {
		return r.IssueID
	}
	return r.DependsOnID
}

func appendID(ids []string, id string) []string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ids
	}
	return append(ids, id)
}

func parseOptionalTime(taskID string, field string, value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}

	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, fmt.Errorf("parse Beads task %q %s %q: %w", taskID, field, value, err)
	}
	return &parsed, nil
}

func parseMetadata(taskID string, raw json.RawMessage) (task.Metadata, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}

	values, err := metadataObject(raw)
	if err != nil {
		return nil, fmt.Errorf("parse Beads task %q metadata: %w", taskID, err)
	}
	if len(values) == 0 {
		return task.Metadata{}, nil
	}

	metadata := make(task.Metadata, len(values))
	for key, value := range values {
		metadata[key] = metadataValueToString(value)
	}
	return metadata, nil
}

func metadataObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err == nil {
		return values, nil
	}

	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, err
	}

	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return map[string]json.RawMessage{}, nil
	}
	if err := json.Unmarshal([]byte(encoded), &values); err != nil {
		return nil, err
	}
	return values, nil
}

func metadataValueToString(raw json.RawMessage) string {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text
	}

	var buffer bytes.Buffer
	if err := json.Compact(&buffer, raw); err == nil {
		return buffer.String()
	}
	return string(raw)
}

func isNotFoundResult(result Result) bool {
	response := parseErrorResponse(result.Stdout)
	message := strings.ToLower(strings.Join([]string{response.Error, response.Message, result.Stdout, result.Stderr}, " "))
	return strings.Contains(message, "no issue") ||
		strings.Contains(message, "no matching") ||
		strings.Contains(message, "not found")
}

func parseErrorResponse(output string) bdErrorResponse {
	var response bdErrorResponse
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		return bdErrorResponse{}
	}
	return response
}
