package workflow

import (
	"context"
	"fmt"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
)

// ReviewCandidateGit observes exact committed review input and its destination.
type ReviewCandidateGit interface {
	VerifyRevision(context.Context, string, gitmeta.CheckoutIdentity, gitmeta.Revision) error
	ObserveDestination(context.Context, string, string) (work.Destination, error)
}

// LocalReviewCandidateGit uses ordinary local Git and the configured origin.
type LocalReviewCandidateGit struct{}

func (LocalReviewCandidateGit) VerifyRevision(ctx context.Context, dir string, identity gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	return gitmeta.VerifyRevision(ctx, dir, identity, revision)
}

func (LocalReviewCandidateGit) ObserveDestination(ctx context.Context, dir, branch string) (work.Destination, error) {
	commit, err := gitmeta.ObserveRemoteBranch(ctx, dir, branch)
	if err != nil {
		return work.Destination{}, err
	}
	return work.Destination{Remote: "origin", Branch: branch, Commit: commit}, nil
}

func (s ReviewLifecycleService) outputService() ImplementationService {
	boundary := s.OutputGit
	if boundary == nil {
		boundary = LocalOutputGit{}
	}
	return ImplementationService{Paths: s.Paths, Store: workstate.NewStore(s.Paths), Git: boundary, Logger: s.Logger}
}

func (s ReviewLifecycleService) inspectReviewCandidate(ctx context.Context, reviewCtx ReviewContext, workdir string) error {
	snapshot, err := s.RunStore.Load(reviewCtx.RepoID(), reviewCtx.TaskID())
	if err != nil {
		return fmt.Errorf("load task state: %w", err)
	}
	run, ok := taskstate.LatestRun(snapshot)
	if !ok || snapshot.Work == nil {
		return fmt.Errorf("review requires a workflow-owned implementation")
	}
	w := snapshot.Work.ActiveWorkflow()
	if w == nil {
		return fmt.Errorf("review requires an open workflow")
	}
	effort, ok := w.Phases[run.Address.Phase].(*work.ImplementationPhase)
	if !ok {
		return fmt.Errorf("review requires implementation output")
	}
	if effort.Output == nil {
		if _, err := s.outputService().Capture(ctx, run.Address); err != nil {
			return fmt.Errorf("retain completed implementation before review: %w", err)
		}
		snapshot, err = s.RunStore.Load(reviewCtx.RepoID(), reviewCtx.TaskID())
		if err != nil {
			return err
		}
		w = snapshot.Work.ActiveWorkflow()
	}
	output, err := w.Output(work.OutputRef{Phase: run.Address.Phase})
	if err != nil {
		return err
	}
	boundary := s.CandidateGit
	if boundary == nil {
		boundary = LocalReviewCandidateGit{}
	}
	return boundary.VerifyRevision(ctx, workdir, gitmeta.CheckoutIdentity{Commit: output.Commit, Ref: "refs/heads/" + w.Context().TaskBranch}, gitmeta.Revision{Commit: output.Commit, Tree: output.Tree, RetainedRef: output.RetainedRef})
}

func committedCandidate(snapshot taskstate.TaskState, ref work.AttemptRef) (review.CommittedCandidate, error) {
	if snapshot.Work == nil || ref.Workflow < 0 || ref.Workflow >= len(snapshot.Work.Workflows) {
		return review.CommittedCandidate{}, fmt.Errorf("review has no workflow-owned scope")
	}
	w := &snapshot.Work.Workflows[ref.Workflow]
	if ref.Phase < 0 || ref.Phase >= len(w.Phases) {
		return review.CommittedCandidate{}, fmt.Errorf("invalid review phase")
	}
	p, ok := w.Phases[ref.Phase].(*work.ReviewPhase)
	if !ok {
		return review.CommittedCandidate{}, fmt.Errorf("expected review phase")
	}
	output, err := w.Output(p.Scope.Output)
	if err != nil {
		return review.CommittedCandidate{}, err
	}
	return review.CommittedCandidate{Identity: gitmeta.CheckoutIdentity{Commit: output.Commit, Ref: "refs/heads/" + w.Context().TaskBranch}, Revision: gitmeta.Revision{Commit: output.Commit, Tree: output.Tree, RetainedRef: output.RetainedRef}}, nil
}
