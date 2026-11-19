package review

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// The checkout and resolved pipeline are invocation-local observations, not a
// reservation or durable progress cursor.
type admittedCheckout struct {
	target              tasktarget.Target
	pipeline            Pipeline
	selected            *work.CodeOutput
	notice              *RevisionSelectionNotice
	replacesPausedCheck bool
}

func (s *Admission) inspect(intent AdmissionIntent) phaseexec.Operation {
	return phaseexec.Operation{Name: "admit selected review checkout", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		if err := validateAdmissionOwner(*current, intent); err != nil {
			return phaseexec.Prepared{}, err
		}
		frozen := *current
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			target, err := s.reviewTarget(frozen)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			observed := admittedCheckout{target: target}
			if err := s.admitCheckout(ctx, frozen, intent, &observed); err != nil {
				return checkoutAdmissionFailure(intent, err)
			}
			if !intent.Resume {
				observed.pipeline, err = s.freshPipeline(frozen, intent.PipelineName)
				if err != nil {
					return phaseexec.Result{Err: fmt.Errorf("task run %s: %w", s.Task.ID, err)}
				}
			}
			s.checkout = &observed
			return phaseexec.Result{}
		}}, nil
	}}
}

func checkoutAdmissionFailure(intent AdmissionIntent, err error) phaseexec.Result {
	if !intent.Resume || !errors.Is(err, gitmeta.ErrCheckoutIdentityMismatch) {
		return phaseexec.Result{Err: err}
	}
	cause := fmt.Errorf("admit committed review revision: %w", err)
	return phaseexec.Result{Update: AdmissionFailureChange(intent.Owner, cause), Err: cause}
}

func validateAdmissionOwner(current work.State, intent AdmissionIntent) error {
	if intent.Resume {
		_, _, err := currentReview(&current, intent.Owner)
		return err
	}
	return validateAdmissionInput(current, intent.Owner)
}

func (s *Admission) freshPipeline(current work.State, name string) (Pipeline, error) {
	view, err := taskstate.Project(current)
	if err != nil {
		return Pipeline{}, err
	}
	if latest, ok := taskstate.LatestReview(view); ok && strings.TrimSpace(name) == "" &&
		(latest.AutomatedBlockerDecisionInterrupted || endedWithFailedCheck(current.ActiveWorkflow())) {
		return resolveRecordedTaskReviewPipeline(s.Paths, latest)
	}
	return ResolveTaskReviewPipeline(s.Paths, s.Repository, name)
}

func (s *Admission) reviewTarget(current work.State) (tasktarget.Target, error) {
	view, err := taskstate.Project(current)
	if err != nil {
		return tasktarget.Target{}, err
	}
	facts, ok := taskstate.GitFactsFor(view)
	if !ok {
		return tasktarget.Target{}, fmt.Errorf("task has no Orpheus target; run `orpheus task run %s` first", s.Task.ID)
	}
	targets, err := tasktarget.ExpectedTargetsForTaskOrRecordedBranch(s.Repository, s.Task, facts.Branch, s.Paths)
	if err != nil {
		return tasktarget.Target{}, err
	}
	target, err := tasktarget.ClassifyGitFacts(facts, targets)
	if err != nil {
		return tasktarget.Target{}, fmt.Errorf("task has inconsistent taskstate target: %w", err)
	}
	metadataTarget, err := tasktarget.ClassifyMetadataTarget(s.Task.OrpheusMetadata(), targets)
	if err != nil {
		return tasktarget.Target{}, fmt.Errorf("task %s metadata target is invalid: %w", s.Task.ID, err)
	}
	if metadataTarget.Branch != target.Branch || metadataTarget.Worktree != target.Worktree {
		return tasktarget.Target{}, fmt.Errorf("task %s metadata target %q/%q does not mirror taskstate target %q/%q", s.Task.ID, metadataTarget.Branch, metadataTarget.Worktree, target.Branch, target.Worktree)
	}
	return target, nil
}

func (s *Admission) admitCheckout(ctx context.Context, current work.State, intent AdmissionIntent, observed *admittedCheckout) error {
	if intent.Resume {
		candidate, err := CommittedInput(taskstate.TaskState{Work: &current}, intent.Owner)
		if err != nil {
			return err
		}
		return s.Git.VerifyRevision(ctx, observed.target.Worktree, candidate.Identity, candidate.Revision)
	}
	w := current.ActiveWorkflow()
	output, err := w.Output(implementationOutputForAdmission(w, intent.Owner))
	if err != nil {
		return err
	}
	comparison := gitmeta.Revision{Commit: output.Commit, Tree: output.Tree, RetainedRef: output.RetainedRef}
	afterFailedCheck := endedWithFailedCheck(w)
	if afterFailedCheck {
		snapshot := taskstate.TaskState{Work: &current}
		prior, err := CommittedInput(snapshot, intent.Owner)
		if err != nil {
			return err
		}
		comparison = prior.Revision
		observed.replacesPausedCheck, err = waitingForAutomatedDecision(current, intent.Owner)
		if err != nil {
			return err
		}
	}
	owner := intent.Owner
	owner.Phase++
	target := observed.target
	selection, err := s.Git.SelectReviewRevision(ctx, gitmeta.ReviewRevisionRequest{
		Directory: target.Worktree, Branch: target.Branch, Ref: phaseexec.RevisionNamespace(owner), Comparison: comparison,
	})
	if err != nil {
		return err
	}
	revision := selection.Revision
	if observed.replacesPausedCheck && revision.Commit == comparison.Commit {
		return errors.New("paused check checkout returned to the reviewed commit; rerun to resume its decision")
	}
	if revision.Commit != comparison.Commit {
		observed.notice = &RevisionSelectionNotice{ComparedTo: comparison.Commit, AfterFailedCheck: afterFailedCheck, Selection: selection}
	}
	if revision.Commit != output.Commit {
		observed.selected = &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: revision.Commit, Tree: revision.Tree}, RetainedRef: revision.RetainedRef}
	}
	return nil
}
