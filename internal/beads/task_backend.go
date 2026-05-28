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

var _ task.ReadBackend = TaskBackend{}

// TaskBackend reads task items from one explicit Beads workspace.
//
// List and Ready return active task items. Get returns the backend item so
// callers can report closed or non-task items as out of scope when needed.
// Use NewTaskBackend or NewTaskBackendWithRunner to construct a valid value.
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

// List lists active Beads items with issue_type=task.
func (b TaskBackend) List(ctx context.Context) ([]task.Task, error) {
	result, err := b.run(ctx, "list", "list", "--type", string(task.IssueTypeTask), "--limit", "0")
	if err != nil {
		return nil, err
	}

	tasks, err := parseTaskArray(result.Stdout)
	if err != nil {
		return nil, fmt.Errorf("list Beads tasks in %q: parse bd list JSON: %w%s", b.dir, err, formattedOutput(result))
	}
	return activeTasks(tasks), nil
}

// Ready lists active Beads task items that Beads considers ready.
func (b TaskBackend) Ready(ctx context.Context) ([]task.Task, error) {
	result, err := b.run(ctx, "list ready", "ready", "--type", string(task.IssueTypeTask), "--limit", "0")
	if err != nil {
		return nil, err
	}

	tasks, err := parseTaskArray(result.Stdout)
	if err != nil {
		return nil, fmt.Errorf("list ready Beads tasks in %q: parse bd ready JSON: %w%s", b.dir, err, formattedOutput(result))
	}
	return activeTasks(tasks), nil
}

func normalizeTaskBackendDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("create Beads task backend: directory is required")
	}
	return normalizePath(dir)
}

func (b TaskBackend) run(ctx context.Context, operation string, args ...string) (Result, error) {
	if b.runner == nil {
		return Result{}, fmt.Errorf("%s Beads tasks in %q: runner is required", operation, b.dir)
	}
	if strings.TrimSpace(b.dir) == "" {
		return Result{}, fmt.Errorf("%s Beads tasks: directory is required", operation)
	}
	if err := ctx.Err(); err != nil {
		return Result{}, fmt.Errorf("%s Beads tasks in %q: %w", operation, b.dir, err)
	}

	allArgs := append([]string{"--json", "--readonly", "--sandbox"}, args...)
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

func activeTasks(tasks []task.Task) []task.Task {
	active := make([]task.Task, 0, len(tasks))
	for _, taskItem := range tasks {
		if isActiveTask(taskItem) {
			active = append(active, taskItem)
		}
	}
	return active
}

func isActiveTask(taskItem task.Task) bool {
	return taskItem.IssueType == task.IssueTypeTask && taskItem.Status != task.StatusClosed
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
