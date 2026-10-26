// Package work defines task-owned work history and invariants without I/O or
// progression. Containment establishes ownership; append-only indexes address
// evidence across the tree. Aggregate outcomes are derived, never stored twice.
package work

// StateVersion is the persistence conflict counter, not an output or schema version.
type StateVersion uint64

// State owns one task's local work history, not its source-owned lifecycle.
type State struct {
	Version      StateVersion
	RepositoryID string
	TaskID       string
	Workflows    []Workflow
}

// Workflow owns typed occurrences and immutable captured review requirements.
// Reset appends a workflow. It never replaces a referenced slot in history.
type Workflow struct {
	ReviewContracts []ReviewContract
	Phases          []Phase
}

// WorkContext separates the task's location and retained baseline from delivery.
type WorkContext struct {
	Directory     string
	TaskBranch    string
	CheckpointRef string
	Baseline      CodeIdentity
}

// CodeIdentity names actual Git output, independently of a publication commit.
type CodeIdentity struct {
	Commit string
	Tree   string
}

// ActiveWorkflow returns the last workflow unless its closure is recorded.
// Earlier workflows are history, even if they stopped at a pending manual gate.
func (s *State) ActiveWorkflow() *Workflow {
	if s == nil || len(s.Workflows) == 0 {
		return nil
	}
	w := &s.Workflows[len(s.Workflows)-1]
	if w.closed() {
		return nil
	}
	return w
}

// LastPhase returns only the last recorded occurrence, including terminal work.
func (w *Workflow) LastPhase() Phase {
	if w == nil || len(w.Phases) == 0 {
		return nil
	}
	return w.Phases[len(w.Phases)-1]
}

// Context returns the established preparation context, not a current Git guess.
func (w *Workflow) Context() *WorkContext {
	for _, phase := range w.Phases {
		if p, ok := phase.(*PreparationPhase); ok && p != nil && p.EstablishedAt != nil {
			return p.Context
		}
	}
	return nil
}

func (w *Workflow) closed() bool {
	for _, phase := range w.Phases {
		if p, ok := phase.(*FinalizationPhase); ok && p != nil && p.Closure != nil {
			return true
		}
	}
	return false
}
