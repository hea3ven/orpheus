package review

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// Continuation captures only the forward requirements of a selected Review.
// Previously started slots and their original contracts remain historical facts.
// An instance belongs to one task invocation, not to a batch or repository.
type Continuation struct {
	Paths      state.Paths
	Repository task.Repository
	pipeline   *Pipeline
}

type ContinuationRequest struct{}
type ForwardRequirementsCaptured struct{}

// Next resolves the recorded pipeline and captures only its still-unstarted suffix.
func (s *Continuation) Next(current work.State, owner work.AttemptRef) (phaseexec.Selection[ContinuationRequest, ForwardRequirementsCaptured], error) {
	w, phase, err := currentReview(&current, owner)
	if err != nil {
		return phaseexec.Selection[ContinuationRequest, ForwardRequirementsCaptured]{}, err
	}
	if s.pipeline == nil {
		return phaseexec.Run[ContinuationRequest, ForwardRequirementsCaptured](phaseexec.Operation{
			Name: "resolve review continuation",
			Prepare: func(current *work.State) (phaseexec.Prepared, error) {
				latest, err := reviewAt(*current, owner)
				if err != nil {
					return phaseexec.Prepared{}, err
				}
				return phaseexec.Prepared{Run: func(context.Context) phaseexec.Result {
					pipeline, err := ResolvePausedTaskReviewPipeline(s.Paths, s.Repository, latest, "")
					if err == nil {
						s.pipeline = &pipeline
					}
					return phaseexec.Result{Err: err}
				}}, nil
			},
		}), nil
	}
	contract, err := forwardContract(*s.pipeline, w, phase, current, owner)
	if err != nil {
		return phaseexec.Selection[ContinuationRequest, ForwardRequirementsCaptured]{}, err
	}
	if reflect.DeepEqual(w.ReviewContracts[phase.Contract], contract) {
		return phaseexec.Return[ContinuationRequest](ForwardRequirementsCaptured{}), nil
	}
	version := current.Version
	return phaseexec.Run[ContinuationRequest, ForwardRequirementsCaptured](phaseexec.Operation{
		Name: "capture forward review requirements",
		Prepare: func(current *work.State) (phaseexec.Prepared, error) {
			changed, err := continueChange(owner, contract, version)(current)
			return phaseexec.Prepared{Changed: changed}, err
		},
	}), nil
}

func forwardContract(pipeline Pipeline, w *work.Workflow, phase *work.ReviewPhase, current work.State, owner work.AttemptRef) (work.ReviewContract, error) {
	paused, err := reviewAt(current, owner)
	if err != nil {
		return work.ReviewContract{}, err
	}
	start, err := ContinuationIndex(pipeline, paused)
	if err != nil {
		return work.ReviewContract{}, err
	}
	prior := w.ReviewContracts[phase.Contract]
	if len(phase.Steps) == 0 && prior.Slots[0].Kind != work.ReviewStepKind(pipeline.Steps[start].Kind) {
		return work.ReviewContract{}, fmt.Errorf("%w: current review step %q changed type from %q; restore its type in config.yaml", ErrInvalidExecution, paused.Step, prior.Slots[0].Kind)
	}
	// Slot addresses belong to retained history, not the live configuration's
	// prefix. A restarted slot can have several actual attempts at one address.
	prefix := 0
	for _, step := range phase.Steps {
		var slot int
		switch step := step.(type) {
		case *work.CheckStep:
			slot = step.Slot
		case *work.AgentReviewStep:
			slot = step.Slot
		case *work.ManualReviewStep:
			slot = step.Slot
		}
		prefix = max(prefix, slot+1)
	}
	contract := work.ReviewContract{PipelineKey: prior.PipelineKey, Slots: append([]work.ReviewSlot(nil), prior.Slots[:prefix]...)}
	if len(phase.Steps) > 0 {
		start++
	}
	for _, step := range pipeline.Steps[start:] {
		contract.Slots = append(contract.Slots, work.ReviewSlot{Kind: work.ReviewStepKind(step.Kind), ConfigKey: step.Name})
	}
	return contract, nil
}

// The existing version check protects the selected saved input. No started
// slot or historical definition is rewritten when the forward contract changes.
func continueChange(owner work.AttemptRef, contract work.ReviewContract, expected work.StateVersion) phaseexec.Change {
	contract.Slots = append([]work.ReviewSlot(nil), contract.Slots...)
	return func(current *work.State) (bool, error) {
		if current.Version != expected {
			return false, errors.New("review continuation is stale; inspect current work before retrying")
		}
		w, phase, err := currentReview(current, owner)
		if err != nil {
			return false, err
		}
		if reflect.DeepEqual(w.ReviewContracts[phase.Contract], contract) {
			return false, nil
		}
		phase.Contract = len(w.ReviewContracts)
		w.ReviewContracts = append(w.ReviewContracts, contract)
		return true, nil
	}
}

func reviewAt(current work.State, owner work.AttemptRef) (taskstate.ReviewAttempt, error) {
	if _, _, err := currentReview(&current, owner); err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	projected, err := taskstate.Project(current)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	latest, ok := taskstate.LatestReview(projected)
	if !ok {
		return taskstate.ReviewAttempt{}, fmt.Errorf("%w: current review phase is missing", ErrInvalidExecution)
	}
	return latest, nil
}
