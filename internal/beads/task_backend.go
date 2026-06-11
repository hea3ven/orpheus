package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/task"
)

var (
	_ task.ReadBackend     = TaskBackend{}
	_ task.DispatchMutator = TaskBackend{}
	_ task.PRURLMutator    = TaskBackend{}
	_ task.CloseMutator    = TaskBackend{}
)

// TaskBackend reads task items from one explicit Beads workspace.
//
// List returns visible backend items. Get returns the backend item so callers can
// report closed items as out of scope when needed. Use NewTaskBackend or
// NewTaskBackendWithRunner to construct a valid value.
type TaskBackend struct {
	dir    string
	runner Runner
}

// NewTaskBackend returns a Beads-backed task reader using the bd binary.
func NewTaskBackend(dir string) (TaskBackend, error) {
	return NewTaskBackendWithRunner(dir, CommandRunner{})
}

// NewTaskBackendWithRunner returns a Beads-backed task reader using runner.
func NewTaskBackendWithRunner(dir string, runner Runner) (TaskBackend, error) {
	if runner == nil {
		return TaskBackend{}, errors.New("create Beads task backend: runner is required")
	}

	normalizedDir, err := normalizeTaskBackendDir(dir)
	if err != nil {
		return TaskBackend{}, err
	}

	return TaskBackend{dir: normalizedDir, runner: runner}, nil
}

// Get fetches one Beads item by id.
func (b TaskBackend) Get(ctx context.Context, id string) (task.Task, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return task.Task{}, fmt.Errorf("get Beads task in %q: task id is required", b.dir)
	}

	result, err := b.run(ctx, "get", "show", "--id", id)
	if err != nil {
		if isNotFoundResult(result) {
			return task.Task{}, fmt.Errorf("get Beads task %q in %q: %w%s", id, b.dir, task.ErrNotFound, formattedOutput(result))
		}
		return task.Task{}, err
	}

	tasks, err := parseTaskArray(result.Stdout)
	if err != nil {
		return task.Task{}, fmt.Errorf("get Beads task %q in %q: parse bd show JSON: %w%s", id, b.dir, err, formattedOutput(result))
	}
	if len(tasks) == 0 {
		return task.Task{}, fmt.Errorf("get Beads task %q in %q: %w", id, b.dir, task.ErrNotFound)
	}

	for _, taskItem := range tasks {
		if taskItem.ID != id {
			continue
		}
		return taskItem, nil
	}

	return task.Task{}, fmt.Errorf("get Beads task %q in %q: %w", id, b.dir, task.ErrNotFound)
}

// List lists visible Beads items, including closed items and non-task issue types.
func (b TaskBackend) List(ctx context.Context) ([]task.Task, error) {
	result, err := b.run(ctx, "list", "list", "--all", "--limit", "0")
	if err != nil {
		return nil, err
	}

	tasks, err := parseTaskArray(result.Stdout)
	if err != nil {
		return nil, fmt.Errorf("list Beads tasks in %q: parse bd list JSON: %w%s", b.dir, err, formattedOutput(result))
	}
	return tasks, nil
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

	result, err := b.runWrite(
		ctx,
		"mark in-progress",
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

	result, err := b.runWrite(
		ctx,
		"set PR URL",
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

	result, err := b.runWrite(ctx, "close", "close", id)
	if err != nil {
		if isNotFoundResult(result) {
			return fmt.Errorf("close Beads task %q in %q: %w%s", id, b.dir, task.ErrNotFound, formattedOutput(result))
		}
		return err
	}
	return nil
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
	return b.runBD(ctx, operation, []string{"--json", "--readonly", "--sandbox"}, args...)
}

func (b TaskBackend) runWrite(ctx context.Context, operation string, args ...string) (Result, error) {
	return b.runBD(ctx, operation, []string{"--json", "--sandbox"}, args...)
}

func (b TaskBackend) runBD(ctx context.Context, operation string, globalArgs []string, args ...string) (Result, error) {
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
	result, err := b.runner.Run(b.dir, allArgs...)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return result, fmt.Errorf("%s Beads tasks in %q: bd executable not found; install Beads or ensure bd is on PATH: %w", operation, b.dir, err)
		}
		return result, fmt.Errorf("%s Beads tasks in %q: run bd %s: %w%s", operation, b.dir, strings.Join(allArgs, " "), err, formattedOutput(result))
	}
	return result, nil
}

type bdTask struct {
	ID                 string          `json:"id"`
	Title              string          `json:"title"`
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

func parseTaskArray(output string) ([]task.Task, error) {
	var rawTasks []bdTask
	if err := json.Unmarshal([]byte(output), &rawTasks); err != nil {
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
		if relationType == "blocks" {
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
