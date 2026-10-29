// Package taskstate persists Orpheus-owned per-task execution state.
package taskstate

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/pathutil"
	"github.com/hea3ven/orpheus/internal/publication"
	orstate "github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
	"gopkg.in/yaml.v3"
)

// RunStatus presents the observed process boundary of an attached effort.
type RunStatus string

const (
	// RunStatusRunning means an attached agent attempt was started and has not been recorded as finished.
	RunStatusRunning RunStatus = "running"

	// RunStatusSucceeded means the attached agent attempt exited successfully.
	RunStatusSucceeded RunStatus = "succeeded"

	// RunStatusFailed means the attached agent attempt failed or could not start.
	RunStatusFailed RunStatus = "failed"

	// RunStatusInterrupted means Orpheus proved attached supervision ended
	// without observing a process exit result.
	RunStatusInterrupted RunStatus = "interrupted"
)

// AgentExecutionPurpose identifies why an agent process was launched.
type AgentExecutionPurpose string

const (
	AgentExecutionPurposeImplementation         AgentExecutionPurpose = "implementation"
	AgentExecutionPurposeReview                 AgentExecutionPurpose = "review"
	AgentExecutionPurposeSyncConflictResolution AgentExecutionPurpose = "sync_conflict_resolution"
)

// UsageCaptureStatus records whether usage telemetry was captured.
type UsageCaptureStatus string

const (
	UsageCaptureCaptured  UsageCaptureStatus = "captured"
	UsageCaptureUnknown   UsageCaptureStatus = "unknown"
	UsageCaptureAmbiguous UsageCaptureStatus = "ambiguous"
)

// ReviewStatus is the status for a local review attempt.
type ReviewStatus string

const (
	ReviewStatusRunning ReviewStatus = "running"
	ReviewStatusBlocked ReviewStatus = "blocked"
	ReviewStatusFailed  ReviewStatus = "failed"
	ReviewStatusPassed  ReviewStatus = "passed"
	ReviewStatusAborted ReviewStatus = "aborted"

	// ReviewStatusWaitingForManual means automated review paused before the named manual step.
	ReviewStatusWaitingForManual ReviewStatus = "waiting_for_manual"
	// ReviewStatusWaitingForAutomatedDecision means a check or agent-review blocker is awaiting an operator disposition.
	ReviewStatusWaitingForAutomatedDecision ReviewStatus = "waiting_for_automated_decision"
)

// FindingType classifies a human-recorded review finding.
type FindingType string

const (
	FindingTypeBlocking     FindingType = "blocking"
	FindingTypeAdvisory     FindingType = "advisory"
	FindingTypeSeparateTask FindingType = "separate_task"
)

// EventType labels a transient history observation derived from the work tree.
type EventType string

const (
	EventWorkPrepared         EventType = "work_prepared"
	EventPRObserved           EventType = "pr_observed"
	EventWorktreeRemoved      EventType = "worktree_removed"
	EventRunStarted           EventType = "run_started"
	EventRunFinished          EventType = "run_finished"
	EventRunStartFailed       EventType = "run_start_failed"
	EventRunInterrupted       EventType = "run_interrupted"
	EventReviewInterrupted    EventType = "review_interrupted"
	EventCompletionRecorded   EventType = "completion_recorded"
	EventChangesPushed        EventType = "changes_pushed"
	EventTaskClosed           EventType = "task_closed"
	EventSyncConflictStarted  EventType = "sync_conflict_started"
	EventSyncConflictFinished EventType = "sync_conflict_finished"
	EventSyncConflictFailed   EventType = "sync_conflict_failed"
	EventBranchSyncRolledBack EventType = "branch_sync_rolled_back"
	EventBranchSyncStarted    EventType = "branch_sync_started"
	EventBranchSyncCommitted  EventType = "branch_sync_committed"
	EventBranchSyncPushed     EventType = "branch_sync_pushed"
)

const (
	// PushTargetMain identifies a publication to the registered default branch.
	PushTargetMain = "main"

	// PushTargetBranch identifies a publication to a feature branch.
	PushTargetBranch = "branch"

	// CloseReasonDefaultBranchPublished identifies closure after a default-branch push.
	CloseReasonDefaultBranchPublished = "default_branch_published"

	// CloseReasonPRMerged identifies closure after a recorded pull request is merged.
	CloseReasonPRMerged = "pr_merged"
)

var (
	// ErrActiveRun indicates the latest run attempt is still running.
	ErrActiveRun = errors.New("latest run attempt is still running")

	// ErrCompletionConflict indicates a run already has different completion content.
	ErrCompletionConflict = errors.New("run completion already recorded with different summary/description/detailed_description")

	// ErrFinalizationConflict indicates finalization facts already contain different data.
	ErrFinalizationConflict = errors.New("task finalization already recorded with different facts")
)

// Store is a YAML-backed per-task state store under the Orpheus data root.
type Store struct {
	paths  orstate.Paths
	now    func() time.Time
	logger *slog.Logger
}

// TaskState is a transient service and presentation projection of validated work state.
type TaskState struct {
	// Work is the validated authority behind workflow-derived service views.
	// Projections are never an input to workflow persistence.
	Work            *work.State `yaml:"-" json:"-"`
	CurrentWorkflow int         `yaml:"-" json:"-"`

	Version work.StateVersion `yaml:"version"`
	RepoID  string            `yaml:"repo_id"`
	TaskID  string            `yaml:"task_id"`

	// WorkDirectory is the immutable filesystem location selected at first
	// dispatch. Branch and other Git facts may evolve during publication.
	WorkDirectory WorkDirectory `yaml:"work_directory,omitempty"`

	// BaselineCommit is the immutable starting commit, not the integration destination.
	BaselineCommit string `yaml:"baseline_commit,omitempty"`

	// GitFacts are the current branch and checkout facts for the fixed work
	// directory. Publication consumes these facts without selecting another branch.
	GitFacts GitFacts `yaml:"git_facts,omitempty"`

	Runs    []RunAttempt    `yaml:"runs,omitempty"`
	Reviews []ReviewAttempt `yaml:"reviews,omitempty"`
	Events  []Event         `yaml:"events,omitempty"`

	Finalization *Finalization `yaml:"finalization,omitempty"`

	// ActiveSyncConflict is a transient recovery view derived from branch-sync history.
	ActiveSyncConflict *SyncConflictOperation `yaml:"active_sync_conflict,omitempty"`
}

// UnmarshalYAML decodes only validated workflow documents into derived views.
func (s *TaskState) UnmarshalYAML(value *yaml.Node) error {
	data, err := yaml.Marshal(value)
	if err != nil {
		return err
	}
	snapshot, err := workstate.Decode(data)
	if err != nil {
		return err
	}
	view, err := Project(snapshot)
	if err != nil {
		return err
	}
	*s = view
	return nil
}

