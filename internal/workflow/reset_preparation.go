package workflow

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// ResetGit separates confirmed cleanup from ordinary task setup.
type ResetGit interface {
	InspectWorkflowReset(context.Context, gitmeta.WorkflowResetRequest) (string, error)
	ResolveWorkflowReset(context.Context, gitmeta.WorkflowResetRequest) (gitmeta.Revision, error)
	ApplyWorkflowReset(context.Context, gitmeta.WorkflowResetRequest) error
}

func (LocalDispatchGit) InspectWorkflowReset(ctx context.Context, req gitmeta.WorkflowResetRequest) (string, error) {
	return gitmeta.InspectWorkflowReset(ctx, req)
}
func (LocalDispatchGit) ResolveWorkflowReset(ctx context.Context, req gitmeta.WorkflowResetRequest) (gitmeta.Revision, error) {
	return gitmeta.ResolveWorkflowReset(ctx, req)
}
func (LocalDispatchGit) ApplyWorkflowReset(ctx context.Context, req gitmeta.WorkflowResetRequest) error {
	return gitmeta.ApplyWorkflowReset(ctx, req)
}

func (s DispatchService) prepareReset(ctx context.Context, opts DispatchStartOptions, snapshot taskstate.TaskState) (gitmeta.TaskWorktreeSetupResult, error) {
	w := snapshot.Work.ActiveWorkflow()
	previous := snapshot.Work.ResetContext()
	if previous == nil {
		return gitmeta.TaskWorktreeSetupResult{}, errors.New("pending reset has no established checkout")
	}
	adapter := s.Git
	if adapter == nil {
		adapter = LocalDispatchGit{}
	}
	git, ok := adapter.(ResetGit)
	if !ok {
		return gitmeta.TaskWorktreeSetupResult{}, errors.New("git adapter does not support workflow reset")
	}
	store, ok := s.RunStore.(interface {
		RecordResetTarget(string, string, work.WorkContext) error
	})
	if !ok {
		return gitmeta.TaskWorktreeSetupResult{}, errors.New("state adapter does not support workflow reset")
	}
	ref := fmt.Sprintf("refs/orpheus/baselines/%s/%s/%d", hex.EncodeToString([]byte(snapshot.RepoID)), hex.EncodeToString([]byte(snapshot.TaskID)), snapshot.CurrentWorkflow)
	repo := opts.Source.Repository
	req := gitmeta.WorkflowResetRequest{RepoPath: repo.Path, Directory: previous.Directory, Branch: previous.TaskBranch, DefaultBranch: repo.DefaultBranch, Ref: ref}
	var target *work.WorkContext
	if p, ok := w.LastPhase().(*work.PreparationPhase); ok {
		target = p.ResetTarget
	}
	if target == nil {
		resolved, err := git.ResolveWorkflowReset(ctx, req)
		if err != nil {
			return gitmeta.TaskWorktreeSetupResult{}, err
		}
		target = &work.WorkContext{Directory: previous.Directory, TaskBranch: previous.TaskBranch, CheckpointRef: resolved.RetainedRef, Baseline: work.CodeIdentity{Commit: resolved.Commit, Tree: resolved.Tree}}
		if err := store.RecordResetTarget(repo.ID, opts.TaskID, *target); err != nil {
			return gitmeta.TaskWorktreeSetupResult{}, err
		}
	}
	req.Target = gitmeta.Revision{Commit: target.Baseline.Commit, Tree: target.Baseline.Tree, RetainedRef: target.CheckpointRef}
	req.RetainCommits = resetPublicHistory(snapshot.Work)
	if err := git.ApplyWorkflowReset(ctx, req); err != nil {
		return gitmeta.TaskWorktreeSetupResult{}, err
	}
	// This durable boundary consumes authorization before any backend/agent work.
	if err := s.RunStore.PrepareWork(repo.ID, opts.TaskID, *target); err != nil {
		return gitmeta.TaskWorktreeSetupResult{}, err
	}
	return gitmeta.TaskWorktreeSetupResult{Branch: target.TaskBranch, WorktreePath: target.Directory, StartingCommit: target.Baseline.Commit, StartingTree: target.Baseline.Tree, CheckpointRef: target.CheckpointRef, Lifecycle: gitmeta.TaskWorktreeLifecycleReused}, nil
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
