// Package taskstate persists Orpheus-owned per-task execution state.
package taskstate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/pathutil"
	"github.com/hea3ven/orpheus/internal/publication"
	orstate "github.com/hea3ven/orpheus/internal/state"
	"gopkg.in/yaml.v3"
)

const schemaVersion = 4

// RunStatus is the M3 status for an attached run attempt.
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

// EventType is a trace/audit event type stored in the per-task state file.
type EventType string

const (
	EventWorktreeCreated        EventType = "worktree_created"
	EventTaskBranchCreated      EventType = "task_branch_created"
	EventWorktreeReused         EventType = "worktree_reused"
	EventWorktreeRecreated      EventType = "worktree_recreated"
	EventWorktreeRemoved        EventType = "worktree_removed"
	EventRunStarted             EventType = "run_started"
	EventRunFinished            EventType = "run_finished"
	EventRunStartFailed         EventType = "run_start_failed"
	EventRunInterrupted         EventType = "run_interrupted"
	EventReviewInterrupted      EventType = "review_interrupted"
	EventCompletionRecorded     EventType = "completion_recorded"
	EventCompletionRepeated     EventType = "completion_repeated"
	EventChangesPushed          EventType = "changes_pushed"
	EventPRCreated              EventType = "pr_created"
	EventPRRecovered            EventType = "pr_recovered"
	EventFinalizationFailed     EventType = "finalization_failed"
	EventTaskClosed             EventType = "task_closed"
	EventSyncConflictStarted    EventType = "sync_conflict_started"
	EventSyncConflictFinished   EventType = "sync_conflict_finished"
	EventSyncConflictFailed     EventType = "sync_conflict_failed"
	EventSyncConflictRolledBack EventType = "sync_conflict_rolled_back"
	EventSyncConflictUnresolved EventType = "sync_conflict_unresolved"
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

// TaskState is the human-readable YAML schema for one task's Orpheus state.
type TaskState struct {
	Version int    `yaml:"version"`
	RepoID  string `yaml:"repo_id"`
	TaskID  string `yaml:"task_id"`

	// WorkDirectory is the immutable filesystem location selected at first
	// dispatch. Branch and other Git facts may evolve during publication.
	WorkDirectory WorkDirectory `yaml:"work_directory,omitempty"`

	// GitFacts are the current branch and checkout facts for the fixed work
	// directory. Publication may update these facts without changing WorkDirectory.
	GitFacts GitFacts `yaml:"git_facts,omitempty"`

	// Target is retained only to reconcile state written before work-directory
	// selection was separated from publication Git facts. New state must use
	// GitFacts instead.
	Target TaskTarget `yaml:"target,omitempty"`

	Runs    []RunAttempt    `yaml:"runs,omitempty"`
	Reviews []ReviewAttempt `yaml:"reviews,omitempty"`
	Events  []Event         `yaml:"events,omitempty"`

	Finalization *Finalization `yaml:"finalization,omitempty"`

	// ActiveSyncConflict is authoritative recovery state, never inferred from
	// the append-only sync-conflict audit events.
	ActiveSyncConflict *SyncConflictOperation `yaml:"active_sync_conflict,omitempty"`
}

// UnmarshalYAML normalizes task-level state after direct YAML decodes.
func (s *TaskState) UnmarshalYAML(value *yaml.Node) error {
	type plain TaskState
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	normalized := TaskState(decoded)
	normalized.Target = normalizeTaskTarget(normalized.Target)
	normalized.GitFacts = normalizeGitFacts(normalized.GitFacts)
	normalized.WorkDirectory = normalizeWorkDirectory(normalized.WorkDirectory)
	*s = normalized
	return nil
}

// WorkDirectory records the immutable task working directory selected by first dispatch.
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

// TaskTarget is the legacy persisted target representation. It is an alias so
// callers decoding old state can be reconciled into GitFacts without conversion.
type TaskTarget = GitFacts

// RunAttempt records one attached execution attempt.
type RunAttempt struct {
	Attempt int       `yaml:"attempt"`
	Status  RunStatus `yaml:"status"`

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
	Purpose AgentExecutionPurpose `yaml:"purpose"`
	Status  RunStatus             `yaml:"status"`

	Agent    string `yaml:"agent,omitempty"`
	Profile  string `yaml:"profile,omitempty"`
	Harness  string `yaml:"harness,omitempty"`
	Model    string `yaml:"model,omitempty"`
	Thinking string `yaml:"thinking,omitempty"`

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

	// FindingIndexes are required blocking findings. The field name is retained
	// for compatibility with persisted follow-up provenance.
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
	Attempt int          `yaml:"attempt"`
	Status  ReviewStatus `yaml:"status"`

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
	Kind       string            `yaml:"kind"`
	Name       string            `yaml:"name"`
	Execution  *AgentExecution   `yaml:"execution,omitempty"`
	Comparison *ReviewComparison `yaml:"comparison,omitempty"`
	ExitCode   *int              `yaml:"exit_code,omitempty"`
}

// ReviewComparison records an opt-in alternate reviewer execution and its raw findings.
type ReviewComparison struct {
	AlternateExecution *AgentExecution          `yaml:"alternate_execution,omitempty"`
	AlternateFindings  []AlternateReviewFinding `yaml:"alternate_findings,omitempty"`
	Failure            string                   `yaml:"failure,omitempty"`
	InputInterrupted   bool                     `yaml:"input_interrupted,omitempty"`
}

// AlternateReviewFinding is an alternate finding retained outside the authoritative flow.
type AlternateReviewFinding struct {
	Finding        ReviewFinding                  `yaml:"finding"`
	Classification AlternateFindingClassification `yaml:"classification,omitempty"`
	DuplicateOf    int                            `yaml:"duplicate_of,omitempty"`
}

// AlternateFindingClassification records the operator's comparison decision.
type AlternateFindingClassification string

const (
	AlternateFindingAdmitted  AlternateFindingClassification = "admitted"
	AlternateFindingDuplicate AlternateFindingClassification = "duplicate"
	AlternateFindingExcluded  AlternateFindingClassification = "excluded"
)

// ReviewFinding records one review finding.
type ReviewFinding struct {
	Type        FindingType `yaml:"type"`
	Title       string      `yaml:"title"`
	Description string      `yaml:"description"`

	Step              string             `yaml:"step,omitempty"`
	Reviewer          string             `yaml:"reviewer,omitempty"`
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

// UnmarshalYAML accepts the structured proposal schema and older scalar proposals.
func (p *ReviewTaskProposal) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		text := strings.TrimSpace(value.Value)
		*p = ReviewTaskProposal{
			Title:              text,
			Description:        text,
			AcceptanceCriteria: text,
		}
		return nil
	}

	type plain ReviewTaskProposal
	var decoded plain
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*p = ReviewTaskProposal(decoded)
	return nil
}

// Finalization records factual data from human-side main/solo finalization.
type Finalization struct {
	IntegrationFlow      publication.IntegrationFlow `yaml:"integration_flow,omitempty"`
	DestinationBranch    string                      `yaml:"destination_branch,omitempty"`
	PublicationStartedAt *time.Time                  `yaml:"publication_started_at,omitempty"`
	CommittedAt          *time.Time                  `yaml:"committed_at,omitempty"`
	Commit               string                      `yaml:"commit,omitempty"`
	PendingCommit        *FinalizationCommitIntent   `yaml:"pending_commit,omitempty"`
	MergeCommit          string                      `yaml:"merge_commit,omitempty"`
	PushedAt             *time.Time                  `yaml:"pushed_at,omitempty"`
	ClosedAt             *time.Time                  `yaml:"closed_at,omitempty"`
}

// FinalizationCommitIntent records the commit Orpheus is about to create so a
// task-done retry can safely recover if Git succeeds before state persistence.
type FinalizationCommitIntent struct {
	Parent  string `yaml:"parent"`
	Message string `yaml:"message"`
}

// SyncConflictPhase records the ordered side-effect boundary of a conflicted sync.
type SyncConflictPhase string

const (
	SyncConflictPhasePrepared       SyncConflictPhase = "prepared"
	SyncConflictPhaseConflicted     SyncConflictPhase = "conflicted"
	SyncConflictPhaseResolving      SyncConflictPhase = "resolving"
	SyncConflictPhaseLocalCompleted SyncConflictPhase = "local_completed"
	SyncConflictPhasePushIntent     SyncConflictPhase = "push_intent"
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

// SyncConflictOperation is the first-class recovery authority for one local
// sync conflict resolver. It remains present when recovery is unresolved.
type SyncConflictOperation struct {
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
	Type EventType `yaml:"type"`
	At   time.Time `yaml:"at"`

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

	Message                       string `yaml:"message,omitempty"`
	RequestedSummary              string `yaml:"requested_summary,omitempty"`
	RequestedDescription          string `yaml:"requested_description,omitempty"`
	RequestedDetailedDescription  string `yaml:"requested_detailed_description,omitempty"`
	RequestedTechnicalExplanation string `yaml:"requested_technical_explanation,omitempty"`

	PRURL           string `yaml:"pr_url,omitempty"`
	ObservedPRState string `yaml:"observed_pr_state,omitempty"`
	PushTarget      string `yaml:"push_target,omitempty"`
	CloseReason     string `yaml:"close_reason,omitempty"`
}

// DisplayName returns the concise human-readable name for an audit event.
//
//nolint:funlen // One switch intentionally lists every persisted audit event.
func (e Event) DisplayName() string {
	switch e.Type {
	case EventWorktreeCreated:
		return "Worktree created"
	case EventTaskBranchCreated:
		return "Task branch created"
	case EventWorktreeReused:
		return "Worktree reused"
	case EventWorktreeRecreated:
		return "Worktree recreated"
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
		return "Primary review interrupted"
	case EventCompletionRecorded:
		return "Completion recorded"
	case EventCompletionRepeated:
		return "Completion repeated"
	case EventChangesPushed:
		return "Pushed " + e.PushTarget
	case EventPRCreated:
		return "PR created"
	case EventPRRecovered:
		return "PR recovered"
	case EventFinalizationFailed:
		return "Finalization failed"
	case EventTaskClosed:
		return "Task closed"
	case EventSyncConflictStarted:
		return "Sync conflict resolution started"
	case EventSyncConflictFinished:
		return "Sync conflict resolution finished"
	case EventSyncConflictFailed:
		return "Sync conflict resolution failed"
	case EventSyncConflictRolledBack:
		return "Sync conflict resolution rolled back"
	case EventSyncConflictUnresolved:
		return "Sync conflict resolution unresolved"
	default:
		return string(e.Type)
	}
}

// SetupEventOptions describes task execution target context for a setup event.
type SetupEventOptions struct {
	Branch   string
	Worktree string
}

// StartRunOptions describes the run attempt being started.
type StartRunOptions struct {
	Agent     string
	Profile   string
	Selection AgentSelection
	Harness   string
	Model     string
	Thinking  string

	Command     string
	Args        []string
	SessionName string

	// WorkDirectory is immutable after the first run. Branch is an evolving Git
	// fact and Worktree remains for compatibility with persisted legacy state.
	WorkDirectory string
	Branch        string
	Worktree      string

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

// InterruptPrimaryReviewExecutionOptions records a safely recovered primary
// reviewer execution and the recovery source.
type InterruptPrimaryReviewExecutionOptions struct {
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

// SyncConflictResolutionEventOptions describes a sync conflict-repair audit event.
type SyncConflictResolutionEventOptions struct {
	Execution     AgentExecution
	Branch        string
	DefaultBranch string
	Worktree      string
	PRURL         string
	ConflictFiles []string
	Commit        string
	Usage         RecordRunUsageOptions
}

type completeRunPayload struct {
	summary              string
	description          string
	detailedDescription  string
	technicalExplanation string
	commit               string
	commitError          string
}

// RepeatedCompletionOptions describes an ignored repeated agent completion payload.
type RepeatedCompletionOptions struct {
	Summary              string
	Description          string
	DetailedDescription  string
	TechnicalExplanation string
}

// StartReviewOptions describes the selected review pipeline.
type StartReviewOptions struct {
	Pipeline string
	Step     string
}

// RecordReviewStepOptions describes one executed review step.
type RecordReviewStepOptions struct {
	Kind      string
	Name      string
	Execution *AgentExecution
	ExitCode  *int
}

// FinishReviewStepExecutionOptions describes terminal facts for a review agent step execution.
type FinishReviewStepExecutionOptions struct {
	Status     RunStatus
	FinishedAt time.Time

	Session      *AgentSession
	Usage        *AgentUsage
	UsageCost    *AgentUsageCost
	UsageCapture AgentUsageCapture
	Model        string
}

// AlternateReviewFindingDecision is one operator classification for an alternate finding.
type AlternateReviewFindingDecision struct {
	FindingIndex   int
	Classification AlternateFindingClassification
	DuplicateOf    int
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
	dir, err := s.paths.DataPath(filepath.Join("repos", repoID, "tasks"))
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		return nil, fmt.Errorf("list task states for repo %s: %w", repoID, err)
	}

	taskIDs := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".yaml" {
			continue
		}
		taskID := strings.TrimSuffix(entry.Name(), ".yaml")
		if _, err := cleanPathComponent("task id", taskID); err != nil {
			continue
		}
		taskIDs = append(taskIDs, taskID)
	}
	sort.Strings(taskIDs)
	return taskIDs, nil
}

// Load reads a task state file. Missing files load as an empty task state.
func (s Store) Load(repoID, taskID string) (TaskState, error) {
	repoID, taskID, rel, err := normalizedLocation(repoID, taskID)
	if err != nil {
		return TaskState{}, err
	}
	path, _ := s.paths.DataPath(rel)
	span := logging.Start(context.Background(), s.logger, "task state load",
		slog.String("component", "taskstate"),
		slog.String("operation", "load"),
		slog.String("path", path),
		slog.String("repo_id", repoID),
		slog.String("task_id", taskID),
	)

	loaded := emptyTaskState(repoID, taskID)
	if err := s.paths.ReadDataYAML(rel, &loaded); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			span.Finish(context.Background(), logging.StatusExpectedAbsence, slog.Int("event_count", 0))
			return emptyTaskState(repoID, taskID), nil
		}
		err = fmt.Errorf("load task state %s/%s: %w", repoID, taskID, err)
		span.FinishError(context.Background(), err)
		return TaskState{}, err
	}

	loaded = migrateLoadedState(loaded)
	if err := validateLoadedState(loaded, repoID, taskID); err != nil {
		err = fmt.Errorf("load task state %s/%s: %w", repoID, taskID, err)
		span.FinishError(context.Background(), err)
		return TaskState{}, err
	}
	loaded = normalizeState(loaded, repoID, taskID)
	span.Finish(context.Background(), logging.StatusSuccess, slog.Int("event_count", len(loaded.Events)))
	return loaded, nil
}

// LatestRun returns the highest-numbered run attempt for a task.
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

// RecordSetupEvent appends a durable task execution setup event.
func (s Store) RecordSetupEvent(repoID, taskID string, eventType EventType, opts SetupEventOptions) (Event, error) {
	switch eventType {
	case EventWorktreeCreated, EventTaskBranchCreated, EventWorktreeReused, EventWorktreeRecreated:
	default:
		return Event{}, fmt.Errorf("record setup event for task %s/%s: unsupported setup event type %q", repoID, taskID, eventType)
	}

	return s.appendEvent(repoID, taskID, Event{
		Type:     eventType,
		Branch:   strings.TrimSpace(opts.Branch),
		Worktree: strings.TrimSpace(opts.Worktree),
	})
}

// StartRun appends a new running attempt and a run_started event.
func (s Store) StartRun(repoID, taskID string, opts StartRunOptions) (RunAttempt, error) {
	span := s.operationSpan("start_run", repoID, taskID)
	state, err := s.Load(repoID, taskID)
	if err != nil {
		span.FinishError(context.Background(), err)
		return RunAttempt{}, err
	}
	if active, ok := ActiveRun(state); ok {
		err := fmt.Errorf("start run attempt for task %s/%s: %w (attempt %d)", repoID, taskID, ErrActiveRun, active.Attempt)
		span.FinishError(context.Background(), err, slog.Int("active_attempt", active.Attempt))
		return RunAttempt{}, err
	}

	now := s.nowUTC()
	selection := opts.agentSelection()
	attempt := RunAttempt{
		Attempt:        nextAttemptNumber(state),
		Status:         RunStatusRunning,
		Execution:      startRunAgentExecution(opts, selection, now),
		ReviewFollowUp: normalizeReviewFollowUp(opts.ReviewFollowUp),
	}
	if err := lockTaskWorkDirectory(&state, WorkDirectory{Path: opts.WorkDirectory}); err != nil {
		err = fmt.Errorf("start run attempt for task %s/%s: %w", repoID, taskID, err)
		span.FinishError(context.Background(), err, slog.Int("attempt", attempt.Attempt))
		return RunAttempt{}, err
	}
	if err := lockTaskGitFacts(&state, GitFacts{
		Branch:   opts.Branch,
		Worktree: opts.Worktree,
	}); err != nil {
		err = fmt.Errorf("start run attempt for task %s/%s: %w", repoID, taskID, err)
		span.FinishError(context.Background(), err, slog.Int("attempt", attempt.Attempt))
		return RunAttempt{}, err
	}
	state.Runs = append(state.Runs, attempt)
	state.Events = append(state.Events, Event{
		Type:    EventRunStarted,
		At:      now,
		Attempt: attempt.Attempt,
		Status:  RunStatusRunning,
		Agent:   attempt.Execution.Agent,
	})

	if err := s.save(state); err != nil {
		span.FinishError(context.Background(), err, slog.Int("attempt", attempt.Attempt))
		return RunAttempt{}, err
	}
	span.Finish(context.Background(), logging.StatusSuccess, slog.Int("attempt", attempt.Attempt))
	return attempt, nil
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
		Command:       opts.Command,
		Args:          cloneStrings(opts.Args),
		SessionName:   opts.SessionName,
		StartedAt:     now,
		Launch:        opts.Launch,
		SupervisorPID: opts.SupervisorPID,
	})
}