// MarshalYAML emits only the workflow authority, never the derived view fields.
func (s TaskState) MarshalYAML() (any, error) {
	if s.Work == nil {
		return nil, errors.New("task projections cannot be persisted without workflow authority")
	}
	data, err := workstate.Encode(*s.Work)
	if err != nil {
		return nil, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	return document.Content[0], nil
}

// WorkDirectory is the derived location of the owning workflow's prepared context.
type WorkDirectory struct {
	Path string `yaml:"path,omitempty"`
}

// IsZero reports whether no work directory has been selected.
func (d WorkDirectory) IsZero() bool { return strings.TrimSpace(d.Path) == "" }

// GitFacts records evolving branch and checkout facts for a task's fixed work directory.
type GitFacts struct {
	Branch   string `yaml:"branch,omitempty"`
	Worktree string `yaml:"worktree,omitempty"`
}

// IsZero allows YAML omitempty to omit absent Git facts.
func (f GitFacts) IsZero() bool {
	return strings.TrimSpace(f.Branch) == "" && strings.TrimSpace(f.Worktree) == ""
}

// RunAttempt records one attached execution attempt.
type RunAttempt struct {
	Address work.AttemptRef `yaml:"-" json:"-"`
	Attempt int             `yaml:"attempt"`
	Status  RunStatus       `yaml:"status"`

	Execution AgentExecution `yaml:"execution"`

	Completion *Completion `yaml:"completion,omitempty"`

	ReviewFollowUp *ReviewFollowUp `yaml:"review_follow_up,omitempty"`
}

// AgentSelection records the normalized model cohort selected for an agent process.
type AgentSelection struct {
	Harness  string `yaml:"harness,omitempty"`
	Model    string `yaml:"model,omitempty"`
	Thinking string `yaml:"thinking,omitempty"`
}

const agentSelectionDefaultThinking = "default"

// NewAgentSelection returns normalized agent selection metadata for persistence and stats cohorts.
func NewAgentSelection(harness string, model string, thinking string) AgentSelection {
	selection := AgentSelection{
		Harness:  strings.TrimSpace(harness),
		Model:    strings.TrimSpace(model),
		Thinking: strings.TrimSpace(thinking),
	}
	if agentSelectionHasDefaultThinking(selection.Harness) && selection.Thinking == "" {
		selection.Thinking = agentSelectionDefaultThinking
	}
	return selection
}

func agentSelectionHasDefaultThinking(harness string) bool {
	switch strings.ToLower(strings.TrimSpace(harness)) {
	case "codex", "pi":
		return true
	default:
		return false
	}
}

// IsZero reports whether the selection carries no model-cohort metadata.
func (s AgentSelection) IsZero() bool {
	return strings.TrimSpace(s.Harness) == "" &&
		strings.TrimSpace(s.Model) == "" &&
		strings.TrimSpace(s.Thinking) == ""
}

// CohortLabel returns a stable display key for model-comparison cohorts.
func (s AgentSelection) CohortLabel() string {
	selection := NewAgentSelection(s.Harness, s.Model, s.Thinking)
	model := selection.Model
	if model == "" {
		model = "unknown"
	}

	qualifiers := make([]string, 0, 2)
	if selection.Harness != "" {
		qualifiers = append(qualifiers, "harness="+selection.Harness)
	}
	if selection.Harness != "" && selection.Thinking != "" {
		qualifiers = append(qualifiers, "thinking="+selection.Thinking)
	}
	if len(qualifiers) == 0 {
		return model
	}
	return model + " (" + strings.Join(qualifiers, ", ") + ")"
}

// AgentSelection returns normalized model-cohort metadata for this execution.
func (e AgentExecution) AgentSelection() AgentSelection {
	return NewAgentSelection(e.Harness, e.Model, e.Thinking)
}

// AgentExecution records common facts for one agent process execution.
type AgentExecution struct {
	Address work.AttemptRef       `yaml:"-" json:"-"`
	Process work.ProcessResult    `yaml:"-" json:"-"`
	Purpose AgentExecutionPurpose `yaml:"purpose"`
	Status  RunStatus             `yaml:"status"`

	Agent   string `yaml:"agent,omitempty"`
	Profile string `yaml:"profile,omitempty"`
	Harness string `yaml:"harness,omitempty"`
	// ObservedModel distinguishes known native identity from the launch-selected model.
	ObservedModel string `yaml:"-" json:"-"`
	Model         string `yaml:"model,omitempty"`
	Thinking      string `yaml:"thinking,omitempty"`
	Interactive   bool   `yaml:"interactive"`

	Command     string   `yaml:"command,omitempty"`
	Args        []string `yaml:"args,omitempty"`
	SessionName string   `yaml:"session_name,omitempty"`

	StartedAt      time.Time  `yaml:"started_at"`
	FinishedAt     *time.Time `yaml:"finished_at,omitempty"`
	DurationMillis int64      `yaml:"duration_millis,omitempty"`

	// SupervisorPID is the Orpheus process that created this attached execution.
	// ChildPID is the direct attached command started by that supervisor.
	SupervisorPID int `yaml:"supervisor_pid,omitempty"`
	ChildPID      int `yaml:"child_pid,omitempty"`

	// Interruption facts are set only when local recovery proves supervision
	// disappeared before a terminal result was persisted.
	InterruptionReason  string `yaml:"interruption_reason,omitempty"`
	InterruptionTrigger string `yaml:"interruption_trigger,omitempty"`

	Launch       *AgentLaunch      `yaml:"launch,omitempty"`
	Session      *AgentSession     `yaml:"session,omitempty"`
	Usage        *AgentUsage       `yaml:"usage,omitempty"`
	UsageCost    *AgentUsageCost   `yaml:"usage_cost,omitempty"`
	UsageCapture AgentUsageCapture `yaml:"usage_capture,omitempty"`
}

// AgentLaunchMode identifies whether an implementation process started a new
// harness session or resumed an existing one.
type AgentLaunchMode string

const (
	AgentLaunchFresh   AgentLaunchMode = "fresh"
	AgentLaunchResumed AgentLaunchMode = "resumed"
)

// AgentLaunch records follow-up session provenance. When available, usage
// baselines are the cumulative source-session values immediately before a
// resumed process starts; a nil baseline records that incremental usage cannot
// be measured safely.
type AgentLaunch struct {
	Mode             AgentLaunchMode `yaml:"mode"`
	SourceRunAttempt int             `yaml:"source_run_attempt,omitempty"`
	SourceSession    *AgentSession   `yaml:"source_session,omitempty"`
	FallbackReason   string          `yaml:"fallback_reason,omitempty"`
	UsageBaseline    *AgentUsage     `yaml:"usage_baseline,omitempty"`
	CostBaseline     *int64          `yaml:"cost_baseline_micro_usd,omitempty"`
}

// AgentSession records harness-specific session correlation facts.
type AgentSession struct {
	ID      string `yaml:"id,omitempty"`
	LogPath string `yaml:"log_path,omitempty"`
}

// AgentUsage records token usage fields reported by the agent harness.
type AgentUsage struct {
	InputTokens           int `yaml:"input_tokens,omitempty" json:"input_tokens,omitempty"`
	CachedInputTokens     int `yaml:"cached_input_tokens,omitempty" json:"cached_input_tokens,omitempty"`
	OutputTokens          int `yaml:"output_tokens,omitempty" json:"output_tokens,omitempty"`
	ReasoningOutputTokens int `yaml:"reasoning_output_tokens,omitempty" json:"reasoning_output_tokens,omitempty"`
	TotalTokens           int `yaml:"total_tokens,omitempty" json:"total_tokens,omitempty"`
}

// AgentUsageCost records a harness-reported or Orpheus-estimated usage cost.
type AgentUsageCost struct {
	Kind           string             `yaml:"kind,omitempty"`
	Currency       string             `yaml:"currency,omitempty"`
	AmountMicroUSD int64              `yaml:"amount_micro_usd"`
	Pricing        *AgentUsagePricing `yaml:"pricing,omitempty"`
	Source         string             `yaml:"source,omitempty"`
	Notes          string             `yaml:"notes,omitempty"`
}

// AgentUsagePricing is the immutable public pricing snapshot used for an
// API-equivalent usage-cost estimate.
type AgentUsagePricing struct {
	Provider                  string `yaml:"provider,omitempty"`
	Model                     string `yaml:"model,omitempty"`
	ServiceTier               string `yaml:"service_tier,omitempty"`
	EffectiveDate             string `yaml:"effective_date,omitempty"`
	Unit                      string `yaml:"unit,omitempty"`
	InputUSDPerMillionTokens  string `yaml:"input_usd_per_million_tokens,omitempty"`
	CachedUSDPerMillionTokens string `yaml:"cached_usd_per_million_tokens,omitempty"`
	OutputUSDPerMillionTokens string `yaml:"output_usd_per_million_tokens,omitempty"`
	ReasoningOutputTreatment  string `yaml:"reasoning_output_treatment,omitempty"`
	Source                    string `yaml:"source,omitempty"`
	SourceURL                 string `yaml:"source_url,omitempty"`
	SourceAccessed            string `yaml:"source_accessed,omitempty"`
	SourcePublished           string `yaml:"source_published,omitempty"`
	Notes                     string `yaml:"notes,omitempty"`
}

// AgentUsageCapture records diagnostics from a usage-capture attempt.
type AgentUsageCapture struct {
	Status         UsageCaptureStatus `yaml:"status,omitempty"`
	Reason         string             `yaml:"reason,omitempty"`
	CandidateCount int                `yaml:"candidate_count,omitempty"`
	CapturedAt     *time.Time         `yaml:"captured_at,omitempty"`
}

// IsZero allows YAML omitempty to omit absent usage-capture diagnostics.
func (c AgentUsageCapture) IsZero() bool {
	return c.Status == "" &&
		strings.TrimSpace(c.Reason) == "" &&
		c.CandidateCount == 0 &&
		c.CapturedAt == nil
}

// ReviewFollowUp records the scoped findings shown to a blocker-triggered follow-up run.
type ReviewFollowUp struct {
	ReviewAttempt int `yaml:"review_attempt"`

	// FindingIndexes are display positions of required workflow-owned blocking findings.
	FindingIndexes []int `yaml:"finding_indexes,omitempty"`

	// AdvisoryFindingIndexes are best-effort advisory opportunities shown with
	// the required blockers. They are not claimed or resolved by this run.
	AdvisoryFindingIndexes []int `yaml:"advisory_finding_indexes,omitempty"`
}

// Completion records agent-authored completion facts for a run attempt.
type Completion struct {
	Summary              string    `yaml:"summary"`
	Description          string    `yaml:"description"`
	DetailedDescription  string    `yaml:"detailed_description"`
	TechnicalExplanation string    `yaml:"technical_explanation"`
	CompletedAt          time.Time `yaml:"completed_at"`
	Commit               string    `yaml:"commit,omitempty"`
	CommitError          string    `yaml:"commit_error,omitempty"`
}

// ReviewAttempt records one local review pipeline attempt.
type ReviewAttempt struct {
	Address work.AttemptRef `yaml:"-" json:"-"`
	Attempt int             `yaml:"attempt"`
	Status  ReviewStatus    `yaml:"status"`

	Pipeline string `yaml:"pipeline"`
	Step     string `yaml:"step"`

	StartedAt  time.Time       `yaml:"started_at"`
	FinishedAt *time.Time      `yaml:"finished_at,omitempty"`
	Steps      []ReviewStep    `yaml:"steps,omitempty"`
	Findings   []ReviewFinding `yaml:"findings,omitempty"`

	AutonomousBudgetExhausted           bool `yaml:"autonomous_budget_exhausted,omitempty"`
	AutomatedBlockerDecisionKept        bool `yaml:"automated_blocker_decision_kept,omitempty"`
	AutomatedBlockerDecisionInterrupted bool `yaml:"automated_blocker_decision_interrupted,omitempty"`
}

const (
	// ReviewStepKindManual identifies review steps that wait for operator input.
	ReviewStepKindManual = "manual"
	// ReviewStepKindCheck identifies automated command-based review steps.
	ReviewStepKindCheck = "check"
	// ReviewStepKindAgentReview identifies automated agent-review steps.
	ReviewStepKindAgentReview = "agent_review"
)

// ReviewStep records one executed review pipeline step.
type ReviewStep struct {
	Address   work.AttemptRef `yaml:"-" json:"-"`
	Kind      string          `yaml:"kind"`
	Name      string          `yaml:"name"`
	Execution *AgentExecution `yaml:"execution,omitempty"`
	ExitCode  *int            `yaml:"exit_code,omitempty"`
}

// ReviewFinding records one review finding.
type ReviewFinding struct {
	NonAuthoritative bool                    `yaml:"-" json:"-"`
	Superseded       bool                    `yaml:"-" json:"-"`
	Actor            *work.AttemptRef        `yaml:"-" json:"-"`
	Address          *work.FindingRef        `yaml:"-" json:"-"`
	ReportedType     FindingType             `yaml:"-" json:"-"`
	Decision         work.FindingDisposition `yaml:"-" json:"-"`
	Type             FindingType             `yaml:"type"`
	Title            string                  `yaml:"title"`
	Description      string                  `yaml:"description"`

	Step              string             `yaml:"step,omitempty"`
	SuggestedAction   string             `yaml:"suggested_action,omitempty"`
	DowngradeReason   string             `yaml:"downgrade_reason,omitempty"`
	AddressedManually string             `yaml:"addressed_manually,omitempty"`
	Waiver            string             `yaml:"waiver,omitempty"`
	TaskProposal      ReviewTaskProposal `yaml:"task_proposal,omitempty"`
	CreatedTaskID     string             `yaml:"created_task_id,omitempty"`
	CreatedTaskAt     *time.Time         `yaml:"created_task_at,omitempty"`

	TargetedByRunAttempt int `yaml:"targeted_by_run_attempt,omitempty"`
}

// ReviewTaskProposal describes follow-up work proposed by a separate-task finding.
type ReviewTaskProposal struct {
	Title              string `yaml:"title,omitempty"`
	Description        string `yaml:"description,omitempty"`
	AcceptanceCriteria string `yaml:"acceptance_criteria,omitempty"`
}

// IsZero allows YAML omitempty to omit empty task proposals.
func (p ReviewTaskProposal) IsZero() bool {
	return strings.TrimSpace(p.Title) == "" &&
		strings.TrimSpace(p.Description) == "" &&
		strings.TrimSpace(p.AcceptanceCriteria) == ""
}

// Finalization records factual data from human-side main/solo finalization.
type Finalization struct {
	IntegrationFlow   publication.IntegrationFlow `yaml:"integration_flow,omitempty"`
	DestinationBranch string                      `yaml:"destination_branch,omitempty"`
	CommittedAt       *time.Time                  `yaml:"committed_at,omitempty"`
	Commit            string                      `yaml:"commit,omitempty"`
	MergeCommit       string                      `yaml:"merge_commit,omitempty"`
	PushedAt          *time.Time                  `yaml:"pushed_at,omitempty"`
	ClosedAt          *time.Time                  `yaml:"closed_at,omitempty"`
}

// SyncConflictPhase labels the facts observed for a branch-sync occurrence.
type SyncConflictPhase string

const (
	SyncConflictPhasePrepared       SyncConflictPhase = "prepared"
	SyncConflictPhaseResolving      SyncConflictPhase = "resolving"
	SyncConflictPhaseLocalCompleted SyncConflictPhase = "local_completed"
	SyncConflictPhasePushed         SyncConflictPhase = "pushed"
	SyncConflictPhaseUnresolved     SyncConflictPhase = "unresolved"
)

// SyncConflictCheckpoint is the clean local and remote state recorded before
// Git begins a merge that can leave conflicts.
type SyncConflictCheckpoint struct {
	LocalHead   string `yaml:"local_head"`
	RemoteHead  string `yaml:"remote_head"`
	MergeSource string `yaml:"merge_source"`
}

// SyncConflictOperation is a transient recovery view of a branch-sync occurrence.
// Its ID is the containment address; authoritative receipts stay in work.State.
type SyncConflictOperation struct {
	Address            work.AttemptRef        `yaml:"-" json:"-"`
	ID                 string                 `yaml:"id"`
	Branch             string                 `yaml:"branch"`
	Worktree           string                 `yaml:"worktree"`
	DefaultBranch      string                 `yaml:"default_branch"`
	Checkpoint         SyncConflictCheckpoint `yaml:"checkpoint"`
	Phase              SyncConflictPhase      `yaml:"phase"`
	ConflictFiles      []string               `yaml:"conflict_files,omitempty"`
	Execution          *AgentExecution        `yaml:"execution,omitempty"`
	CreatedAt          time.Time              `yaml:"created_at"`
	UpdatedAt          time.Time              `yaml:"updated_at"`
	LocalHead          string                 `yaml:"local_head,omitempty"`
	ObservedRemoteHead string                 `yaml:"observed_remote_head,omitempty"`
	Outcome            string                 `yaml:"outcome,omitempty"`
	Reason             string                 `yaml:"reason,omitempty"`
}

// Event records a small trace/audit event for a task.
type Event struct {
	Address work.AttemptRef `yaml:"-" json:"-"`
	Type    EventType       `yaml:"type"`
	At      time.Time       `yaml:"at"`

	Attempt   int             `yaml:"attempt,omitempty"`
	Status    RunStatus       `yaml:"status,omitempty"`
	Agent     string          `yaml:"agent,omitempty"`
	Execution *AgentExecution `yaml:"execution,omitempty"`

	Branch        string   `yaml:"branch,omitempty"`
	DefaultBranch string   `yaml:"default_branch,omitempty"`
	Worktree      string   `yaml:"worktree,omitempty"`
	ConflictFiles []string `yaml:"conflict_files,omitempty"`
	Commit        string   `yaml:"commit,omitempty"`
	Error         string   `yaml:"error,omitempty"`

	InterruptionReason  string `yaml:"interruption_reason,omitempty"`
	InterruptionTrigger string `yaml:"interruption_trigger,omitempty"`

	Message string `yaml:"message,omitempty"`

	PRURL           string `yaml:"pr_url,omitempty"`
	ObservedPRState string `yaml:"observed_pr_state,omitempty"`
	PushTarget      string `yaml:"push_target,omitempty"`
	CloseReason     string `yaml:"close_reason,omitempty"`
}

// DisplayName returns the concise human-readable name for an audit event.
func (e Event) DisplayName() string {
	switch e.Type {
	case EventWorkPrepared:
		return "Work prepared"
	case EventPRObserved:
		return "PR observed"
	case EventWorktreeRemoved:
		return "Worktree removed"
	case EventRunStarted:
		return "Run started"
	case EventRunFinished:
		return "Run finished"
	case EventRunStartFailed:
		return "Run start failed"
	case EventRunInterrupted:
		return "Run interrupted"
	case EventReviewInterrupted:
		return "Review interrupted"
	case EventCompletionRecorded:
		return "Completion recorded"
	case EventChangesPushed:
		return "Pushed " + e.PushTarget
	case EventTaskClosed:
		return "Task closed"
	case EventSyncConflictStarted:
		return "Sync conflict resolution started"
	case EventSyncConflictFinished:
		return "Sync conflict resolution finished"
	case EventSyncConflictFailed:
		return "Sync conflict resolution failed"
	case EventBranchSyncRolledBack:
		return "Branch sync rolled back"
	case EventBranchSyncStarted:
		return "Branch sync checkpoint recorded"
	case EventBranchSyncCommitted:
		return "Branch sync local result retained"
	case EventBranchSyncPushed:
		return "Branch sync remote head verified"
	default:
		return string(e.Type)
	}
}

// StartRunOptions describes the run attempt being started.
type StartRunOptions struct {
	Context     *work.WorkContext
	Agent       string
	Profile     string
	Selection   AgentSelection
	Harness     string
	Model       string
	Thinking    string
	Interactive bool

	Command     string
	Args        []string
	SessionName string

	// WorkDirectory is immutable after the first run. Branch is an evolving Git
	// fact; Worktree mirrors it in this transient service request.
	WorkDirectory  string
	BaselineCommit string
	Branch         string
	Worktree       string

	ReviewFollowUp *ReviewFollowUp
	Launch         *AgentLaunch
	SupervisorPID  int
}

// InterruptRunOptions records why an attached implementation run was safely
// reconciled after its supervision disappeared.
type InterruptRunOptions struct {
	Reason  string
	Trigger string
}

// InterruptReviewExecutionOptions records a safely recovered reviewer
// execution and the recovery source.
type InterruptReviewExecutionOptions struct {
	Reason  string
	Trigger string
}

func (opts StartRunOptions) agentSelection() AgentSelection {
	if !opts.Selection.IsZero() {
		return NewAgentSelection(opts.Selection.Harness, opts.Selection.Model, opts.Selection.Thinking)
	}
	return NewAgentSelection(opts.Harness, opts.Model, opts.Thinking)
}

// CompleteRunOptions describes the agent-authored completion payload.
type CompleteRunOptions struct {
	Summary              string
	Description          string
	DetailedDescription  string
	TechnicalExplanation string
	Commit               string
	CommitError          string
}

// RecordRunUsageOptions describes usage and correlation facts to attach to a run.
type RecordRunUsageOptions struct {
	Session      *AgentSession
	Usage        *AgentUsage
	UsageCost    *AgentUsageCost
	UsageCapture AgentUsageCapture
	Candidates   []UsageCaptureCandidate
	Model        string
}

// UsageCaptureCandidate describes a session candidate considered during usage capture.
type UsageCaptureCandidate struct {
	SessionID         string
	SessionName       string
	LogPath           string
	CWD               string
	Model             string
	StartedAt         time.Time
	StartOffsetMillis int64
}

// StartReviewOptions describes the selected review pipeline.
type StartReviewOptions struct {
	Pipeline string
	Step     string
	Contract work.ReviewContract
	Scope    work.ReviewScope
}

// RecordReviewStepOptions describes one executed review step.
type RecordReviewStepOptions struct {
	ObservedAt   time.Time
	Address      work.AttemptRef
	StartFailure *work.StartFailedProcess
	Findings     []ReviewFinding
	Kind         string
	Name         string
	Execution    *AgentExecution
	ExitCode     *int
}

// FinishReviewStepExecutionOptions describes terminal facts for a review agent step execution.
type FinishReviewStepExecutionOptions struct {
	ExecutionFailure *work.ReviewExecutionFailure
	ChildPID         int
	ReadOnlyFailure  *work.ReadOnlyFailure
	Address          work.AttemptRef
	Process          work.ProcessResult
	Status           RunStatus
	FinishedAt       time.Time

	Session      *AgentSession
	Usage        *AgentUsage
	UsageCost    *AgentUsageCost
	UsageCapture AgentUsageCapture
	Model        string
}

// WorktreeCleanupOptions describes a successful deterministic worktree cleanup.
type WorktreeCleanupOptions struct {
	Worktree string
}

// TaskClosedOptions describes the facts recorded when a task is closed.
type TaskClosedOptions struct {
	Reason          string
	PRURL           string
	ObservedPRState string
}

// FinalizationPushOptions describes the successful publication boundary.
type FinalizationPushOptions struct {
	Branch     string
	PushTarget string
}

// FinalizationCloseOptions describes why a successful task finalization closed a task.
type FinalizationCloseOptions struct {
	Reason string
}

// FeatureBranchPROptions describes a created or recovered feature-branch PR.
type FeatureBranchPROptions struct {
	PRURL        string
	Branch       string
	WasRecovered bool
}

// NewStore creates a per-task state store using paths.
func NewStore(paths orstate.Paths) Store {
	return NewStoreWithLogger(paths, nil)
}

// NewStoreWithLogger creates a per-task state store that emits diagnostics to logger.
func NewStoreWithLogger(paths orstate.Paths, logger *slog.Logger) Store {
	return Store{paths: paths, now: func() time.Time { return time.Now().UTC() }, logger: logger}
}

// NewStoreWithClock creates a store with a deterministic clock for tests.
func NewStoreWithClock(paths orstate.Paths, now func() time.Time) Store {
	store := NewStore(paths)
	if now != nil {
		store.now = now
	}
	return store
}

// NewStoreWithClockAndLogger creates a store with a deterministic clock and logger.
func NewStoreWithClockAndLogger(paths orstate.Paths, now func() time.Time, logger *slog.Logger) Store {
	store := NewStoreWithLogger(paths, logger)
	if now != nil {
		store.now = now
	}
	return store
}

// Path returns the absolute YAML file path for one task state file.
func (s Store) Path(repoID, taskID string) (string, error) {
	rel, err := taskStateRelPath(repoID, taskID)
	if err != nil {
		return "", err
	}
	return s.paths.DataPath(rel)
}

// TaskIDs returns task IDs with persisted local Orpheus state for repoID.
func (s Store) TaskIDs(repoID string) ([]string, error) {
	repoID, err := cleanPathComponent("repo id", repoID)
	if err != nil {
		return nil, err
	}
	entries, err := s.paths.ListDataFiles(filepath.Join("repos", repoID, "tasks"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("list task states for repo %s: %w", repoID, err)
	}

	taskIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if filepath.Ext(entry) != ".yaml" {
			continue
		}
		taskID := strings.TrimSuffix(entry, ".yaml")
		if _, err := cleanPathComponent("task id", taskID); err != nil {
			continue
		}
		taskIDs = append(taskIDs, taskID)
	}
	return taskIDs, nil
}

// Load reads a task state file. Missing files load as an empty task state.
func (s Store) Load(repoID, taskID string) (TaskState, error) {
	snapshot, err := workstate.NewStore(s.paths).Load(repoID, taskID)
	if err != nil {
		return TaskState{}, err
	}
	return Project(snapshot)
}

// LatestRun returns the owning workflow's last implementation occurrence.
func (s Store) LatestRun(repoID, taskID string) (RunAttempt, bool, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return RunAttempt{}, false, err
	}
	latest, ok := LatestRun(state)
	return latest, ok, nil
}

