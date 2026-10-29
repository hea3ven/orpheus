package work

import "time"

// Phase is a closed alternative, not a set of independently optional payloads.
// Only ReviewPhase owns steps. Display names and statuses are not stored here.
type Phase interface{ phase() }

// PreparationPhase records establishment of one immutable work context.
type PreparationPhase struct {
	Context       *WorkContext
	EstablishedAt *time.Time
}

// ImplementationPhase owns one agent effort, its handoff, and captured output.
// A new agent attempt appends another occurrence; it cannot overwrite this one.
type ImplementationPhase struct {
	Input          *OutputRef
	TargetFindings []FindingRef
	// AdvisoryFindings are selected best-effort input, not repair claims.
	AdvisoryFindings []FindingRef
	Execution        AgentExecution
	Completion       *Completion
	Output           Output
}

// ReviewPhase owns the actual attempts of its referenced required-step contract.
// Scope is inherited by findings; there is no second aggregate result to edit.
type ReviewPhase struct {
	Abort             *AbortedReview
	ExecutionFailure  *ReviewExecutionFailure
	Contract          int
	Scope             ReviewScope
	StartedAt         time.Time
	AdmissionFailure  *ReadOnlyFailure
	RepairBudgetStops []RepairBudgetStop
	Steps             []ReviewStep
}

// PublicationPhase owns immutable intent, explicit consent, and observed delivery.
// Idempotent find/import/create retries establish Delivery in this same record.
type PublicationPhase struct {
	Plan     PublicationPlan
	Consent  *PublicationConsent
	Delivery Delivery
}

// ExternalReviewPhase owns post-publication PR branch maintenance, not PR feedback.
// Each new synchronization effort appends; push retries finish the same entry.
type ExternalReviewPhase struct {
	Publication PublicationRef
	BranchSyncs []BranchSync
}

// FinalizationPhase owns actual closure and separately appendable cleanup evidence.
// Acceptance supplies the publication; there is no duplicate delivery selector.
type FinalizationPhase struct {
	Acceptance Acceptance
	Closure    *TaskClosure
	Cleanup    []CleanupObservation
}

func (*PreparationPhase) phase()    {}
func (*ImplementationPhase) phase() {}
func (*ReviewPhase) phase()         {}
func (*PublicationPhase) phase()    {}
func (*ExternalReviewPhase) phase() {}
func (*FinalizationPhase) phase()   {}

// ReviewStep is a closed kind of actual attempt. Slot addresses the configurable
// contract entry, not this attempt's identity. Retry appends another step.
type ReviewStep interface{ reviewStep() }

// CheckStep retains its exit result even when explicitly waived.
type CheckStep struct {
	Slot             int
	Result           *CheckResult
	StartFailure     *StartFailedProcess
	Findings         []Finding
	Waiver           *Waiver
	ReadOnlyFailures []ReadOnlyFailure
	DecisionPauses   []AutomatedDecisionPause
}

// CheckResult is the observed result of a configured check.
type CheckResult struct {
	ExitCode   *int
	FinishedAt time.Time
}

// Waiver is a direct domain disposition, not a question/answer protocol.
type Waiver struct {
	Reason string
	At     time.Time
}

// AgentReviewStep owns a process and any reported findings. Successful exit
// satisfies this slot only when its recorded findings do not require changes.
type AgentReviewStep struct {
	ExecutionFailure *ReviewExecutionFailure
	Slot             int
	Execution        AgentExecution
	Report           *ReviewReport
	ReadOnlyFailures []ReadOnlyFailure
	DecisionPauses   []AutomatedDecisionPause
}

// ManualReviewStep owns findings as they are reported, including while the
// operator's assessment is unanswered. Deciding does not move their addresses.
type ManualReviewStep struct {
	Slot             int
	CommandResult    ProcessResult
	Findings         []Finding
	Decision         ManualAssessment
	ReadOnlyFailures []ReadOnlyFailure
}

func (*CheckStep) reviewStep()        {}
func (*AgentReviewStep) reviewStep()  {}
func (*ManualReviewStep) reviewStep() {}

// ManualAssessment separates approval from findings structurally.
type ManualAssessment interface{ manualAssessment() }

// ApprovedReview accepts its step's findings only when none still require work.
type ApprovedReview struct {
	// Publication retains only choices actually accepted at this manual approval.
	// It is intent, never authorization to deliver.
	Publication   *ReviewedPublication
	At            time.Time
	Verifications []FindingVerification
}

// RequestedChanges requires the findings retained by its owning manual step.
type RequestedChanges struct {
	At      time.Time
	Targets []FindingRef
}

// AbortedReview records that the assessment was deliberately stopped.
type AbortedReview struct {
	At     time.Time
	Reason string
}

func (*ApprovedReview) manualAssessment()   {}
func (*RequestedChanges) manualAssessment() {}
func (*AbortedReview) manualAssessment()    {}

// ReviewedPublication is the final destination/flow selected during manual review.
// Tentative choices on a blocked or aborted assessment are not persisted.
type ReviewedPublication struct {
	Destination Destination
	Target      PublicationTarget
}
