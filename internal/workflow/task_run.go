package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// TaskRunAction identifies the next workflow transition selected from persisted
// task state. The CLI owns presentation; the controller owns flag validation and
// the state transition selection.
type TaskRunAction string

const (
	TaskRunActionStartImplementation  TaskRunAction = "start_implementation"
	TaskRunActionTargetedRepair       TaskRunAction = "targeted_repair"
	TaskRunActionStartReview          TaskRunAction = "start_review"
	TaskRunActionResumeReview         TaskRunAction = "resume_review"
	TaskRunActionPreparePublication   TaskRunAction = "prepare_publication"
	TaskRunActionPublish              TaskRunAction = "publish"
	TaskRunActionAwaitDecision        TaskRunAction = "await_decision"
	TaskRunActionRetryFinalization    TaskRunAction = "retry_finalization"
	TaskRunActionImplementationActive TaskRunAction = "implementation_active"
	TaskRunActionReviewActive         TaskRunAction = "review_active"
	TaskRunActionOpenPR               TaskRunAction = "open_pr"
	TaskRunActionCompleted            TaskRunAction = "completed"
	TaskRunActionNeedsAttention       TaskRunAction = "needs_attention"
)

// TaskRunRoute is the next action selected for task run.
type TaskRunRoute struct {
	Action   TaskRunAction
	Attempt  int
	Step     string
	PRURL    string
	Advice   string
	Recovery *AttachedExecutionInspection
	// Owner and Version are an invocation's expected state, not a persisted cursor.
	Owner   *work.AttemptRef
	Version work.StateVersion
}

// TaskRunRecoveryStore supplies both attached-process recovery transitions
// required before task-run routing.
type TaskRunRecoveryStore interface {
	ImplementationRunRecoveryStore
	ReviewRecoveryStore
}

// PrepareTaskRunOptions supplies the workflow-owned state and process facts
// needed to reconcile stale attached work before selecting its route.
type PrepareTaskRunOptions struct {
	Paths   state.Paths
	Store   TaskRunRecoveryStore
	RepoID  string
	TaskID  string
	Task    task.Task
	Probe   ProcessProbe
	Trigger string
}

// PreparedTaskRun is the reloaded task state, recovery inspection, and route
// selected after workflow reconciliation.
type PreparedTaskRun struct {
	State            taskstate.TaskState
	Inspection       ImplementationRunInspection
	ReviewInspection AttachedExecutionInspection
	Route            TaskRunRoute
}

// PrepareTaskRun reconciles an active implementation run under mutation
// protection, reloads state after that decision, and selects the next route.
func PrepareTaskRun(ctx context.Context, opts PrepareTaskRunOptions) (PreparedTaskRun, error) {
	if opts.Store == nil {
		return PreparedTaskRun{}, errors.New("task run preparation store is required")
	}
	if opts.Probe == nil {
		opts.Probe = agentexec.ProbePID
	}
	taskState, err := opts.Store.Load(opts.RepoID, opts.TaskID)
	if err != nil {
		return PreparedTaskRun{}, err
	}
	var inspection ImplementationRunInspection
	if active, ok := taskstate.ActiveRun(taskState); ok {
		inspection, err = ReconcileImplementationRun(ctx, opts.Paths, opts.Store, opts.RepoID, opts.TaskID, active.Attempt, opts.Trigger, opts.Probe)
		if err != nil {
			return PreparedTaskRun{}, err
		}
		taskState, err = opts.Store.Load(opts.RepoID, opts.TaskID)
		if err != nil {
			return PreparedTaskRun{}, fmt.Errorf("reload reconciled local task-state: %w", err)
		}
	}
	var reviewInspection AttachedExecutionInspection
	if execution, ok := ActiveReviewExecution(taskState); ok {
		reviewInspection, err = ReconcileReviewExecution(ctx, opts.Paths, opts.Store, opts.RepoID, opts.TaskID, execution.Attempt, opts.Trigger, opts.Probe)
		if err != nil {
			return PreparedTaskRun{}, err
		}
		taskState, err = opts.Store.Load(opts.RepoID, opts.TaskID)
		if err != nil {
			return PreparedTaskRun{}, fmt.Errorf("reload reconciled review state: %w", err)
		}
	}
	route, err := SelectTaskRunRoute(opts.Task, taskState)
	if err != nil {
		return PreparedTaskRun{}, err
	}
	applyExecutionRecovery(&route, taskState, opts.Probe)
	return PreparedTaskRun{State: taskState, Inspection: inspection, ReviewInspection: reviewInspection, Route: route}, nil
}

