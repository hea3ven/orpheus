package work

import (
	"fmt"
	"time"
)

// Required positions are zero-based integers; zero addresses the first element.
// Optional positions and references use pointers, with nil meaning absent.
// Decoders must reject omitted required fields before constructing these values.

// OutputRef addresses the output owned by one implementation phase.
type OutputRef struct{ Phase int }

// ReviewRef addresses one review occurrence in the owning workflow.
type ReviewRef struct{ Phase int }

// PublicationRef addresses one publication occurrence in the owning workflow.
type PublicationRef struct{ Phase int }

// ExecutionRef selects one implementation, review step, or branch-sync resolver.
// Step and BranchSync are mutually exclusive; nil for both means implementation.
type ExecutionRef struct {
	Phase      int
	Step       *int
	BranchSync *int
}

// FindingRef addresses a finding owned by a task-review step.
type FindingRef struct {
	Phase   int
	Step    int
	Finding int
}

// AttemptRef crosses a callback boundary. Workflow prevents old children from
// attaching after reset; repository/task identity prevents cross-task attachment.
type AttemptRef struct {
	RepositoryID string
	TaskID       string
	Workflow     int
	Phase        int
	Step         *int
	BranchSync   *int
}

func element[T any](items []T, index int) (*T, error) {
	if index < 0 || index >= len(items) {
		return nil, fmt.Errorf("out-of-range index")
	}
	return &items[index], nil
}

func phaseAs[T Phase](w *Workflow, index int) (T, error) {
	var zero T
	if w == nil {
		return zero, fmt.Errorf("workflow is required")
	}
	phase, err := element(w.Phases, index)
	if err != nil {
		return zero, err
	}
	value, ok := (*phase).(T)
	if !ok {
		return zero, fmt.Errorf("phase has the wrong kind")
	}
	return value, nil
}

// Output resolves captured Git code through the same tree, including explicit reuse.
func (w *Workflow) Output(ref OutputRef) (*CodeOutput, error) {
	output, _, err := w.resolveOutput(ref)
	return output, err
}

// resolveOutput retains the safe boundary of every effort along explicit reuse,
// not just the original producer's older exit or the newest effort's handoff.
func (w *Workflow) resolveOutput(ref OutputRef) (*CodeOutput, time.Time, error) {
	var boundary time.Time
	seen := make(map[int]bool)
	for {
		if seen[ref.Phase] {
			return nil, boundary, fmt.Errorf("cyclic output reuse")
		}
		seen[ref.Phase] = true
		phase, err := phaseAs[*ImplementationPhase](w, ref.Phase)
		if err != nil || phase == nil || phase.Output == nil {
			return nil, boundary, fmt.Errorf("output reference does not identify captured implementation output")
		}
		if err := captureProblem(phase); err != nil {
			return nil, boundary, err
		}
		exit := phase.Execution.Process.(*ExitedProcess) // captureProblem requires an observed successful exit.
		if exit.At.After(boundary) {
			boundary = exit.At
		}
		if code, ok := phase.Output.(*CodeOutput); ok {
			return code, boundary, validateCodeOutput(code)
		}
		reused, ok := phase.Output.(*ReusedOutput)
		if !ok || reused == nil || phase.Input == nil || reused.Input != *phase.Input {
			return nil, boundary, fmt.Errorf("unchanged output must reuse this effort's exact input")
		}
		if reused.Input.Phase < 0 || reused.Input.Phase >= ref.Phase {
			return nil, boundary, fmt.Errorf("output reuse requires an earlier input")
		}
		ref = reused.Input
	}
}

// Execution resolves the process at its owning effort, not by a generated ID.
func (w *Workflow) Execution(ref ExecutionRef) (*AgentExecution, error) {
	if ref.Step != nil && ref.BranchSync != nil {
		return nil, fmt.Errorf("execution cannot select both a review step and branch sync")
	}
	if ref.BranchSync != nil {
		p, err := phaseAs[*ExternalReviewPhase](w, ref.Phase)
		if err != nil || p == nil {
			return nil, fmt.Errorf("execution reference requires external review")
		}
		sync, err := element(p.BranchSyncs, *ref.BranchSync)
		if err != nil || sync.ConflictResolution == nil {
			return nil, fmt.Errorf("execution reference requires branch-sync conflict resolution")
		}
		return &sync.ConflictResolution.Execution, nil
	}
	if ref.Step == nil {
		p, err := phaseAs[*ImplementationPhase](w, ref.Phase)
		if err != nil || p == nil {
			return nil, fmt.Errorf("execution reference requires implementation")
		}
		return &p.Execution, nil
	}
	p, err := phaseAs[*ReviewPhase](w, ref.Phase)
	if err != nil || p == nil {
		return nil, fmt.Errorf("execution reference requires review")
	}
	step, err := element(p.Steps, *ref.Step)
	if err != nil {
		return nil, err
	}
	agent, ok := (*step).(*AgentReviewStep)
	if !ok || agent == nil {
		return nil, fmt.Errorf("execution reference requires agent review")
	}
	return &agent.Execution, nil
}

// Finding follows containment; scope/output and origin are not repeated fields.
func (w *Workflow) Finding(ref FindingRef) (*Finding, error) {
	p, err := phaseAs[*ReviewPhase](w, ref.Phase)
	if err != nil || p == nil {
		return nil, fmt.Errorf("finding reference requires review")
	}
	step, err := element(p.Steps, ref.Step)
	if err != nil {
		return nil, err
	}
	return element(stepFindings(*step), ref.Finding)
}

// Equal compares containment values, not the allocation identity of optional
// index pointers from independently decoded snapshots.
func (ref AttemptRef) Equal(other AttemptRef) bool {
	return ref.RepositoryID == other.RepositoryID && ref.TaskID == other.TaskID && ref.Workflow == other.Workflow && ref.Phase == other.Phase && sameOptionalIndex(ref.Step, other.Step) && sameOptionalIndex(ref.BranchSync, other.BranchSync)
}
func sameOptionalIndex(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