// ActiveRun returns the latest attempt only when it is still running.
func (s Store) ActiveRun(repoID, taskID string) (RunAttempt, bool, error) {
	latest, ok, err := s.LatestRun(repoID, taskID)
	if err != nil || !ok || latest.Status != RunStatusRunning {
		return RunAttempt{}, false, err
	}
	return latest, true, nil
}

// PrepareWork retains the immutable native checkout and baseline observations.
func (s Store) PrepareWork(repoID, taskID string, context work.WorkContext) error {
	_, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		return prepareWorkflow(snapshot, &context, s.nowUTC())
	})
	return err
}

// StartRun appends a new running attempt and a run_started event.
func (s Store) StartRun(repoID, taskID string, opts StartRunOptions) (RunAttempt, error) {
	var ref work.AttemptRef
	startedAt := s.nowUTC()
	view, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		if len(snapshot.Workflows) == 0 || snapshot.ActiveWorkflow() == nil || snapshot.ActiveWorkflow().Context() == nil {
			if _, err := prepareWorkflow(snapshot, opts.Context, startedAt); err != nil {
				return false, err
			}
		}
		w := snapshot.ActiveWorkflow()
		if w == nil {
			return false, errors.New("implementation requires an open workflow")
		}
		if err := validateImplementationContext(snapshot, opts, startedAt); err != nil {
			return false, err
		}

		if previous, ok := w.LastPhase().(*work.ImplementationPhase); ok {
			if _, running := previous.Execution.Process.(*work.RunningProcess); running {
				return false, ErrActiveRun
			}
		}
		targets, err := implementationTargets(snapshot, opts.ReviewFollowUp)
		if err != nil {
			return false, err
		}
		selection := opts.agentSelection()
		execution, err := observedExecution(startRunAgentExecution(opts, selection, startedAt))
		if err != nil {
			return false, err
		}
		execution.Launch, err = workExecutionLaunch(snapshot, opts.Launch)
		if err != nil {
			return false, err
		}
		advisories, err := implementationAdvisories(snapshot, opts.ReviewFollowUp)
		if err != nil {
			return false, err
		}
		p := &work.ImplementationPhase{Execution: execution, TargetFindings: targets, AdvisoryFindings: advisories}
		if input, exists := w.LatestOutput(); exists {
			p.Input = &input
		}
		ref = work.AttemptRef{RepositoryID: repoID, TaskID: taskID, Workflow: len(snapshot.Workflows) - 1, Phase: len(w.Phases)}
		w.Phases = append(w.Phases, p)
		return true, nil
	})
	if err != nil {
		return RunAttempt{}, err
	}
	return viewRunAt(view, ref)
}

