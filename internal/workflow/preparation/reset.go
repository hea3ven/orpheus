package preparation

import (
	"context"
	"errors"
	"slices"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

func (s *Service) nextReset(current work.State) (phaseexec.Selection[Request, ContextEstablished], error) {
	previous := current.ResetContext()
	if previous == nil {
		return phaseexec.Selection[Request, ContextEstablished]{}, errors.New("pending reset has no established checkout")
	}
	git, ok := s.Git.(ResetGit)
	if !ok {
		return phaseexec.Selection[Request, ContextEstablished]{}, errors.New("git adapter does not support workflow reset")
	}
	owner := len(current.Workflows) - 1
	req := gitmeta.WorkflowResetRequest{RepoPath: s.Repository.Path, Directory: previous.Directory, Branch: previous.TaskBranch, DefaultBranch: s.Repository.DefaultBranch, Ref: baselineRef(s.Repository.ID, s.Task.ID, owner)}
	p, _ := current.ActiveWorkflow().LastPhase().(*work.PreparationPhase)
	if p == nil || p.ResetTarget == nil {
		return phaseexec.Run[Request, ContextEstablished](phaseexec.Operation{Name: "resolve workflow reset target", Prepare: func(snapshot *work.State) (phaseexec.Prepared, error) {
			if err := pendingReset(snapshot, owner); err != nil {
				return phaseexec.Prepared{}, err
			}
			return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
				resolved, err := git.ResolveWorkflowReset(ctx, req)
				if err != nil {
					return phaseexec.Result{Err: err}
				}
				target := work.WorkContext{Directory: previous.Directory, TaskBranch: previous.TaskBranch, CheckpointRef: resolved.RetainedRef, Baseline: work.CodeIdentity{Commit: resolved.Commit, Tree: resolved.Tree}}
				return phaseexec.Result{Update: func(snapshot *work.State) (bool, error) {
					if err := pendingReset(snapshot, owner); err != nil {
						return false, err
					}
					w := snapshot.ActiveWorkflow()
					if len(w.Phases) != 0 {
						return false, errors.New("reset target requires pending, unresolved Preparation")
					}
					w.Phases = append(w.Phases, &work.PreparationPhase{ResetTarget: &target})
					return true, nil
				}}
			}}, nil
		}}), nil
	}
	target := *p.ResetTarget
	req.Target = gitmeta.Revision{Commit: target.Baseline.Commit, Tree: target.Baseline.Tree, RetainedRef: target.CheckpointRef}
	req.RetainCommits = resetPublicHistory(&current)
	return phaseexec.Run[Request, ContextEstablished](phaseexec.Operation{Name: "apply authorized workflow reset", Prepare: func(snapshot *work.State) (phaseexec.Prepared, error) {
		if err := pendingReset(snapshot, owner); err != nil {
			return phaseexec.Prepared{}, err
		}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			if err := git.ApplyWorkflowReset(ctx, req); err != nil {
				return phaseexec.Result{Err: err}
			}
			s.setup = &gitmeta.TaskWorktreeSetupResult{Branch: target.TaskBranch, WorktreePath: target.Directory, StartingCommit: target.Baseline.Commit, StartingTree: target.Baseline.Tree, CheckpointRef: target.CheckpointRef, Lifecycle: gitmeta.TaskWorktreeLifecycleReused}
			return phaseexec.Result{Update: func(snapshot *work.State) (bool, error) {
				if err := pendingReset(snapshot, owner); err != nil {
					return false, err
				}
				// Consume authorization before backend activation or agent work.
				return establish(snapshot, &target, time.Now().UTC())
			}}
		}}, nil
	}}), nil
}

func pendingReset(snapshot *work.State, owner int) error {
	w := snapshot.ActiveWorkflow()
	if w == nil || owner != len(snapshot.Workflows)-1 || w.ResetRequestedAt == nil || w.Context() != nil {
		return errors.New("workflow reset no longer owns pending Preparation")
	}
	return nil
}

func resetPublicHistory(snapshot *work.State) []string {
	commits := map[string]bool{}
	keep := func(code *work.CodeIdentity) {
		if code != nil {
			commits[code.Commit] = true
		}
	}
	for _, w := range snapshot.Workflows {
		for _, phase := range w.Phases {
			switch p := phase.(type) {
			case *work.PublicationPhase:
				if p.Operation != nil {
					keep(p.Operation.Public)
				}
				if p.DirectSync != nil {
					keep(p.DirectSync.Result)
				}
				if p.Integration != nil {
					keep(p.Integration.Result)
				}
			case *work.ExternalReviewPhase:
				for _, sync := range p.BranchSyncs {
					if sync.LocalCommit != nil {
						commits[sync.LocalCommit.Head] = true
					}
					commits[sync.Checkpoint.LocalHead] = true
				}
			}
		}
	}
	result := make([]string, 0, len(commits))
	for commit := range commits {
		result = append(result, commit)
	}
	slices.Sort(result)
	return result
}