// RecordRunUsage records best-effort session and usage telemetry for a run.
func (s Store) RecordRunUsage(
	repoID,
	taskID string,
	attempt int,
	opts RecordRunUsageOptions,
) (RunAttempt, error) {
	span := s.operationSpan("record_run_usage", repoID, taskID, slog.Int("attempt", attempt))
	state, err := s.Load(repoID, taskID)
	if err != nil {
		span.FinishError(context.Background(), err)
		return RunAttempt{}, err
	}
	index := runAttemptIndex(state, attempt)
	if index < 0 {
		err := fmt.Errorf("record run usage for task %s/%s: attempt %d was not found", repoID, taskID, attempt)
		span.FinishError(context.Background(), err)
		return RunAttempt{}, err
	}

	state.Runs[index].Execution = normalizeAgentExecution(
		applyRunUsageOptions(state.Runs[index].Execution, opts, s.nowUTC()),
	)
	if err := s.save(state); err != nil {
		span.FinishError(context.Background(), err)
		return RunAttempt{}, err
	}
	span.Finish(context.Background(), logging.StatusSuccess,
		slog.String("capture_status", string(opts.UsageCapture.Status)),
		slog.Int("candidate_count", opts.UsageCapture.CandidateCount),
	)
	return state.Runs[index], nil
}

