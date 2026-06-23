// Package readiness contains backend-neutral task readiness policies.
package readiness

import (
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/task"
)

// ParentEpicGateState reports whether a task's immediate parent permits work.
type ParentEpicGateState string

const (
	// ParentEpicGateAllowed means the task has no parent or its immediate parent
	// is an in-progress epic.
	ParentEpicGateAllowed ParentEpicGateState = "allowed"

	// ParentEpicGateBlocked means the immediate parent is an open epic that has
	// not yet entered progress.
	ParentEpicGateBlocked ParentEpicGateState = "blocked"

	// ParentEpicGateAttention means the immediate parent relation cannot be used
	// as a valid active epic gate.
	ParentEpicGateAttention ParentEpicGateState = "needs_attention"
)

// ParentEpicGate is the result of evaluating a task's immediate parent against
// the repository snapshot that contains the task.
type ParentEpicGate struct {
	State    ParentEpicGateState
	ParentID string
	Parent   *task.Task
}

// EvaluateParentEpicGate evaluates only child.Relations.ParentID. A task with
// no parent is allowed. A parent qualifies only when it resolves in tasks as an
// epic whose status is in_progress; ancestor state is intentionally ignored.
func EvaluateParentEpicGate(child task.Task, tasks []task.Task) ParentEpicGate {
	index := make(map[string]task.Task, len(tasks))
	for _, candidate := range tasks {
		id := strings.TrimSpace(candidate.ID)
		if id != "" {
			index[id] = candidate
		}
	}
	return EvaluateParentEpicGateFromIndex(child, index)
}

// EvaluateParentEpicGateFromIndex evaluates the parent gate using an index of
// tasks from one repository snapshot.
func EvaluateParentEpicGateFromIndex(child task.Task, index map[string]task.Task) ParentEpicGate {
	parentID := strings.TrimSpace(child.Relations.ParentID)
	if parentID == "" {
		return ParentEpicGate{State: ParentEpicGateAllowed}
	}

	candidate, ok := index[parentID]
	if !ok {
		return ParentEpicGate{
			State:    ParentEpicGateAttention,
			ParentID: parentID,
		}
	}
	parent := candidate.Clone()
	gate := ParentEpicGate{ParentID: parentID, Parent: &parent}
	if parent.IssueType != task.IssueTypeEpic {
		gate.State = ParentEpicGateAttention
		return gate
	}
	switch parent.Status {
	case task.StatusInProgress:
		gate.State = ParentEpicGateAllowed
	case task.StatusOpen:
		gate.State = ParentEpicGateBlocked
	default:
		gate.State = ParentEpicGateAttention
	}
	return gate
}

// Detail returns an operator-facing explanation of a failed parent epic gate.
// It is empty when the gate allows work.
func (g ParentEpicGate) Detail() string {
	if g.State == ParentEpicGateAllowed {
		return ""
	}
	if g.Parent == nil {
		return fmt.Sprintf(
			"immediate parent epic %s is missing from this repository snapshot; immediate parent epic must be in_progress",
			g.ParentID,
		)
	}
	if g.Parent.IssueType != task.IssueTypeEpic {
		return fmt.Sprintf(
			"immediate parent %s has issue_type=%s; immediate parent epic must be in_progress",
			g.ParentID,
			formatIssueType(g.Parent.IssueType),
		)
	}
	return fmt.Sprintf(
		"immediate parent epic %s is %s; immediate parent epic must be in_progress",
		g.ParentID,
		formatStatus(g.Parent.Status),
	)
}

func formatIssueType(issueType task.IssueType) string {
	value := strings.TrimSpace(string(issueType))
	if value == "" {
		return "unknown"
	}
	return value
}

func formatStatus(status task.Status) string {
	value := strings.TrimSpace(string(status))
	if value == "" {
		return "unknown"
	}
	return value
}
