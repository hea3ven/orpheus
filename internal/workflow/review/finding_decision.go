package review

import (
	"errors"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// FindingDecisionChange appends an operator decision to the original reporting
// owner. It also supports prior blockers at the captured pre-review boundary.
func FindingDecisionChange(actor work.AttemptRef, target work.FindingRef, decision work.FindingDisposition) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		w, err := findingDecisionOwner(current, actor, target)
		if err != nil {
			return false, err
		}
		finding, err := w.Finding(target)
		if err != nil {
			return false, err
		}
		view, err := findingView(*current, actor, target)
		if err != nil {
			return false, err
		}
		if err := validateFindingChoice(view, decision); err != nil {
			return false, err
		}
		stamped, err := observedDecision(decision, time.Now().UTC())
		if err != nil {
			return false, err
		}
		finding.Dispositions = append(finding.Dispositions, stamped)
		return true, nil
	}
}

func findingView(current work.State, actor work.AttemptRef, target work.FindingRef) (taskstate.ReviewFinding, error) {
	view, err := taskstate.Project(current)
	if err != nil {
		return taskstate.ReviewFinding{}, err
	}
	for _, attempt := range view.Reviews {
		if attempt.Address.Workflow != actor.Workflow || attempt.Address.Phase != target.Phase {
			continue
		}
		for _, finding := range attempt.Findings {
			if finding.Address != nil && *finding.Address == target {
				return finding, nil
			}
		}
	}
	return taskstate.ReviewFinding{}, errors.New("finding has no retained review projection")
}

func validateFindingChoice(view taskstate.ReviewFinding, choice work.FindingDisposition) error {
	if view.Superseded || view.NonAuthoritative {
		return errors.New("audit-only findings cannot authorize repair or a live disposition")
	}
	if _, promoted := choice.(*work.PromotedFinding); promoted {
		resolution := taskstate.ResolveReviewFinding(view)
		if view.Type != taskstate.FindingTypeAdvisory || (resolution != taskstate.ReviewFindingResolutionNonBlocking && resolution != taskstate.ReviewFindingResolutionDowngraded) {
			return errors.New("promotion requires an unresolved advisory opportunity")
		}
		return nil
	}
	if !taskstate.IsOpenBlockingReviewFinding(view) {
		return errors.New("finding decision requires an unresolved blocking finding")
	}
	return nil
}

func findingDecisionOwner(current *work.State, actor work.AttemptRef, target work.FindingRef) (*work.Workflow, error) {
	w := current.ActiveWorkflow()
	if w == nil || actor.RepositoryID != current.RepositoryID || actor.TaskID != current.TaskID || actor.Workflow != len(current.Workflows)-1 || actor.Phase != len(w.Phases)-1 || actor.BranchSync != nil || target.Phase > actor.Phase {
		return nil, errors.New("finding decision no longer owns the current workflow")
	}
	switch phase := w.LastPhase().(type) {
	case *work.ImplementationPhase:
		if actor.Step != nil || phase.Output == nil {
			return nil, errors.New("pre-review decisions require safely captured implementation output")
		}
	case *work.ReviewPhase:
		if err := reviewDecisionActor(current, actor, target, phase); err != nil {
			return nil, err
		}
	default:
		return nil, errors.New("finding decision requires review or captured pre-review work")
	}
	return w, nil
}

func reviewDecisionActor(current *work.State, actor work.AttemptRef, target work.FindingRef, phase *work.ReviewPhase) error {
	if actor.Step == nil {
		if len(phase.Steps) == 0 {
			return nil
		}
		switch step := phase.Steps[len(phase.Steps)-1].(type) {
		case *work.AgentReviewStep:
			if _, running := step.Execution.Process.(*work.RunningProcess); running {
				return errors.New("a live reviewer owns this boundary")
			}
		case *work.CheckStep:
			if step.Result == nil && step.StartFailure == nil {
				return errors.New("a pending check owns this boundary")
			}
		}
		return nil
	}
	if *actor.Step < 0 || *actor.Step != len(phase.Steps)-1 {
		return errors.New("decision came from a superseded step")
	}
	if _, manual := phase.Steps[*actor.Step].(*work.ManualReviewStep); manual {
		return current.CheckActiveAttempt(actor)
	}
	if target.Phase != actor.Phase || target.Step != *actor.Step {
		return errors.New("automated decision is outside its reporting step")
	}
	return nil
}

func observedDecision(decision work.FindingDisposition, at time.Time) (work.FindingDisposition, error) {
	switch choice := decision.(type) {
	case *work.RepairRequired:
		if choice != nil {
			return &work.RepairRequired{At: at}, nil
		}
	case *work.PromotedFinding:
		if choice != nil {
			return &work.PromotedFinding{At: at}, nil
		}
	case *work.WaivedFinding:
		if choice != nil {
			return &work.WaivedFinding{Waiver: work.Waiver{At: at, Reason: choice.Reason}}, nil
		}
	case *work.DowngradedFinding:
		if choice != nil {
			return &work.DowngradedFinding{Waiver: work.Waiver{At: at, Reason: choice.Reason}}, nil
		}
	case *work.DismissedFinding:
		if choice != nil {
			return &work.DismissedFinding{Waiver: work.Waiver{At: at, Reason: choice.Reason}}, nil
		}
	case *work.AddressedFinding:
		if choice != nil {
			return &work.AddressedFinding{Waiver: work.Waiver{At: at, Reason: choice.Reason}}, nil
		}
	}
	return nil, errors.New("an explicit finding decision is required")
}