func validateImplementationContext(snapshot *work.State, opts StartRunOptions, at time.Time) error {
	w := snapshot.ActiveWorkflow()
	if opts.Context != nil {
		if _, err := prepareWorkflow(snapshot, opts.Context, at); err != nil {
			return err
		}
	}
	prepared := w.Context()
	if (opts.WorkDirectory != "" && opts.WorkDirectory != prepared.Directory) || (opts.Worktree != "" && opts.Worktree != prepared.Directory) || (opts.Branch != "" && opts.Branch != prepared.TaskBranch) || (opts.BaselineCommit != "" && opts.BaselineCommit != prepared.Baseline.Commit) {
		return errors.New("implementation request differs from the prepared workflow context")
	}
	return nil
}

func startRunAgentExecution(opts StartRunOptions, selection AgentSelection, now time.Time) AgentExecution {
	return normalizeAgentExecution(AgentExecution{
		Purpose:       AgentExecutionPurposeImplementation,
		Status:        RunStatusRunning,
		Agent:         opts.Agent,
		Profile:       opts.Profile,
		Harness:       selection.Harness,
		Model:         selection.Model,
		Thinking:      selection.Thinking,
		Interactive:   opts.Interactive,
		Command:       opts.Command,
		Args:          cloneStrings(opts.Args),
		SessionName:   opts.SessionName,
		StartedAt:     now,
		Launch:        opts.Launch,
		SupervisorPID: opts.SupervisorPID,
	})
}

