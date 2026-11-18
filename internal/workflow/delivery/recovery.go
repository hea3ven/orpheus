package delivery

import (
	"context"
	"fmt"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"time"
)

func recoveryRequest(w *work.Workflow, p *work.PublicationPhase, repo task.Repository) gitmeta.PublicCommitRequest {
	if p.Integration != nil {
		return gitmeta.PublicCommitRequest{Directory: repo.Path, Branch: p.Integration.Destination.Branch, Input: gitmeta.Revision{Commit: p.Integration.Destination.Commit}}
	}
	if p.DirectSync != nil {
		return gitmeta.PublicCommitRequest{Directory: w.Context().Directory, Branch: w.Context().TaskBranch, Input: gitmeta.Revision{Commit: p.DirectSync.Source.Commit}}
	}
	return publicationRequest(w, p)
}

func (s *Service) verifyDirectRecoveryExecution(p *work.PublicationPhase) error {
	if p.DirectSync == nil || p.DirectSync.Execution == nil {
		return nil
	}
	e := p.DirectSync.Execution
	if _, running := e.Process.(*work.RunningProcess); !running {
		return nil
	}
	inspection := phaseexec.InspectAttachedExecution(taskstate.AgentExecution{Status: taskstate.RunStatusRunning, SupervisorPID: e.SupervisorPID, ChildPID: e.ChildPID}, s.ProcessProbe)
	if inspection.Condition != phaseexec.AttachedExecutionRecoverable {
		return fmt.Errorf("direct resolver prevents recovery: %s", inspection.Reason)
	}
	return nil
}

func (s *Service) validateRecovery(ctx context.Context, w *work.Workflow, p *work.PublicationPhase) error {
	if err := verifyPublicationOwnership(ctx, s.Source, s.Task, s.Backend, w); err != nil {
		return err
	}
	return s.verifyDirectRecoveryExecution(p)
}

// InspectRecovery discloses restoration without changing Git or work state.
func (s *Service) InspectRecovery(ctx context.Context, current work.State, id phaseexec.InputID) (gitmeta.PublicationRecovery, error) {
	if id.Kind != RecoveryInput {
		return gitmeta.PublicationRecovery{}, phaseexec.ErrStaleInput
	}
	if err := validateInput(current, id); err != nil {
		return gitmeta.PublicationRecovery{}, err
	}
	w, p, err := publicationAt(&current, id.Owner)
	if err != nil {
		return gitmeta.PublicationRecovery{}, err
	}
	if err := s.validateRecovery(ctx, w, p); err != nil {
		return gitmeta.PublicationRecovery{}, err
	}
	return s.boundary().InspectPublicationRecovery(ctx, recoveryRequest(w, p, s.Source.Repository))
}
func (s *Service) repairedPublicHistory(ctx context.Context, snapshot *work.State, owner work.AttemptRef) phaseexec.Result {
	w := snapshot.ActiveWorkflow()
	p := w.LastPhase().(*work.PublicationPhase)
	if p.Integration != nil && w.Context().Directory == s.Source.Repository.Path {
		if err := s.directBoundary(s.boundary()).ResumeDirectTaskCheckout(ctx, w.Context().Directory, w.Context().TaskBranch); err != nil {
			return phaseexec.Result{Err: err}
		}
	}
	current, err := s.boundary().InspectPublicationCheckout(ctx, w.Context().Directory, w.Context().TaskBranch)
	if err != nil {
		return phaseexec.Result{Err: err}
	}
	destination, err := s.boundary().ObserveDestination(ctx, w.Context().Directory, w.FixedDestination().Branch)
	if err != nil {
		return phaseexec.Result{Err: err}
	}
	if p.Integration != nil {
		selected := committedPublication{publication: p, prepared: w.Context()}
		checkpoints, err := s.directBoundary(s.boundary()).InspectDirectIntegrationCheckpoints(ctx, directIntegrationRequest(s.Source.Repository, selected))
		if err != nil {
			return phaseexec.Result{Err: err}
		}
		if checkpoints.Destination.Commit != p.Integration.Destination.Commit && checkpoints.Destination.Commit != destination.Commit {
			return phaseexec.Result{Err: fmt.Errorf("local destination %s in %s still needs repair; restore recorded checkpoint %s or the observed destination %s before continuing the selected task input", p.Integration.Destination.Branch, s.Source.Repository.Path, p.Integration.Destination.Commit, destination.Commit)}
		}
	}
	if p.DirectSync != nil && p.DirectSync.Execution != nil {
		if _, running := p.DirectSync.Execution.Process.(*work.RunningProcess); running {
			p.DirectSync.Execution.Process = &work.InterruptedProcess{At: time.Now().UTC(), Reason: "Operator recovered abandoned direct-sync checkout", Trigger: "publication_recovery"}
		}
	}
	selected := committedPublication{workflowIndex: len(snapshot.Workflows) - 1, phaseIndex: len(w.Phases) - 1, workflow: w, publication: p, prepared: w.Context()}
	err = s.renewDirectPublication(ctx, s.boundary(), selected, work.CodeIdentity{Commit: current.Commit, Tree: current.Tree}, destination, "Operator restored or manually repaired public history; result was not separately task reviewed", false)

	if err != nil {
		return phaseexec.Result{Err: err}
	}
	plan := snapshot.ActiveWorkflow().LastPhase().(*work.PublicationPhase)
	var interrupted *work.InterruptedProcess
	if p.DirectSync != nil && p.DirectSync.Execution != nil {
		interrupted, _ = p.DirectSync.Execution.Process.(*work.InterruptedProcess)
	}
	return phaseexec.Result{Update: change(owner, func(_ *work.State, w *work.Workflow, p *work.PublicationPhase) (bool, error) {
		if interrupted != nil {
			p.DirectSync.Execution.Process = interrupted
		}
		w.Phases = append(w.Phases, plan)
		return true, nil
	})}
}
