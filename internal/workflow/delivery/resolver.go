package delivery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

func (s *Service) nextResolver(current work.State, selected committedPublication) (phaseexec.Selection[Request, Outcome], error) {
	owner := selected.owner(current)
	if s.resolverObserved {
		return phaseexec.Run[Request, Outcome](s.completeConflictMerge(owner)), nil
	}
	if s.resolverLaunch != nil {
		return phaseexec.Run[Request, Outcome](s.runResolver(owner)), nil
	}
	if s.freshConflict {
		return phaseexec.Run[Request, Outcome](s.prepareResolver(owner)), nil
	}
	return phaseexec.Await[Request, Outcome](RecoveryRequest{ID: phaseexec.InputID{Owner: owner, Version: current.Version, Kind: RecoveryInput}}), nil
}

func (s *Service) prepareResolver(owner work.AttemptRef) phaseexec.Operation {
	return operation("prepare direct conflict resolver", owner, func(_ *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		opts := phaseexec.ConflictResolutionOptions{Address: owner, Repository: s.Source.Repository, Task: s.Task, Branch: w.Context().TaskBranch, Worktree: w.Context().Directory, DefaultBranch: p.DirectSync.Destination.Branch, ConflictFiles: append([]string(nil), p.DirectSync.ConflictFiles...)}
		opts.RecordChildPID = func(pid int) error {
			_, err := s.Commit(context.Background(), change(owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (bool, error) {
				if p.DirectSync == nil || p.DirectSync.Execution == nil || pid <= 0 || p.DirectSync.Execution.ChildPID != 0 {
					return false, errors.New("direct resolver child identity changed")
				}
				p.DirectSync.Execution.ChildPID = pid
				return true, nil
			}))
			return err
		}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			launch, err := s.ConflictResolver.PrepareSyncConflictResolution(ctx, opts)
			if err == nil {
				s.resolverLaunch = &launch
			}
			return phaseexec.Result{Err: err}
		}}, nil
	})
}

func (s *Service) runResolver(owner work.AttemptRef) phaseexec.Operation {
	return operation("run direct conflict resolver", owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		launch := *s.resolverLaunch
		execution := launch.Execution
		execution.StartedAt, execution.SupervisorPID, execution.Address = time.Now().UTC(), os.Getpid(), owner
		observed, err := taskstate.ObservedExecution(execution)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		p.DirectSync.Execution = &observed
		return phaseexec.Prepared{Changed: true, Scope: phaseexec.ReleaseForEffect, Run: func(ctx context.Context) phaseexec.Result {
			result := launch.Execute(ctx, execution)
			if result.Started && result.Exit == nil {
				return phaseexec.Result{Err: errors.Join(result.Err, errors.New("direct resolver has no safe observed exit; publication remains unresolved"))}
			}
			effectErr := result.Err
			if effectErr == nil && (result.Exit == nil || result.Exit.Code != 0) {
				effectErr = fmt.Errorf("direct resolver did not exit successfully; continue task run for operator recovery")
			}
			if effectErr == nil {
				s.resolverObserved = true
			}
			return phaseexec.Result{Err: effectErr, Update: change(owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (bool, error) {
				if p.DirectSync == nil || p.DirectSync.Execution == nil {
					return false, phaseexec.ErrStaleInput
				}
				err := observeDirectResolverResult(p.DirectSync.Execution, result)
				return err == nil, err
			})}
		}}, nil
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
	}
	taskstate.ApplyObservedUsage(e, result.Usage, time.Now().UTC())
	return nil
}

func (s *Service) completeConflictMerge(owner work.AttemptRef) phaseexec.Operation {
	return operation("complete direct conflict merge", owner, func(_ *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		opts := directSyncOptions(s.Source.Repository, committedPublication{prepared: w.Context(), destination: &p.Destination})
		files := append([]string(nil), p.DirectSync.ConflictFiles...)
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			if _, err := s.DirectGit.CommitTaskBranchConflictResolution(ctx, opts, files); err != nil {
				return phaseexec.Result{Err: err}
			}
			current, err := s.Git.InspectPublicationCheckout(ctx, opts.Worktree, opts.Branch)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			observed := work.CodeIdentity{Commit: current.Commit, Tree: current.Tree}
			return phaseexec.Result{Update: change(owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (bool, error) {
				p.DirectSync.Result = &observed
				return true, nil
			})}
		}}, nil
	})
}
