package workflow

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hea3ven/orpheus/internal/task"
)

// ErrNotEpic indicates an item cannot use an epic lifecycle command.
var ErrNotEpic = errors.New("item is not an epic")

// EpicLifecycleBackend supplies the narrow capabilities required to activate
// and close epics while retaining the task source as lifecycle authority.
type EpicLifecycleBackend interface {
	task.Getter
	task.Lister
	task.EpicStartMutator
	task.CloseMutator
}

// EpicLifecycleResult reports whether a lifecycle command changed source state.
type EpicLifecycleResult struct {
	Changed bool
}

// EpicLifecycleService applies source-neutral epic lifecycle policy.
type EpicLifecycleService struct{}

// Start changes an open epic to in progress after verifying its immediate
// parent and all blocking dependencies. An in-progress epic is a no-op.
func (EpicLifecycleService) Start(ctx context.Context, backend EpicLifecycleBackend, id string) (EpicLifecycleResult, error) {
	if backend == nil {
		return EpicLifecycleResult{}, errors.New("epic lifecycle backend is required")
	}
	item, err := getEpicLifecycleItem(ctx, backend, id)
	if err != nil {
		return EpicLifecycleResult{}, err
	}
	if err := requireEpic(item); err != nil {
		return EpicLifecycleResult{}, err
	}
	switch item.Status {
	case task.StatusInProgress:
		return EpicLifecycleResult{}, nil
	case task.StatusClosed:
		return EpicLifecycleResult{}, fmt.Errorf("epic %s is closed and cannot be started", item.ID)
	case task.StatusOpen:
		// Eligible to continue validation.
	default:
		return EpicLifecycleResult{}, fmt.Errorf("epic %s can only be started when open; current status is %s", item.ID, lifecycleStatus(item.Status))
	}
	if err := verifyEpicParent(ctx, backend, item); err != nil {
		return EpicLifecycleResult{}, err
	}
	if err := verifyBlockingDependencies(ctx, backend, item); err != nil {
		return EpicLifecycleResult{}, err
	}
	if err := backend.StartEpic(ctx, item.ID); err != nil {
		return EpicLifecycleResult{}, lifecycleFailure{
			message: fmt.Sprintf("cannot start epic %s", item.ID),
			cause:   err,
		}
	}
	return EpicLifecycleResult{Changed: true}, nil
}

// Close changes an in-progress epic to closed only after verifying every direct
// child is visible and closed. A closed epic is a no-op.
func (EpicLifecycleService) Close(ctx context.Context, backend EpicLifecycleBackend, id string) (EpicLifecycleResult, error) {
	if backend == nil {
		return EpicLifecycleResult{}, errors.New("epic lifecycle backend is required")
	}
	item, err := getEpicLifecycleItem(ctx, backend, id)
	if err != nil {
		return EpicLifecycleResult{}, err
	}
	if err := requireEpic(item); err != nil {
		return EpicLifecycleResult{}, err
	}
	switch item.Status {
	case task.StatusClosed:
		return EpicLifecycleResult{}, nil
	case task.StatusInProgress:
		// Eligible to inspect children.
	default:
		return EpicLifecycleResult{}, fmt.Errorf("epic %s can only be closed when in progress; current status is %s", item.ID, lifecycleStatus(item.Status))
	}

	tasks, err := backend.List(ctx)
	if err != nil {
		return EpicLifecycleResult{}, lifecycleFailure{
			message: fmt.Sprintf("cannot inspect direct children of epic %s", item.ID),
			cause:   err,
		}
	}
	children, err := directEpicChildren(item.ID, tasks)
	if err != nil {
		return EpicLifecycleResult{}, err
	}
	if item.Relations.ChildCount > len(children) {
		return EpicLifecycleResult{}, fmt.Errorf(
			"cannot verify direct children of epic %s: source reports %d child items but only %d could be inspected",
			item.ID,
			item.Relations.ChildCount,
			len(children),
		)
	}
	if active := activeChildIDs(children); len(active) > 0 {
		return EpicLifecycleResult{}, fmt.Errorf("epic %s cannot be closed; direct child items are still active: %s", item.ID, strings.Join(active, ", "))
	}
	if err := backend.Close(ctx, item.ID); err != nil {
		return EpicLifecycleResult{}, lifecycleFailure{
			message: fmt.Sprintf("cannot close epic %s", item.ID),
			cause:   err,
		}
	}
	return EpicLifecycleResult{Changed: true}, nil
}

