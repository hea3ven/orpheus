package work

import "time"

// Completion is the handoff owned by this effort, not a separately keyed artifact.
type Completion struct {
	Summary              string
	Description          string
	DetailedDescription  string
	TechnicalExplanation string
	CompletedAt          time.Time
}

// Output is captured Git code or explicit unchanged-input reuse.
type Output interface{ output() }

// CodeOutput retains native Git identity and a ref that keeps the object reachable.
type CodeOutput struct {
	CodeIdentity
	RetainedRef string
}

// ReusedOutput refers to the unchanged input while the new effort keeps its handoff.
type ReusedOutput struct{ Input OutputRef }

func (*CodeOutput) output()   {}
func (*ReusedOutput) output() {}

// ReviewScope owns the evaluated requirements and output/comparison endpoints.
// The full-task baseline comes from preparation, not a synchronized copy here.
type ReviewScope struct {
	Requirements string
	Output       OutputRef
	DeltaFrom    *OutputRef
	Destination  *Destination
}

// Destination is the observed integration context, distinct from the task baseline.
type Destination struct {
	Remote string
	Branch string
	Commit string
}

// FindingClassification preserves reported severity separately from disposition.
type FindingClassification string

const (
	FindingBlocking     FindingClassification = "blocking"
	FindingAdvisory     FindingClassification = "advisory"
	FindingSeparateTask FindingClassification = "separate_task"
)

// Finding inherits origin/output from its reporting task-review step.
// Reported content is immutable; Dispositions retains append-only decisions.
// Repair targets and later verifications live on those later efforts, not here.
type Finding struct {
	Title          string
	Classification FindingClassification
	Description    string
	Dispositions   []FindingDisposition
}

// Disposition derives the current decision without discarding earlier choices.
// An empty history has no decision and cannot satisfy a blocking finding.
func (f *Finding) Disposition() FindingDisposition {
	return f.dispositionAt(time.Time{})
}

// dispositionAt selects decisions available at the cutoff; zero includes all.
// Only the initial reported RepairRequired choice may lack an observation time.
func (f *Finding) dispositionAt(cutoff time.Time) FindingDisposition {
	for i := len(f.Dispositions) - 1; i >= 0; i-- {
		disposition := f.Dispositions[i]
		if observedBy(dispositionTime(disposition), cutoff) {
			return disposition
		}
	}
	return nil
}

// FindingDisposition records a domain outcome without a generic interaction.
type FindingDisposition interface{ findingDisposition() }

// RepairRequired retains an issue for targeted work; it does not satisfy a blocker.
// At may be absent only for the initial reported choice, not a later reopening.
type RepairRequired struct{ At time.Time }

// WaivedFinding records an explicit, reasoned exception to a reported issue.
type WaivedFinding struct{ Waiver }

// DismissedFinding records a reasoned assessment that the issue does not apply.
type DismissedFinding struct{ Waiver }

// DowngradedFinding retains original severity and the reason it no longer blocks.
type DowngradedFinding struct{ Waiver }

// AddressedFinding records an explicit manual resolution, not inferred review success.
type AddressedFinding struct{ Waiver }

func (*RepairRequired) findingDisposition()    {}
func (*WaivedFinding) findingDisposition()     {}
func (*DismissedFinding) findingDisposition()  {}
func (*DowngradedFinding) findingDisposition() {}
func (*AddressedFinding) findingDisposition()  {}

// FindingResolution is an explicit task-review finding assessment, not PR merge.
type FindingResolution string

const (
	FindingResolved   FindingResolution = "resolved"
	FindingUnresolved FindingResolution = "unresolved"
)

// FindingVerification belongs to the later reviewer reporting it. Its scope and
// safe execution determine authority; no overall pass fabricates this assessment.
type FindingVerification struct {
	Finding    FindingRef
	Resolution FindingResolution
	Evidence   string
}

// FeatureCompletion returns the first safely captured implementation's intent.
// Containment and recorded order select it; later repair handoffs cannot replace it.
func (w *Workflow) FeatureCompletion() *Completion {
	for i, phase := range w.Phases {
		p, ok := phase.(*ImplementationPhase)
		if !ok || p == nil || p.Output == nil {
			continue
		}
		if _, err := w.Output(OutputRef{Phase: i}); err != nil {
			return nil
		}
		return p.Completion
	}
	return nil
}

// LatestOutput returns the last captured implementation output, independently of
// which earlier output is still published. It never selects pending agent work.
func (w *Workflow) LatestOutput() (OutputRef, bool) {
	for i := len(w.Phases) - 1; i >= 0; i-- {
		p, ok := w.Phases[i].(*ImplementationPhase)
		if !ok || p == nil || p.Output == nil {
			continue
		}
		ref := OutputRef{Phase: i}
		if _, err := w.Output(ref); err != nil {
			return OutputRef{}, false
		}
		return ref, true
	}
	return OutputRef{}, false
}
