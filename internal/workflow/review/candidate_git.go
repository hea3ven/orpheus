package review

import (
	"context"
	"fmt"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// ReviewCandidateGit observes exact committed review input and its destination.
type ReviewCandidateGit interface {
	VerifyRevision(context.Context, string, gitmeta.CheckoutIdentity, gitmeta.Revision) error
	ObserveDestination(context.Context, string, string) (work.Destination, error)
	DiffStat(context.Context, string, string, string) (string, error)
}

// LocalReviewCandidateGit uses ordinary local Git and the configured origin.
type LocalReviewCandidateGit struct{}

func (LocalReviewCandidateGit) VerifyRevision(ctx context.Context, dir string, identity gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	return gitmeta.VerifyRevision(ctx, dir, identity, revision)
}

func (LocalReviewCandidateGit) DiffStat(ctx context.Context, dir, baseline, revision string) (string, error) {
	return gitmeta.DiffStat(ctx, dir, baseline, revision)
}

func (LocalReviewCandidateGit) ObserveDestination(ctx context.Context, dir, branch string) (work.Destination, error) {
	commit, err := gitmeta.ObserveRemoteBranch(ctx, dir, branch)
	if err != nil {
		return work.Destination{}, err
	}
	return work.Destination{Remote: "origin", Branch: branch, Commit: commit}, nil
}

func CommittedInput(snapshot taskstate.TaskState, ref work.AttemptRef) (CommittedCandidate, error) {
	if snapshot.Work == nil || ref.Workflow < 0 || ref.Workflow >= len(snapshot.Work.Workflows) {
		return CommittedCandidate{}, fmt.Errorf("review has no workflow-owned scope")
	}
	w := &snapshot.Work.Workflows[ref.Workflow]
	if ref.Phase < 0 || ref.Phase >= len(w.Phases) {
		return CommittedCandidate{}, fmt.Errorf("invalid review phase")
	}
	p, ok := w.Phases[ref.Phase].(*work.ReviewPhase)
	if !ok {
		return CommittedCandidate{}, fmt.Errorf("expected review phase")
	}
	output, err := w.Output(p.Scope.Output)
	if err != nil {
		return CommittedCandidate{}, err
	}
	return CommittedCandidate{Identity: gitmeta.CheckoutIdentity{Commit: output.Commit, Ref: "refs/heads/" + w.Context().TaskBranch}, Revision: gitmeta.Revision{Commit: output.Commit, Tree: output.Tree, RetainedRef: output.RetainedRef}}, nil
}