// SelectTaskRunRoute derives legal work from the last typed occurrence. Source
// metadata can describe an unowned PR, but cannot override controller-owned work.
func SelectTaskRunRoute(item task.Task, view taskstate.TaskState) (TaskRunRoute, error) {
	if item.Status == task.StatusClosed {
		return TaskRunRoute{}, fmt.Errorf("task %s is closed", item.ID)
	}
	if view.Work == nil || len(view.Work.Workflows) == 0 {
		var version work.StateVersion
		if view.Work != nil {
			version = view.Work.Version
		}
		metadata := item.OrpheusMetadata()
		if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
			return TaskRunRoute{Action: TaskRunActionOpenPR, PRURL: strings.TrimSpace(metadata.PRURL), Version: version}, nil
		}
		return TaskRunRoute{Action: TaskRunActionStartImplementation, Version: version}, nil
	}
	snapshot := view.Work
	canonical, err := taskstate.Project(*snapshot)
	if err != nil {
		return TaskRunRoute{}, err
	}
	w := &snapshot.Workflows[len(snapshot.Workflows)-1]
	route, err := phaseRoute(item, canonical, w)
	route.Version = snapshot.Version
	if len(w.Phases) > 0 {
		owner := work.AttemptRef{RepositoryID: snapshot.RepositoryID, TaskID: snapshot.TaskID, Workflow: len(snapshot.Workflows) - 1, Phase: len(w.Phases) - 1}
		if p, ok := w.LastPhase().(*work.ReviewPhase); ok && len(p.Steps) > 0 {
			index := len(p.Steps) - 1
			owner.Step = &index
		}
		if p, ok := w.LastPhase().(*work.ExternalReviewPhase); ok && len(p.BranchSyncs) > 0 {
			index := len(p.BranchSyncs) - 1
			owner.BranchSync = &index
		}
		route.Owner = &owner
	}
	return route, err
}

func phaseRoute(item task.Task, view taskstate.TaskState, w *work.Workflow) (TaskRunRoute, error) {
	if advice := sourceWorkAdvice(item, view); advice != "" {
		return TaskRunRoute{Action: TaskRunActionNeedsAttention, Advice: advice}, nil
	}
	switch p := w.LastPhase().(type) {
	case nil, *work.PreparationPhase:
		return TaskRunRoute{Action: TaskRunActionStartImplementation}, nil
	case *work.ImplementationPhase:
		run, ok := taskstate.LatestRun(view)
		if !ok {
			return TaskRunRoute{}, errors.New("implementation projection is missing its phase owner")
		}
		if _, running := p.Execution.Process.(*work.RunningProcess); running {
			return TaskRunRoute{Action: TaskRunActionImplementationActive, Attempt: run.Attempt}, nil
		}
		exit, observed := p.Execution.Process.(*work.ExitedProcess)
		if observed && exit.Code != nil && *exit.Code == 0 && p.Completion != nil {
			return TaskRunRoute{Action: TaskRunActionStartReview}, nil
		}
		action := TaskRunActionStartImplementation
		if len(p.TargetFindings) > 0 {
			action = TaskRunActionTargetedRepair
		}
		return TaskRunRoute{Action: action}, nil
	case *work.ReviewPhase:
		latest, ok := taskstate.LatestReview(view)
		if !ok {
			return TaskRunRoute{}, errors.New("review projection is missing its phase owner")
		}
		return selectReviewTaskRunRoute(view, latest, item.ID)
	case *work.PublicationPhase:
		if p.Consent == nil {
			return TaskRunRoute{Action: TaskRunActionAwaitDecision}, nil
		}
		return TaskRunRoute{Action: TaskRunActionPublish}, nil
	case *work.ExternalReviewPhase:
		publication := w.Phases[p.Publication.Phase].(*work.PublicationPhase)
		delivery := publication.Delivery.(*work.PullRequestDelivery)
		return TaskRunRoute{Action: TaskRunActionOpenPR, PRURL: delivery.PullRequest.URL}, nil
	case *work.FinalizationPhase:
		if p.Closure != nil {
			return TaskRunRoute{Action: TaskRunActionCompleted}, nil
		}
		return TaskRunRoute{Action: TaskRunActionRetryFinalization}, nil
	default:
		return TaskRunRoute{}, errors.New("unsupported workflow phase")
	}
}

