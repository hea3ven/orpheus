//nolint:testpackage // Keeps route assertions concise beside the state-routing API.
package workflow

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestSelectTaskRunRoute(t *testing.T) {
	completedRun := taskstate.RunAttempt{
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary: "done",
		},
	}
	blocker := taskstate.ReviewFinding{Type: taskstate.FindingTypeBlocking, Title: "fix", Description: "fix it"}

	tests := []struct {
		name    string
		task    task.Task
		state   taskstate.TaskState
		want    TaskRunAction
		wantErr string
	}{
		{name: "initial implementation", task: task.Task{ID: "op-1"}, want: TaskRunActionStartImplementation},
		{name: "failed implementation replacement", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{{Attempt: 1, Status: taskstate.RunStatusFailed}}}, want: TaskRunActionStartImplementation},
		{name: "incomplete implementation replacement", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{{Attempt: 1, Status: taskstate.RunStatusSucceeded}}}, want: TaskRunActionStartImplementation},
		{name: "active implementation", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{{Attempt: 1, Status: taskstate.RunStatusRunning}}}, want: TaskRunActionImplementationActive},
		{name: "completed implementation starts review", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{completedRun}}, want: TaskRunActionStartReview},
		{name: "manual review resumes", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{completedRun}, Reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusWaitingForManual, Step: "manual"}}}, want: TaskRunActionResumeReview},
		{name: "kept blocker dispatches repair", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{completedRun}, Reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusBlocked, Findings: []taskstate.ReviewFinding{blocker}}}}, want: TaskRunActionTargetedRepair},
		{name: "unkept automated blocker restarts review", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{completedRun}, Reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusBlocked, Steps: []taskstate.ReviewStep{{Kind: taskstate.ReviewStepKindCheck, Name: "check"}}, Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Title: "fix", Description: "fix it", Step: "check"}}}}}, want: TaskRunActionStartReview},
		{name: "completed targeted repair starts fresh review", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{completedRun, {Attempt: 2, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{Summary: "fixed"}, ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}}}}, Reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusBlocked, Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Title: "fix", Description: "fix it", TargetedByRunAttempt: 2}}}}}, want: TaskRunActionStartReview},
		{name: "incomplete targeted repair starts replacement", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{completedRun, {Attempt: 2, Status: taskstate.RunStatusSucceeded, ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}}}}, Reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusBlocked, Findings: []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Title: "fix", Description: "fix it", TargetedByRunAttempt: 2}}}}}, want: TaskRunActionTargetedRepair},
		{name: "aborted review restarts", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{completedRun}, Reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusAborted}}}, want: TaskRunActionStartReview},
		{name: "failed review restarts", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{completedRun}, Reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusFailed}}}, want: TaskRunActionStartReview},
		{name: "review running is reported", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{completedRun}, Reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusRunning}}}, want: TaskRunActionReviewActive},
		{name: "passed review retries finalization", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{completedRun}, Reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusPassed}}}, want: TaskRunActionRetryFinalization},
		{name: "passed review retries finalization despite stale completed run", task: task.Task{ID: "op-1"}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{{Attempt: 1, Status: taskstate.RunStatusRunning, Completion: &taskstate.Completion{Summary: "done"}}}, Reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusPassed}}}, want: TaskRunActionRetryFinalization},
		{name: "open pr is reported", task: task.Task{ID: "op-1", Metadata: task.Metadata{task.MetadataPRURL: "https://example.test/pr/1"}}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{completedRun}}, want: TaskRunActionOpenPR},
		{name: "closed task is rejected", task: task.Task{ID: "op-1", Status: task.StatusClosed}, wantErr: "task op-1 is closed"},
		{name: "closed task takes precedence over passed review", task: task.Task{ID: "op-1", Status: task.StatusClosed}, state: taskstate.TaskState{Runs: []taskstate.RunAttempt{completedRun}, Reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusPassed}}}, wantErr: "task op-1 is closed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			route, err := SelectTaskRunRoute(tt.task, tt.state)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("SelectTaskRunRoute() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectTaskRunRoute() error = %v", err)
			}
			if route.Action != tt.want {
				t.Fatalf("route action = %q, want %q", route.Action, tt.want)
			}
		})
	}
}

func TestSelectTaskRunRouteDispatchesNewerReviewBlockersAfterOlderRepair(t *testing.T) {
	state := taskstate.TaskState{
		Runs: []taskstate.RunAttempt{
			{
				Attempt:    1,
				Status:     taskstate.RunStatusSucceeded,
				Completion: &taskstate.Completion{Summary: "implemented"},
			},
			{
				Attempt:    2,
				Status:     taskstate.RunStatusSucceeded,
				Completion: &taskstate.Completion{Summary: "repaired review one"},
				ReviewFollowUp: &taskstate.ReviewFollowUp{
					ReviewAttempt:  1,
					FindingIndexes: []int{0},
				},
			},
		},
		Reviews: []taskstate.ReviewAttempt{
			{
				Attempt: 1,
				Status:  taskstate.ReviewStatusBlocked,
				Findings: []taskstate.ReviewFinding{{
					Type:                 taskstate.FindingTypeBlocking,
					Title:                "first blocker",
					Description:          "fixed by repair",
					TargetedByRunAttempt: 2,
				}},
			},
			{
				Attempt: 2,
				Status:  taskstate.ReviewStatusBlocked,
				Findings: []taskstate.ReviewFinding{{
					Type:        taskstate.FindingTypeBlocking,
					Title:       "second blocker",
					Description: "needs repair",
				}},
			},
		},
	}

	route, err := SelectTaskRunRoute(task.Task{ID: "op-1"}, state)
	if err != nil {
		t.Fatalf("SelectTaskRunRoute() error = %v", err)
	}
	if route.Action != TaskRunActionTargetedRepair {
		t.Fatalf("route action = %q, want %q", route.Action, TaskRunActionTargetedRepair)
	}
	if route.Attempt != 2 {
		t.Fatalf("route review attempt = %d, want 2", route.Attempt)
	}
}
