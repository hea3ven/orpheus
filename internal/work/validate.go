package work

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Validate checks a snapshot without choosing progression or causing effects.
// Incomplete evidence never substitutes for required approval or delivery.
func (s *State) Validate() error {
	if s == nil {
		return fmt.Errorf("state is required")
	}
	v := validator{}
	v.check(strings.TrimSpace(s.RepositoryID) != "" && strings.TrimSpace(s.TaskID) != "", "repository and task identities are required")
	for wi := range s.Workflows {
		w := &s.Workflows[wi]
		child := validator{w: w, live: wi == len(s.Workflows)-1 && !w.closed()}
		child.workflow()
		if err := child.err(); err != nil {
			v.errors = append(v.errors, fmt.Errorf("workflow %d: %w", wi, err))
			continue
		}
		for pi, phase := range w.Phases {
			if p, ok := phase.(*PublicationPhase); ok && p != nil {
				if err := s.validateConsent(wi, pi, p); err != nil {
					v.errors = append(v.errors, err)
				}
			}
		}
	}
	return v.err()
}

type validator struct {
	w      *Workflow
	phase  int
	step   int
	sync   int
	live   bool
	errors []error
}

func (v *validator) check(ok bool, format string, args ...any) {
	if !ok {
		v.errors = append(v.errors, fmt.Errorf(format, args...))
	}
}

func (v *validator) err() error { return errors.Join(v.errors...) }

func (v *validator) workflow() {
	for _, contract := range v.w.ReviewContracts {
		v.contract(contract)
	}
	established := 0
	closed := false
	for i, phase := range v.w.Phases {
		v.phase, v.step, v.sync = i, -1, -1
		if p, ok := phase.(*PreparationPhase); ok && p != nil && p.EstablishedAt != nil {
			established++
		}
		v.check(!closed, "work cannot be appended after source closure")
		v.phaseValue(phase)
		if p, ok := phase.(*FinalizationPhase); ok && p != nil && p.Closure != nil {
			closed = true
		}
	}
	v.check(established <= 1, "workflow has more than one established preparation context")
}

func (v *validator) phaseValue(phase Phase) {
	switch phase := phase.(type) {
	case *PreparationPhase:
		v.preparation(phase)
	case *ImplementationPhase:
		v.implementation(phase)
	case *ReviewPhase:
		v.review(phase)
	case *PublicationPhase:
		v.publication(phase)
	case *ExternalReviewPhase:
		v.externalReview(phase)
	case *FinalizationPhase:
		v.finalization(phase)
	default:
		v.check(false, "phase is required")
	}
}

func (v *validator) preparation(p *PreparationPhase) {
	if p == nil {
		v.check(false, "preparation is required")
		return
	}
	v.check((p.Context == nil) == (p.EstablishedAt == nil), "established preparation requires both context and observation time")
	if p.Context == nil {
		return
	}
	c := p.Context
	v.check(p.EstablishedAt != nil && !p.EstablishedAt.IsZero(), "preparation observation time is required")
	v.check(filepath.IsAbs(c.Directory), "work directory must be absolute")
	v.check(strings.TrimSpace(c.TaskBranch) != "" && strings.HasPrefix(c.CheckpointRef, "refs/"), "context requires task branch and owned checkpoint ref")
	v.code(c.Baseline)
}

func (v *validator) prepared() {
	for i, phase := range v.w.Phases {
		if p, ok := phase.(*PreparationPhase); ok && p != nil && p.Context != nil && p.EstablishedAt != nil && i < v.phase {
			v.preparation(p)
			return
		}
	}
	v.check(false, "work requires earlier established preparation")
}

func (v *validator) prior(index int, label string) bool {
	ok := index >= 0 && index < v.phase
	v.check(ok, "%s requires an earlier phase index", label)
	return ok
}

func (v *validator) text(text, label string) {
	v.check(strings.TrimSpace(text) != "", "%s requires nonblank text", label)
}

func (v *validator) code(code CodeIdentity) {
	v.check(strings.TrimSpace(code.Commit) != "" && strings.TrimSpace(code.Tree) != "", "code requires commit and tree identity")
}

func (v *validator) destination(d *Destination) {
	v.check(d != nil && d.Remote != "" && d.Branch != "" && d.Commit != "", "destination requires remote, branch, and observed commit")
}

// CheckActiveAttempt accepts new agent/step results only for the last active owner.
// It does not authorize output capture, idempotent replay, or an atomic save.
func (s *State) CheckActiveAttempt(ref AttemptRef) error {
	if err := s.Validate(); err != nil {
		return err
	}
	w := s.ActiveWorkflow()
	if w == nil || ref.RepositoryID != s.RepositoryID || ref.TaskID != s.TaskID || ref.Workflow < 0 || ref.Workflow != len(s.Workflows)-1 || ref.Phase < 0 || ref.Phase != len(w.Phases)-1 {
		return fmt.Errorf("attempt does not own the active last work")
	}
	if !activeEffort(w.LastPhase(), ref.Step, ref.BranchSync) {
		return fmt.Errorf("attempt does not own the active last work")
	}
	return nil
}

func activeEffort(phase Phase, step, branchSync *int) bool {
	switch phase := phase.(type) {
	case *ImplementationPhase:
		return phase != nil && step == nil && branchSync == nil && running(&phase.Execution)
	case *ReviewPhase:
		if phase == nil || branchSync != nil || step == nil || *step < 0 || *step != len(phase.Steps)-1 {
			return false
		}
		switch attempt := phase.Steps[*step].(type) {
		case *AgentReviewStep:
			return attempt != nil && running(&attempt.Execution)
		case *CheckStep:
			return attempt != nil && attempt.Result == nil && attempt.StartFailure == nil
		case *ManualReviewStep:
			return attempt != nil && attempt.Decision == nil && !manualCommandFailed(attempt.CommandResult)
		}
	case *ExternalReviewPhase:
		if phase == nil || step != nil || branchSync == nil || *branchSync < 0 || *branchSync != len(phase.BranchSyncs)-1 {
			return false
		}
		resolution := phase.BranchSyncs[*branchSync].ConflictResolution
		return resolution != nil && running(&resolution.Execution)
	}
	return false
}
