package externalreview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

func (s *Service) resolverSelectionOperation(ref work.AttemptRef, opts gitmeta.TaskBranchSyncOptions, url string) phaseexec.Operation {
	return phaseexec.Operation{Name: "select branch conflict resolver", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		sync, err := syncAt(current, ref)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		checkpoint := sync.Checkpoint
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			if s.ConflictResolver == nil {
				cause := fmt.Errorf("update open PR branch for task %s: merge conflicts require a configured conflict resolver", s.Task.ID)
				return s.recoverFailure(ctx, ref, opts, checkpoint, nil, "", cause)
			}
			if s.Checkpoint == nil {
				return phaseexec.Result{Err: errors.New("branch-sync checkpoint recorder is required")}
			}
			prepared, err := s.ConflictResolver.PrepareSyncConflictResolution(ctx, phaseexec.ConflictResolutionOptions{
				Address: ref, Repository: s.Repository, Task: s.Task.Clone(), Branch: opts.Branch, Worktree: opts.Worktree, DefaultBranch: opts.DefaultBranch, PRURL: url, ConflictFiles: append([]string(nil), s.branchResult.ConflictFiles...),
				RecordChildPID: func(pid int) error {
					_, err := s.Checkpoint(context.Background(), branchChange(ref, func(sync *work.BranchSync) error {
						if sync.ConflictResolution == nil {
							return errors.New("resolver execution is not recorded")
						}
						sync.ConflictResolution.Execution.ChildPID = pid
						return nil
					}))
					return err
				},
			})
			if err != nil {
				return s.recoverFailure(ctx, ref, opts, checkpoint, nil, "", fmt.Errorf("prepare merge conflict agent for task %s: %w", s.Task.ID, err))
			}
			prepared.Execution.Address = ref
			prepared.Execution.SupervisorPID = os.Getpid()
			if prepared.Execution.StartedAt.IsZero() {
				prepared.Execution.StartedAt = time.Now().UTC()
			}
			s.resolver = &prepared
			return phaseexec.Result{}
		}}, nil
	}}
}

func (s *Service) resolverOperation(ref work.AttemptRef, opts gitmeta.TaskBranchSyncOptions) phaseexec.Operation {
	return phaseexec.Operation{Name: "resolve PR branch conflicts", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		sync, err := syncAt(current, ref)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		checkpoint := sync.Checkpoint
		execution, err := taskstate.ObservedExecution(s.resolver.Execution)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		files := append([]string(nil), s.branchResult.ConflictFiles...)
		return phaseexec.Prepared{Reload: true, Run: func(ctx context.Context) phaseexec.Result {
			_, err := s.Checkpoint(context.WithoutCancel(ctx), branchChange(ref, func(sync *work.BranchSync) error {
				sync.ConflictResolution = &work.ConflictResolution{Files: files, Execution: execution}
				return nil
			}))
			if err != nil {
				return s.recoverFailure(ctx, ref, opts, checkpoint, nil, "", err)
			}
			result := s.resolver.Execute(ctx, s.resolver.Execution)
			recordErr := s.recordResolverResult(ctx, ref, result)
			cause := errors.Join(result.Err, recordErr)
			if result.Exit != nil && result.Exit.Code != 0 && cause == nil {
				cause = fmt.Errorf("resolver exited with code %d", result.Exit.Code)
			}
			if cause != nil {
				// Checkpoint recording is independent of this result update. Reconciliation
				// records only the verified native rollback/push and preserves those writes.
				return s.recoverFailure(ctx, ref, opts, checkpoint, &execution, "", fmt.Errorf("resolve merge conflicts for task %s with agent: %w", s.Task.ID, cause))
			}
			return phaseexec.Result{}
		}}, nil
	}}
}

func (s *Service) recordResolverResult(ctx context.Context, ref work.AttemptRef, result agent.ExecutionResult) error {
	if result.Started && result.Exit == nil {
		return errors.New("resolver has no safe observed process exit; leave its workflow effort unresolved")
	}
	if !result.Started && result.Err == nil {
		return errors.New("resolver reported neither a started process nor a startup failure")
	}
	process := branchChange(ref, func(sync *work.BranchSync) error {
		execution := &sync.ConflictResolution.Execution
		if !result.Started {
			execution.Process = &work.StartFailedProcess{At: time.Now().UTC(), Reason: result.Err.Error()}
		} else {
			execution.Process = &work.ExitedProcess{At: result.Exit.At, Code: &result.Exit.Code}
			execution.ChildPID = result.Exit.PID
			execution.Duration = result.Exit.At.Sub(execution.StartedAt)
		}
		return nil
	})
	usage := func(current *work.State) (bool, error) {
		if ref.Workflow < 0 || ref.Workflow >= len(current.Workflows) {
			return false, errors.New("usage has no owning workflow")
		}
		execution, err := current.Workflows[ref.Workflow].Execution(work.ExecutionRef{Phase: ref.Phase, BranchSync: ref.BranchSync})
		if err != nil {
			return false, err
		}
		taskstate.ApplyObservedUsage(execution, result.Usage, time.Now().UTC())
		return true, nil
	}
	_, processErr, usageErr := (phaseexec.AgentResults{Commit: s.Checkpoint}).ProcessThenUsage(ctx, process, usage)
	return errors.Join(processErr, usageErr)
}
