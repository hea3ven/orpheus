package readiness_test

import (
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/readiness"
	"github.com/hea3ven/orpheus/internal/task"
)

func TestEvaluateParentEpicGate(t *testing.T) {
	child := task.Task{ID: "child", Relations: task.RelationSummary{ParentID: "parent"}}

	for _, tt := range parentEpicGateCases() {
		t.Run(tt.name, func(t *testing.T) {
			tasks := []task.Task{}
			if tt.parent != nil {
				tasks = append(tasks, *tt.parent)
			}
			got := readiness.EvaluateParentEpicGate(child, tasks)
			if got.State != tt.wantState {
				t.Fatalf("gate state = %q, want %q", got.State, tt.wantState)
			}
			if tt.wantDetail != "" && !strings.Contains(got.Detail(), tt.wantDetail) {
				t.Fatalf("detail = %q, want it to contain %q", got.Detail(), tt.wantDetail)
			}
		})
	}
}

type parentEpicGateCase struct {
	name       string
	parent     *task.Task
	wantState  readiness.ParentEpicGateState
	wantDetail string
}

func parentEpicGateCases() []parentEpicGateCase {
	return []parentEpicGateCase{
		{
			name:       "missing parent",
			wantState:  readiness.ParentEpicGateAttention,
			wantDetail: "missing from this repository snapshot",
		},
		{
			name:       "non epic parent",
			parent:     &task.Task{ID: "parent", IssueType: task.IssueTypeTask, Status: task.StatusInProgress},
			wantState:  readiness.ParentEpicGateAttention,
			wantDetail: "issue_type=task",
		},
		{
			name:       "open epic",
			parent:     &task.Task{ID: "parent", IssueType: task.IssueTypeEpic, Status: task.StatusOpen},
			wantState:  readiness.ParentEpicGateBlocked,
			wantDetail: "is open",
		},
		{
			name:       "closed epic",
			parent:     &task.Task{ID: "parent", IssueType: task.IssueTypeEpic, Status: task.StatusClosed},
			wantState:  readiness.ParentEpicGateAttention,
			wantDetail: "is closed",
		},
		{
			name:       "unknown epic status",
			parent:     &task.Task{ID: "parent", IssueType: task.IssueTypeEpic, Status: task.Status("paused")},
			wantState:  readiness.ParentEpicGateAttention,
			wantDetail: "is paused",
		},
		{
			name:      "active epic",
			parent:    &task.Task{ID: "parent", IssueType: task.IssueTypeEpic, Status: task.StatusInProgress},
			wantState: readiness.ParentEpicGateAllowed,
		},
	}
}

func TestEvaluateParentEpicGateAllowsStandaloneItems(t *testing.T) {
	got := readiness.EvaluateParentEpicGate(task.Task{ID: "standalone"}, nil)
	if got.State != readiness.ParentEpicGateAllowed {
		t.Fatalf("gate state = %q, want %q", got.State, readiness.ParentEpicGateAllowed)
	}
}