// CheckExpectedRoute refuses a stale invocation before reserving work. It is
// called under mutation protection; attached child callbacks use their own exact
// attempt addresses and do not need to retain this pre-reservation version.
func CheckExpectedRoute(expected *TaskRunRoute, item task.Task, view taskstate.TaskState) error {
	if expected == nil {
		return nil
	}
	actual, err := SelectTaskRunRoute(item, view)
	if err != nil {
		return err
	}
	sameOwner := sameRouteOwner(expected.Owner, actual.Owner)
	if !sameOwner || expected.Version != actual.Version || expected.Action != actual.Action {
		return errors.New("workflow advancement is stale; inspect current work before retrying")
	}
	return nil
}

func selectReviewTaskRunRoute(
	state taskstate.TaskState,
	latestReview taskstate.ReviewAttempt,
	taskID string,
) (TaskRunRoute, error) {
	switch latestReview.Status {
	case taskstate.ReviewStatusRunning:
		// A hard stop while prompting for an automated blocker leaves the
		// persisted review running even though no review execution remains.
		// Route through the review lifecycle so its existing disposition guard
		// preserves the finding and requires an explicit operator decision.
		if taskstate.HasUnkeptAutomatedBlockingFindingsInState(state, latestReview) {
			return TaskRunRoute{Action: TaskRunActionStartReview, Attempt: latestReview.Attempt}, nil
		}
		if len(latestReview.Steps) == 0 {
			return TaskRunRoute{Action: TaskRunActionResumeReview, Attempt: latestReview.Attempt, Step: latestReview.Step}, nil
		}
		last := latestReview.Steps[len(latestReview.Steps)-1]
		active := last.Execution != nil && last.Execution.Status == taskstate.RunStatusRunning || last.Kind == "check" && last.ExitCode == nil
		if !active {
			return TaskRunRoute{Action: TaskRunActionResumeReview, Attempt: latestReview.Attempt, Step: latestReview.Step}, nil
		}
		return TaskRunRoute{Action: TaskRunActionReviewActive, Attempt: latestReview.Attempt}, nil
	case taskstate.ReviewStatusWaitingForManual:
		return TaskRunRoute{
			Action:  TaskRunActionResumeReview,
			Attempt: latestReview.Attempt,
			Step:    latestReview.Step,
		}, nil
	case taskstate.ReviewStatusWaitingForAutomatedDecision:
		return TaskRunRoute{
			Action:  TaskRunActionResumeReview,
			Attempt: latestReview.Attempt,
			Step:    latestReview.Step,
		}, nil
	case taskstate.ReviewStatusBlocked:
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
		return TaskRunRoute{Action: TaskRunActionPreparePublication, Attempt: latestReview.Attempt}, nil
	default:
		return TaskRunRoute{}, fmt.Errorf(
			"latest review attempt %d for task %s has unsupported status %q",
			latestReview.Attempt,
			taskID,
			latestReview.Status,
		)
	}
}

func sameRouteOwner(expected, actual *work.AttemptRef) bool {
	return expected == nil && actual == nil || expected != nil && actual != nil && expected.Equal(*actual)
}

func sourceWorkAdvice(item task.Task, view taskstate.TaskState) string {
	if item.Status != task.StatusOpen {
		return ""
	}
	run, ok := taskstate.LatestRun(view)
	if !ok {
		return ""
	}
	activity := string(run.Status)
	if run.Status == taskstate.RunStatusRunning {
		activity = "is running"
	}
	return fmt.Sprintf("backend status is open but local run attempt %d %s; inspect task state before continuing", run.Attempt, activity)
}
