package review

import (
	"context"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// PausedCheckCommitChanged observes the checkout before forward requirements
// can be captured. Fresh admission still verifies and retains the clean tip.
func PausedCheckCommitChanged(ctx context.Context, snapshot taskstate.TaskState, owner work.AttemptRef, boundary ReviewCandidateGit) (bool, error) {
	if snapshot.Work == nil || !endedWithFailedCheck(snapshot.Work.ActiveWorkflow()) {
		return false, nil
	}
	waiting, err := waitingForAutomatedDecision(*snapshot.Work, owner)
	if err != nil || !waiting {
		return false, err
	}
	candidate, err := CommittedInput(snapshot, owner)
	if err != nil {
		return false, err
	}
	if boundary == nil {
		boundary = LocalReviewCandidateGit{}
	}
	head, err := boundary.CheckoutIdentity(ctx, snapshot.Work.ActiveWorkflow().Context().Directory)
	if err != nil {
		return false, err
	}
	return head.Ref == candidate.Identity.Ref && head.Commit != candidate.Identity.Commit, nil
}

func waitingForAutomatedDecision(current work.State, owner work.AttemptRef) (bool, error) {
	latest, err := reviewAt(current, owner)
	return latest.Status == taskstate.ReviewStatusWaitingForAutomatedDecision, err
}

func endedWithFailedCheck(w *work.Workflow) bool {
	p, ok := w.LastPhase().(*work.ReviewPhase)
	if !ok || len(p.Steps) == 0 {
		return false
	}
	step, ok := p.Steps[len(p.Steps)-1].(*work.CheckStep)
	return ok && (step.StartFailure != nil || step.Result != nil && *step.Result.ExitCode != 0)
}
