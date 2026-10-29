package work

import "time"

// AgentExecution is the concrete process and telemetry record owned by an effort.
// Role follows containment. Process is the only process-outcome authority.
type AgentExecution struct {
	Profile string
	Harness string
	Model   string
	// ObservedModel is native usage/session evidence, distinct from the model
	// selected at launch. It remains empty until the harness supplies it.
	ObservedModel string
	Thinking      string
	Interactive   bool
	// SessionName is the label actually selected for this launch, not a current
	// task-title snapshot or a substitute for the native session ID.
	SessionName   string
	Command       string
	Args          []string
	Session       *Session
	Launch        ExecutionLaunch
	SupervisorPID int
	ChildPID      int
	StartedAt     time.Time
	Duration      time.Duration
	Process       ProcessResult
	Usage         *Usage
	Cost          *UsageCost
	UsageCapture  UsageCapture
}

// ExecutionPurpose is derived from concrete ownership/context for reporting.
type ExecutionPurpose string

const (
	PurposeImplementation     ExecutionPurpose = "implementation"
	PurposeReview             ExecutionPurpose = "review"
	PurposeConflictResolution ExecutionPurpose = "sync_conflict_resolution"
)

// ExecutionPurpose resolves actual ownership instead of storing a duplicate role.
func (w *Workflow) ExecutionPurpose(ref ExecutionRef) (ExecutionPurpose, error) {
	if _, err := w.Execution(ref); err != nil {
		return "", err
	}
	if ref.BranchSync != nil {
		return PurposeConflictResolution, nil
	}
	if ref.Step != nil {
		return PurposeReview, nil
	}
	return PurposeImplementation, nil
}

// ProcessResult is one observed state, rather than Status plus conflicting facts.
type ProcessResult interface{ processResult() }

// RunningProcess has no independently editable terminal result.
type RunningProcess struct{}

// ExitedProcess retains both successful and unsuccessful observed exits.
type ExitedProcess struct {
	At   time.Time
	Code *int
}

// InterruptedProcess retains uncertainty instead of fabricating an exit code.
type InterruptedProcess struct {
	At      time.Time
	Reason  string
	Trigger string
}

// StartFailedProcess records failure before a child process was established.
type StartFailedProcess struct {
	At     time.Time
	Reason string
}

func (*RunningProcess) processResult()     {}
func (*ExitedProcess) processResult()      {}
func (*InterruptedProcess) processResult() {}
func (*StartFailedProcess) processResult() {}

// Session retains the native harness identity, not a generated internal ID.
type Session struct {
	ID      string
	LogPath string
}

// ExecutionLaunch has fresh and resumed alternatives without duplicate sessions.
type ExecutionLaunch interface{ executionLaunch() }

// FreshExecution can explain why a requested resumption was not used.
type FreshExecution struct{ FallbackReason string }

// ResumedExecution references its source session through the previous execution.
// Baselines are observations at resumption, not copies of mutable usage totals.
type ResumedExecution struct {
	From                 ExecutionRef
	UsageBaseline        *Usage
	CostBaselineMicroUSD *int64
}

func (*FreshExecution) executionLaunch()   {}
func (*ResumedExecution) executionLaunch() {}

// Usage retains harness-reported counts; overlapping token categories are not summed.
type Usage struct {
	InputTokens           int64
	CachedInputTokens     int64
	OutputTokens          int64
	ReasoningOutputTokens int64
	TotalTokens           int64
}

// UsageCaptureStatus distinguishes captured telemetry from explicit uncertainty.
type UsageCaptureStatus string

const (
	UsageCaptured  UsageCaptureStatus = "captured"
	UsageUnknown   UsageCaptureStatus = "unknown"
	UsageAmbiguous UsageCaptureStatus = "ambiguous"
)

// UsageCapture preserves capture diagnostics rather than treating unknown as zero.
type UsageCapture struct {
	Status         UsageCaptureStatus
	Reason         string
	CandidateCount int
	CapturedAt     *time.Time
}

// UsageCost retains exact micro-USD amounts and their reported/estimated origin.
type UsageCost struct {
	Kind           string
	Currency       string
	AmountMicroUSD int64
	Pricing        *UsagePricing
	Source         string
	Notes          string
}

// UsagePricing is the retained rate/source snapshot, not a live pricing lookup.
type UsagePricing struct {
	Provider                  string
	Model                     string
	ServiceTier               string
	EffectiveDate             string
	Unit                      string
	InputUSDPerMillionTokens  string
	CachedUSDPerMillionTokens string
	OutputUSDPerMillionTokens string
	ReasoningOutputTreatment  string
	Source                    string
	SourceURL                 string
	SourceAccessed            string
	SourcePublished           string
	Notes                     string
}

func successful(e *AgentExecution) bool {
	exit, ok := e.Process.(*ExitedProcess)
	return ok && exit != nil && exit.Code != nil && *exit.Code == 0 && !exit.At.IsZero() && !e.StartedAt.IsZero() && !exit.At.Before(e.StartedAt)
}

func running(e *AgentExecution) bool {
	process, ok := e.Process.(*RunningProcess)
	return ok && process != nil
}