func getEpicLifecycleItem(ctx context.Context, backend task.Getter, id string) (task.Task, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return task.Task{}, errors.New("item id is required")
	}
	item, err := backend.Get(ctx, id)
	if err != nil {
		message := fmt.Sprintf("cannot inspect epic %s", id)
		if errors.Is(err, task.ErrNotFound) {
			message = fmt.Sprintf("epic %s was not found", id)
		}
		return task.Task{}, lifecycleFailure{message: message, cause: err}
	}
	return item, nil
}

func requireEpic(item task.Task) error {
	if item.IssueType == task.IssueTypeEpic {
		return nil
	}
	return fmt.Errorf("%w: item %s", ErrNotEpic, item.ID)
}

func verifyEpicParent(ctx context.Context, backend task.Getter, item task.Task) error {
	parentID := strings.TrimSpace(item.Relations.ParentID)
	if parentID == "" {
		return nil
	}
	parent, err := backend.Get(ctx, parentID)
	if err != nil {
		return lifecycleFailure{
			message: fmt.Sprintf("cannot inspect parent epic %s", parentID),
			cause:   err,
		}
	}
	if parent.IssueType != task.IssueTypeEpic {
		return fmt.Errorf("parent item %s is not an epic", parentID)
	}
	if parent.Status != task.StatusInProgress {
		return fmt.Errorf("parent epic %s must be in progress before starting epic %s; current status is %s", parentID, item.ID, lifecycleStatus(parent.Status))
	}
	return nil
}

func verifyBlockingDependencies(ctx context.Context, backend task.Getter, item task.Task) error {
	dependencyIDs := uniqueSortedIDs(item.Relations.DependencyIDs)
	if item.Relations.BlockedByCount > len(dependencyIDs) {
		return fmt.Errorf(
			"cannot verify all blocking dependencies of epic %s: source reports %d blockers but only %d identifiers could be inspected",
			item.ID,
			item.Relations.BlockedByCount,
			len(dependencyIDs),
		)
	}

	active := make([]string, 0)
	for _, dependencyID := range dependencyIDs {
		dependency, err := backend.Get(ctx, dependencyID)
		if err != nil {
			return lifecycleFailure{
				message: fmt.Sprintf("cannot inspect blocking dependency %s", dependencyID),
				cause:   err,
			}
		}
		if dependency.Status != task.StatusClosed {
			active = append(active, dependencyID)
		}
	}
	if len(active) > 0 {
		return fmt.Errorf("epic %s cannot be started; blocking dependencies are not closed: %s", item.ID, strings.Join(active, ", "))
	}
	return nil
}

func directEpicChildren(epicID string, tasks []task.Task) ([]task.Task, error) {
	children := make([]task.Task, 0)
	for _, candidate := range tasks {
		if strings.TrimSpace(candidate.Relations.ParentID) != epicID {
			continue
		}
		if strings.TrimSpace(candidate.ID) == "" {
			return nil, fmt.Errorf("cannot verify direct children of epic %s: a child item has no identifier", epicID)
		}
		children = append(children, candidate)
	}
	return children, nil
}

func activeChildIDs(children []task.Task) []string {
	ids := make([]string, 0)
	for _, child := range children {
		if child.Status != task.StatusClosed {
			ids = append(ids, strings.TrimSpace(child.ID))
		}
	}
	return uniqueSortedIDs(ids)
}

func uniqueSortedIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id := strings.TrimSpace(value)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// lifecycleFailure retains a concrete task-source error for diagnostics while
// presenting a source-neutral message to lifecycle command callers.
type lifecycleFailure struct {
	message string
	cause   error
}

func (e lifecycleFailure) Error() string { return e.message }

func (e lifecycleFailure) Unwrap() error { return e.cause }

func lifecycleStatus(status task.Status) string {
	if normalized := strings.TrimSpace(string(status)); normalized != "" {
		return normalized
	}
	return "unknown"
}