// InterruptRun records a reconciled interrupted terminal state without claiming
// a successful or failed process exit.
func (s Store) InterruptRun(repoID, taskID string, attempt int, opts InterruptRunOptions) (RunAttempt, error) {
	if strings.TrimSpace(opts.Reason) == "" || strings.TrimSpace(opts.Trigger) == "" {
		return RunAttempt{}, errors.New("interruption requires observed reason and trigger")
	}
	var ref work.AttemptRef
	view, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		projected, err := Project(*snapshot)
		if err != nil {
			return false, err
		}
		run, ok := ActiveRun(projected)
		if !ok || run.Attempt != attempt {
			return false, errors.New("run no longer owns the active workflow")
		}
		ref = run.Address
		phase, err := currentImplementation(snapshot, ref)
		if err != nil {
			return false, err
		}
		phase.Execution.Process = &work.InterruptedProcess{At: s.nowUTC(), Reason: opts.Reason, Trigger: opts.Trigger}
		return true, nil
	})
	if err != nil {
		return RunAttempt{}, err
	}
	return viewRunAt(view, ref)
}

// StartReviewWithOptions appends a new running local review attempt.
func (s Store) StartReviewWithOptions(repoID, taskID string, opts StartReviewOptions) (ReviewAttempt, error) {
	var ref work.AttemptRef
	view, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		w := snapshot.ActiveWorkflow()
		if w == nil || w.Context() == nil {
			return false, errors.New("review requires an established workflow context")
		}
		if len(opts.Contract.Slots) == 0 || opts.Contract.PipelineKey != opts.Pipeline {
			return false, errors.New("review requires its complete captured pipeline contract")
		}
		ref = work.AttemptRef{RepositoryID: repoID, TaskID: taskID, Workflow: len(snapshot.Workflows) - 1, Phase: len(w.Phases)}
		contract := len(w.ReviewContracts)
		w.ReviewContracts = append(w.ReviewContracts, opts.Contract)
		w.Phases = append(w.Phases, &work.ReviewPhase{Contract: contract, Scope: opts.Scope, StartedAt: s.nowUTC()})
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// RecordReviewStep appends an executed step record to a running review attempt.
func (s Store) RecordReviewStep(repoID, taskID string, attempt int, opts RecordReviewStepOptions) (ReviewAttempt, error) {
	if opts.ObservedAt.IsZero() {
		opts.ObservedAt = s.nowUTC()
	}
	view, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		w, p, err := currentReview(snapshot, opts.Address)
		if err != nil {
			return false, err
		}
		if opts.Address.Step != nil {
			if opts.Kind != ReviewStepKindManual {
				return false, errors.New("step-owned results cannot start another review attempt")
			}
			if err := snapshot.CheckActiveAttempt(opts.Address); err != nil {
				return false, err
			}
		}
		if phaseNumber(w, opts.Address.Phase, true) != attempt {
			return false, errors.New("review display number differs from its owner")
		}
		return appendObservedStep(w, p, opts)
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, opts.Address)
}

// InterruptReviewExecution atomically records a recovered interrupted
// reviewer and its containing failed review attempt.
func (s Store) InterruptReviewExecution(repoID, taskID string, attempt int, stepName string, opts InterruptReviewExecutionOptions) (ReviewAttempt, error) {
	if strings.TrimSpace(opts.Reason) == "" || strings.TrimSpace(opts.Trigger) == "" {
		return ReviewAttempt{}, errors.New("interruption requires observed reason and trigger")
	}
	var ref work.AttemptRef
	view, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		projected, err := Project(*snapshot)
		if err != nil {
			return false, err
		}
		review, ok := LatestReview(projected)
		if !ok || review.Attempt != attempt || len(review.Steps) == 0 {
			return false, errors.New("review no longer owns the active workflow")
		}
		step := review.Steps[len(review.Steps)-1]
		if step.Name != stepName || step.Execution == nil {
			return false, errors.New("interruption requires the active last reviewer")
		}
		ref = step.Address
		if err := snapshot.CheckActiveAttempt(ref); err != nil {
			return false, err
		}
		execution, err := snapshot.Workflows[ref.Workflow].Execution(work.ExecutionRef{Phase: ref.Phase, Step: ref.Step})
		if err != nil {
			return false, err
		}
		execution.Process = &work.InterruptedProcess{At: s.nowUTC(), Reason: opts.Reason, Trigger: opts.Trigger}
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// FinishReviewStepExecution records terminal state and best-effort usage telemetry for a review agent step.
func (s Store) FinishReviewStepExecution(repoID, taskID string, attempt int, stepName string, opts FinishReviewStepExecutionOptions) (ReviewAttempt, error) {
	view, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		if err := snapshot.CheckActiveAttempt(opts.Address); err != nil {
			return false, err
		}
		w, p, err := currentReview(snapshot, opts.Address)
		if err != nil {
			return false, err
		}
		if opts.Address.Step == nil || phaseNumber(w, opts.Address.Phase, true) != attempt {
			return false, errors.New("review execution result has a different owner")
		}
		step, ok := p.Steps[*opts.Address.Step].(*work.AgentReviewStep)
		if !ok || w.ReviewContracts[p.Contract].Slots[step.Slot].ConfigKey != stepName {
			return false, errors.New("execution result does not identify its captured review slot")
		}
		if opts.Process == nil {
			return false, errors.New("reviewer completion requires the actual observed process outcome")
		}
		if opts.ChildPID < 0 || step.Execution.ChildPID != 0 && step.Execution.ChildPID != opts.ChildPID {
			return false, errors.New("review process identity differs from its recorded child")
		}
		if opts.ChildPID > 0 {
			step.Execution.ChildPID = opts.ChildPID
		}
		step.Execution.Process = opts.Process
		if exit, ok := opts.Process.(*work.ExitedProcess); ok && exit != nil && !exit.At.Before(step.Execution.StartedAt) {
			step.Execution.Duration = exit.At.Sub(step.Execution.StartedAt)
		}
		step.ExecutionFailure = opts.ExecutionFailure
		applyObservedUsage(&step.Execution, RecordRunUsageOptions{Session: opts.Session, Usage: opts.Usage, UsageCost: opts.UsageCost, UsageCapture: opts.UsageCapture, Model: opts.Model}, s.nowUTC())
		if opts.ReadOnlyFailure != nil {
			step.ReadOnlyFailures = append(step.ReadOnlyFailures, *opts.ReadOnlyFailure)
		}
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, opts.Address)
}

// RecordReviewFinding appends a finding to a running review attempt.
func (s Store) RecordReviewFinding(repoID, taskID string, attempt int, finding ReviewFinding) (ReviewAttempt, error) {
	if finding.Actor == nil {
		return ReviewAttempt{}, errors.New("finding requires its exact live reporting actor address")
	}
	ref := *finding.Actor
	view, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		if err := snapshot.CheckActiveAttempt(ref); err != nil {
			return false, err
		}
		w, p, err := currentReview(snapshot, ref)
		if err != nil {
			return false, err
		}
		if ref.Step == nil || phaseNumber(w, ref.Phase, true) != attempt {
			return false, errors.New("finding actor does not identify this review step")
		}
		reported, err := workFinding(finding)
		if err != nil {
			return false, err
		}
		switch step := p.Steps[*ref.Step].(type) {
		case *work.AgentReviewStep:
			if step.Report == nil {
				step.Report = &work.ReviewReport{}
			}
			step.Report.Findings = append(step.Report.Findings, reported)
		case *work.ManualReviewStep:
			step.Findings = append(step.Findings, reported)
		default:
			return false, errors.New("findings must be reported by the active reviewer or pending manual assessment")
		}
		return true, nil
	})
	if err != nil {
		return ReviewAttempt{}, err
	}
	return viewReviewAt(view, ref)
}

// ReviewFindingResolution describes the current lifecycle outcome for a review finding.
type ReviewFindingResolution string

const (
	ReviewFindingResolutionOpen              ReviewFindingResolution = "open"
	ReviewFindingResolutionSuperseded        ReviewFindingResolution = "superseded"
	ReviewFindingResolutionUnverified        ReviewFindingResolution = "unverified_reviewer_result"
	ReviewFindingResolutionAddressedManually ReviewFindingResolution = "addressed_manually"
	ReviewFindingResolutionWaived            ReviewFindingResolution = "waived"
	ReviewFindingResolutionDowngraded        ReviewFindingResolution = "downgraded"
	ReviewFindingResolutionCreatedTask       ReviewFindingResolution = "created_task"
	ReviewFindingResolutionTargetedByRun     ReviewFindingResolution = "targeted_by_run"
	ReviewFindingResolutionNonBlocking       ReviewFindingResolution = "non_blocking"
	ReviewFindingResolutionSeparateTask      ReviewFindingResolution = "separate_task"
)

