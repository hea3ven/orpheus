package workflow

import (
	"context"
	"errors"
	"fmt"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// ResetDisclosure describes authorization for deferred effects, not a baseline preview.
type ResetDisclosure struct {
	TaskID, Title, Directory, Branch, Changes string
}

// Reset owns confirmation and state-only scheduling. The frontend supplies the
// explicit operator decision; unavailable input never authorizes a restart.
func (s Controller) Reset(ctx context.Context, taskID string, confirm func(ResetDisclosure) (bool, error)) (bool, error) {
	base, err := s.resolveReviewTask(ctx, taskID)
	if err != nil {
		return false, err
	}
	snapshot, err := s.RunStore.Load(base.RepoID(), base.TaskID())
	if err != nil {
		return false, err
	}
	var previous *work.WorkContext
	if snapshot.Work != nil && len(snapshot.Work.Workflows) > 0 {
		previous = snapshot.Work.Workflows[len(snapshot.Work.Workflows)-1].Context()
		if previous == nil {
			previous = snapshot.Work.ResetContext()
		}
	}
	if previous == nil {
		return false, errors.New("workflow reset requires an established task checkout")
	}
	adapter := s.DispatchGit
	if adapter == nil {
		adapter = LocalDispatchGit{}
	}
	git, ok := adapter.(ResetGit)
	if !ok {
		return false, errors.New("git adapter does not support workflow reset")
	}
	changes, err := git.InspectWorkflowReset(ctx, gitmeta.WorkflowResetRequest{RepoPath: base.Source.Repository.Path, Directory: previous.Directory, Branch: previous.TaskBranch})
	if err != nil {
		return false, err
	}
	if confirm == nil {
		return false, errors.New("workflow reset requires explicit confirmation")
	}
	approved, err := confirm(ResetDisclosure{TaskID: base.TaskID(), Title: base.Task.Title, Directory: previous.Directory, Branch: previous.TaskBranch, Changes: changes})
	if err != nil || !approved {
		return false, err
	}
	return s.scheduleReset(ctx, base)
}

func (s Controller) scheduleReset(ctx context.Context, base ReviewContext) (bool, error) {
	// Reuse T12 reconciliation, rather than inventing reset process ownership.
	prepared, err := PrepareTaskRun(ctx, PrepareTaskRunOptions{Paths: s.Paths, Store: s.RunStore, RepoID: base.RepoID(), TaskID: base.TaskID(), Task: base.Task, Probe: s.ProcessProbe, Trigger: "task_reset"})
	if err != nil {
		return false, err
	}
	if _, active := taskstate.ActiveRun(prepared.State); active {
		return false, fmt.Errorf("cannot reset workflow: %s", prepared.Inspection.Advice(base.TaskID(), "implementation", prepared.Route.Attempt))
	}
	if _, active := ActiveReviewExecution(prepared.State); active {
		return false, fmt.Errorf("cannot reset workflow: %s", prepared.ReviewInspection.Advice(base.TaskID(), "review", prepared.Route.Attempt))
	}
	if err := s.checkResetResolver(prepared.State); err != nil {
		return false, err
	}
	store, ok := s.RunStore.(interface{ ScheduleReset(string, string) error })
	if !ok {
		return false, errors.New("state adapter does not support workflow reset")
	}
	err = state.WithGlobalMutationLockLogger(ctx, s.Paths, "schedule workflow reset", s.Logger, func() error {
		return store.ScheduleReset(base.RepoID(), base.TaskID())
	})
	return err == nil, err
}

func (s Controller) checkResetResolver(snapshot taskstate.TaskState) error {
	var execution *taskstate.AgentExecution
	if sync := snapshot.ActiveSyncConflict; sync != nil {
		execution = sync.Execution
	}
	if snapshot.Work != nil && snapshot.Work.ActiveWorkflow() != nil {
		if p, ok := snapshot.Work.ActiveWorkflow().LastPhase().(*work.PublicationPhase); ok && p.DirectSync != nil && p.DirectSync.Execution != nil {
			e := p.DirectSync.Execution
			if _, running := e.Process.(*work.RunningProcess); running {
				execution = &taskstate.AgentExecution{Status: taskstate.RunStatusRunning, SupervisorPID: e.SupervisorPID, ChildPID: e.ChildPID}
			}
		}
	}
	if execution == nil || execution.Status != taskstate.RunStatusRunning {
		return nil
	}
	inspection := InspectAttachedExecution(*execution, s.ProcessProbe)
	if inspection.Condition == AttachedExecutionLive || inspection.Condition == AttachedExecutionUnverifiable {
		return fmt.Errorf("cannot reset workflow: %s", inspection.Advice(snapshot.TaskID, "sync resolver", 0))
	}
	return nil
}
