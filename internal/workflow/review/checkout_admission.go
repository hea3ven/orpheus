package review

import (
	"context"
	"fmt"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// The checkout and resolved pipeline are invocation-local observations, not a
// reservation or durable progress cursor. Failed admission leaves history intact.
type admittedCheckout struct {
	target   tasktarget.Target
	pipeline Pipeline
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
			if err := s.verifyCheckout(ctx, frozen, target.Worktree); err != nil {
				return phaseexec.Result{Err: err}
			}
			observed := admittedCheckout{target: target}
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
	if latest, ok := taskstate.LatestReview(view); ok && latest.AutomatedBlockerDecisionInterrupted && name == "" {
		name = latest.Pipeline
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

func (s *Admission) verifyCheckout(ctx context.Context, current work.State, workdir string) error {
	view, err := taskstate.Project(current)
	if err != nil {
		return err
	}
	run, ok := taskstate.LatestRun(view)
	if !ok {
		return fmt.Errorf("review requires a workflow-owned implementation")
	}
	w := current.ActiveWorkflow()
	if w == nil {
		return fmt.Errorf("review requires an open workflow")
	}
	effort, ok := w.Phases[run.Address.Phase].(*work.ImplementationPhase)
	if !ok || effort.Output == nil {
		return fmt.Errorf("review requires recorded implementation output; finish implementation capture before review")
	}
	output, err := w.Output(work.OutputRef{Phase: run.Address.Phase})
	if err != nil {
		return err
	}
	boundary := s.Git
	if boundary == nil {
		boundary = LocalReviewCandidateGit{}
	}
	return boundary.VerifyRevision(ctx, workdir, gitmeta.CheckoutIdentity{Commit: output.Commit, Ref: "refs/heads/" + w.Context().TaskBranch}, gitmeta.Revision{Commit: output.Commit, Tree: output.Tree, RetainedRef: output.RetainedRef})
}