// TargetReviewFindings marks findings from a review as addressed by a run attempt.
func (s Store) TargetReviewFindings(
	repoID,
	taskID string,
	reviewAttempt int,
	findingIndexes []int,
	runAttempt int,
) (ReviewAttempt, error) {
	if reviewAttempt <= 0 {
		return ReviewAttempt{}, fmt.Errorf("target review findings for task %s/%s: review attempt must be positive", repoID, taskID)
	}
	if runAttempt <= 0 {
		return ReviewAttempt{}, fmt.Errorf("target review findings for task %s/%s: run attempt must be positive", repoID, taskID)
	}
	if len(findingIndexes) == 0 {
		return ReviewAttempt{}, fmt.Errorf("target review findings for task %s/%s: at least one finding index is required", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	reviewIndex := reviewAttemptIndex(state, reviewAttempt)
	if reviewIndex < 0 {
		return ReviewAttempt{}, fmt.Errorf("target review findings for task %s/%s: review attempt %d was not found", repoID, taskID, reviewAttempt)
	}
	for _, findingIndex := range findingIndexes {
		if findingIndex < 0 || findingIndex >= len(state.Reviews[reviewIndex].Findings) {
			return ReviewAttempt{}, fmt.Errorf("target review findings for task %s/%s: finding index %d is out of range", repoID, taskID, findingIndex)
		}
		finding := state.Reviews[reviewIndex].Findings[findingIndex]
		if finding.TargetedByRunAttempt != 0 &&
			finding.TargetedByRunAttempt != runAttempt &&
			!ReviewFindingTargetedByRetryableRun(state, finding) {
			return ReviewAttempt{}, fmt.Errorf(
				"target review findings for task %s/%s: finding index %d is already targeted by run attempt %d",
				repoID,
				taskID,
				findingIndex,
				finding.TargetedByRunAttempt,
			)
		}
		state.Reviews[reviewIndex].Findings[findingIndex].TargetedByRunAttempt = runAttempt
	}

	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[reviewIndex], nil
}

// FinishRun records a succeeded or failed attached process exit and appends run_finished.
func (s Store) FinishRun(repoID, taskID string, attempt int, status RunStatus) (RunAttempt, error) {
	if status != RunStatusSucceeded && status != RunStatusFailed {
		return RunAttempt{}, fmt.Errorf("finish run attempt for task %s/%s: status must be %q or %q, got %q", repoID, taskID, RunStatusSucceeded, RunStatusFailed, status)
	}
	return s.completeRun(repoID, taskID, attempt, status, EventRunFinished, "")
}

// RecordRunChildPID records the direct child PID observed immediately after launch.
func (s Store) RecordRunChildPID(repoID, taskID string, attempt int, pid int) (RunAttempt, error) {
	if pid <= 0 {
		return RunAttempt{}, fmt.Errorf("record child PID for task %s/%s: PID must be positive", repoID, taskID)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return RunAttempt{}, err
	}
	index := runAttemptIndex(state, attempt)
	if index < 0 {
		return RunAttempt{}, fmt.Errorf("record child PID for task %s/%s: attempt %d was not found", repoID, taskID, attempt)
	}
	if state.Runs[index].Status != RunStatusRunning {
		return RunAttempt{}, fmt.Errorf("record child PID for task %s/%s: attempt %d is %q, expected %q", repoID, taskID, attempt, state.Runs[index].Status, RunStatusRunning)
	}
	state.Runs[index].Execution.ChildPID = pid
	if err := s.save(state); err != nil {
		return RunAttempt{}, err
	}
	return state.Runs[index], nil
}

// InterruptRun records a reconciled interrupted terminal state without claiming
// a successful or failed process exit.
func (s Store) InterruptRun(repoID, taskID string, attempt int, opts InterruptRunOptions) (RunAttempt, error) {
	reason := strings.TrimSpace(opts.Reason)
	trigger := strings.TrimSpace(opts.Trigger)
	if reason == "" || trigger == "" {
		return RunAttempt{}, fmt.Errorf("interrupt run attempt for task %s/%s: reason and trigger are required", repoID, taskID)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return RunAttempt{}, err
	}
	index := runAttemptIndex(state, attempt)
	if index < 0 {
		return RunAttempt{}, fmt.Errorf("interrupt run attempt for task %s/%s: attempt %d was not found", repoID, taskID, attempt)
	}
	if state.Runs[index].Status != RunStatusRunning {
		return RunAttempt{}, fmt.Errorf("interrupt run attempt for task %s/%s: attempt %d is %q, expected %q", repoID, taskID, attempt, state.Runs[index].Status, RunStatusRunning)
	}
	now := s.nowUTC()
	state.Runs[index].Status = RunStatusInterrupted
	state.Runs[index].Execution.Status = RunStatusInterrupted
	state.Runs[index].Execution.FinishedAt = &now
	state.Runs[index].Execution.DurationMillis = durationMillis(state.Runs[index].Execution.StartedAt, now)
	updated := state.Runs[index]
	state.Events = append(state.Events, Event{Type: EventRunInterrupted, At: now, Attempt: attempt, Status: RunStatusInterrupted, Agent: updated.Execution.Agent, InterruptionReason: reason, InterruptionTrigger: trigger})
	if err := s.save(state); err != nil {
		return RunAttempt{}, err
	}
	return updated, nil
}

// CompleteRun records agent-authored completion facts without finishing the attached run.
func (s Store) CompleteRun(repoID, taskID string, attempt int, opts CompleteRunOptions) (RunAttempt, error) {
	span := s.operationSpan("record_completion", repoID, taskID, slog.Int("attempt", attempt))
	payload, err := completeRunPayloadFromOptions(repoID, taskID, opts)
	if err != nil {
		span.FinishError(context.Background(), err)
		return RunAttempt{}, err
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		span.FinishError(context.Background(), err)
		return RunAttempt{}, err
	}

	index := runAttemptIndex(state, attempt)
	if index < 0 {
		err := fmt.Errorf("complete run attempt for task %s/%s: attempt %d was not found", repoID, taskID, attempt)
		span.FinishError(context.Background(), err)
		return RunAttempt{}, err
	}

	run := state.Runs[index]
	if run.Completion != nil {
		updated, err := s.completeExistingRun(state, index, repoID, taskID, payload)
		span.FinishError(context.Background(), err)
		return updated, err
	}

	if run.Status != RunStatusRunning {
		err := fmt.Errorf(
			"complete run attempt for task %s/%s: attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			run.Status,
			RunStatusRunning,
		)
		span.FinishError(context.Background(), err)
		return RunAttempt{}, err
	}

	now := s.nowUTC()
	completedAt := now
	state.Runs[index].Completion = &Completion{
		Summary:              payload.summary,
		Description:          payload.description,
		DetailedDescription:  payload.detailedDescription,
		TechnicalExplanation: payload.technicalExplanation,
		CompletedAt:          completedAt,
		Commit:               payload.commit,
		CommitError:          payload.commitError,
	}
	state.Events = append(state.Events, runEvent(run, EventCompletionRecorded, now, run.Status, ""))

	if err := s.save(state); err != nil {
		span.FinishError(context.Background(), err)
		return RunAttempt{}, err
	}
	span.Finish(context.Background(), logging.StatusSuccess)
	return state.Runs[index], nil
}

func completeRunPayloadFromOptions(repoID, taskID string, opts CompleteRunOptions) (completeRunPayload, error) {
	summary := strings.TrimSpace(opts.Summary)
	if summary == "" {
		return completeRunPayload{}, fmt.Errorf("complete run attempt for task %s/%s: summary is required", repoID, taskID)
	}
	description := strings.TrimSpace(opts.Description)
	if description == "" {
		return completeRunPayload{}, fmt.Errorf("complete run attempt for task %s/%s: description is required", repoID, taskID)
	}
	if strings.TrimSpace(opts.DetailedDescription) == "" {
		return completeRunPayload{}, fmt.Errorf("complete run attempt for task %s/%s: detailed_description is required", repoID, taskID)
	}
	if strings.TrimSpace(opts.TechnicalExplanation) == "" {
		return completeRunPayload{}, fmt.Errorf("complete run attempt for task %s/%s: technical_explanation is required", repoID, taskID)
	}
	return completeRunPayload{
		summary:              summary,
		description:          description,
		detailedDescription:  opts.DetailedDescription,
		technicalExplanation: opts.TechnicalExplanation,
		commit:               strings.TrimSpace(opts.Commit),
		commitError:          strings.TrimSpace(opts.CommitError),
	}, nil
}

func (s Store) completeExistingRun(
	state TaskState,
	index int,
	repoID string,
	taskID string,
	payload completeRunPayload,
) (RunAttempt, error) {
	run := state.Runs[index]
	completion, changed, err := mergeCompletionPayload(*run.Completion, payload)
	if err != nil {
		return RunAttempt{}, fmt.Errorf("complete run attempt for task %s/%s: %w", repoID, taskID, err)
	}
	if !changed {
		return run, nil
	}

	state.Runs[index].Completion = &completion
	if err := s.save(state); err != nil {
		return RunAttempt{}, err
	}
	return state.Runs[index], nil
}

func mergeCompletionPayload(completion Completion, payload completeRunPayload) (Completion, bool, error) {
	if completion.Summary != payload.summary ||
		completion.Description != payload.description ||
		completion.DetailedDescription != payload.detailedDescription ||
		completion.TechnicalExplanation != payload.technicalExplanation {
		return Completion{}, false, ErrCompletionConflict
	}

	changed, err := mergeCompletionOptionalFact(&completion.Commit, payload.commit)
	if err != nil {
		return Completion{}, false, err
	}
	commitErrorChanged, err := mergeCompletionOptionalFact(&completion.CommitError, payload.commitError)
	if err != nil {
		return Completion{}, false, err
	}
	return completion, changed || commitErrorChanged, nil
}

func mergeCompletionOptionalFact(existing *string, requested string) (bool, error) {
	if requested == "" {
		return false, nil
	}
	if strings.TrimSpace(*existing) != "" && *existing != requested {
		return false, ErrCompletionConflict
	}
	changed := *existing != requested
	*existing = requested
	return changed, nil
}

// RecordRepeatedCompletion records a local diagnostic for an ignored repeated agent completion.
func (s Store) RecordRepeatedCompletion(
	repoID,
	taskID string,
	attempt int,
	opts RepeatedCompletionOptions,
) (Event, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Event{}, err
	}

	index := runAttemptIndex(state, attempt)
	if index < 0 {
		return Event{}, fmt.Errorf("record repeated completion for task %s/%s: attempt %d was not found", repoID, taskID, attempt)
	}

	run := state.Runs[index]
	if run.Completion == nil {
		return Event{}, fmt.Errorf("record repeated completion for task %s/%s: attempt %d has no recorded completion", repoID, taskID, attempt)
	}

	now := s.nowUTC()
	event := runEvent(run, EventCompletionRepeated, now, run.Status, "")
	event.Message = "agent done repeated after completion already recorded; preserved first completion"
	event.RequestedSummary = strings.TrimSpace(opts.Summary)
	event.RequestedDescription = strings.TrimSpace(opts.Description)
	event.RequestedDetailedDescription = opts.DetailedDescription
	event.RequestedTechnicalExplanation = opts.TechnicalExplanation
	state.Events = append(state.Events, event)

	if err := s.save(state); err != nil {
		return Event{}, err
	}
	return event, nil
}

// StartReview appends a new running local review attempt for the built-in pipeline.
func (s Store) StartReview(repoID, taskID string) (ReviewAttempt, error) {
	return s.StartReviewWithOptions(repoID, taskID, StartReviewOptions{
		Pipeline: "default",
		Step:     "local-review",
	})
}

// StartReviewWithOptions appends a new running local review attempt.
func (s Store) StartReviewWithOptions(repoID, taskID string, opts StartReviewOptions) (ReviewAttempt, error) {
	pipeline := strings.TrimSpace(opts.Pipeline)
	if pipeline == "" {
		return ReviewAttempt{}, fmt.Errorf("start review attempt for task %s/%s: pipeline is required", repoID, taskID)
	}
	step := strings.TrimSpace(opts.Step)
	if step == "" {
		return ReviewAttempt{}, fmt.Errorf("start review attempt for task %s/%s: step is required", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}

	now := s.nowUTC()
	attempt := ReviewAttempt{
		Attempt:   nextReviewAttemptNumber(state),
		Status:    ReviewStatusRunning,
		Pipeline:  pipeline,
		Step:      step,
		StartedAt: now,
	}
	state.Reviews = append(state.Reviews, attempt)
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return attempt, nil
}

// PauseReviewForManual marks a running review attempt as waiting before a manual step.
func (s Store) PauseReviewForManual(repoID, taskID string, attempt int, step string) (ReviewAttempt, error) {
	step = strings.TrimSpace(step)
	if step == "" {
		return ReviewAttempt{}, fmt.Errorf("pause review attempt for task %s/%s: step is required", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("pause review attempt for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if state.Reviews[index].Status != ReviewStatusRunning {
		return ReviewAttempt{}, fmt.Errorf(
			"pause review attempt for task %s/%s: review attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
			ReviewStatusRunning,
		)
	}

	state.Reviews[index].Status = ReviewStatusWaitingForManual
	state.Reviews[index].Step = step
	state.Reviews[index].FinishedAt = nil
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// PauseReviewForAutomatedBlockerDecision preserves a check or agent-review blocker disposition for a later task run invocation.
func (s Store) PauseReviewForAutomatedBlockerDecision(repoID, taskID string, attempt int, step string) (ReviewAttempt, error) {
	step = strings.TrimSpace(step)
	if step == "" {
		return ReviewAttempt{}, fmt.Errorf("pause automated blocker decision for task %s/%s: step is required", repoID, taskID)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("pause automated blocker decision for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	review := &state.Reviews[index]
	if review.Status != ReviewStatusRunning {
		return ReviewAttempt{}, fmt.Errorf("pause automated blocker decision for task %s/%s: review attempt %d is %q, expected %q", repoID, taskID, attempt, review.Status, ReviewStatusRunning)
	}
	review.Status = ReviewStatusWaitingForAutomatedDecision
	review.Step = step
	review.FinishedAt = nil
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return *review, nil
}

// RestartReviewAutomatedStep discards the latest automated step execution and its findings, then leaves the attempt running for the same step to execute again.
func (s Store) RestartReviewAutomatedStep(repoID, taskID string, attempt int, stepName string) (ReviewAttempt, error) {
	stepName = strings.TrimSpace(stepName)
	if stepName == "" {
		return ReviewAttempt{}, fmt.Errorf("restart automated review step for task %s/%s: step is required", repoID, taskID)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("restart automated review step for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	review := &state.Reviews[index]
	if review.Status != ReviewStatusRunning {
		return ReviewAttempt{}, fmt.Errorf("restart automated review step for task %s/%s: review attempt %d is %q, expected %q", repoID, taskID, attempt, review.Status, ReviewStatusRunning)
	}
	stepIndex := -1
	for i := len(review.Steps) - 1; i >= 0; i-- {
		if review.Steps[i].Name == stepName {
			stepIndex = i
			break
		}
	}
	if stepIndex < 0 {
		return ReviewAttempt{}, fmt.Errorf("restart automated review step for task %s/%s: review attempt %d step %q was not found", repoID, taskID, attempt, stepName)
	}
	step := review.Steps[stepIndex]
	if step.Kind != ReviewStepKindCheck && step.Kind != ReviewStepKindAgentReview {
		return ReviewAttempt{}, fmt.Errorf("restart automated review step for task %s/%s: step %q is %q, expected check or agent_review", repoID, taskID, stepName, step.Kind)
	}
	review.Steps = append(review.Steps[:stepIndex], review.Steps[stepIndex+1:]...)
	findings := review.Findings[:0]
	for _, finding := range review.Findings {
		if finding.Step != stepName {
			findings = append(findings, finding)
		}
	}
	review.Findings = findings
	review.Step = stepName
	review.AutomatedBlockerDecisionKept = false
	review.AutomatedBlockerDecisionInterrupted = false
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return *review, nil
}

// ResumeReview marks a waiting review attempt as running again.
func (s Store) ResumeReview(repoID, taskID string, attempt int) (ReviewAttempt, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("resume review attempt for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if state.Reviews[index].Status != ReviewStatusWaitingForManual && state.Reviews[index].Status != ReviewStatusWaitingForAutomatedDecision {
		return ReviewAttempt{}, fmt.Errorf(
			"resume review attempt for task %s/%s: review attempt %d is %q, expected a waiting review",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
		)
	}

	state.Reviews[index].Status = ReviewStatusRunning
	state.Reviews[index].FinishedAt = nil
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// RecordReviewStep appends an executed step record to a running review attempt.
func (s Store) RecordReviewStep(
	repoID,
	taskID string,
	attempt int,
	opts RecordReviewStepOptions,
) (ReviewAttempt, error) {
	step, err := normalizeReviewStep(ReviewStep{
		Kind:      opts.Kind,
		Name:      opts.Name,
		Execution: cloneAgentExecutionPointer(opts.Execution),
		ExitCode:  cloneIntPointer(opts.ExitCode),
	})
	if err != nil {
		return ReviewAttempt{}, fmt.Errorf("record review step for task %s/%s: %w", repoID, taskID, err)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("record review step for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if !reviewCanRecordStep(state.Reviews[index], step) {
		return ReviewAttempt{}, fmt.Errorf(
			"record review step for task %s/%s: review attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
			ReviewStatusRunning,
		)
	}

	state.Reviews[index].Step = step.Name
	state.Reviews[index].Steps = append(state.Reviews[index].Steps, step)
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// RecordReviewStepChildPID records the direct child PID observed immediately after
// a primary reviewer launches. The launcher stops the child when this fails.
func (s Store) RecordReviewStepChildPID(repoID, taskID string, attempt int, stepName string, pid int) (ReviewAttempt, error) {
	stepName = strings.TrimSpace(stepName)
	if stepName == "" || pid <= 0 {
		return ReviewAttempt{}, fmt.Errorf("record review child PID for task %s/%s: step name and positive PID are required", repoID, taskID)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	reviewIndex, stepIndex, err := finishReviewStepExecutionIndexes(state, repoID, taskID, attempt, stepName)
	if err != nil {
		return ReviewAttempt{}, err
	}
	execution := state.Reviews[reviewIndex].Steps[stepIndex].Execution
	if execution == nil || execution.Purpose != AgentExecutionPurposeReview || execution.Status != RunStatusRunning {
		return ReviewAttempt{}, fmt.Errorf("record review child PID for task %s/%s: review attempt %d step %q has no running primary reviewer", repoID, taskID, attempt, stepName)
	}
	execution.ChildPID = pid
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[reviewIndex], nil
}

// InterruptPrimaryReviewExecution atomically records a recovered interrupted
// primary reviewer and its containing failed review attempt.
func (s Store) InterruptPrimaryReviewExecution(repoID, taskID string, attempt int, stepName string, opts InterruptPrimaryReviewExecutionOptions) (ReviewAttempt, error) {
	reason, trigger, stepName := strings.TrimSpace(opts.Reason), strings.TrimSpace(opts.Trigger), strings.TrimSpace(stepName)
	if reason == "" || trigger == "" || stepName == "" {
		return ReviewAttempt{}, fmt.Errorf("interrupt primary review execution for task %s/%s: reason, trigger, and step name are required", repoID, taskID)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	if len(state.Reviews) == 0 || state.Reviews[len(state.Reviews)-1].Attempt != attempt {
		return ReviewAttempt{}, fmt.Errorf("interrupt primary review execution for task %s/%s: review attempt %d is not latest", repoID, taskID, attempt)
	}
	reviewIndex := len(state.Reviews) - 1
	review := &state.Reviews[reviewIndex]
	if review.Status != ReviewStatusRunning {
		return ReviewAttempt{}, fmt.Errorf("interrupt primary review execution for task %s/%s: review attempt %d is %q, expected %q", repoID, taskID, attempt, review.Status, ReviewStatusRunning)
	}
	stepIndex := latestReviewStepExecutionIndex(*review, stepName)
	if stepIndex < 0 {
		return ReviewAttempt{}, fmt.Errorf("interrupt primary review execution for task %s/%s: review attempt %d step %q was not found", repoID, taskID, attempt, stepName)
	}
	execution := review.Steps[stepIndex].Execution
	if execution == nil || execution.Purpose != AgentExecutionPurposeReview || execution.Status != RunStatusRunning {
		return ReviewAttempt{}, fmt.Errorf("interrupt primary review execution for task %s/%s: review attempt %d step %q has no running primary reviewer", repoID, taskID, attempt, stepName)
	}
	now := s.nowUTC()
	execution.Status = RunStatusInterrupted
	execution.FinishedAt = &now
	execution.DurationMillis = durationMillis(execution.StartedAt, now)
	execution.InterruptionReason = reason
	execution.InterruptionTrigger = trigger
	review.Status = ReviewStatusFailed
	review.FinishedAt = &now
	updated := *review
	state.Events = append(state.Events, Event{Type: EventReviewInterrupted, At: now, Attempt: attempt, Status: RunStatusInterrupted, Agent: execution.Agent, InterruptionReason: reason, InterruptionTrigger: trigger})
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return updated, nil
}

// FinishReviewStepExecution records terminal state and best-effort usage telemetry for a review agent step.
func (s Store) FinishReviewStepExecution(
	repoID,
	taskID string,
	attempt int,
	stepName string,
	opts FinishReviewStepExecutionOptions,
) (ReviewAttempt, error) {
	stepName = strings.TrimSpace(stepName)
	if err := validateFinishReviewStepExecutionInput(repoID, taskID, stepName, opts.Status); err != nil {
		return ReviewAttempt{}, err
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	reviewIndex, stepIndex, err := finishReviewStepExecutionIndexes(state, repoID, taskID, attempt, stepName)
	if err != nil {
		return ReviewAttempt{}, err
	}

	finishedAt := opts.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = s.nowUTC()
	}
	finishedAt = finishedAt.UTC()
	usageOpts := RecordRunUsageOptions{
		Session:      opts.Session,
		Usage:        opts.Usage,
		UsageCost:    opts.UsageCost,
		UsageCapture: opts.UsageCapture,
		Model:        opts.Model,
	}
	execution := *state.Reviews[reviewIndex].Steps[stepIndex].Execution
	execution = applyRunUsageOptions(execution, usageOpts, finishedAt)
	execution.Status = opts.Status
	execution.FinishedAt = &finishedAt
	execution.DurationMillis = durationMillis(execution.StartedAt, finishedAt)
	state.Reviews[reviewIndex].Steps[stepIndex].Execution = normalizeOptionalAgentExecution(&execution)

	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[reviewIndex], nil
}

// RecordReviewStepUsage records best-effort usage telemetry for an existing review agent step.
func (s Store) RecordReviewStepUsage(
	repoID,
	taskID string,
	attempt int,
	stepName string,
	opts RecordRunUsageOptions,
) (ReviewAttempt, error) {
	stepName = strings.TrimSpace(stepName)
	if stepName == "" {
		return ReviewAttempt{}, fmt.Errorf("record review step usage for task %s/%s: step name is required", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	reviewIndex := reviewAttemptIndex(state, attempt)
	if reviewIndex < 0 {
		return ReviewAttempt{}, fmt.Errorf("record review step usage for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	stepIndex := latestReviewStepExecutionIndex(state.Reviews[reviewIndex], stepName)
	if stepIndex < 0 {
		return ReviewAttempt{}, fmt.Errorf(
			"record review step usage for task %s/%s: review attempt %d step %q was not found",
			repoID,
			taskID,
			attempt,
			stepName,
		)
	}
	if state.Reviews[reviewIndex].Steps[stepIndex].Execution == nil {
		return ReviewAttempt{}, fmt.Errorf(
			"record review step usage for task %s/%s: review attempt %d step %q has no agent execution",
			repoID,
			taskID,
			attempt,
			stepName,
		)
	}

	execution := *state.Reviews[reviewIndex].Steps[stepIndex].Execution
	execution = applyRunUsageOptions(execution, opts, s.nowUTC())
	state.Reviews[reviewIndex].Steps[stepIndex].Execution = normalizeOptionalAgentExecution(&execution)
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[reviewIndex], nil
}

func validateFinishReviewStepExecutionInput(repoID, taskID, stepName string, status RunStatus) error {
	if stepName == "" {
		return fmt.Errorf("finish review step execution for task %s/%s: step name is required", repoID, taskID)
	}
	if status != RunStatusSucceeded && status != RunStatusFailed {
		return fmt.Errorf(
			"finish review step execution for task %s/%s: status must be %q or %q, got %q",
			repoID,
			taskID,
			RunStatusSucceeded,
			RunStatusFailed,
			status,
		)
	}
	return nil
}

func finishReviewStepExecutionIndexes(
	state TaskState,
	repoID,
	taskID string,
	attempt int,
	stepName string,
) (int, int, error) {
	reviewIndex := reviewAttemptIndex(state, attempt)
	if reviewIndex < 0 {
		return 0, 0, fmt.Errorf("finish review step execution for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if state.Reviews[reviewIndex].Status != ReviewStatusRunning {
		return 0, 0, fmt.Errorf(
			"finish review step execution for task %s/%s: review attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			state.Reviews[reviewIndex].Status,
			ReviewStatusRunning,
		)
	}

	stepIndex := latestReviewStepExecutionIndex(state.Reviews[reviewIndex], stepName)
	if stepIndex < 0 {
		return 0, 0, fmt.Errorf(
			"finish review step execution for task %s/%s: review attempt %d step %q was not found",
			repoID,
			taskID,
			attempt,
			stepName,
		)
	}
	return reviewIndex, stepIndex, nil
}

// RecordReviewFinding appends a finding to a running review attempt.
func (s Store) RecordReviewFinding(
	repoID,
	taskID string,
	attempt int,
	finding ReviewFinding,
) (ReviewAttempt, error) {
	normalizedFinding, err := normalizeReviewFinding(finding)
	if err != nil {
		return ReviewAttempt{}, fmt.Errorf("record review finding for task %s/%s: %w", repoID, taskID, err)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("record review finding for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if !reviewCanRecordFinding(state.Reviews[index], normalizedFinding) {
		return ReviewAttempt{}, fmt.Errorf(
			"record review finding for task %s/%s: review attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
			ReviewStatusRunning,
		)
	}

	state.Reviews[index].Findings = append(state.Reviews[index].Findings, normalizedFinding)
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// StartReviewStepComparison records the alternate execution before it launches.
func (s Store) StartReviewStepComparison(repoID, taskID string, attempt int, stepName string, execution AgentExecution) (ReviewAttempt, error) {
	stepName = strings.TrimSpace(stepName)
	execution = normalizeAgentExecution(execution)
	if stepName == "" {
		return ReviewAttempt{}, fmt.Errorf("start review comparison for task %s/%s: step name is required", repoID, taskID)
	}
	if execution.Purpose != AgentExecutionPurposeReview || execution.Status != RunStatusRunning {
		return ReviewAttempt{}, fmt.Errorf("start review comparison for task %s/%s: alternate execution must be a running review execution", repoID, taskID)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	reviewIndex, stepIndex, err := reviewStepIndex(state, repoID, taskID, attempt, stepName)
	if err != nil {
		return ReviewAttempt{}, err
	}
	step := &state.Reviews[reviewIndex].Steps[stepIndex]
	if step.Execution == nil || step.Comparison != nil {
		return ReviewAttempt{}, fmt.Errorf("start review comparison for task %s/%s: step %q is not eligible", repoID, taskID, stepName)
	}
	step.Comparison = &ReviewComparison{AlternateExecution: &execution}
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[reviewIndex], nil
}

// FinishReviewStepComparison records alternate terminal execution facts and telemetry.
func (s Store) FinishReviewStepComparison(repoID, taskID string, attempt int, stepName string, opts FinishReviewStepExecutionOptions) (ReviewAttempt, error) {
	if err := validateFinishReviewStepExecutionInput(repoID, taskID, strings.TrimSpace(stepName), opts.Status); err != nil {
		return ReviewAttempt{}, err
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	reviewIndex, stepIndex, err := reviewStepIndex(state, repoID, taskID, attempt, strings.TrimSpace(stepName))
	if err != nil {
		return ReviewAttempt{}, err
	}
	comparison := state.Reviews[reviewIndex].Steps[stepIndex].Comparison
	if comparison == nil || comparison.AlternateExecution == nil {
		return ReviewAttempt{}, fmt.Errorf("finish review comparison for task %s/%s: step %q has no alternate execution", repoID, taskID, stepName)
	}
	finishedAt := opts.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = s.nowUTC()
	}
	usageOpts := RecordRunUsageOptions{Session: opts.Session, Usage: opts.Usage, UsageCost: opts.UsageCost, UsageCapture: opts.UsageCapture, Model: opts.Model}
	finishedAt = finishedAt.UTC()
	execution := applyRunUsageOptions(*comparison.AlternateExecution, usageOpts, finishedAt)
	execution.Status = opts.Status
	execution.FinishedAt = &finishedAt
	execution.DurationMillis = durationMillis(execution.StartedAt, finishedAt)
	comparison.AlternateExecution = normalizeOptionalAgentExecution(&execution)
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[reviewIndex], nil
}

// RecordReviewComparisonFailure preserves a non-fatal alternate execution failure.
func (s Store) RecordReviewComparisonFailure(repoID, taskID string, attempt int, stepName string, failure string) (ReviewAttempt, error) {
	failure = strings.TrimSpace(failure)
	if failure == "" {
		return ReviewAttempt{}, fmt.Errorf("record review comparison failure for task %s/%s: failure is required", repoID, taskID)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	reviewIndex, stepIndex, err := reviewStepIndex(state, repoID, taskID, attempt, strings.TrimSpace(stepName))
	if err != nil {
		return ReviewAttempt{}, err
	}
	comparison := state.Reviews[reviewIndex].Steps[stepIndex].Comparison
	if comparison == nil || comparison.AlternateExecution == nil || comparison.AlternateExecution.Status != RunStatusFailed {
		return ReviewAttempt{}, fmt.Errorf("record review comparison failure for task %s/%s: step %q has no failed alternate execution", repoID, taskID, stepName)
	}
	comparison.Failure = failure
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[reviewIndex], nil
}

// RecordAlternateReviewFinding appends a raw finding to the active alternate execution.
func (s Store) RecordAlternateReviewFinding(repoID, taskID string, attempt int, stepName string, finding ReviewFinding) (ReviewAttempt, error) {
	finding.Reviewer = "alternate"
	finding, err := normalizeReviewFinding(finding)
	if err != nil {
		return ReviewAttempt{}, fmt.Errorf("record alternate review finding for task %s/%s: %w", repoID, taskID, err)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	reviewIndex, stepIndex, err := reviewStepIndex(state, repoID, taskID, attempt, strings.TrimSpace(stepName))
	if err != nil {
		return ReviewAttempt{}, err
	}
	comparison := state.Reviews[reviewIndex].Steps[stepIndex].Comparison
	if state.Reviews[reviewIndex].Status != ReviewStatusRunning || comparison == nil || comparison.AlternateExecution == nil || comparison.AlternateExecution.Status != RunStatusRunning {
		return ReviewAttempt{}, fmt.Errorf("record alternate review finding for task %s/%s: step %q has no running alternate execution", repoID, taskID, stepName)
	}
	comparison.AlternateFindings = append(comparison.AlternateFindings, AlternateReviewFinding{Finding: finding})
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[reviewIndex], nil
}

// ClassifyAlternateReviewFindings atomically admits, deduplicates, or excludes all alternate findings.
func (s Store) ClassifyAlternateReviewFindings(repoID, taskID string, attempt int, stepName string, decisions []AlternateReviewFindingDecision) (ReviewAttempt, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	reviewIndex, stepIndex, err := reviewStepIndex(state, repoID, taskID, attempt, strings.TrimSpace(stepName))
	if err != nil {
		return ReviewAttempt{}, err
	}
	review := &state.Reviews[reviewIndex]
	comparison := review.Steps[stepIndex].Comparison
	if review.Status != ReviewStatusRunning || comparison == nil || comparison.AlternateExecution == nil || comparison.AlternateExecution.Status != RunStatusSucceeded || comparison.InputInterrupted {
		return ReviewAttempt{}, fmt.Errorf("classify alternate review findings for task %s/%s: step %q is not ready", repoID, taskID, stepName)
	}
	for _, finding := range comparison.AlternateFindings {
		if finding.Classification != "" {
			return ReviewAttempt{}, fmt.Errorf("classify alternate review findings for task %s/%s: step %q is already classified", repoID, taskID, stepName)
		}
	}
	if err := validateAlternateDecisions(*review, stepName, comparison.AlternateFindings, decisions); err != nil {
		return ReviewAttempt{}, err
	}
	for _, decision := range decisions {
		alternate := &comparison.AlternateFindings[decision.FindingIndex]
		alternate.Classification = decision.Classification
		alternate.DuplicateOf = decision.DuplicateOf
		if decision.Classification == AlternateFindingAdmitted {
			admitted := alternate.Finding
			admitted.Reviewer = "alternate"
			review.Findings = append(review.Findings, admitted)
		}
	}
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return *review, nil
}

// MarkReviewComparisonInputInterrupted records an incomplete comparison without implicit classifications.
func (s Store) MarkReviewComparisonInputInterrupted(repoID, taskID string, attempt int, stepName string) (ReviewAttempt, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	reviewIndex, stepIndex, err := reviewStepIndex(state, repoID, taskID, attempt, strings.TrimSpace(stepName))
	if err != nil {
		return ReviewAttempt{}, err
	}
	comparison := state.Reviews[reviewIndex].Steps[stepIndex].Comparison
	if comparison == nil {
		return ReviewAttempt{}, fmt.Errorf("mark review comparison interruption for task %s/%s: step %q has no comparison", repoID, taskID, stepName)
	}
	comparison.InputInterrupted = true
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[reviewIndex], nil
}

func reviewStepIndex(state TaskState, repoID, taskID string, attempt int, stepName string) (int, int, error) {
	reviewIndex := reviewAttemptIndex(state, attempt)
	if reviewIndex < 0 {
		return 0, 0, fmt.Errorf("review step for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	stepIndex := latestReviewStepExecutionIndex(state.Reviews[reviewIndex], stepName)
	if stepIndex < 0 {
		return 0, 0, fmt.Errorf("review step for task %s/%s: review attempt %d step %q was not found", repoID, taskID, attempt, stepName)
	}
	return reviewIndex, stepIndex, nil
}

func validateAlternateDecisions(review ReviewAttempt, stepName string, findings []AlternateReviewFinding, decisions []AlternateReviewFindingDecision) error {
	if len(decisions) != len(findings) {
		return fmt.Errorf("expected classifications for %d alternate findings, got %d", len(findings), len(decisions))
	}
	seen := make(map[int]bool, len(decisions))
	for _, decision := range decisions {
		if decision.FindingIndex < 0 || decision.FindingIndex >= len(findings) || seen[decision.FindingIndex] {
			return fmt.Errorf("alternate finding classification %d is invalid", decision.FindingIndex+1)
		}
		seen[decision.FindingIndex] = true
		switch decision.Classification {
		case AlternateFindingAdmitted, AlternateFindingExcluded:
			if decision.DuplicateOf != 0 {
				return fmt.Errorf("alternate finding %d cannot specify duplicate_of", decision.FindingIndex+1)
			}
		case AlternateFindingDuplicate:
			if decision.DuplicateOf < 0 || decision.DuplicateOf >= len(review.Findings) || review.Findings[decision.DuplicateOf].Step != stepName || review.Findings[decision.DuplicateOf].Reviewer == "alternate" {
				return fmt.Errorf("alternate finding %d has invalid primary duplicate target", decision.FindingIndex+1)
			}
		default:
			return fmt.Errorf("alternate finding %d has unsupported classification %q", decision.FindingIndex+1, decision.Classification)
		}
	}
	return nil
}

// PromoteReviewAdvisoryFinding changes an unresolved advisory finding into a blocking finding.
func (s Store) PromoteReviewAdvisoryFinding(
	repoID,
	taskID string,
	attempt int,
	findingIndex int,
) (ReviewAttempt, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	reviewIndex := reviewAttemptIndex(state, attempt)
	if reviewIndex < 0 {
		return ReviewAttempt{}, fmt.Errorf("promote review advisory for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if !reviewCanChangeManualFinding(state.Reviews[reviewIndex]) {
		return ReviewAttempt{}, fmt.Errorf(
			"promote review advisory for task %s/%s: review attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			state.Reviews[reviewIndex].Status,
			ReviewStatusRunning,
		)
	}
	if findingIndex < 0 || findingIndex >= len(state.Reviews[reviewIndex].Findings) {
		return ReviewAttempt{}, fmt.Errorf("promote review advisory for task %s/%s: finding index %d is out of range", repoID, taskID, findingIndex)
	}

	finding := state.Reviews[reviewIndex].Findings[findingIndex]
	if finding.Type != FindingTypeAdvisory {
		return ReviewAttempt{}, fmt.Errorf(
			"promote review advisory for task %s/%s: finding index %d is %q, expected %q",
			repoID,
			taskID,
			findingIndex,
			finding.Type,
			FindingTypeAdvisory,
		)
	}
	if ReviewFindingResolved(finding) {
		return ReviewAttempt{}, fmt.Errorf("promote review advisory for task %s/%s: finding index %d is already resolved", repoID, taskID, findingIndex)
	}

	state.Reviews[reviewIndex].Findings[findingIndex].Type = FindingTypeBlocking
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[reviewIndex], nil
}

// DowngradeReviewBlockingFinding changes an unresolved blocking finding into an advisory finding.
func (s Store) DowngradeReviewBlockingFinding(
	repoID,
	taskID string,
	attempt int,
	findingIndex int,
	reason string,
) (ReviewAttempt, error) {
	return s.reclassifyReviewBlockingFinding(
		repoID,
		taskID,
		attempt,
		findingIndex,
		reason,
		reviewBlockingReclassificationDowngrade,
	)
}

func reviewCanRecordStep(review ReviewAttempt, step ReviewStep) bool {
	if review.Status == ReviewStatusRunning {
		return true
	}
	return review.Status == ReviewStatusWaitingForManual &&
		step.Kind == "manual" &&
		strings.TrimSpace(step.Name) == strings.TrimSpace(review.Step)
}

func reviewCanRecordFinding(review ReviewAttempt, finding ReviewFinding) bool {
	if review.Status == ReviewStatusRunning {
		return true
	}
	return review.Status == ReviewStatusWaitingForManual &&
		strings.TrimSpace(finding.Step) == strings.TrimSpace(review.Step)
}

func reviewCanChangeManualFinding(review ReviewAttempt) bool {
	return review.Status == ReviewStatusRunning || review.Status == ReviewStatusWaitingForManual
}

// AddressReviewBlockingFindingManually records that an operator fixed an
// unresolved blocker outside an Orpheus follow-up run.
func (s Store) AddressReviewBlockingFindingManually(
	repoID,
	taskID string,
	attempt int,
	findingIndex int,
	reason string,
) (ReviewAttempt, error) {
	return s.reclassifyReviewBlockingFinding(
		repoID,
		taskID,
		attempt,
		findingIndex,
		reason,
		reviewBlockingReclassificationAddressedManually,
	)
}

// WaiveReviewBlockingFinding records an operator waiver for an unresolved blocking finding.
func (s Store) WaiveReviewBlockingFinding(
	repoID,
	taskID string,
	attempt int,
	findingIndex int,
	reason string,
) (ReviewAttempt, error) {
	return s.reclassifyReviewBlockingFinding(
		repoID,
		taskID,
		attempt,
		findingIndex,
		reason,
		reviewBlockingReclassificationWaive,
	)
}

type reviewBlockingReclassification string

const (
	reviewBlockingReclassificationDowngrade         reviewBlockingReclassification = "downgrade"
	reviewBlockingReclassificationAddressedManually reviewBlockingReclassification = "address manually"
	reviewBlockingReclassificationWaive             reviewBlockingReclassification = "waive"
)

func (s Store) reclassifyReviewBlockingFinding(
	repoID,
	taskID string,
	attempt int,
	findingIndex int,
	reason string,
	reclassification reviewBlockingReclassification,
) (ReviewAttempt, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ReviewAttempt{}, fmt.Errorf(
			"%s review blocking finding for task %s/%s: reason is required",
			reclassification,
			repoID,
			taskID,
		)
	}

	state, reviewIndex, err := s.reviewBlockingReclassificationTarget(
		repoID,
		taskID,
		attempt,
		findingIndex,
		reclassification,
	)
	if err != nil {
		return ReviewAttempt{}, err
	}
	applyReviewBlockingReclassification(&state, reviewIndex, findingIndex, reason, reclassification)

	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[reviewIndex], nil
}

func (s Store) reviewBlockingReclassificationTarget(
	repoID,
	taskID string,
	attempt int,
	findingIndex int,
	reclassification reviewBlockingReclassification,
) (TaskState, int, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return TaskState{}, -1, err
	}
	reviewIndex, err := requireResolvableReviewFinding(
		state,
		repoID,
		taskID,
		attempt,
		findingIndex,
		string(reclassification)+" review blocking finding",
	)
	if err != nil {
		return TaskState{}, -1, err
	}
	finding := state.Reviews[reviewIndex].Findings[findingIndex]
	if finding.Type != FindingTypeBlocking {
		return TaskState{}, -1, fmt.Errorf(
			"%s review blocking finding for task %s/%s: finding index %d is %q, expected %q",
			reclassification,
			repoID,
			taskID,
			findingIndex,
			finding.Type,
			FindingTypeBlocking,
		)
	}
	if ReviewFindingResolved(finding) {
		return TaskState{}, -1, fmt.Errorf(
			"%s review blocking finding for task %s/%s: finding index %d is already resolved",
			reclassification,
			repoID,
			taskID,
			findingIndex,
		)
	}
	return state, reviewIndex, nil
}

func applyReviewBlockingReclassification(
	state *TaskState,
	reviewIndex int,
	findingIndex int,
	reason string,
	reclassification reviewBlockingReclassification,
) {
	switch reclassification {
	case reviewBlockingReclassificationDowngrade:
		state.Reviews[reviewIndex].Findings[findingIndex].Type = FindingTypeAdvisory
		state.Reviews[reviewIndex].Findings[findingIndex].DowngradeReason = reason
	case reviewBlockingReclassificationAddressedManually:
		state.Reviews[reviewIndex].Findings[findingIndex].AddressedManually = reason
	case reviewBlockingReclassificationWaive:
		state.Reviews[reviewIndex].Findings[findingIndex].Waiver = reason
	}
}

// reviewCanResolveBlockingFindings reports whether an attempt can retain an
// audit disposition for an existing blocker. A manual review remains owned by
// its pipeline until it resumes, so it cannot be changed by a fresh-review
// guard.
func reviewCanResolveBlockingFindings(review ReviewAttempt) bool {
	switch review.Status {
	case ReviewStatusRunning,
		ReviewStatusBlocked,
		ReviewStatusFailed,
		ReviewStatusPassed,
		ReviewStatusAborted:
		return true
	default:
		return false
	}
}

func requireResolvableReviewFinding(
	state TaskState,
	repoID,
	taskID string,
	attempt int,
	findingIndex int,
	operation string,
) (int, error) {
	reviewIndex := reviewAttemptIndex(state, attempt)
	if reviewIndex < 0 {
		return -1, fmt.Errorf("%s for task %s/%s: review attempt %d was not found", operation, repoID, taskID, attempt)
	}
	if !reviewCanResolveBlockingFindings(state.Reviews[reviewIndex]) {
		return -1, fmt.Errorf(
			"%s for task %s/%s: review attempt %d is %q, expected running, blocked, failed, passed, or aborted",
			operation,
			repoID,
			taskID,
			attempt,
			state.Reviews[reviewIndex].Status,
		)
	}
	if findingIndex < 0 || findingIndex >= len(state.Reviews[reviewIndex].Findings) {
		return -1, fmt.Errorf("%s for task %s/%s: finding index %d is out of range", operation, repoID, taskID, findingIndex)
	}
	return reviewIndex, nil
}

// ReviewFindingResolution describes the current lifecycle outcome for a review finding.
type ReviewFindingResolution string

const (
	ReviewFindingResolutionOpen              ReviewFindingResolution = "open"
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
		if run.Attempt == finding.TargetedByRunAttempt {
			return run.Status == RunStatusFailed ||
				((run.Status == RunStatusSucceeded || run.Status == RunStatusInterrupted) && run.Completion == nil)
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
			InterruptedPrimaryReviewFinding(review, finding) ||
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
		if IsOpenBlockingReviewFinding(finding) && !InterruptedPrimaryReviewFinding(review, finding) {
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
		if IsOpenBlockingReviewFindingInState(state, finding) && !InterruptedPrimaryReviewFinding(review, finding) {
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

// InterruptedPrimaryReviewFinding reports whether a finding belongs to a
// primary reviewer execution recovered as interrupted. Such findings are
// retained for audit but never drive blocker routing.
func InterruptedPrimaryReviewFinding(review ReviewAttempt, finding ReviewFinding) bool {
	return InterruptedPrimaryReviewStepNames(review)[strings.TrimSpace(finding.Step)]
}

// PrimaryReviewExecutionInterrupted reports whether primary-reviewer recovery
// interrupted any execution in this attempt.
func PrimaryReviewExecutionInterrupted(review ReviewAttempt) bool {
	return len(InterruptedPrimaryReviewStepNames(review)) > 0
}

func InterruptedPrimaryReviewStepNames(review ReviewAttempt) map[string]bool {
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

// ReviewComparisonInputInterrupted reports whether an alternate comparison lost operator input.
func ReviewComparisonInputInterrupted(review ReviewAttempt) bool {
	for _, step := range review.Steps {
		if step.Comparison != nil && step.Comparison.InputInterrupted {
			return true
		}
	}
	return false
}

func currentReviewStepKind(review ReviewAttempt) string {
	for index := len(review.Steps) - 1; index >= 0; index-- {
		if review.Steps[index].Name == review.Step {
			return review.Steps[index].Kind
		}
	}
	return ""
}

func latestReviewStepExecutionIndex(review ReviewAttempt, stepName string) int {
	for index := len(review.Steps) - 1; index >= 0; index-- {
		step := review.Steps[index]
		if step.Name == stepName && step.Execution != nil {
			return index
		}
	}
	return -1
}

func applyRunUsageOptions(execution AgentExecution, opts RecordRunUsageOptions, capturedAt time.Time) AgentExecution {
	if strings.TrimSpace(opts.Model) != "" {
		execution.Model = strings.TrimSpace(opts.Model)
	}
	if opts.Session != nil {
		session := normalizeAgentSession(*opts.Session)
		if !agentSessionIsZero(session) {
			execution.Session = &session
		}
	}
	if opts.Usage != nil {
		usage := normalizeAgentUsage(*opts.Usage)
		if !agentUsageIsZero(usage) {
			execution.Usage = &usage
		}
	}
	if opts.UsageCost != nil && !hasImmutableCodexUsageCost(execution) {
		cost := normalizeAgentUsageCost(*opts.UsageCost)
		if !agentUsageCostIsZero(cost) {
			execution.UsageCost = &cost
		}
	}
	capture := normalizeAgentUsageCapture(opts.UsageCapture, capturedAt)
	if !capture.IsZero() {
		execution.UsageCapture = capture
	}
	return execution
}

// RecordReviewFindingCreatedTask records the backend task created from a separate-task finding.
func (s Store) RecordReviewFindingCreatedTask(
	repoID,
	taskID string,
	attempt int,
	findingIndex int,
	createdTaskID string,
) (ReviewAttempt, error) {
	createdTaskID = strings.TrimSpace(createdTaskID)
	if createdTaskID == "" {
		return ReviewAttempt{}, fmt.Errorf("record created review task for task %s/%s: created task id is required", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("record created review task for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if state.Reviews[index].Status != ReviewStatusRunning {
		return ReviewAttempt{}, fmt.Errorf(
			"record created review task for task %s/%s: review attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
			ReviewStatusRunning,
		)
	}
	if findingIndex < 0 || findingIndex >= len(state.Reviews[index].Findings) {
		return ReviewAttempt{}, fmt.Errorf("record created review task for task %s/%s: finding index %d is out of range", repoID, taskID, findingIndex)
	}
	finding := state.Reviews[index].Findings[findingIndex]
	if finding.Type != FindingTypeSeparateTask {
		return ReviewAttempt{}, fmt.Errorf("record created review task for task %s/%s: finding index %d is %q, expected %q", repoID, taskID, findingIndex, finding.Type, FindingTypeSeparateTask)
	}
	if finding.CreatedTaskID != "" && finding.CreatedTaskID != createdTaskID {
		return ReviewAttempt{}, fmt.Errorf("record created review task for task %s/%s: finding index %d already created task %q", repoID, taskID, findingIndex, finding.CreatedTaskID)
	}

	if state.Reviews[index].Findings[findingIndex].CreatedTaskID == "" {
		now := s.nowUTC()
		state.Reviews[index].Findings[findingIndex].CreatedTaskAt = &now
	}
	state.Reviews[index].Findings[findingIndex].CreatedTaskID = createdTaskID
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// FinishReview records the terminal status for a running review attempt.
func (s Store) FinishReview(repoID, taskID string, attempt int, status ReviewStatus) (ReviewAttempt, error) {
	if status == ReviewStatusRunning || status == ReviewStatusWaitingForManual || status == ReviewStatusWaitingForAutomatedDecision || !validReviewStatus(status) {
		return ReviewAttempt{}, fmt.Errorf("finish review attempt for task %s/%s: unsupported terminal status %q", repoID, taskID, status)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("finish review attempt for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if state.Reviews[index].Status != ReviewStatusRunning {
		return ReviewAttempt{}, fmt.Errorf(
			"finish review attempt for task %s/%s: review attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
			ReviewStatusRunning,
		)
	}

	now := s.nowUTC()
	finished := now
	state.Reviews[index].Status = status
	state.Reviews[index].FinishedAt = &finished
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// PrepareReviewForTargetedFollowUp makes a review attempt authoritative for a
// targeted repair. A running review is terminally blocked at the time of this
// decision; terminal reviews retain their original pipeline completion time.
func (s Store) PrepareReviewForTargetedFollowUp(repoID, taskID string, attempt int) (ReviewAttempt, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("prepare review for targeted follow-up for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if !reviewCanResolveBlockingFindings(state.Reviews[index]) {
		return ReviewAttempt{}, fmt.Errorf(
			"prepare review for targeted follow-up for task %s/%s: review attempt %d is %q, expected running, blocked, failed, passed, or aborted",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
		)
	}

	review := &state.Reviews[index]
	if review.Status == ReviewStatusRunning {
		now := s.nowUTC()
		review.FinishedAt = &now
	}
	review.Status = ReviewStatusBlocked
	review.AutomatedBlockerDecisionInterrupted = false
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// MarkReviewAutomatedBlockerDecisionKept records that the operator explicitly
// kept at least one automated blocker during classification.
func (s Store) MarkReviewAutomatedBlockerDecisionKept(repoID, taskID string, attempt int) (ReviewAttempt, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("mark automated blocker decision kept for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if !reviewCanResolveBlockingFindings(state.Reviews[index]) {
		return ReviewAttempt{}, fmt.Errorf(
			"mark automated blocker decision kept for task %s/%s: review attempt %d is %q, expected running, blocked, failed, passed, or aborted",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
		)
	}

	state.Reviews[index].AutomatedBlockerDecisionKept = true
	state.Reviews[index].AutomatedBlockerDecisionInterrupted = false
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// MarkReviewAutomatedBlockerDecisionInterrupted records that automated blocker
// classification could not continue because operator input was unavailable.
func (s Store) MarkReviewAutomatedBlockerDecisionInterrupted(repoID, taskID string, attempt int) (ReviewAttempt, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("mark automated blocker decision interrupted for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if state.Reviews[index].Status != ReviewStatusRunning {
		return ReviewAttempt{}, fmt.Errorf(
			"mark automated blocker decision interrupted for task %s/%s: review attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
			ReviewStatusRunning,
		)
	}

	state.Reviews[index].AutomatedBlockerDecisionInterrupted = true
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// MarkReviewAutonomousBudgetExhausted records that automatic review/fix attempts stopped at the budget.
func (s Store) MarkReviewAutonomousBudgetExhausted(repoID, taskID string, attempt int) (ReviewAttempt, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return ReviewAttempt{}, err
	}
	index := reviewAttemptIndex(state, attempt)
	if index < 0 {
		return ReviewAttempt{}, fmt.Errorf("mark autonomous review budget exhausted for task %s/%s: review attempt %d was not found", repoID, taskID, attempt)
	}
	if state.Reviews[index].Status != ReviewStatusBlocked {
		return ReviewAttempt{}, fmt.Errorf(
			"mark autonomous review budget exhausted for task %s/%s: review attempt %d is %q, expected %q",
			repoID,
			taskID,
			attempt,
			state.Reviews[index].Status,
			ReviewStatusBlocked,
		)
	}

	state.Reviews[index].AutonomousBudgetExhausted = true
	if err := s.save(state); err != nil {
		return ReviewAttempt{}, err
	}
	return state.Reviews[index], nil
}

// SetFinalizationIntegrationFlow persists the selected integration flow before
// publication mutates Git or backend state. It permits a manual review choice
// to change until publication begins, but never after partial publication.
func (s Store) SetFinalizationIntegrationFlow(repoID, taskID string, flow publication.IntegrationFlow) (Finalization, error) {
	if err := publication.ValidateIntegrationFlow(flow); err != nil || strings.TrimSpace(string(flow)) == "" {
		if err != nil {
			return Finalization{}, fmt.Errorf("set finalization integration flow for task %s/%s: %w", repoID, taskID, err)
		}
		return Finalization{}, fmt.Errorf("set finalization integration flow for task %s/%s: integration flow is required", repoID, taskID)
	}
	flow = publication.IntegrationFlow(strings.TrimSpace(string(flow)))
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Finalization{}, err
	}
	finalization := ensureFinalization(state.Finalization)
	if finalization.IntegrationFlow == flow {
		return finalization, nil
	}
	if finalizationHasPublicationMutation(finalization) {
		// Historical finalizations have no flow field. They are always the
		// compatible pull-request flow and can be annotated on retry.
		if finalization.IntegrationFlow == "" && flow == publication.IntegrationFlowPullRequest {
			finalization.IntegrationFlow = flow
			state.Finalization = &finalization
			if err := s.save(state); err != nil {
				return Finalization{}, err
			}
			return finalization, nil
		}
		return Finalization{}, fmt.Errorf("set finalization integration flow for task %s/%s: %w; flow is locked as %q", repoID, taskID, ErrFinalizationConflict, finalization.IntegrationFlow)
	}
	finalization.IntegrationFlow = flow
	state.Finalization = &finalization
	if err := s.save(state); err != nil {
		return Finalization{}, err
	}
	return finalization, nil
}

// SetFinalizationDestination persists the integration destination selected in
// manual review. The choice remains adjustable until publication begins, then
// becomes immutable so retries cannot target a different branch.
func (s Store) SetFinalizationDestination(repoID, taskID, destination string) (Finalization, error) {
	destination = strings.TrimSpace(destination)
	if destination == "" {
		return Finalization{}, fmt.Errorf("set finalization destination for task %s/%s: destination branch is required", repoID, taskID)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Finalization{}, err
	}
	finalization := ensureFinalization(state.Finalization)
	if finalization.DestinationBranch == destination {
		return finalization, nil
	}
	if finalizationHasPublicationMutation(finalization) {
		return Finalization{}, fmt.Errorf("set finalization destination for task %s/%s: %w; destination is locked as %q; retry publication with that branch", repoID, taskID, ErrFinalizationConflict, finalization.DestinationBranch)
	}
	finalization.DestinationBranch = destination
	state.Finalization = &finalization
	if err := s.save(state); err != nil {
		return Finalization{}, err
	}
	return finalization, nil
}

// RecordFinalizationMerge records the successful merge of a task branch into
// the registered default branch before that default branch is pushed.
func (s Store) RecordFinalizationMerge(repoID, taskID, commit string) (Finalization, error) {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return Finalization{}, fmt.Errorf("record finalization merge for task %s/%s: merge commit is required", repoID, taskID)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Finalization{}, err
	}
	finalization := ensureFinalization(state.Finalization)
	if finalization.MergeCommit != "" {
		if finalization.MergeCommit != commit {
			return Finalization{}, fmt.Errorf("record finalization merge for task %s/%s: %w", repoID, taskID, ErrFinalizationConflict)
		}
		return finalization, nil
	}
	finalization.MergeCommit = commit
	state.Finalization = &finalization
	if err := s.save(state); err != nil {
		return Finalization{}, err
	}
	return finalization, nil
}

func finalizationHasPublicationMutation(finalization Finalization) bool {
	return finalization.PublicationStartedAt != nil || finalization.CommittedAt != nil ||
		strings.TrimSpace(finalization.Commit) != "" || finalization.PendingCommit != nil ||
		strings.TrimSpace(finalization.MergeCommit) != "" || finalization.PushedAt != nil ||
		finalization.ClosedAt != nil
}

// RecordFinalizationPublicationStart records the durable boundary after which
// the selected integration flow cannot change, before publication mutates Git
// or backend state.
func (s Store) RecordFinalizationPublicationStart(repoID, taskID string) (Finalization, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Finalization{}, err
	}
	finalization := ensureFinalization(state.Finalization)
	if finalization.PublicationStartedAt != nil {
		return finalization, nil
	}
	if strings.TrimSpace(string(finalization.IntegrationFlow)) == "" {
		return Finalization{}, fmt.Errorf("record finalization publication start for task %s/%s: integration flow is required", repoID, taskID)
	}
	if strings.TrimSpace(finalization.DestinationBranch) == "" {
		return Finalization{}, fmt.Errorf("record finalization publication start for task %s/%s: integration destination is required", repoID, taskID)
	}
	now := s.now().UTC()
	finalization.PublicationStartedAt = &now
	state.Finalization = &finalization
	if err := s.save(state); err != nil {
		return Finalization{}, err
	}
	return finalization, nil
}

// RecordFinalizationCommitIntent persists the expected parent and message before
// task finalization creates its publication commit.
func (s Store) RecordFinalizationCommitIntent(
	repoID string,
	taskID string,
	parent string,
	message string,
) (Finalization, error) {
	parent = strings.TrimSpace(parent)
	message = strings.TrimSpace(message)
	if parent == "" {
		return Finalization{}, fmt.Errorf("record finalization commit intent for task %s/%s: parent is required", repoID, taskID)
	}
	if message == "" {
		return Finalization{}, fmt.Errorf("record finalization commit intent for task %s/%s: message is required", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Finalization{}, err
	}
	finalization := ensureFinalization(state.Finalization)
	if strings.TrimSpace(finalization.Commit) != "" {
		return finalization, nil
	}
	intent := FinalizationCommitIntent{Parent: parent, Message: message}
	if finalization.PendingCommit != nil {
		if *finalization.PendingCommit != intent {
			return Finalization{}, fmt.Errorf(
				"record finalization commit intent for task %s/%s: %w",
				repoID,
				taskID,
				ErrFinalizationConflict,
			)
		}
		return finalization, nil
	}

	finalization.PendingCommit = &intent
	state.Finalization = &finalization
	if err := s.save(state); err != nil {
		return Finalization{}, err
	}
	return finalization, nil
}

// RecordFinalizationCommit records the commit created by task finalization.
func (s Store) RecordFinalizationCommit(repoID, taskID string, commit string) (Finalization, error) {
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return Finalization{}, fmt.Errorf("record finalization commit for task %s/%s: commit is required", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Finalization{}, err
	}
	finalization := ensureFinalization(state.Finalization)
	if strings.TrimSpace(finalization.Commit) != "" {
		if finalization.Commit != commit {
			return Finalization{}, fmt.Errorf(
				"record finalization commit for task %s/%s: %w",
				repoID,
				taskID,
				ErrFinalizationConflict,
			)
		}
		return finalization, nil
	}

	now := s.nowUTC()
	finalization.Commit = commit
	finalization.PendingCommit = nil
	finalization.CommittedAt = &now
	state.Finalization = &finalization
	if err := s.save(state); err != nil {
		return Finalization{}, err
	}
	return finalization, nil
}

// RecordFinalizationPush records that the finalization commit was pushed.
func (s Store) RecordFinalizationPush(repoID, taskID string, opts FinalizationPushOptions) (Finalization, error) {
	branch := strings.TrimSpace(opts.Branch)
	pushTarget := strings.TrimSpace(opts.PushTarget)
	if branch == "" {
		return Finalization{}, fmt.Errorf("record finalization push for task %s/%s: branch is required", repoID, taskID)
	}
	if !validPushTarget(pushTarget) {
		return Finalization{}, fmt.Errorf("record finalization push for task %s/%s: unsupported push target %q", repoID, taskID, pushTarget)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Finalization{}, err
	}
	finalization := ensureFinalization(state.Finalization)
	if strings.TrimSpace(finalization.Commit) == "" {
		return Finalization{}, fmt.Errorf("record finalization push for task %s/%s: finalization commit is required", repoID, taskID)
	}
	if finalization.PushedAt != nil {
		return finalization, nil
	}

	now := s.nowUTC()
	finalization.PushedAt = &now
	state.Finalization = &finalization
	state.Events = append(state.Events, Event{
		Type:       EventChangesPushed,
		At:         now,
		Branch:     branch,
		PushTarget: pushTarget,
	})
	if err := s.save(state); err != nil {
		return Finalization{}, err
	}
	return finalization, nil
}

// RecordFinalizationClose records that the backend task was closed.
func (s Store) RecordFinalizationClose(repoID, taskID string, opts FinalizationCloseOptions) (Finalization, error) {
	reason := strings.TrimSpace(opts.Reason)
	if reason == "" {
		return Finalization{}, fmt.Errorf("record finalization close for task %s/%s: reason is required", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Finalization{}, err
	}
	finalization := ensureFinalization(state.Finalization)
	if strings.TrimSpace(finalization.Commit) == "" {
		return Finalization{}, fmt.Errorf("record finalization close for task %s/%s: finalization commit is required", repoID, taskID)
	}
	if finalization.PushedAt == nil {
		return Finalization{}, fmt.Errorf("record finalization close for task %s/%s: finalization push is required", repoID, taskID)
	}
	if finalization.ClosedAt != nil {
		return finalization, nil
	}

	now := s.nowUTC()
	finalization.ClosedAt = &now
	state.Finalization = &finalization
	state.Events = append(state.Events, Event{
		Type:        EventTaskClosed,
		At:          now,
		CloseReason: reason,
	})
	if err := s.save(state); err != nil {
		return Finalization{}, err
	}
	return finalization, nil
}

// RecordFeatureBranchPR appends an idempotent audit event after the backend
// task has recorded a feature-branch PR URL.
func (s Store) RecordFeatureBranchPR(repoID, taskID string, opts FeatureBranchPROptions) (Event, error) {
	prURL := strings.TrimSpace(opts.PRURL)
	branch := strings.TrimSpace(opts.Branch)
	if prURL == "" {
		return Event{}, fmt.Errorf("record feature branch PR for task %s/%s: PR URL is required", repoID, taskID)
	}
	if branch == "" {
		return Event{}, fmt.Errorf("record feature branch PR for task %s/%s: branch is required", repoID, taskID)
	}

	eventType := EventPRCreated
	if opts.WasRecovered {
		eventType = EventPRRecovered
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Event{}, err
	}
	for _, event := range state.Events {
		if event.Type == eventType && strings.TrimSpace(event.PRURL) == prURL {
			return event, nil
		}
	}

	event := Event{
		Type:   eventType,
		At:     s.nowUTC(),
		Branch: branch,
		PRURL:  prURL,
	}
	state.Events = append(state.Events, event)
	if err := s.save(state); err != nil {
		return Event{}, err
	}
	return event, nil
}

// RecordFinalizationFailure appends a durable diagnostic for a failed task done
// publication/finalization attempt.
func (s Store) RecordFinalizationFailure(repoID, taskID string, cause error) (Event, error) {
	var message string
	if cause != nil {
		message = strings.TrimSpace(cause.Error())
	}
	if message == "" {
		return Event{}, fmt.Errorf("record finalization failure for task %s/%s: error is required", repoID, taskID)
	}

	return s.appendEvent(repoID, taskID, Event{
		Type:  EventFinalizationFailed,
		Error: message,
	})
}

// BeginSyncConflictOperation persists the rollback checkpoint before Git enters
// the conflict-producing merge. A task may own only one active operation.
func (s Store) BeginSyncConflictOperation(repoID, taskID string, operation SyncConflictOperation) (SyncConflictOperation, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return SyncConflictOperation{}, err
	}
	if state.ActiveSyncConflict != nil {
		return SyncConflictOperation{}, fmt.Errorf("begin sync conflict operation for task %s/%s: an active operation already exists", repoID, taskID)
	}
	operation = normalizeSyncConflictOperation(operation)
	now := s.nowUTC()
	if operation.CreatedAt.IsZero() {
		operation.CreatedAt = now
	}
	operation.UpdatedAt = now
	if err := validateSyncConflictOperation(operation); err != nil {
		return SyncConflictOperation{}, fmt.Errorf("begin sync conflict operation for task %s/%s: %w", repoID, taskID, err)
	}
	state.ActiveSyncConflict = &operation
	if err := s.save(state); err != nil {
		return SyncConflictOperation{}, err
	}
	return *state.ActiveSyncConflict, nil
}

// UpdateSyncConflictOperation records additional observed recovery facts.
func (s Store) UpdateSyncConflictOperation(repoID, taskID, operationID string, update func(*SyncConflictOperation) error) (SyncConflictOperation, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return SyncConflictOperation{}, err
	}
	operation := state.ActiveSyncConflict
	if operation == nil || operation.ID != strings.TrimSpace(operationID) {
		return SyncConflictOperation{}, fmt.Errorf("update sync conflict operation for task %s/%s: active operation was not found", repoID, taskID)
	}
	if update != nil {
		if err := update(operation); err != nil {
			return SyncConflictOperation{}, err
		}
	}
	operation.UpdatedAt = s.nowUTC()
	*operation = normalizeSyncConflictOperation(*operation)
	if err := validateSyncConflictOperation(*operation); err != nil {
		return SyncConflictOperation{}, err
	}
	if err := s.save(state); err != nil {
		return SyncConflictOperation{}, err
	}
	return *state.ActiveSyncConflict, nil
}

// ClearSyncConflictOperation clears an active operation after a separately
// durable successful terminal audit event has been recorded.
func (s Store) ClearSyncConflictOperation(repoID, taskID, operationID string) error {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return err
	}
	if state.ActiveSyncConflict == nil || state.ActiveSyncConflict.ID != strings.TrimSpace(operationID) {
		return fmt.Errorf("clear sync conflict operation for task %s/%s: active operation was not found", repoID, taskID)
	}
	state.ActiveSyncConflict = nil
	return s.save(state)
}

// ResolveSyncConflictOperation clears an active operation only after the
// caller has completed and verified its durable terminal transition.
func (s Store) ResolveSyncConflictOperation(repoID, taskID, operationID, outcome, reason string) error {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return err
	}
	operation := state.ActiveSyncConflict
	if operation == nil || operation.ID != strings.TrimSpace(operationID) {
		return fmt.Errorf("resolve sync conflict operation for task %s/%s: active operation was not found", repoID, taskID)
	}
	outcome, reason = strings.TrimSpace(outcome), strings.TrimSpace(reason)
	if outcome == "" {
		return fmt.Errorf("resolve sync conflict operation for task %s/%s: outcome is required", repoID, taskID)
	}
	now := s.nowUTC()
	var execution *AgentExecution
	agentName := ""
	if operation.Execution != nil {
		interrupted := *operation.Execution
		agentName = interrupted.Agent
		interrupted.Status = RunStatusInterrupted
		interrupted.FinishedAt = &now
		interrupted.DurationMillis = durationMillis(interrupted.StartedAt, now)
		interrupted.InterruptionReason = reason
		interrupted.InterruptionTrigger = "sync_recovery"
		execution = &interrupted
	}
	state.Events = append(state.Events, Event{Type: EventSyncConflictRolledBack, At: now, Status: RunStatusInterrupted, Agent: agentName, Execution: execution, Branch: operation.Branch, DefaultBranch: operation.DefaultBranch, Worktree: operation.Worktree, ConflictFiles: cloneStrings(operation.ConflictFiles), Message: outcome, Error: reason})
	state.ActiveSyncConflict = nil
	return s.save(state)
}

// MarkSyncConflictOperationUnresolved keeps the active operation as a guard
// and records a durable operator-facing diagnostic.
func (s Store) MarkSyncConflictOperationUnresolved(repoID, taskID, operationID, reason string) error {
	_, err := s.UpdateSyncConflictOperation(repoID, taskID, operationID, func(operation *SyncConflictOperation) error {
		operation.Phase = SyncConflictPhaseUnresolved
		operation.Outcome = "unresolved"
		operation.Reason = strings.TrimSpace(reason)
		if operation.Reason == "" {
			return errors.New("unresolved reason is required")
		}
		return nil
	})
	if err != nil {
		return err
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return err
	}
	operation := state.ActiveSyncConflict
	state.Events = append(state.Events, Event{Type: EventSyncConflictUnresolved, At: s.nowUTC(), Branch: operation.Branch, DefaultBranch: operation.DefaultBranch, Worktree: operation.Worktree, Error: operation.Reason})
	return s.save(state)
}

// RecordSyncConflictResolutionStarted records the launch of a sync conflict-repair agent.
func (s Store) RecordSyncConflictResolutionStarted(
	repoID,
	taskID string,
	opts SyncConflictResolutionEventOptions,
) (Event, error) {
	return s.recordSyncConflictResolutionEvent(
		repoID,
		taskID,
		EventSyncConflictStarted,
		RunStatusRunning,
		opts,
		nil,
	)
}

// RecordSyncConflictResolutionFinished records a successful sync conflict repair and pushed merge.
func (s Store) RecordSyncConflictResolutionFinished(
	repoID,
	taskID string,
	opts SyncConflictResolutionEventOptions,
) (Event, error) {
	return s.recordSyncConflictResolutionEvent(
		repoID,
		taskID,
		EventSyncConflictFinished,
		RunStatusSucceeded,
		opts,
		nil,
	)
}

// RecordSyncConflictResolutionFailed records a failed sync conflict-repair attempt.
func (s Store) RecordSyncConflictResolutionFailed(
	repoID,
	taskID string,
	opts SyncConflictResolutionEventOptions,
	cause error,
) (Event, error) {
	return s.recordSyncConflictResolutionEvent(
		repoID,
		taskID,
		EventSyncConflictFailed,
		RunStatusFailed,
		opts,
		cause,
	)
}

// RecordSyncConflictResolutionUsage attaches recovered usage telemetry to one terminal sync-conflict event.
func (s Store) RecordSyncConflictResolutionUsage(
	repoID,
	taskID string,
	target Event,
	opts RecordRunUsageOptions,
) (Event, error) {
	if !isTerminalSyncConflictEventType(target.Type) {
		return Event{}, fmt.Errorf(
			"record sync conflict resolution usage for task %s/%s: event type %q is not terminal sync conflict resolution",
			repoID,
			taskID,
			target.Type,
		)
	}
	if target.Execution == nil {
		return Event{}, fmt.Errorf(
			"record sync conflict resolution usage for task %s/%s: target event has no execution facts",
			repoID,
			taskID,
		)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Event{}, err
	}

	matchIndex := -1
	for index, event := range state.Events {
		if syncConflictResolutionUsageTargetMatches(event, target) {
			if matchIndex >= 0 {
				return Event{}, fmt.Errorf(
					"record sync conflict resolution usage for task %s/%s: multiple terminal sync conflict events match stable execution facts",
					repoID,
					taskID,
				)
			}
			matchIndex = index
		}
	}
	if matchIndex < 0 {
		return Event{}, fmt.Errorf(
			"record sync conflict resolution usage for task %s/%s: terminal sync conflict event was not found",
			repoID,
			taskID,
		)
	}

	execution := *state.Events[matchIndex].Execution
	execution = applyRunUsageOptions(execution, opts, s.nowUTC())
	state.Events[matchIndex].Execution = normalizeOptionalAgentExecution(&execution)
	if err := s.save(state); err != nil {
		return Event{}, err
	}
	return state.Events[matchIndex], nil
}

// RecordWorktreeCleanup appends an idempotent audit event after Orpheus removes
// a deterministic closed-task worktree.
func (s Store) RecordWorktreeCleanup(repoID, taskID string, opts WorktreeCleanupOptions) (Event, error) {
	worktree := strings.TrimSpace(opts.Worktree)
	if worktree == "" {
		return Event{}, fmt.Errorf("record worktree cleanup for task %s/%s: worktree is required", repoID, taskID)
	}
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Event{}, err
	}
	for _, event := range state.Events {
		if event.Type == EventWorktreeRemoved && strings.TrimSpace(event.Worktree) == worktree {
			return event, nil
		}
	}
	event := Event{Type: EventWorktreeRemoved, At: s.nowUTC(), Worktree: worktree}
	if err := validateEvent(event); err != nil {
		return Event{}, fmt.Errorf("record worktree cleanup for task %s/%s: %w", repoID, taskID, err)
	}
	state.Events = append(state.Events, event)
	if err := s.save(state); err != nil {
		return Event{}, err
	}
	return event, nil
}

// RecordTaskClosed appends an idempotent local audit event after a backend task
// is closed. PR facts are recorded when the closure followed a merged PR.
func (s Store) RecordTaskClosed(repoID, taskID string, opts TaskClosedOptions) (Event, error) {
	reason := strings.TrimSpace(opts.Reason)
	if reason == "" {
		return Event{}, fmt.Errorf("record task closed event for task %s/%s: reason is required", repoID, taskID)
	}
	prURL := strings.TrimSpace(opts.PRURL)
	observedState := strings.TrimSpace(opts.ObservedPRState)
	if reason == CloseReasonPRMerged && prURL == "" {
		return Event{}, fmt.Errorf("record task closed event for task %s/%s: PR URL is required for merged PR closure", repoID, taskID)
	}
	if reason == CloseReasonPRMerged && observedState == "" {
		return Event{}, fmt.Errorf("record task closed event for task %s/%s: observed PR state is required for merged PR closure", repoID, taskID)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Event{}, err
	}
	for _, event := range state.Events {
		if event.Type == EventTaskClosed &&
			strings.TrimSpace(event.CloseReason) == reason &&
			strings.TrimSpace(event.PRURL) == prURL &&
			strings.TrimSpace(event.ObservedPRState) == observedState {
			return event, nil
		}
	}

	event := Event{
		Type:            EventTaskClosed,
		At:              s.nowUTC(),
		CloseReason:     reason,
		PRURL:           prURL,
		ObservedPRState: observedState,
	}
	if err := validateEvent(event); err != nil {
		return Event{}, fmt.Errorf("record task closed event for task %s/%s: %w", repoID, taskID, err)
	}
	state.Events = append(state.Events, event)
	if err := s.save(state); err != nil {
		return Event{}, err
	}
	return event, nil
}

// FailRunStart records that an attempt failed before the agent process started.
func (s Store) FailRunStart(repoID, taskID string, attempt int, cause error) (RunAttempt, error) {
	errorText := ""
	if cause != nil {
		errorText = cause.Error()
	}
	return s.completeRun(repoID, taskID, attempt, RunStatusFailed, EventRunStartFailed, errorText)
}

func (s Store) recordSyncConflictResolutionEvent(
	repoID,
	taskID string,
	eventType EventType,
	status RunStatus,
	opts SyncConflictResolutionEventOptions,
	cause error,
) (Event, error) {
	event, err := syncConflictResolutionEvent(eventType, status, s.nowUTC(), opts, cause)
	if err != nil {
		return Event{}, fmt.Errorf("record sync conflict resolution for task %s/%s: %w", repoID, taskID, err)
	}

	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Event{}, err
	}
	state.Events = append(state.Events, event)
	if err := s.save(state); err != nil {
		return Event{}, err
	}
	return event, nil
}

// Events returns a copy of trace/audit events for a task.
func (s Store) Events(repoID, taskID string) ([]Event, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return nil, err
	}
	return cloneEvents(state.Events), nil
}

// LatestRun returns the highest-numbered run attempt from state.
func LatestRun(state TaskState) (RunAttempt, bool) {
	if len(state.Runs) == 0 {
		return RunAttempt{}, false
	}

	latest := state.Runs[0]
	for _, run := range state.Runs[1:] {
		if run.Attempt > latest.Attempt {
			latest = run
		}
	}
	return latest, true
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
	latest, ok := LatestRun(state)
	if !ok {
		return CompletionRunHistory{}, errors.New("latest run completion is required")
	}
	if latest.Completion == nil {
		return CompletionRunHistory{}, fmt.Errorf("latest run attempt %d completion is required", latest.Attempt)
	}

	var original RunAttempt
	for _, run := range state.Runs {
		if run.Completion == nil || run.ReviewFollowUp != nil {
			continue
		}
		if original.Attempt == 0 || run.Attempt < original.Attempt {
			original = run
		}
	}
	if original.Attempt == 0 {
		return CompletionRunHistory{}, errors.New("original implementation completion is required")
	}
	return CompletionRunHistory{Original: original, Latest: latest}, nil
}

// LatestReview returns the highest-numbered review attempt from state.
func LatestReview(state TaskState) (ReviewAttempt, bool) {
	if len(state.Reviews) == 0 {
		return ReviewAttempt{}, false
	}

	latest := state.Reviews[0]
	for _, review := range state.Reviews[1:] {
		if review.Attempt > latest.Attempt {
			latest = review
		}
	}
	return latest, true
}

// LatestFinalizationFailure returns the latest recorded task done
// publication/finalization failure event from state.
func LatestFinalizationFailure(state TaskState) (Event, bool) {
	var latest Event
	for _, event := range state.Events {
		if event.Type != EventFinalizationFailed {
			continue
		}
		if latest.Type == "" || event.At.After(latest.At) {
			latest = event
		}
	}
	return latest, latest.Type != ""
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

// GitFactsFor returns the current branch and checkout facts. Old persisted
// Target values are reconciled only as a compatibility fallback.
func GitFactsFor(state TaskState) (GitFacts, bool) {
	facts := normalizeGitFacts(state.GitFacts)
	if !facts.IsZero() {
		return facts, true
	}
	legacy := normalizeTaskTarget(state.Target)
	return legacy, !legacy.IsZero()
}

// WorkDirectoryFor returns the immutable task working directory.
func WorkDirectoryFor(state TaskState) (WorkDirectory, bool) {
	directory := normalizeWorkDirectory(state.WorkDirectory)
	return directory, !directory.IsZero()
}

// RecordGitFacts updates the current branch after repository-root publication
// materializes its deterministic task branch. The work directory remains fixed.
func (s Store) RecordGitFacts(repoID, taskID, branch, worktree string) (TaskState, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return TaskState{}, err
	}
	directory, ok := WorkDirectoryFor(state)
	if !ok {
		return TaskState{}, fmt.Errorf("record Git facts for task %s/%s: work directory is missing", repoID, taskID)
	}
	facts := normalizeGitFacts(GitFacts{Branch: branch, Worktree: worktree})
	if err := validateGitFacts(facts); err != nil {
		return TaskState{}, fmt.Errorf("record Git facts for task %s/%s: %w", repoID, taskID, err)
	}
	if directory.Path != facts.Worktree {
		return TaskState{}, fmt.Errorf("record Git facts for task %s/%s: work directory %q does not match %q", repoID, taskID, directory.Path, facts.Worktree)
	}
	state.GitFacts = facts
	if err := s.save(state); err != nil {
		return TaskState{}, err
	}
	return state, nil
}

func (s Store) appendEvent(repoID, taskID string, event Event) (Event, error) {
	state, err := s.Load(repoID, taskID)
	if err != nil {
		return Event{}, err
	}
	event.At = nonZeroTime(event.At, s.nowUTC())
	if err := validateEvent(event); err != nil {
		return Event{}, fmt.Errorf("append event for task %s/%s: %w", repoID, taskID, err)
	}
	state.Events = append(state.Events, event)
	if err := s.save(state); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (s Store) completeRun(repoID, taskID string, attempt int, status RunStatus, eventType EventType, errorText string) (RunAttempt, error) {
	span := s.operationSpan("complete_run", repoID, taskID,
		slog.Int("attempt", attempt),
		slog.String("run_status", string(status)),
		slog.String("event_type", string(eventType)),
	)
	state, err := s.Load(repoID, taskID)
	if err != nil {
		span.FinishError(context.Background(), err)
		return RunAttempt{}, err
	}

	index := runAttemptIndex(state, attempt)
	if index < 0 {
		err := fmt.Errorf("complete run attempt for task %s/%s: attempt %d was not found", repoID, taskID, attempt)
		span.FinishError(context.Background(), err)
		return RunAttempt{}, err
	}

	now := s.nowUTC()
	finished := now
	state.Runs[index].Status = status
	state.Runs[index].Execution.Status = status
	state.Runs[index].Execution.FinishedAt = &finished
	state.Runs[index].Execution.DurationMillis = durationMillis(state.Runs[index].Execution.StartedAt, finished)
	updated := state.Runs[index]
	state.Events = append(state.Events, runEvent(updated, eventType, now, status, errorText))

	if err := s.save(state); err != nil {
		span.FinishError(context.Background(), err)
		return RunAttempt{}, err
	}
	span.Finish(context.Background(), logging.StatusSuccess)
	return updated, nil
}

func runEvent(run RunAttempt, eventType EventType, at time.Time, status RunStatus, errorText string) Event {
	return Event{
		Type:    eventType,
		At:      at,
		Attempt: run.Attempt,
		Status:  status,
		Agent:   run.Execution.Agent,
		Error:   strings.TrimSpace(errorText),
	}
}

func isTerminalSyncConflictEventType(eventType EventType) bool {
	return eventType == EventSyncConflictFinished || eventType == EventSyncConflictFailed
}

func syncConflictResolutionUsageTargetMatches(event Event, target Event) bool {
	if event.Type != target.Type || event.Status != target.Status || !isTerminalSyncConflictEventType(event.Type) {
		return false
	}
	if event.Execution == nil || target.Execution == nil {
		return false
	}
	if event.Branch != target.Branch || event.DefaultBranch != target.DefaultBranch || event.Worktree != target.Worktree {
		return false
	}
	return agentExecutionStableFactsMatch(*event.Execution, *target.Execution)
}

func agentExecutionStableFactsMatch(left AgentExecution, right AgentExecution) bool {
	if left.Purpose != right.Purpose || left.Status != right.Status {
		return false
	}
	if left.Agent != right.Agent || left.Profile != right.Profile || left.Harness != right.Harness || left.Command != right.Command {
		return false
	}
	if left.SessionName != right.SessionName || !slices.Equal(left.Args, right.Args) {
		return false
	}
	if !left.StartedAt.Equal(right.StartedAt) || left.DurationMillis != right.DurationMillis {
		return false
	}
	return equalOptionalTimes(left.FinishedAt, right.FinishedAt)
}

func equalOptionalTimes(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func syncConflictResolutionEvent(
	eventType EventType,
	status RunStatus,
	at time.Time,
	opts SyncConflictResolutionEventOptions,
	cause error,
) (Event, error) {
	if status != RunStatusRunning && status != RunStatusSucceeded && status != RunStatusFailed {
		return Event{}, fmt.Errorf("unsupported status %q", status)
	}
	errorText := ""
	if cause != nil {
		errorText = strings.TrimSpace(cause.Error())
	}
	if eventType == EventSyncConflictFailed && errorText == "" {
		return Event{}, errors.New("failed conflict resolution event requires an error")
	}

	execution := normalizeAgentExecution(opts.Execution)
	execution.Purpose = AgentExecutionPurposeSyncConflictResolution
	execution.Status = status
	if execution.StartedAt.IsZero() {
		execution.StartedAt = at
	}
	if status != RunStatusRunning {
		execution = applyRunUsageOptions(execution, opts.Usage, at)
		finished := at
		execution.FinishedAt = &finished
		execution.DurationMillis = durationMillis(execution.StartedAt, finished)
	}

	event := Event{
		Type:          eventType,
		At:            at,
		Status:        status,
		Agent:         execution.Agent,
		Execution:     &execution,
		Branch:        strings.TrimSpace(opts.Branch),
		DefaultBranch: strings.TrimSpace(opts.DefaultBranch),
		Worktree:      strings.TrimSpace(opts.Worktree),
		ConflictFiles: cloneStrings(opts.ConflictFiles),
		Commit:        strings.TrimSpace(opts.Commit),
		Error:         errorText,
		PRURL:         strings.TrimSpace(opts.PRURL),
	}
	if err := validateEvent(event); err != nil {
		return Event{}, err
	}
	return event, nil
}

func (s Store) save(taskState TaskState) error {
	normalized, err := normalizeStateForSave(taskState)
	if err != nil {
		return err
	}
	rel, err := taskStateRelPath(normalized.RepoID, normalized.TaskID)
	if err != nil {
		return err
	}
	path, _ := s.paths.DataPath(rel)
	span := logging.Start(context.Background(), s.logger, "task state save",
		slog.String("component", "taskstate"),
		slog.String("operation", "save"),
		slog.String("path", path),
		slog.String("repo_id", normalized.RepoID),
		slog.String("task_id", normalized.TaskID),
	)
	if err := s.paths.WriteDataYAML(rel, normalized); err != nil {
		err = fmt.Errorf("save task state %s/%s: %w", normalized.RepoID, normalized.TaskID, err)
		span.FinishError(context.Background(), err)
		return err
	}
	span.Finish(context.Background(), logging.StatusSuccess, slog.Int("event_count", len(normalized.Events)))
	return nil
}

func (s Store) operationSpan(operation string, repoID string, taskID string, attrs ...slog.Attr) logging.Span {
	baseAttrs := []slog.Attr{
		slog.String("component", "taskstate"),
		slog.String("operation", operation),
		slog.String("repo_id", repoID),
		slog.String("task_id", taskID),
	}
	baseAttrs = append(baseAttrs, attrs...)
	return logging.Start(context.Background(), s.logger, "task state operation", baseAttrs...)
}

func (s Store) nowUTC() time.Time {
	return s.now().UTC()
}

func emptyTaskState(repoID, taskID string) TaskState {
	return TaskState{Version: schemaVersion, RepoID: repoID, TaskID: taskID}
}

func normalizedLocation(repoID, taskID string) (string, string, string, error) {
	normalizedRepoID, err := cleanPathComponent("repo id", repoID)
	if err != nil {
		return "", "", "", err
	}
	normalizedTaskID, err := cleanPathComponent("task id", taskID)
	if err != nil {
		return "", "", "", err
	}
	rel, err := taskStateRelPath(normalizedRepoID, normalizedTaskID)
	if err != nil {
		return "", "", "", err
	}
	return normalizedRepoID, normalizedTaskID, rel, nil
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

func validateSyncConflictOperation(operation SyncConflictOperation) error {
	if strings.TrimSpace(operation.ID) == "" || strings.TrimSpace(operation.Branch) == "" || strings.TrimSpace(operation.Worktree) == "" || strings.TrimSpace(operation.DefaultBranch) == "" {
		return errors.New("id, branch, worktree, and default branch are required")
	}
	if strings.TrimSpace(operation.Checkpoint.LocalHead) == "" || strings.TrimSpace(operation.Checkpoint.RemoteHead) == "" || strings.TrimSpace(operation.Checkpoint.MergeSource) == "" {
		return errors.New("checkpoint local head, remote head, and merge source are required")
	}
	switch operation.Phase {
	case SyncConflictPhasePrepared, SyncConflictPhaseConflicted, SyncConflictPhaseResolving, SyncConflictPhaseLocalCompleted, SyncConflictPhasePushIntent, SyncConflictPhasePushed, SyncConflictPhaseUnresolved:
	default:
		return fmt.Errorf("unsupported phase %q", operation.Phase)
	}
	if operation.CreatedAt.IsZero() || operation.UpdatedAt.IsZero() {
		return errors.New("created and updated timestamps are required")
	}
	if operation.Execution != nil {
		if err := validateAgentExecution(*operation.Execution); err != nil {
			return fmt.Errorf("execution is invalid: %w", err)
		}
	}
	return nil
}

func normalizeSyncConflictOperation(operation SyncConflictOperation) SyncConflictOperation {
	operation.ID = strings.TrimSpace(operation.ID)
	operation.Branch = strings.TrimSpace(operation.Branch)
	operation.Worktree = strings.TrimSpace(operation.Worktree)
	operation.DefaultBranch = strings.TrimSpace(operation.DefaultBranch)
	operation.Checkpoint.LocalHead = strings.TrimSpace(operation.Checkpoint.LocalHead)
	operation.Checkpoint.RemoteHead = strings.TrimSpace(operation.Checkpoint.RemoteHead)
	operation.Checkpoint.MergeSource = strings.TrimSpace(operation.Checkpoint.MergeSource)
	operation.Phase = SyncConflictPhase(strings.TrimSpace(string(operation.Phase)))
	operation.ConflictFiles = cloneStrings(operation.ConflictFiles)
	operation.Execution = normalizeOptionalAgentExecution(operation.Execution)
	operation.LocalHead = strings.TrimSpace(operation.LocalHead)
	operation.ObservedRemoteHead = strings.TrimSpace(operation.ObservedRemoteHead)
	operation.Outcome = strings.TrimSpace(operation.Outcome)
	operation.Reason = strings.TrimSpace(operation.Reason)
	if !operation.CreatedAt.IsZero() {
		operation.CreatedAt = operation.CreatedAt.UTC()
	}
	if !operation.UpdatedAt.IsZero() {
		operation.UpdatedAt = operation.UpdatedAt.UTC()
	}
	return operation
}

func validateLoadedState(taskState TaskState, repoID, taskID string) error {
	if strings.TrimSpace(taskState.RepoID) != repoID {
		return fmt.Errorf("repo_id is %q, expected %q", taskState.RepoID, repoID)
	}
	if strings.TrimSpace(taskState.TaskID) != taskID {
		return fmt.Errorf("task_id is %q, expected %q", taskState.TaskID, taskID)
	}
	if taskState.Version != schemaVersion {
		if taskState.Version != 0 || !taskStateContentIsEmpty(taskState) {
			return unsupportedTaskStateVersionError(taskState.Version)
		}
	}
	if err := validateWorkDirectory(taskState.WorkDirectory); err != nil {
		return fmt.Errorf("work_directory is invalid: %w", err)
	}
	if err := validateGitFacts(taskState.GitFacts); err != nil {
		return fmt.Errorf("git_facts is invalid: %w", err)
	}
	if err := validateLegacyTaskTarget(taskState.Target); err != nil {
		return fmt.Errorf("legacy target is invalid: %w", err)
	}
	for _, run := range taskState.Runs {
		if err := validateRun(run); err != nil {
			return err
		}
	}
	for _, event := range taskState.Events {
		if err := validateEvent(event); err != nil {
			return err
		}
	}
	for _, review := range taskState.Reviews {
		if err := validateReview(review); err != nil {
			return err
		}
	}
	if err := validateFinalization(taskState.Finalization); err != nil {
		return fmt.Errorf("finalization is invalid: %w", err)
	}
	if taskState.ActiveSyncConflict != nil {
		if err := validateSyncConflictOperation(*taskState.ActiveSyncConflict); err != nil {
			return fmt.Errorf("active sync conflict is invalid: %w", err)
		}
	}
	return nil
}

func unsupportedTaskStateVersionError(version int) error {
	return fmt.Errorf("unsupported task state version %d", version)
}

func migrateLoadedState(taskState TaskState) TaskState {
	// Target was the former immutable execution model. Reconcile it only when
	// loading old state so all current callers consume explicit Git facts.
	if taskState.GitFacts.IsZero() && !taskState.Target.IsZero() {
		taskState.GitFacts = taskState.Target
	}
	switch taskState.Version {
	case 3:
		return migrateTaskStateV3(taskState)
	case 0:
		if taskStateContentIsEmpty(taskState) {
			taskState.Version = schemaVersion
		}
	}
	return taskState
}

func migrateTaskStateV3(taskState TaskState) TaskState {
	taskState.Version = schemaVersion
	for i := range taskState.Runs {
		completion := taskState.Runs[i].Completion
		if completion == nil || strings.TrimSpace(completion.TechnicalExplanation) != "" {
			continue
		}
		completion.TechnicalExplanation = legacyTechnicalExplanation(*completion)
	}
	return taskState
}

func legacyTechnicalExplanation(completion Completion) string {
	return strings.TrimSpace(strings.Join([]string{
		"This completion was recorded before Orpheus required a dedicated technical explanation.",
		"Legacy detailed description:",
		strings.TrimSpace(completion.DetailedDescription),
	}, "\n\n"))
}

func taskStateContentIsEmpty(taskState TaskState) bool {
	return taskState.WorkDirectory.IsZero() &&
		taskState.GitFacts.IsZero() &&
		taskState.Target.IsZero() &&
		len(taskState.Runs) == 0 &&
		len(taskState.Reviews) == 0 &&
		len(taskState.Events) == 0 &&
		taskState.Finalization == nil &&
		taskState.ActiveSyncConflict == nil
}

func normalizeStateForSave(taskState TaskState) (TaskState, error) {
	repoID, taskID, _, err := normalizedLocation(taskState.RepoID, taskState.TaskID)
	if err != nil {
		return TaskState{}, err
	}
	if taskState.Version == 0 {
		taskState.Version = schemaVersion
	}
	if err := validateLoadedState(taskState, repoID, taskID); err != nil {
		return TaskState{}, err
	}
	if err := validateCommandArgsForSave(taskState); err != nil {
		return TaskState{}, err
	}
	return normalizeState(taskState, repoID, taskID), nil
}

func normalizeState(taskState TaskState, repoID, taskID string) TaskState {
	taskState.Version = schemaVersion
	taskState.RepoID = repoID
	taskState.TaskID = taskID
	taskState.Target = normalizeTaskTarget(taskState.Target)
	taskState.GitFacts = normalizeGitFacts(taskState.GitFacts)
	taskState.WorkDirectory = normalizeWorkDirectory(taskState.WorkDirectory)
	if taskState.Finalization != nil {
		finalization := ensureFinalization(taskState.Finalization)
		taskState.Finalization = &finalization
	}
	if taskState.ActiveSyncConflict != nil {
		operation := normalizeSyncConflictOperation(*taskState.ActiveSyncConflict)
		taskState.ActiveSyncConflict = &operation
	}
	for i := range taskState.Runs {
		taskState.Runs[i] = normalizeRunAttempt(taskState.Runs[i])
	}
	for i := range taskState.Events {
		taskState.Events[i] = normalizeEvent(taskState.Events[i])
	}
	for i := range taskState.Reviews {
		for j := range taskState.Reviews[i].Steps {
			step, err := normalizeReviewStep(taskState.Reviews[i].Steps[j])
			if err == nil {
				taskState.Reviews[i].Steps[j] = step
			}
		}
	}
	return taskState
}

func normalizeRunAttempt(run RunAttempt) RunAttempt {
	run.Execution = normalizeAgentExecution(run.Execution)
	run.Status = run.Execution.Status
	if run.Status == "" {
		run.Status = RunStatusRunning
		run.Execution.Status = run.Status
	}
	return run
}

func normalizeEvent(event Event) Event {
	event.Type = EventType(strings.TrimSpace(string(event.Type)))
	event.Status = RunStatus(strings.TrimSpace(string(event.Status)))
	event.Agent = strings.TrimSpace(event.Agent)
	if event.Execution != nil {
		execution := normalizeAgentExecution(*event.Execution)
		event.Execution = &execution
		if event.Agent == "" {
			event.Agent = execution.Agent
		}
	}
	event.Branch = strings.TrimSpace(event.Branch)
	event.DefaultBranch = strings.TrimSpace(event.DefaultBranch)
	event.Worktree = strings.TrimSpace(event.Worktree)
	event.ConflictFiles = cloneStrings(event.ConflictFiles)
	event.Commit = strings.TrimSpace(event.Commit)
	event.Error = strings.TrimSpace(event.Error)
	event.InterruptionReason = strings.TrimSpace(event.InterruptionReason)
	event.InterruptionTrigger = strings.TrimSpace(event.InterruptionTrigger)
	event.Message = strings.TrimSpace(event.Message)
	event.RequestedSummary = strings.TrimSpace(event.RequestedSummary)
	event.RequestedDescription = strings.TrimSpace(event.RequestedDescription)
	event.RequestedDetailedDescription = strings.TrimSpace(event.RequestedDetailedDescription)
	event.RequestedTechnicalExplanation = strings.TrimSpace(event.RequestedTechnicalExplanation)
	event.PRURL = strings.TrimSpace(event.PRURL)
	event.ObservedPRState = strings.TrimSpace(event.ObservedPRState)
	event.PushTarget = strings.TrimSpace(event.PushTarget)
	event.CloseReason = strings.TrimSpace(event.CloseReason)
	return event
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

func normalizeOptionalAgentExecution(execution *AgentExecution) *AgentExecution {
	if execution == nil {
		return nil
	}
	normalized := normalizeAgentExecution(*execution)
	return &normalized
}

func validateAgentExecution(execution AgentExecution) error {
	if !validAgentExecutionPurpose(execution.Purpose) {
		return fmt.Errorf("unsupported purpose %q", execution.Purpose)
	}
	if !validRunStatus(execution.Status) {
		return fmt.Errorf("unsupported status %q", execution.Status)
	}
	if execution.StartedAt.IsZero() {
		return errors.New("started_at is required")
	}
	if execution.SupervisorPID < 0 || execution.ChildPID < 0 {
		return errors.New("process PIDs cannot be negative")
	}
	if execution.Status == RunStatusRunning && execution.FinishedAt != nil {
		return errors.New("finished_at cannot be recorded while running")
	}
	if execution.Status != RunStatusRunning && (execution.FinishedAt == nil || execution.FinishedAt.IsZero()) {
		return fmt.Errorf("finished_at is required for status %q", execution.Status)
	}
	if execution.DurationMillis < 0 {
		return errors.New("duration_millis cannot be negative")
	}
	if execution.Launch != nil {
		if err := validateAgentLaunch(*execution.Launch); err != nil {
			return fmt.Errorf("launch is invalid: %w", err)
		}
	}
	if execution.Session != nil && agentSessionIsZero(normalizeAgentSession(*execution.Session)) {
		return errors.New("session must include id or log_path")
	}
	if execution.Usage != nil && agentUsageIsZero(normalizeAgentUsage(*execution.Usage)) {
		return errors.New("usage must include at least one token field")
	}
	if execution.UsageCost != nil && agentUsageCostIsZero(normalizeAgentUsageCost(*execution.UsageCost)) {
		return errors.New("usage_cost must include a positive amount or a known zero-cost estimate")
	}
	if !execution.UsageCapture.IsZero() && !validUsageCaptureStatus(execution.UsageCapture.Status) {
		return fmt.Errorf("unsupported usage_capture status %q", execution.UsageCapture.Status)
	}
	return nil
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

func validateAgentLaunch(launch AgentLaunch) error {
	launch = normalizeAgentLaunch(launch)
	switch launch.Mode {
	case AgentLaunchFresh:
		if launch.SourceRunAttempt != 0 || launch.SourceSession != nil || launch.UsageBaseline != nil || launch.CostBaseline != nil {
			return errors.New("fresh launch cannot include resumed-session provenance")
		}
	case AgentLaunchResumed:
		if launch.SourceRunAttempt <= 0 {
			return errors.New("resumed launch requires a positive source_run_attempt")
		}
		if launch.SourceSession == nil || strings.TrimSpace(launch.SourceSession.ID) == "" || strings.TrimSpace(launch.SourceSession.LogPath) == "" {
			return errors.New("resumed launch requires source session id and log_path")
		}
		if launch.FallbackReason != "" {
			return errors.New("resumed launch cannot include a fallback reason")
		}
	default:
		return fmt.Errorf("unsupported mode %q", launch.Mode)
	}
	return nil
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

func hasImmutableCodexUsageCost(execution AgentExecution) bool {
	return strings.TrimSpace(execution.Harness) == "codex" &&
		execution.UsageCost != nil &&
		strings.TrimSpace(execution.UsageCost.Kind) == "estimated_api_equivalent"
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

func lockTaskWorkDirectory(state *TaskState, requested WorkDirectory) error {
	requested = normalizeWorkDirectory(requested)
	if requested.IsZero() {
		return nil
	}
	// A persisted Target without a work directory is an active legacy task.
	// Preserve its direct-publication classification on follow-up runs instead
	// of silently converting it into modern repository-root PR publication.
	if state.WorkDirectory.IsZero() && !state.Target.IsZero() {
		return nil
	}
	if err := validateWorkDirectory(requested); err != nil {
		return err
	}
	current := normalizeWorkDirectory(state.WorkDirectory)
	if current.IsZero() {
		state.WorkDirectory = requested
		return nil
	}
	if current.Path != requested.Path {
		return fmt.Errorf("work directory is already locked to %q; requested %q", current.Path, requested.Path)
	}
	state.WorkDirectory = current
	return nil
}

func lockTaskGitFacts(state *TaskState, requested GitFacts) error {
	requested = normalizeGitFacts(requested)
	if requested.IsZero() {
		return nil
	}
	if err := validateGitFacts(requested); err != nil {
		return err
	}

	current, ok := GitFactsFor(*state)
	if !ok {
		state.GitFacts = requested
		return nil
	}
	if current.Branch != requested.Branch || current.Worktree != requested.Worktree {
		return fmt.Errorf(
			"git facts are already branch %q and worktree %q; requested branch %q and worktree %q",
			current.Branch,
			current.Worktree,
			requested.Branch,
			requested.Worktree,
		)
	}
	state.GitFacts = current
	return nil
}

func normalizeWorkDirectory(directory WorkDirectory) WorkDirectory {
	return WorkDirectory{Path: normalizeWorktreePath(directory.Path)}
}

func validateWorkDirectory(directory WorkDirectory) error {
	directory = normalizeWorkDirectory(directory)
	if directory.IsZero() {
		return nil
	}
	if !filepath.IsAbs(directory.Path) {
		return fmt.Errorf("work directory must be absolute, got %q", directory.Path)
	}
	return nil
}

func normalizeGitFacts(facts GitFacts) GitFacts {
	return GitFacts{
		Branch:   strings.TrimSpace(facts.Branch),
		Worktree: normalizeWorktreePath(facts.Worktree),
	}
}

func normalizeTaskTarget(target TaskTarget) TaskTarget {
	return TaskTarget{
		Branch:   strings.TrimSpace(target.Branch),
		Worktree: normalizeWorktreePath(target.Worktree),
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

func validateGitFacts(facts GitFacts) error {
	facts = normalizeGitFacts(facts)
	if facts.IsZero() {
		return nil
	}
	if facts.Branch == "" {
		return errors.New("branch is required when worktree is set")
	}
	if facts.Worktree == "" {
		return errors.New("worktree is required when branch is set")
	}
	if !filepath.IsAbs(facts.Worktree) {
		return fmt.Errorf("worktree must be absolute, got %q", facts.Worktree)
	}
	return nil
}

func validateLegacyTaskTarget(target TaskTarget) error {
	target = normalizeTaskTarget(target)
	if target.IsZero() {
		return nil
	}
	if target.Branch == "" {
		return errors.New("branch is required when worktree is set")
	}
	if target.Worktree == "" {
		return errors.New("worktree is required when branch is set")
	}
	if !filepath.IsAbs(target.Worktree) {
		return fmt.Errorf("worktree must be absolute, got %q", target.Worktree)
	}
	return nil
}

func validateRun(run RunAttempt) error {
	if run.Attempt <= 0 {
		return fmt.Errorf("run attempt must be positive, got %d", run.Attempt)
	}
	if !validRunStatus(run.Status) {
		return fmt.Errorf("run attempt %d has unsupported status %q", run.Attempt, run.Status)
	}
	if err := validateAgentExecution(run.Execution); err != nil {
		return fmt.Errorf("run attempt %d has invalid execution: %w", run.Attempt, err)
	}
	if run.Execution.Purpose != AgentExecutionPurposeImplementation {
		return fmt.Errorf("run attempt %d execution purpose is %q, expected %q", run.Attempt, run.Execution.Purpose, AgentExecutionPurposeImplementation)
	}
	if run.Execution.Status != run.Status {
		return fmt.Errorf("run attempt %d execution status is %q, expected %q", run.Attempt, run.Execution.Status, run.Status)
	}
	if run.Completion != nil {
		if err := validateCompletion(*run.Completion); err != nil {
			return fmt.Errorf("run attempt %d has invalid completion: %w", run.Attempt, err)
		}
	}
	if run.ReviewFollowUp != nil {
		if err := validateReviewFollowUp(*run.ReviewFollowUp); err != nil {
			return fmt.Errorf("run attempt %d has invalid review follow-up: %w", run.Attempt, err)
		}
	}
	return nil
}

func validateCompletion(completion Completion) error {
	if strings.TrimSpace(completion.Summary) == "" {
		return errors.New("summary is required")
	}
	if strings.TrimSpace(completion.Description) == "" {
		return errors.New("description is required")
	}
	if strings.TrimSpace(completion.DetailedDescription) == "" {
		return errors.New("detailed_description is required")
	}
	if strings.TrimSpace(completion.TechnicalExplanation) == "" {
		return errors.New("technical_explanation is required")
	}
	if completion.CompletedAt.IsZero() {
		return errors.New("completed_at is required")
	}
	if strings.TrimSpace(completion.CommitError) != "" && strings.TrimSpace(completion.Commit) != "" {
		return errors.New("commit_error cannot be recorded with commit")
	}
	return nil
}

func validateReview(review ReviewAttempt) error {
	if review.Attempt <= 0 {
		return fmt.Errorf("review attempt must be positive, got %d", review.Attempt)
	}
	if !validReviewStatus(review.Status) {
		return fmt.Errorf("review attempt %d has unsupported status %q", review.Attempt, review.Status)
	}
	if strings.TrimSpace(review.Pipeline) == "" {
		return fmt.Errorf("review attempt %d requires pipeline", review.Attempt)
	}
	if strings.TrimSpace(review.Step) == "" {
		return fmt.Errorf("review attempt %d requires step", review.Attempt)
	}
	if review.StartedAt.IsZero() {
		return fmt.Errorf("review attempt %d requires started_at", review.Attempt)
	}
	switch review.Status {
	case ReviewStatusRunning, ReviewStatusWaitingForManual, ReviewStatusWaitingForAutomatedDecision:
		if review.FinishedAt != nil {
			return fmt.Errorf("review attempt %d cannot have finished_at while %s", review.Attempt, review.Status)
		}
	default:
		if review.FinishedAt == nil || review.FinishedAt.IsZero() {
			return fmt.Errorf("review attempt %d requires finished_at for status %q", review.Attempt, review.Status)
		}
	}
	for _, step := range review.Steps {
		if _, err := normalizeReviewStep(step); err != nil {
			return fmt.Errorf("review attempt %d has invalid step: %w", review.Attempt, err)
		}
	}
	for _, finding := range review.Findings {
		if _, err := normalizeReviewFinding(finding); err != nil {
			return fmt.Errorf("review attempt %d has invalid finding: %w", review.Attempt, err)
		}
	}
	return nil
}

func normalizeReviewStep(step ReviewStep) (ReviewStep, error) {
	step.Kind = strings.TrimSpace(step.Kind)
	step.Name = strings.TrimSpace(step.Name)
	step.Execution = normalizeOptionalAgentExecution(step.Execution)
	comparison, err := normalizeReviewComparison(step.Comparison)
	if err != nil {
		return ReviewStep{}, err
	}
	step.Comparison = comparison
	step.ExitCode = cloneIntPointer(step.ExitCode)

	if step.Kind == "" {
		return ReviewStep{}, errors.New("kind is required")
	}
	if step.Name == "" {
		return ReviewStep{}, errors.New("name is required")
	}
	if step.ExitCode != nil && *step.ExitCode < 0 {
		return ReviewStep{}, errors.New("exit_code cannot be negative")
	}
	if step.Execution != nil {
		if step.Execution.Purpose != AgentExecutionPurposeReview {
			return ReviewStep{}, fmt.Errorf("execution purpose is %q, expected %q", step.Execution.Purpose, AgentExecutionPurposeReview)
		}
		if err := validateAgentExecution(*step.Execution); err != nil {
			return ReviewStep{}, fmt.Errorf("execution is invalid: %w", err)
		}
	}
	return step, nil
}

func normalizeReviewComparison(comparison *ReviewComparison) (*ReviewComparison, error) {
	if comparison == nil {
		return nil, nil
	}
	if comparison.AlternateExecution == nil {
		return nil, errors.New("comparison alternate_execution is required")
	}
	execution := normalizeAgentExecution(*comparison.AlternateExecution)
	if execution.Purpose != AgentExecutionPurposeReview {
		return nil, errors.New("comparison alternate_execution purpose must be review")
	}
	if err := validateAgentExecution(execution); err != nil {
		return nil, fmt.Errorf("comparison alternate_execution is invalid: %w", err)
	}
	findings := make([]AlternateReviewFinding, len(comparison.AlternateFindings))
	for i, alternate := range comparison.AlternateFindings {
		finding, err := normalizeReviewFinding(alternate.Finding)
		if err != nil {
			return nil, fmt.Errorf("comparison alternate finding %d is invalid: %w", i+1, err)
		}
		if finding.Reviewer != "alternate" {
			return nil, fmt.Errorf("comparison alternate finding %d reviewer must be alternate", i+1)
		}
		classification := AlternateFindingClassification(strings.TrimSpace(string(alternate.Classification)))
		if classification != "" && classification != AlternateFindingAdmitted && classification != AlternateFindingDuplicate && classification != AlternateFindingExcluded {
			return nil, fmt.Errorf("comparison alternate finding %d has unsupported classification %q", i+1, classification)
		}
		if classification != AlternateFindingDuplicate && alternate.DuplicateOf != 0 {
			return nil, fmt.Errorf("comparison alternate finding %d has duplicate_of without duplicate classification", i+1)
		}
		findings[i] = AlternateReviewFinding{Finding: finding, Classification: classification, DuplicateOf: alternate.DuplicateOf}
	}
	return &ReviewComparison{AlternateExecution: &execution, AlternateFindings: findings, Failure: strings.TrimSpace(comparison.Failure), InputInterrupted: comparison.InputInterrupted}, nil
}

func validateCommandArgsForSave(taskState TaskState) error {
	for _, run := range taskState.Runs {
		if err := validateCommandArgs(run.Execution.Args); err != nil {
			return fmt.Errorf("run attempt %d has invalid args: %w", run.Attempt, err)
		}
	}
	for _, review := range taskState.Reviews {
		for _, step := range review.Steps {
			if step.Execution != nil {
				if err := validateCommandArgs(step.Execution.Args); err != nil {
					return fmt.Errorf("review attempt %d step %q has invalid args: %w", review.Attempt, step.Name, err)
				}
			}
			if step.Comparison != nil && step.Comparison.AlternateExecution != nil {
				if err := validateCommandArgs(step.Comparison.AlternateExecution.Args); err != nil {
					return fmt.Errorf("review attempt %d step %q alternate has invalid args: %w", review.Attempt, step.Name, err)
				}
			}
		}
	}
	return nil
}

func validateCommandArgs(args []string) error {
	for index, arg := range args {
		if strings.HasPrefix(arg, " - ") && strings.Contains(arg, "\n") {
			return fmt.Errorf("arg %d cannot be a multi-line value starting with %q", index, " - ")
		}
	}
	return nil
}

func normalizeReviewFinding(finding ReviewFinding) (ReviewFinding, error) {
	finding.Type = FindingType(strings.TrimSpace(string(finding.Type)))
	finding.Title = strings.TrimSpace(finding.Title)
	finding.Description = strings.TrimSpace(finding.Description)
	finding.Step = strings.TrimSpace(finding.Step)
	finding.Reviewer = strings.TrimSpace(finding.Reviewer)
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

func normalizeReviewFollowUp(followUp *ReviewFollowUp) *ReviewFollowUp {
	if followUp == nil {
		return nil
	}
	clone := ReviewFollowUp{
		ReviewAttempt:          followUp.ReviewAttempt,
		FindingIndexes:         cloneInts(followUp.FindingIndexes),
		AdvisoryFindingIndexes: cloneInts(followUp.AdvisoryFindingIndexes),
	}
	return &clone
}

func validateReviewFollowUp(followUp ReviewFollowUp) error {
	if followUp.ReviewAttempt <= 0 {
		return errors.New("review_attempt must be positive")
	}
	if len(followUp.FindingIndexes) == 0 {
		return errors.New("finding_indexes is required")
	}
	for _, index := range append(cloneInts(followUp.FindingIndexes), followUp.AdvisoryFindingIndexes...) {
		if index < 0 {
			return errors.New("finding index cannot be negative")
		}
	}
	return nil
}

func validateFinalization(finalization *Finalization) error {
	if finalization == nil {
		return nil
	}

	finalization.IntegrationFlow = publication.IntegrationFlow(strings.TrimSpace(string(finalization.IntegrationFlow)))
	finalization.DestinationBranch = strings.TrimSpace(finalization.DestinationBranch)
	if err := publication.ValidateIntegrationFlow(finalization.IntegrationFlow); err != nil {
		return err
	}
	finalization.MergeCommit = strings.TrimSpace(finalization.MergeCommit)
	commit := strings.TrimSpace(finalization.Commit)
	if finalization.PendingCommit != nil {
		if commit != "" {
			return errors.New("pending_commit cannot be recorded with commit")
		}
		if finalization.CommittedAt != nil || finalization.PushedAt != nil || finalization.ClosedAt != nil {
			return errors.New("pending_commit cannot be recorded with finalization timestamps")
		}
		if strings.TrimSpace(finalization.PendingCommit.Parent) == "" {
			return errors.New("pending_commit parent is required")
		}
		if strings.TrimSpace(finalization.PendingCommit.Message) == "" {
			return errors.New("pending_commit message is required")
		}
		return nil
	}
	if commit == "" {
		if finalization.MergeCommit != "" {
			return errors.New("commit is required when merge_commit is recorded")
		}
		if finalization.CommittedAt != nil || finalization.PushedAt != nil || finalization.ClosedAt != nil {
			return errors.New("commit is required when any finalization timestamp is recorded")
		}
		return nil
	}
	if finalization.CommittedAt == nil || finalization.CommittedAt.IsZero() {
		return errors.New("committed_at is required when commit is recorded")
	}
	if finalization.PushedAt != nil && finalization.PushedAt.IsZero() {
		return errors.New("pushed_at must be non-zero when recorded")
	}
	if finalization.ClosedAt != nil && finalization.ClosedAt.IsZero() {
		return errors.New("closed_at must be non-zero when recorded")
	}
	if finalization.ClosedAt != nil && finalization.PushedAt == nil {
		return errors.New("pushed_at is required when closed_at is recorded")
	}
	return nil
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
	if clone.PendingCommit != nil {
		intent := *clone.PendingCommit
		intent.Parent = strings.TrimSpace(intent.Parent)
		intent.Message = strings.TrimSpace(intent.Message)
		clone.PendingCommit = &intent
	}
	return clone
}

func validateEvent(event Event) error {
	if !validEventType(event.Type) {
		return fmt.Errorf("unsupported event type %q", event.Type)
	}
	if event.Status != "" && !validRunStatus(event.Status) {
		return fmt.Errorf("event %q has unsupported run status %q", event.Type, event.Status)
	}
	if event.Execution != nil {
		if err := validateAgentExecution(*event.Execution); err != nil {
			return fmt.Errorf("event %q execution is invalid: %w", event.Type, err)
		}
	}
	if event.Type == EventChangesPushed && !validPushTarget(event.PushTarget) {
		return fmt.Errorf("event %q has unsupported push target %q", event.Type, event.PushTarget)
	}
	if event.Type == EventTaskClosed && strings.TrimSpace(event.CloseReason) == "" {
		return fmt.Errorf("event %q requires a close reason", event.Type)
	}
	if event.Type == EventFinalizationFailed && strings.TrimSpace(event.Error) == "" {
		return fmt.Errorf("event %q requires an error", event.Type)
	}
	if (event.Type == EventRunInterrupted || event.Type == EventReviewInterrupted) && (strings.TrimSpace(event.InterruptionReason) == "" || strings.TrimSpace(event.InterruptionTrigger) == "") {
		return fmt.Errorf("event %q requires interruption reason and trigger", event.Type)
	}
	switch event.Type {
	case EventSyncConflictStarted, EventSyncConflictFinished, EventSyncConflictFailed:
		if event.Execution == nil {
			return fmt.Errorf("event %q requires execution facts", event.Type)
		}
		if event.Execution.Purpose != AgentExecutionPurposeSyncConflictResolution {
			return fmt.Errorf(
				"event %q execution purpose is %q, expected %q",
				event.Type,
				event.Execution.Purpose,
				AgentExecutionPurposeSyncConflictResolution,
			)
		}
		if strings.TrimSpace(event.Branch) == "" {
			return fmt.Errorf("event %q requires a branch", event.Type)
		}
		if strings.TrimSpace(event.DefaultBranch) == "" {
			return fmt.Errorf("event %q requires a default branch", event.Type)
		}
		if event.Type == EventSyncConflictFailed && strings.TrimSpace(event.Error) == "" {
			return fmt.Errorf("event %q requires an error", event.Type)
		}
	}
	return nil
}

func validRunStatus(status RunStatus) bool {
	switch status {
	case RunStatusRunning, RunStatusSucceeded, RunStatusFailed, RunStatusInterrupted:
		return true
	default:
		return false
	}
}

func validAgentExecutionPurpose(purpose AgentExecutionPurpose) bool {
	switch purpose {
	case AgentExecutionPurposeImplementation,
		AgentExecutionPurposeReview,
		AgentExecutionPurposeSyncConflictResolution:
		return true
	default:
		return false
	}
}

func validUsageCaptureStatus(status UsageCaptureStatus) bool {
	switch status {
	case UsageCaptureCaptured, UsageCaptureUnknown, UsageCaptureAmbiguous:
		return true
	default:
		return false
	}
}

func validReviewStatus(status ReviewStatus) bool {
	switch status {
	case ReviewStatusRunning,
		ReviewStatusBlocked,
		ReviewStatusFailed,
		ReviewStatusPassed,
		ReviewStatusAborted,
		ReviewStatusWaitingForManual,
		ReviewStatusWaitingForAutomatedDecision:
		return true
	default:
		return false
	}
}

func validFindingType(findingType FindingType) bool {
	switch findingType {
	case FindingTypeBlocking, FindingTypeAdvisory, FindingTypeSeparateTask:
		return true
	default:
		return false
	}
}

func validEventType(eventType EventType) bool {
	switch eventType {
	case EventWorktreeCreated,
		EventTaskBranchCreated,
		EventWorktreeReused,
		EventWorktreeRecreated,
		EventWorktreeRemoved,
		EventRunStarted,
		EventRunFinished,
		EventRunStartFailed,
		EventRunInterrupted,
		EventReviewInterrupted,
		EventCompletionRecorded,
		EventCompletionRepeated,
		EventChangesPushed,
		EventPRCreated,
		EventPRRecovered,
		EventFinalizationFailed,
		EventTaskClosed,
		EventSyncConflictStarted,
		EventSyncConflictFinished,
		EventSyncConflictFailed,
		EventSyncConflictRolledBack,
		EventSyncConflictUnresolved:
		return true
	default:
		return false
	}
}

func validPushTarget(pushTarget string) bool {
	return pushTarget == PushTargetMain || pushTarget == PushTargetBranch
}

func nextAttemptNumber(state TaskState) int {
	latest, ok := LatestRun(state)
	if !ok {
		return 1
	}
	return latest.Attempt + 1
}

func runAttemptIndex(state TaskState, attempt int) int {
	for i, run := range state.Runs {
		if run.Attempt == attempt {
			return i
		}
	}
	return -1
}

func nextReviewAttemptNumber(state TaskState) int {
	latest, ok := LatestReview(state)
	if !ok {
		return 1
	}
	return latest.Attempt + 1
}

func reviewAttemptIndex(state TaskState, attempt int) int {
	for i, review := range state.Reviews {
		if review.Attempt == attempt {
			return i
		}
	}
	return -1
}

func nonZeroTime(value time.Time, fallback time.Time) time.Time {
	if value.IsZero() {
		return fallback
	}
	return value.UTC()
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

func cloneInts(values []int) []int {
	if values == nil {
		return nil
	}
	clone := make([]int, len(values))
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