// ResolveReviewFinding classifies the finding lifecycle state used for blocking decisions.
func ResolveReviewFinding(finding ReviewFinding) ReviewFindingResolution {
	if finding.Superseded {
		return ReviewFindingResolutionSuperseded
	}
	if finding.NonAuthoritative {
		return ReviewFindingResolutionUnverified
	}
	if strings.TrimSpace(finding.Waiver) != "" {
		return ReviewFindingResolutionWaived
	}
	if strings.TrimSpace(finding.AddressedManually) != "" {
		return ReviewFindingResolutionAddressedManually
	}
	if strings.TrimSpace(finding.DowngradeReason) != "" {
		return ReviewFindingResolutionDowngraded
	}
	if strings.TrimSpace(finding.CreatedTaskID) != "" {
		return ReviewFindingResolutionCreatedTask
	}
	if finding.TargetedByRunAttempt > 0 {
		return ReviewFindingResolutionTargetedByRun
	}
	switch finding.Type {
	case FindingTypeBlocking:
		return ReviewFindingResolutionOpen
	case FindingTypeSeparateTask:
		return ReviewFindingResolutionSeparateTask
	default:
		return ReviewFindingResolutionNonBlocking
	}
}

// ReviewFindingResolved reports whether a finding has an explicit audit resolution.
func ReviewFindingResolved(finding ReviewFinding) bool {
	switch ResolveReviewFinding(finding) {
	case ReviewFindingResolutionAddressedManually,
		ReviewFindingResolutionWaived,
		ReviewFindingResolutionDowngraded,
		ReviewFindingResolutionCreatedTask,
		ReviewFindingResolutionTargetedByRun:
		return true
	default:
		return false
	}
}

// IsOpenBlockingReviewFinding reports whether a finding currently blocks review progress.
func IsOpenBlockingReviewFinding(finding ReviewFinding) bool {
	return finding.Type == FindingTypeBlocking &&
		ResolveReviewFinding(finding) == ReviewFindingResolutionOpen
}

// ResolveReviewFindingInState classifies a finding using the current run state.
// A failed or incomplete follow-up is an unsuccessful claim: its finding remains
// open for a replacement implementation run.
func ResolveReviewFindingInState(state TaskState, finding ReviewFinding) ReviewFindingResolution {
	if ReviewFindingTargetedByRetryableRun(state, finding) {
		finding.TargetedByRunAttempt = 0
	}
	return ResolveReviewFinding(finding)
}

// ReviewFindingTargetedByRetryableRun reports whether a finding's target refers
// to a failed run or to a run that exited without recording completion.
func ReviewFindingTargetedByRetryableRun(state TaskState, finding ReviewFinding) bool {
	if finding.TargetedByRunAttempt <= 0 {
		return false
	}
	for _, run := range state.Runs {
		if finding.Actor != nil && run.Address.Workflow != finding.Actor.Workflow {
			continue
		}
		if run.Attempt == finding.TargetedByRunAttempt {
			return run.Status == RunStatusFailed || run.Status == RunStatusInterrupted ||
				(run.Status == RunStatusSucceeded && run.Completion == nil)
		}
	}
	return false
}

// ReviewFindingTargetedByFailedRun reports whether a finding's recorded target
// refers to a failed run. The run's ReviewFollowUp retains the audit association
// even when a later retry replaces the finding's current target pointer.
func ReviewFindingTargetedByFailedRun(state TaskState, finding ReviewFinding) bool {
	if finding.TargetedByRunAttempt <= 0 {
		return false
	}
	for _, run := range state.Runs {
		if finding.Actor != nil && run.Address.Workflow != finding.Actor.Workflow {
			continue
		}
		if run.Attempt == finding.TargetedByRunAttempt {
			return run.Status == RunStatusFailed
		}
	}
	return false
}

// IsOpenBlockingReviewFindingInState reports whether a finding currently blocks
// progress after accounting for failed follow-up claims.
func IsOpenBlockingReviewFindingInState(state TaskState, finding ReviewFinding) bool {
	return finding.Type == FindingTypeBlocking &&
		ResolveReviewFindingInState(state, finding) == ReviewFindingResolutionOpen
}

// IsOpenAdvisoryReviewFinding reports whether an advisory can still be promoted or acted on.
func IsOpenAdvisoryReviewFinding(finding ReviewFinding) bool {
	return finding.Type == FindingTypeAdvisory &&
		ResolveReviewFinding(finding) == ReviewFindingResolutionNonBlocking
}

// EligibleAdvisoryFindingIndexes returns advisory findings that can be shown as
// best-effort opportunities alongside a blocker-triggered follow-up. Ordinary
// Advisories and downgraded blockers are both advisory findings; interrupted-review,
// waived, and manually addressed findings are excluded.
func EligibleAdvisoryFindingIndexes(review ReviewAttempt) []int {
	indexes := make([]int, 0)
	for index, finding := range review.Findings {
		if finding.Type != FindingTypeAdvisory ||
			AuditOnlyReviewFinding(review, finding) ||
			strings.TrimSpace(finding.Waiver) != "" ||
			strings.TrimSpace(finding.AddressedManually) != "" {
			continue
		}
		indexes = append(indexes, index)
	}
	return indexes
}

// ReviewHasOpenBlockers reports whether an attempt still has unresolved blocking findings.
func ReviewHasOpenBlockers(review ReviewAttempt) bool {
	return len(UntargetedBlockingFindingIndexes(review)) > 0
}

