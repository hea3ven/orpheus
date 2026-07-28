package workflow

import (
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// TaskRunAction identifies the next workflow transition selected from persisted
// task state. The CLI owns presentation and flag validation; this package owns
// the state transition selection.
type TaskRunAction string

const (
	TaskRunActionStartImplementation  TaskRunAction = "start_implementation"
	TaskRunActionTargetedRepair       TaskRunAction = "targeted_repair"
	TaskRunActionStartReview          TaskRunAction = "start_review"
	TaskRunActionResumeReview         TaskRunAction = "resume_review"
	TaskRunActionRetryFinalization    TaskRunAction = "retry_finalization"
	TaskRunActionImplementationActive TaskRunAction = "implementation_active"
	TaskRunActionReviewActive         TaskRunAction = "review_active"
	TaskRunActionOpenPR               TaskRunAction = "open_pr"
	TaskRunActionCompleted            TaskRunAction = "completed"
)

// TaskRunRoute is the next action selected for task run.
type TaskRunRoute struct {
	Action  TaskRunAction
	Attempt int
	Step    string
	PRURL   string
}

// SelectTaskRunRoute maps the persisted implement-review-fix-finalize state to
// exactly one safe next action. It deliberately does not perform any mutation.
func SelectTaskRunRoute(taskItem task.Task, state taskstate.TaskState) (TaskRunRoute, error) {
	if taskItem.Status == task.StatusClosed {
		return TaskRunRoute{}, fmt.Errorf("task %s is closed", taskItem.ID)
	}

	finalization := taskstate.FinalizationFacts(state)
	if finalization.ClosedAt != nil {
		return TaskRunRoute{Action: TaskRunActionCompleted}, nil
	}

	metadata := taskItem.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return TaskRunRoute{Action: TaskRunActionOpenPR, PRURL: strings.TrimSpace(metadata.PRURL)}, nil
	}

	latestReview, hasReview := taskstate.LatestReview(state)
	if hasReview && latestReview.Status == taskstate.ReviewStatusPassed {
		return TaskRunRoute{Action: TaskRunActionRetryFinalization, Attempt: latestReview.Attempt}, nil
	}

	if active, ok := taskstate.ActiveRun(state); ok {
		return TaskRunRoute{Action: TaskRunActionImplementationActive, Attempt: active.Attempt}, nil
	}

	latestRun, hasRun := taskstate.LatestRun(state)
	if !hasRun {
		return TaskRunRoute{Action: TaskRunActionStartImplementation}, nil
	}
	if latestRun.Status == taskstate.RunStatusFailed || latestRun.Completion == nil {
		action := TaskRunActionStartImplementation
		if latestRun.ReviewFollowUp != nil {
			action = TaskRunActionTargetedRepair
		}
		return TaskRunRoute{Action: action}, nil
	}

	if !hasReview {
		return TaskRunRoute{Action: TaskRunActionStartReview}, nil
	}
	return selectReviewTaskRunRoute(state, latestRun, latestReview, taskItem.ID)
}

func selectReviewTaskRunRoute(
	state taskstate.TaskState,
	latestRun taskstate.RunAttempt,
	latestReview taskstate.ReviewAttempt,
	taskID string,
) (TaskRunRoute, error) {
	switch latestReview.Status {
	case taskstate.ReviewStatusRunning:
		return TaskRunRoute{Action: TaskRunActionReviewActive, Attempt: latestReview.Attempt}, nil
	case taskstate.ReviewStatusWaitingForManual:
		return TaskRunRoute{
			Action:  TaskRunActionResumeReview,
			Attempt: latestReview.Attempt,
			Step:    latestReview.Step,
		}, nil
	case taskstate.ReviewStatusBlocked:
		if latestRun.ReviewFollowUp != nil &&
			latestRun.ReviewFollowUp.ReviewAttempt == latestReview.Attempt &&
			latestRun.Status == taskstate.RunStatusSucceeded &&
			latestRun.Completion != nil {
			return TaskRunRoute{Action: TaskRunActionStartReview, Attempt: latestReview.Attempt}, nil
		}
		if latestReview.AutomatedBlockerDecisionInterrupted ||
			taskstate.HasUnkeptAutomatedBlockingFindingsInState(state, latestReview) {
			return TaskRunRoute{Action: TaskRunActionStartReview, Attempt: latestReview.Attempt}, nil
		}
		indexes, eligible := taskstate.UntargetedBlockingFindingIndexesForFollowUpInState(state, latestReview)
		if eligible && len(indexes) > 0 {
			return TaskRunRoute{Action: TaskRunActionTargetedRepair, Attempt: latestReview.Attempt}, nil
		}
		return TaskRunRoute{Action: TaskRunActionStartReview, Attempt: latestReview.Attempt}, nil
	case taskstate.ReviewStatusAborted, taskstate.ReviewStatusFailed:
		return TaskRunRoute{Action: TaskRunActionStartReview, Attempt: latestReview.Attempt}, nil
	case taskstate.ReviewStatusPassed:
		return TaskRunRoute{Action: TaskRunActionRetryFinalization, Attempt: latestReview.Attempt}, nil
	default:
		return TaskRunRoute{}, fmt.Errorf(
			"latest review attempt %d for task %s has unsupported status %q",
			latestReview.Attempt,
			taskID,
			latestReview.Status,
		)
	}
}
