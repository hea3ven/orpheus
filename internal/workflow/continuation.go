package workflow

import (
	"fmt"

	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// reviewContinuationIndex resolves the current configured name and type, never
// an actual-attempt address or a position saved from an earlier configuration.
func reviewContinuationIndex(pipeline review.Pipeline, paused taskstate.ReviewAttempt) (int, error) {
	kind := ""
	if len(paused.Steps) > 0 {
		last := paused.Steps[len(paused.Steps)-1]
		if last.Name != paused.Step {
			return 0, fmt.Errorf("%w: current review step disagrees with its owning attempt", review.ErrInvalidExecution)
		}
		kind = last.Kind
	}
	for index, step := range pipeline.Steps {
		if step.Name == paused.Step && (kind == "" || step.Kind == kind) {
			return index, nil
		}
	}
	return 0, fmt.Errorf("%w: review attempt %d in phase review, pipeline %q, cannot locate current step %q with type %q; restore that name and type in reviews.pipelines.%s in config.yaml", review.ErrInvalidExecution, paused.Attempt, pipeline.Name, paused.Step, kind, pipeline.Name)
}

// continueReview updates only the forward requirements of the live review. The
// immutable previous contract and every started slot/result remain retained.
// Other phases, especially accepted publication plans, never resolve old review
// configuration through this path.
func (s Controller) continueReview(base ReviewContext, snapshot taskstate.TaskState, route TaskRunRoute) (taskstate.TaskState, error) {
	if snapshot.Work == nil || (route.Action != TaskRunActionResumeReview && route.Action != TaskRunActionPreparePublication) {
		return snapshot, nil
	}
	paused, ok := taskstate.LatestReview(snapshot)
	if !ok {
		return snapshot, fmt.Errorf("%w: current review phase is missing", review.ErrInvalidExecution)
	}
	pipeline, err := ResolvePausedTaskReviewPipeline(s.Paths, base.Source.Repository, paused, "")
	if err != nil {
		return snapshot, err
	}
	start, err := reviewContinuationIndex(pipeline, paused)
	if err != nil {
		return snapshot, err
	}
	w := &snapshot.Work.Workflows[route.Owner.Workflow]
	phase := w.Phases[route.Owner.Phase].(*work.ReviewPhase)
	prior := w.ReviewContracts[phase.Contract]
	if len(phase.Steps) == 0 && prior.Slots[0].Kind != work.ReviewStepKind(pipeline.Steps[start].Kind) {
		return snapshot, fmt.Errorf("%w: current review step %q changed type from %q; restore its type in config.yaml", review.ErrInvalidExecution, paused.Step, prior.Slots[0].Kind)
	}
	// Slot addresses belong to retained history. Preserve their prefix, not the
	// prefix of live configuration, then capture only what is still ahead.
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
	if _, err := s.RunStore.ContinueReviewAt(*route.Owner, contract, snapshot.Work.Version); err != nil {
		return snapshot, err
	}
	return s.RunStore.Load(base.RepoID(), base.TaskID())
}

// Validate again at the response boundary: configuration may have changed while
// the prompt was attached, without changing the interaction's state version.
func (s Controller) validateResponseTarget(base ReviewContext, snapshot work.State, pending *Interaction, id InteractionID) error {
	if pending == nil || !pending.ID.Owner.Equal(id.Owner) || pending.ID.Kind != id.Kind || pending.ID.Version != id.Version {
		return ErrStaleInteraction
	}
	if id.Kind != InteractionManualReview && id.Kind != InteractionFindings {
		return nil
	}
	projected, err := taskstate.Project(snapshot)
	if err != nil {
		return err
	}
	paused, ok := taskstate.LatestReview(projected)
	if !ok {
		return ErrStaleInteraction
	}
	pipeline, err := ResolvePausedTaskReviewPipeline(s.Paths, base.Source.Repository, paused, "")
	if err != nil {
		return err
	}
	_, err = reviewContinuationIndex(pipeline, paused)
	return err
}