// UntargetedBlockingFindingIndexes returns open blocker indexes.
func UntargetedBlockingFindingIndexes(review ReviewAttempt) []int {
	indexes := make([]int, 0)
	for index, finding := range review.Findings {
		if IsOpenBlockingReviewFinding(finding) && !AuditOnlyReviewFinding(review, finding) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

// ReviewHasOpenBlockersInState reports whether an attempt has blockers after
// failed follow-up claims are made retryable.
func ReviewHasOpenBlockersInState(state TaskState, review ReviewAttempt) bool {
	return len(UntargetedBlockingFindingIndexesInState(state, review)) > 0
}

// UntargetedBlockingFindingIndexesInState returns open blocker indexes after
// failed follow-up claims are made retryable.
func UntargetedBlockingFindingIndexesInState(state TaskState, review ReviewAttempt) []int {
	indexes := make([]int, 0)
	for index, finding := range review.Findings {
		if IsOpenBlockingReviewFindingInState(state, finding) && !AuditOnlyReviewFinding(review, finding) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

// HasFailedReviewFollowUpTargets reports whether any review finding is claimed
// by a failed follow-up run and is therefore ready for retry.
func HasFailedReviewFollowUpTargets(state TaskState, review ReviewAttempt) bool {
	for _, finding := range review.Findings {
		if ReviewFindingTargetedByFailedRun(state, finding) {
			return true
		}
	}
	return false
}

// UntargetedAutomatedBlockingFindingIndexes returns open blocker indexes that
// came from automated review steps and therefore require an explicit operator
// keep decision before follow-up implementation work may target them. When the
// attempt stopped at a manual step, prior automated-step advisories promoted by
// the operator are treated as manual blocker decisions.
func UntargetedAutomatedBlockingFindingIndexes(review ReviewAttempt) []int {
	if currentReviewStepKind(review) == ReviewStepKindManual {
		return nil
	}
	automatedSteps := automatedReviewStepNames(review)
	indexes := make([]int, 0)
	for _, index := range UntargetedBlockingFindingIndexes(review) {
		finding := review.Findings[index]
		if automatedSteps[finding.Step] {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

// HasUnkeptAutomatedBlockingFindings reports whether follow-up work would
// silently keep automated blockers that were not explicitly kept by the
// operator.
func HasUnkeptAutomatedBlockingFindings(review ReviewAttempt) bool {
	return !review.AutomatedBlockerDecisionKept && len(UntargetedAutomatedBlockingFindingIndexes(review)) > 0
}

// UntargetedBlockingFindingIndexesForFollowUp returns open blocker indexes that
// task-run follow-up may target. ok is false when automated blockers still need
// an explicit keep, downgrade, or waive decision from a fresh task run.
func UntargetedBlockingFindingIndexesForFollowUp(review ReviewAttempt) ([]int, bool) {
	if HasUnkeptAutomatedBlockingFindings(review) {
		return nil, false
	}
	return UntargetedBlockingFindingIndexes(review), true
}

// UntargetedBlockingFindingIndexesForFollowUpInState returns follow-up eligible
// blocker indexes after failed follow-up claims are made retryable.
func UntargetedBlockingFindingIndexesForFollowUpInState(state TaskState, review ReviewAttempt) ([]int, bool) {
	if HasUnkeptAutomatedBlockingFindingsInState(state, review) {
		return nil, false
	}
	return UntargetedBlockingFindingIndexesInState(state, review), true
}

// HasUnkeptAutomatedBlockingFindingsInState reports whether open automated
// blockers still need an operator decision after failed targets are retried.
func HasUnkeptAutomatedBlockingFindingsInState(state TaskState, review ReviewAttempt) bool {
	return !review.AutomatedBlockerDecisionKept &&
		len(UntargetedAutomatedBlockingFindingIndexesInState(state, review)) > 0
}

// UntargetedAutomatedBlockingFindingIndexesInState returns automated blockers
// that still require an operator decision, excluding failed follow-up claims.
func UntargetedAutomatedBlockingFindingIndexesInState(state TaskState, review ReviewAttempt) []int {
	if currentReviewStepKind(review) == ReviewStepKindManual {
		return nil
	}
	automatedSteps := automatedReviewStepNames(review)
	indexes := make([]int, 0)
	for _, index := range UntargetedBlockingFindingIndexesInState(state, review) {
		finding := review.Findings[index]
		if automatedSteps[finding.Step] && !ReviewFindingTargetedByFailedRun(state, finding) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func automatedReviewStepNames(review ReviewAttempt) map[string]bool {
	automatedSteps := make(map[string]bool)
	for _, step := range review.Steps {
		switch step.Kind {
		case ReviewStepKindCheck, ReviewStepKindAgentReview:
			automatedSteps[step.Name] = true
		}
	}
	return automatedSteps
}

// ReviewFindingAuditReason identifies retained findings that cannot drive current
// decisions: superseded slot attempts and unsuccessful or unfinished reviewers.
func ReviewFindingAuditReason(review ReviewAttempt, finding ReviewFinding) string {
	if finding.Superseded {
		return "superseded step"
	}
	var execution *AgentExecution
	if finding.Address != nil && finding.Address.Step >= 0 && finding.Address.Step < len(review.Steps) {
		execution = review.Steps[finding.Address.Step].Execution
	}
	if execution != nil {
		switch execution.Status {
		case RunStatusInterrupted:
			return "interrupted reviewer"
		case RunStatusFailed:
			return "failed reviewer"
		case RunStatusRunning:
			return "unfinished reviewer"
		}
	}
	if finding.NonAuthoritative {
		return "unsuccessful reviewer"
	}
	if finding.Address == nil && InterruptedReviewStepNames(review)[strings.TrimSpace(finding.Step)] {
		return "interrupted reviewer"
	}
	return ""
}

// AuditOnlyReviewFinding reports whether a retained finding is excluded from
// authoritative numbering, blockers, repair input and follow-up creation.
func AuditOnlyReviewFinding(review ReviewAttempt, finding ReviewFinding) bool {
	return ReviewFindingAuditReason(review, finding) != ""
}

// ReviewExecutionInterrupted reports whether reviewer recovery
// interrupted any execution in this attempt.
func ReviewExecutionInterrupted(review ReviewAttempt) bool {
	return len(InterruptedReviewStepNames(review)) > 0
}

func InterruptedReviewStepNames(review ReviewAttempt) map[string]bool {
	steps := make(map[string]bool)
	for _, step := range review.Steps {
		if step.Kind == ReviewStepKindAgentReview && step.Execution != nil &&
			step.Execution.Purpose == AgentExecutionPurposeReview &&
			step.Execution.Status == RunStatusInterrupted &&
			strings.TrimSpace(step.Execution.InterruptionReason) != "" {
			steps[step.Name] = true
		}
	}
	return steps
}

func currentReviewStepKind(review ReviewAttempt) string {
	for index := len(review.Steps) - 1; index >= 0; index-- {
		if review.Steps[index].Name == review.Step {
			return review.Steps[index].Kind
		}
	}
	return ""
}

// Events returns a copy of trace/audit events for a task.
func (s Store) Events(repoID, taskID string) ([]Event, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return nil, err
	}
	return cloneEvents(state.Events), nil
}

// LatestRun returns the last implementation occurrence in the owning workflow.
func LatestRun(state TaskState) (RunAttempt, bool) {
	for i := len(state.Runs) - 1; i >= 0; i-- {
		if state.Runs[i].Address.Workflow == state.CurrentWorkflow {
			return state.Runs[i], true
		}
	}
	return RunAttempt{}, false
}

// CompletionRunHistory identifies the original implementation completion and
// latest run completion for review-facing contexts.
type CompletionRunHistory struct {
	Original RunAttempt
	Latest   RunAttempt
}

// CompletionRunsForReview returns the original implementation completion and
// the latest run completion, allowing review contexts to distinguish follow-up
// rationale from the original implementation rationale. The latest run attempt
// must have completed so reviewers never receive stale completion rationale for
// a newer incomplete attempt.
func CompletionRunsForReview(state TaskState) (CompletionRunHistory, error) {
	if state.Work == nil {
		return CompletionRunHistory{}, errors.New("workflow-owned completion history is required")
	}
	return workflowCompletionRuns(state)
}

// LatestReview returns the last review occurrence in the owning workflow.
func LatestReview(state TaskState) (ReviewAttempt, bool) {
	for i := len(state.Reviews) - 1; i >= 0; i-- {
		if state.Reviews[i].Address.Workflow == state.CurrentWorkflow {
			return state.Reviews[i], true
		}
	}
	return ReviewAttempt{}, false
}

// ActiveRun returns the latest attempt only when it is running.
func ActiveRun(state TaskState) (RunAttempt, bool) {
	latest, ok := LatestRun(state)
	if !ok || latest.Status != RunStatusRunning {
		return RunAttempt{}, false
	}
	return latest, true
}

// FinalizationFacts returns a value copy of any recorded finalization facts.
func FinalizationFacts(state TaskState) Finalization {
	if state.Finalization == nil {
		return Finalization{}
	}
	return ensureFinalization(state.Finalization)
}

// GitFactsFor returns workflow-derived branch and checkout facts.
func GitFactsFor(state TaskState) (GitFacts, bool) {
	facts := normalizeGitFacts(state.GitFacts)
	return facts, !facts.IsZero()
}

// WorkDirectoryFor returns the immutable task working directory.
func WorkDirectoryFor(state TaskState) (WorkDirectory, bool) {
	directory := normalizeWorkDirectory(state.WorkDirectory)
	return directory, !directory.IsZero()
}

func (s Store) nowUTC() time.Time {
	return s.now().UTC()
}

func taskStateRelPath(repoID, taskID string) (string, error) {
	repoID, err := cleanPathComponent("repo id", repoID)
	if err != nil {
		return "", err
	}
	taskID, err = cleanPathComponent("task id", taskID)
	if err != nil {
		return "", err
	}
	return filepath.Join("repos", repoID, "tasks", taskID+".yaml"), nil
}

func cleanPathComponent(label string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if value == "." || value == ".." || strings.ContainsAny(value, `/\\`) || filepath.VolumeName(value) != "" {
		return "", fmt.Errorf("%s %q cannot be used in task state path", label, value)
	}
	return value, nil
}

func normalizeAgentExecution(execution AgentExecution) AgentExecution {
	execution.Purpose = AgentExecutionPurpose(strings.TrimSpace(string(execution.Purpose)))
	execution.Status = RunStatus(strings.TrimSpace(string(execution.Status)))
	execution.Agent = strings.TrimSpace(execution.Agent)
	execution.Profile = strings.TrimSpace(execution.Profile)
	if execution.Profile == "" {
		execution.Profile = execution.Agent
	}
	selection := NewAgentSelection(execution.Harness, execution.Model, execution.Thinking)
	execution.Harness = selection.Harness
	execution.Model = selection.Model
	execution.Thinking = selection.Thinking
	execution.Command = strings.TrimSpace(execution.Command)
	execution.Args = cloneStrings(execution.Args)
	execution.SessionName = strings.TrimSpace(execution.SessionName)
	execution.SupervisorPID, execution.ChildPID = normalizeProcessPIDs(execution.SupervisorPID, execution.ChildPID)
	execution.InterruptionReason = strings.TrimSpace(execution.InterruptionReason)
	execution.InterruptionTrigger = strings.TrimSpace(execution.InterruptionTrigger)
	if !execution.StartedAt.IsZero() {
		execution.StartedAt = execution.StartedAt.UTC()
	}
	if execution.FinishedAt != nil {
		finished := execution.FinishedAt.UTC()
		execution.FinishedAt = &finished
		if execution.DurationMillis == 0 {
			execution.DurationMillis = durationMillis(execution.StartedAt, finished)
		}
	}
	if execution.Session != nil {
		session := normalizeAgentSession(*execution.Session)
		if agentSessionIsZero(session) {
			execution.Session = nil
		} else {
			execution.Session = &session
		}
	}
	if execution.Usage != nil {
		usage := normalizeAgentUsage(*execution.Usage)
		if agentUsageIsZero(usage) {
			execution.Usage = nil
		} else {
			execution.Usage = &usage
		}
	}
	if execution.UsageCost != nil {
		cost := normalizeAgentUsageCost(*execution.UsageCost)
		if agentUsageCostIsZero(cost) {
			execution.UsageCost = nil
		} else {
			execution.UsageCost = &cost
		}
	}
	if execution.Launch != nil {
		launch := normalizeAgentLaunch(*execution.Launch)
		execution.Launch = &launch
	}
	execution.UsageCapture = normalizeAgentUsageCapture(execution.UsageCapture, time.Time{})
	return execution
}

func normalizeProcessPIDs(supervisorPID int, childPID int) (int, int) {
	if supervisorPID < 0 {
		supervisorPID = 0
	}
	if childPID < 0 {
		childPID = 0
	}
	return supervisorPID, childPID
}

func normalizeAgentLaunch(launch AgentLaunch) AgentLaunch {
	launch.Mode = AgentLaunchMode(strings.TrimSpace(string(launch.Mode)))
	launch.FallbackReason = strings.TrimSpace(launch.FallbackReason)
	if launch.SourceSession != nil {
		session := normalizeAgentSession(*launch.SourceSession)
		if agentSessionIsZero(session) {
			launch.SourceSession = nil
		} else {
			launch.SourceSession = &session
		}
	}
	if launch.UsageBaseline != nil {
		usage := normalizeAgentUsage(*launch.UsageBaseline)
		launch.UsageBaseline = &usage
	}
	if launch.CostBaseline != nil {
		cost := *launch.CostBaseline
		if cost < 0 {
			cost = 0
		}
		launch.CostBaseline = &cost
	}
	return launch
}

func normalizeAgentSession(session AgentSession) AgentSession {
	return AgentSession{
		ID:      strings.TrimSpace(session.ID),
		LogPath: strings.TrimSpace(session.LogPath),
	}
}

func agentSessionIsZero(session AgentSession) bool {
	return strings.TrimSpace(session.ID) == "" && strings.TrimSpace(session.LogPath) == ""
}

func normalizeAgentUsage(usage AgentUsage) AgentUsage {
	if usage.InputTokens < 0 {
		usage.InputTokens = 0
	}
	if usage.CachedInputTokens < 0 {
		usage.CachedInputTokens = 0
	}
	if usage.OutputTokens < 0 {
		usage.OutputTokens = 0
	}
	if usage.ReasoningOutputTokens < 0 {
		usage.ReasoningOutputTokens = 0
	}
	if usage.TotalTokens < 0 {
		usage.TotalTokens = 0
	}
	return usage
}

func agentUsageIsZero(usage AgentUsage) bool {
	return usage.InputTokens == 0 &&
		usage.CachedInputTokens == 0 &&
		usage.OutputTokens == 0 &&
		usage.ReasoningOutputTokens == 0 &&
		usage.TotalTokens == 0
}

func normalizeAgentUsageCost(cost AgentUsageCost) AgentUsageCost {
	cost.Kind = strings.TrimSpace(cost.Kind)
	cost.Currency = strings.TrimSpace(cost.Currency)
	if cost.AmountMicroUSD < 0 {
		cost.AmountMicroUSD = 0
	}
	if cost.Pricing != nil {
		pricing := normalizeAgentUsagePricing(*cost.Pricing)
		if agentUsagePricingIsZero(pricing) {
			cost.Pricing = nil
		} else {
			cost.Pricing = &pricing
		}
	}
	cost.Source = strings.TrimSpace(cost.Source)
	cost.Notes = strings.TrimSpace(cost.Notes)
	return cost
}

func normalizeAgentUsagePricing(pricing AgentUsagePricing) AgentUsagePricing {
	pricing.Provider = strings.TrimSpace(pricing.Provider)
	pricing.Model = strings.TrimSpace(pricing.Model)
	pricing.ServiceTier = strings.TrimSpace(pricing.ServiceTier)
	pricing.EffectiveDate = strings.TrimSpace(pricing.EffectiveDate)
	pricing.Unit = strings.TrimSpace(pricing.Unit)
	pricing.InputUSDPerMillionTokens = strings.TrimSpace(pricing.InputUSDPerMillionTokens)
	pricing.CachedUSDPerMillionTokens = strings.TrimSpace(pricing.CachedUSDPerMillionTokens)
	pricing.OutputUSDPerMillionTokens = strings.TrimSpace(pricing.OutputUSDPerMillionTokens)
	pricing.ReasoningOutputTreatment = strings.TrimSpace(pricing.ReasoningOutputTreatment)
	pricing.Source = strings.TrimSpace(pricing.Source)
	pricing.SourceURL = strings.TrimSpace(pricing.SourceURL)
	pricing.SourceAccessed = strings.TrimSpace(pricing.SourceAccessed)
	pricing.SourcePublished = strings.TrimSpace(pricing.SourcePublished)
	pricing.Notes = strings.TrimSpace(pricing.Notes)
	return pricing
}

func agentUsagePricingIsZero(pricing AgentUsagePricing) bool {
	return pricing == (AgentUsagePricing{})
}

func agentUsageCostIsZero(cost AgentUsageCost) bool {
	return cost.AmountMicroUSD == 0 && !agentUsageCostIsKnownZero(cost)
}

func agentUsageCostIsKnownZero(cost AgentUsageCost) bool {
	return cost.Pricing != nil &&
		cost.Kind != "" &&
		cost.Currency != "" &&
		!agentUsagePricingIsZero(*cost.Pricing)
}

func normalizeAgentUsageCapture(capture AgentUsageCapture, capturedAt time.Time) AgentUsageCapture {
	capture.Status = UsageCaptureStatus(strings.TrimSpace(string(capture.Status)))
	capture.Reason = strings.TrimSpace(capture.Reason)
	if capture.CandidateCount < 0 {
		capture.CandidateCount = 0
	}
	if capture.CapturedAt != nil {
		at := capture.CapturedAt.UTC()
		capture.CapturedAt = &at
	} else if capture.Status != "" && !capturedAt.IsZero() {
		at := capturedAt.UTC()
		capture.CapturedAt = &at
	}
	return capture
}

func durationMillis(started time.Time, finished time.Time) int64 {
	if started.IsZero() || finished.IsZero() || finished.Before(started) {
		return 0
	}
	return finished.Sub(started).Milliseconds()
}

func normalizeWorkDirectory(directory WorkDirectory) WorkDirectory {
	return WorkDirectory{Path: normalizeWorktreePath(directory.Path)}
}

func normalizeGitFacts(facts GitFacts) GitFacts {
	return GitFacts{
		Branch:   strings.TrimSpace(facts.Branch),
		Worktree: normalizeWorktreePath(facts.Worktree),
	}
}

func normalizeWorktreePath(worktree string) string {
	worktree = strings.TrimSpace(worktree)
	if filepath.IsAbs(worktree) {
		if canonicalWorktree, err := pathutil.CanonicalAbs(worktree); err == nil {
			return canonicalWorktree
		}
	}
	return worktree
}

func normalizeReviewFinding(finding ReviewFinding) (ReviewFinding, error) {
	finding.Type = FindingType(strings.TrimSpace(string(finding.Type)))
	finding.Title = strings.TrimSpace(finding.Title)
	finding.Description = strings.TrimSpace(finding.Description)
	finding.Step = strings.TrimSpace(finding.Step)
	finding.SuggestedAction = strings.TrimSpace(finding.SuggestedAction)
	finding.DowngradeReason = strings.TrimSpace(finding.DowngradeReason)
	finding.Waiver = strings.TrimSpace(finding.Waiver)
	finding.TaskProposal = normalizeReviewTaskProposal(finding.TaskProposal)
	finding.CreatedTaskID = strings.TrimSpace(finding.CreatedTaskID)
	if finding.CreatedTaskAt != nil && finding.CreatedTaskAt.IsZero() {
		finding.CreatedTaskAt = nil
	}

	if !validFindingType(finding.Type) {
		return ReviewFinding{}, fmt.Errorf("unsupported finding type %q", finding.Type)
	}
	if finding.Title == "" {
		return ReviewFinding{}, errors.New("title is required")
	}
	if finding.Description == "" {
		return ReviewFinding{}, errors.New("description is required")
	}
	if finding.Type == FindingTypeSeparateTask {
		if finding.TaskProposal.Title == "" {
			return ReviewFinding{}, errors.New("task_proposal.title is required for separate-task findings")
		}
		if finding.TaskProposal.Description == "" {
			return ReviewFinding{}, errors.New("task_proposal.description is required for separate-task findings")
		}
		if finding.TaskProposal.AcceptanceCriteria == "" {
			return ReviewFinding{}, errors.New("task_proposal.acceptance_criteria is required for separate-task findings")
		}
	} else if !finding.TaskProposal.IsZero() {
		return ReviewFinding{}, errors.New("task_proposal is only supported for separate-task findings")
	}
	if finding.TargetedByRunAttempt < 0 {
		return ReviewFinding{}, errors.New("targeted_by_run_attempt cannot be negative")
	}
	return finding, nil
}

func normalizeReviewTaskProposal(proposal ReviewTaskProposal) ReviewTaskProposal {
	return ReviewTaskProposal{
		Title:              strings.TrimSpace(proposal.Title),
		Description:        strings.TrimSpace(proposal.Description),
		AcceptanceCriteria: strings.TrimSpace(proposal.AcceptanceCriteria),
	}
}

func ensureFinalization(finalization *Finalization) Finalization {
	if finalization == nil {
		return Finalization{}
	}
	clone := *finalization
	clone.IntegrationFlow = publication.IntegrationFlow(strings.TrimSpace(string(clone.IntegrationFlow)))
	clone.DestinationBranch = strings.TrimSpace(clone.DestinationBranch)
	clone.Commit = strings.TrimSpace(clone.Commit)
	clone.MergeCommit = strings.TrimSpace(clone.MergeCommit)

	return clone
}

func validFindingType(findingType FindingType) bool {
	switch findingType {
	case FindingTypeBlocking, FindingTypeAdvisory, FindingTypeSeparateTask:
		return true
	default:
		return false
	}
}

func cloneEvents(events []Event) []Event {
	if events == nil {
		return nil
	}
	clone := make([]Event, len(events))
	copy(clone, events)
	for i := range clone {
		clone[i].Execution = cloneAgentExecutionPointer(clone[i].Execution)
		clone[i].ConflictFiles = cloneStrings(clone[i].ConflictFiles)
	}
	return clone
}

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	clone := make([]string, len(values))
	copy(clone, values)
	return clone
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneAgentExecutionPointer(value *AgentExecution) *AgentExecution {
	if value == nil {
		return nil
	}
	clone := normalizeAgentExecution(*value)
	return &clone
}
