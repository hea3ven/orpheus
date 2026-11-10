package workflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// runDirectResolver releases the global lock throughout the attached process.
// Results can attach only to the exact reserved publication effort.
func (s Controller) runDirectResolver(ctx context.Context, store WorkStore, boundary FinalizationGit, pending directResolverPending) error {
	var launch PreparedSyncConflictResolution
	var execution taskstate.AgentExecution
	update := func(label string, apply func(*work.State, *work.PublicationPhase) error) error {
		return s.updateDirectResolver(ctx, store, pending, label, apply)
	}
	err := update("reserve direct conflict resolver", func(snapshot *work.State, p *work.PublicationPhase) error {
		var err error
		launch, execution, err = s.prepareDirectResolver(ctx, pending, snapshot, p, func(pid int) error {
			return update("record direct resolver child", func(_ *work.State, active *work.PublicationPhase) error {
				if active.DirectSync.Execution == nil || pid <= 0 || active.DirectSync.Execution.ChildPID != 0 {
					return errors.New("direct resolver child identity changed")
				}
				active.DirectSync.Execution.ChildPID = pid
				return nil
			})
		})
		return err
	})
	if err != nil {
		return err
	}
	result := launch.Execute(ctx, execution)
	if result.Started && result.Exit == nil {
		return errors.Join(result.Err, errors.New("direct resolver has no safe observed exit; publication remains unresolved"))
	}
	recordErr := update("record direct resolver result", func(_ *work.State, p *work.PublicationPhase) error {
		return observeDirectResolverResult(p.DirectSync.Execution, result)
	})
	if recordErr != nil {
		return errors.Join(result.Err, recordErr)
	}
	if result.Err != nil {
		return result.Err
	}
	if result.Exit == nil || result.Exit.Code != 0 {
		return fmt.Errorf("direct resolver did not exit successfully; continue task run for operator recovery")
	}
	return update("complete direct conflict merge", func(snapshot *work.State, p *work.PublicationPhase) error {
		w := snapshot.ActiveWorkflow()
		selected := committedPublication{workflowIndex: pending.owner.Workflow, phaseIndex: pending.owner.Phase, workflow: w, publication: p, prepared: w.Context(), destination: &p.Plan.Destination}
		if _, err := s.Delivery.directBoundary(boundary).CommitTaskBranchConflictResolution(ctx, directSyncOptions(pending.repository, selected), p.DirectSync.ConflictFiles); err != nil {
			return err
		}
		current, err := boundary.InspectPublicationCheckout(ctx, w.Context().Directory, w.Context().TaskBranch)
		if err != nil {
			return err
		}
		p.DirectSync.Result = &work.CodeIdentity{Commit: current.Commit, Tree: current.Tree}
		return nil
	})
}

func (s Controller) updateDirectResolver(ctx context.Context, store WorkStore, pending directResolverPending, label string, apply func(*work.State, *work.PublicationPhase) error) error {
	return state.WithGlobalMutationLockLogger(ctx, s.Delivery.Paths, label, s.Delivery.Logger, func() error {
		snapshot, err := store.Load(pending.owner.RepositoryID, pending.owner.TaskID)
		if err != nil {
			return err
		}
		w := snapshot.ActiveWorkflow()
		if w == nil || pending.owner.Workflow != len(snapshot.Workflows)-1 || pending.owner.Phase != len(w.Phases)-1 {
			return errors.New("direct resolver no longer owns current publication")
		}
		p, ok := w.LastPhase().(*work.PublicationPhase)
		if !ok || p.DirectSync == nil || p.DirectSync.Result != nil {
			return errors.New("direct resolver checkpoint changed")
		}
		if err := apply(&snapshot, p); err != nil {
			return err
		}
		return savePublicationOperation(store, &snapshot)
	})
}

func observeDirectResolverResult(e *work.AgentExecution, result agent.ExecutionResult) error {
	if !result.Started {
		if result.Err == nil {
			return errors.New("direct resolver reported neither startup failure nor started process")
		}
		e.Process = &work.StartFailedProcess{At: time.Now().UTC(), Reason: result.Err.Error()}
	} else {
		e.Process = &work.ExitedProcess{At: result.Exit.At, Code: &result.Exit.Code}
		if e.ChildPID != 0 && result.Exit.PID != e.ChildPID {
			return errors.New("direct resolver exit identity differs from recorded child")
		}
		e.ChildPID = result.Exit.PID
		e.Duration = result.Exit.At.Sub(e.StartedAt)
	}
	taskstate.ApplyObservedUsage(e, result.Usage, time.Now().UTC())
	return nil
}

func (s Controller) prepareDirectResolver(ctx context.Context, pending directResolverPending, snapshot *work.State, p *work.PublicationPhase, recordPID func(int) error) (PreparedSyncConflictResolution, taskstate.AgentExecution, error) {
	w := snapshot.ActiveWorkflow()
	opts := SyncConflictResolutionOptions{Address: pending.owner, Repository: pending.repository, Task: pending.item, Branch: w.Context().TaskBranch, Worktree: w.Context().Directory, DefaultBranch: p.DirectSync.Destination.Branch, ConflictFiles: p.DirectSync.ConflictFiles}
	opts.RecordChildPID = recordPID
	launch, err := s.Delivery.ConflictResolver.PrepareSyncConflictResolution(ctx, opts)
	if err != nil {
		return PreparedSyncConflictResolution{}, taskstate.AgentExecution{}, err
	}
	execution := launch.Execution
	execution.StartedAt, execution.SupervisorPID, execution.Address = time.Now().UTC(), os.Getpid(), pending.owner
	observed, err := taskstate.ObservedExecution(execution)
	if err != nil {
		return PreparedSyncConflictResolution{}, taskstate.AgentExecution{}, err
	}
	p.DirectSync.Execution = &observed
	return launch, execution, nil
}
