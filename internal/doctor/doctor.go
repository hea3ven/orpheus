// Package doctor contains local diagnostics and safe repair routines.
package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

const (
	OutcomeWouldRecover = "would_recover"
	OutcomeRecovered    = "recovered"
	OutcomeUnknown      = "unknown"
	OutcomeAmbiguous    = "ambiguous"
)

// Options describes one doctor diagnostics run.
type Options struct {
	Paths    state.Paths
	Registry registry.Registry
	Fix      bool
	Env      map[string]string
	Probe    workflow.ProcessProbe
}

// Result summarizes one doctor diagnostics run.
type Result struct {
	Rows               []Row
	ImplementationRows []ImplementationRunRow
	PrimaryReviewRows  []PrimaryReviewRow
	SyncConflictRows   []SyncConflictRow
	Summary            Summary
}

// ImplementationRunRow describes stale attached-process recovery diagnostics.
type ImplementationRunRow struct {
	RepoID  string
	TaskID  string
	Attempt int
	Outcome string
	Reason  string
}

// SyncConflictRow describes one persisted sync-conflict recovery operation.
type SyncConflictRow struct {
	RepoID  string
	TaskID  string
	Outcome string
	Reason  string
}

// PrimaryReviewRow describes stale primary-reviewer recovery diagnostics.
type PrimaryReviewRow struct {
	RepoID  string
	TaskID  string
	Attempt int
	Step    string
	Outcome string
	Reason  string
}

// Summary contains operator/script-friendly diagnostic counts.
type Summary struct {
	Checked            int
	Recoverable        int
	Recovered          int
	UnresolvedUnknowns int
	Ambiguous          int
}

// Row describes one Codex usage telemetry diagnostic.
type Row struct {
	RepoID         string
	TaskID         string
	Activity       string
	Attempt        int
	Step           string
	Outcome        string
	Reason         string
	CandidateCount int
	SessionID      string
	LogPath        string
	Model          string
	TotalTokens    int
	CostMicroUSD   int64
	Candidates     []taskstate.UsageCaptureCandidate
}

type executionRef struct {
	repoID                string
	taskID                string
	activity              string
	attempt               int
	step                  string
	reviewAttempt         int
	execution             taskstate.AgentExecution
	executionDirs         []string
	executionDirGroups    [][]string
	resumeBoundary        *agent.ResumedUsageBoundary
	resumeBoundaryFailure string
	event                 taskstate.Event
}

// Run executes doctor diagnostics across registered repositories and local task state.
func Run(opts Options) (Result, error) {
	store := taskstate.NewStore(opts.Paths)
	env := opts.Env
	if env == nil {
		env = agent.UsageCaptureEnvironment()
	}

	var result Result
	for _, repo := range opts.Registry.Repos {
		taskIDs, err := store.TaskIDs(repo.ID)
		if err != nil {
			return Result{}, fmt.Errorf("doctor repo %s: %w", repo.ID, err)
		}
		for _, taskID := range taskIDs {
			taskState, err := store.Load(repo.ID, taskID)
			if err != nil {
				return Result{}, fmt.Errorf("doctor repo %s task %s: %w", repo.ID, taskID, err)
			}
			if err := diagnoseTask(&result, opts.Paths, store, env, repo, taskState, opts.Fix, opts.Probe); err != nil {
				return Result{}, err
			}
		}
	}
	return result, nil
}

func diagnoseTask(
	result *Result,
	paths state.Paths,
	store taskstate.Store,
	env map[string]string,
	repo registry.Repo,
	taskState taskstate.TaskState,
	fix bool,
	probe workflow.ProcessProbe,
) error {
	if err := diagnoseImplementationRunRecovery(result, paths, store, repo, taskState, fix, probe); err != nil {
		return err
	}
	if err := diagnosePrimaryReviewRecovery(result, paths, store, repo, taskState, fix, probe); err != nil {
		return err
	}
	if fix {
		if err := state.WithGlobalMutationLock(paths, "doctor sync recovery", func() error {
			current, err := store.Load(repo.ID, taskState.TaskID)
			if err != nil {
				return fmt.Errorf("reload task state for locked sync recovery: %w", err)
			}
			return diagnoseSyncConflictRecovery(result, store, repo, current, fix, probe)
		}); err != nil {
			return err
		}
	} else if err := diagnoseSyncConflictRecovery(result, store, repo, taskState, fix, probe); err != nil {
		return err
	}
	// Recovery mutates lifecycle facts. Reload before telemetry diagnostics so
	// they cannot overwrite stale running-review state or persist against an
	// execution that just became interrupted.
	var err error
	taskState, err = store.Load(repo.ID, taskState.TaskID)
	if err != nil {
		return fmt.Errorf("reload task state after recovery diagnostics: %w", err)
	}
	executionDirs := taskExecutionDirs(repo, taskState)
	if err := diagnoseImplementationExecutions(result, store, env, repo, taskState, executionDirs, fix); err != nil {
		return err
	}
	if err := diagnoseReviewExecutions(result, store, env, repo, taskState, executionDirs, fix); err != nil {
		return err
	}
	return diagnoseSyncConflictExecutions(result, store, env, repo, taskState, fix)
}

func diagnoseImplementationRunRecovery(
	result *Result,
	paths state.Paths,
	store taskstate.Store,
	repo registry.Repo,
	taskState taskstate.TaskState,
	fix bool,
	probe workflow.ProcessProbe,
) error {
	run, ok := taskstate.ActiveRun(taskState)
	if !ok {
		return nil
	}
	inspection := workflow.InspectImplementationRun(run, probe)
	if inspection.Condition == workflow.ImplementationRunNotRunning {
		return nil
	}
	outcome := string(inspection.Condition)
	if fix && inspection.Condition == workflow.ImplementationRunRecoverable {
		reconciled, err := workflow.ReconcileImplementationRun(context.Background(), paths, store, repo.ID, taskState.TaskID, run.Attempt, "doctor_fix", probe)
		switch {
		case err != nil:
			outcome = "interruption_failed"
			inspection.Reason = "interruption_record_failed: " + err.Error()
		case reconciled.Condition == workflow.ImplementationRunRecoverable:
			outcome = "interrupted"
		default:
			outcome = string(reconciled.Condition)
			inspection.Reason = "diagnosed_attempt_no_longer_active"
		}
	}
	result.ImplementationRows = append(result.ImplementationRows, ImplementationRunRow{
		RepoID: repo.ID, TaskID: taskState.TaskID, Attempt: run.Attempt, Outcome: outcome, Reason: inspection.Reason,
	})
	return nil
}

func diagnosePrimaryReviewRecovery(
	result *Result,
	paths state.Paths,
	store taskstate.Store,
	repo registry.Repo,
	taskState taskstate.TaskState,
	fix bool,
	probe workflow.ProcessProbe,
) error {
	primary, ok := workflow.ActivePrimaryReviewExecution(taskState)
	if !ok {
		return nil
	}
	inspection := workflow.InspectAttachedExecution(primary.Execution, probe)
	outcome := string(inspection.Condition)
	if fix && inspection.Condition == workflow.AttachedExecutionRecoverable {
		reconciled, err := workflow.ReconcilePrimaryReviewExecution(context.Background(), paths, store, repo.ID, taskState.TaskID, primary.Attempt, "doctor_fix", probe)
		switch {
		case err != nil:
			outcome = "interruption_failed"
			inspection.Reason = "interruption_record_failed: " + err.Error()
		case reconciled.Condition == workflow.AttachedExecutionRecoverable:
			outcome = "interrupted"
		default:
			outcome = string(reconciled.Condition)
			inspection.Reason = "diagnosed_attempt_no_longer_active"
		}
	}
	result.PrimaryReviewRows = append(result.PrimaryReviewRows, PrimaryReviewRow{
		RepoID: repo.ID, TaskID: taskState.TaskID, Attempt: primary.Attempt, Step: primary.StepName, Outcome: outcome, Reason: inspection.Reason,
	})
	return nil
}

//nolint:gocognit,nestif,funlen // Doctor renders the explicit recovery classification table.
func diagnoseSyncConflictRecovery(result *Result, store taskstate.Store, repo registry.Repo, taskState taskstate.TaskState, fix bool, probe workflow.ProcessProbe) error {
	operation := taskState.ActiveSyncConflict
	if operation == nil {
		return nil
	}
	outcome, reason := "unresolved", strings.TrimSpace(operation.Reason)
	if strings.TrimSpace(taskState.GitFacts.Branch) == "" || strings.TrimSpace(taskState.GitFacts.Worktree) == "" || taskState.GitFacts.Branch != operation.Branch || taskState.GitFacts.Worktree != operation.Worktree || taskState.WorkDirectory.Path != operation.Worktree {
		outcome, reason = "ownership_mismatch", "current persisted task ownership does not match the recovery operation"
	} else {
		switch operation.Phase {
		case taskstate.SyncConflictPhaseUnresolved:
			if reason == "" {
				reason = "operation was previously marked unresolved"
			}
		default:
			inspection := syncConflictExecutionInspection(operation.Execution, probe)
			switch inspection.Condition {
			case workflow.AttachedExecutionLive:
				outcome, reason = "live", inspection.Reason
			case workflow.AttachedExecutionUnverifiable:
				outcome, reason = "unverifiable", inspection.Reason
			default:
				syncOpts := gitmeta.TaskBranchSyncOptions{RepoPath: repo.Path, DefaultBranch: operation.DefaultBranch, Branch: operation.Branch, Worktree: operation.Worktree}
				remoteHead, err := gitmeta.InspectRemoteTaskBranchHead(context.Background(), syncOpts)
				switch {
				case err != nil:
					outcome, reason = "unverifiable", "remote inspection failed: "+err.Error()
				case operation.LocalHead != "" && remoteHead == operation.LocalHead && (operation.Phase == taskstate.SyncConflictPhasePushIntent || operation.Phase == taskstate.SyncConflictPhasePushed):
					outcome, reason = "pushed", "remote task branch matches the recorded local completion"
				case remoteHead != operation.Checkpoint.RemoteHead:
					outcome, reason = "remote_ambiguous", "remote task branch changed from the recorded checkpoint"
				default:
					checkpoint := gitmeta.TaskBranchConflictCheckpoint{LocalHead: operation.Checkpoint.LocalHead, RemoteHead: operation.Checkpoint.RemoteHead, MergeSource: operation.Checkpoint.MergeSource}
					if eligibilityErr := gitmeta.InspectTaskBranchConflictRollbackEligibility(context.Background(), syncOpts, checkpoint, operation.LocalHead); eligibilityErr != nil {
						outcome, reason = "locally_incompatible", eligibilityErr.Error()
					} else {
						outcome, reason = "rollbackable", inspection.Reason
					}
				}
				if fix && outcome == "pushed" {
					updated, updateErr := store.UpdateSyncConflictOperation(repo.ID, taskState.TaskID, operation.ID, func(active *taskstate.SyncConflictOperation) error {
						active.Phase = taskstate.SyncConflictPhasePushed
						active.ObservedRemoteHead = remoteHead
						return nil
					})
					if updateErr != nil {
						outcome, reason = "state_transition_failed", updateErr.Error()
					} else {
						execution := taskstate.AgentExecution{}
						if updated.Execution != nil {
							execution = *updated.Execution
						}
						_, finishErr := store.RecordSyncConflictResolutionFinished(repo.ID, taskState.TaskID, taskstate.SyncConflictResolutionEventOptions{Execution: execution, Branch: updated.Branch, DefaultBranch: updated.DefaultBranch, Worktree: updated.Worktree, ConflictFiles: updated.ConflictFiles, Commit: updated.LocalHead})
						if finishErr != nil {
							outcome, reason = "state_transition_failed", finishErr.Error()
						} else if clearErr := store.ClearSyncConflictOperation(repo.ID, taskState.TaskID, updated.ID); clearErr != nil {
							outcome, reason = "state_transition_failed", clearErr.Error()
						}
					}
				}
				if fix && outcome == "rollbackable" {
					checkpoint := gitmeta.TaskBranchConflictCheckpoint{LocalHead: operation.Checkpoint.LocalHead, RemoteHead: operation.Checkpoint.RemoteHead, MergeSource: operation.Checkpoint.MergeSource}
					if eligibilityErr := gitmeta.InspectTaskBranchConflictRollbackEligibility(context.Background(), syncOpts, checkpoint, operation.LocalHead); eligibilityErr != nil {
						outcome, reason = "locally_incompatible", eligibilityErr.Error()
					} else {
						switch err := gitmeta.RollbackTaskBranchConflictResolution(context.Background(), syncOpts, checkpoint); {
						case err != nil:
							outcome, reason = "rollback_failed", err.Error()
							if markErr := store.MarkSyncConflictOperationUnresolved(repo.ID, taskState.TaskID, operation.ID, "doctor rollback failed: "+reason); markErr != nil {
								reason += "; failed to persist unresolved state: " + markErr.Error()
							}
						case store.ResolveSyncConflictOperation(repo.ID, taskState.TaskID, operation.ID, "rolled_back", inspection.Reason) != nil:
							outcome, reason = "rollback_failed", "rollback succeeded but state recording failed"
							if markErr := store.MarkSyncConflictOperationUnresolved(repo.ID, taskState.TaskID, operation.ID, "doctor state transition failed: "+reason); markErr != nil {
								reason += "; failed to persist unresolved state: " + markErr.Error()
							}
						default:
							outcome = "rolled_back"
						}
					}
				}
			}
		}
	}
	result.SyncConflictRows = append(result.SyncConflictRows, SyncConflictRow{RepoID: repo.ID, TaskID: taskState.TaskID, Outcome: outcome, Reason: reason})
	return nil
}

func syncConflictExecutionInspection(execution *taskstate.AgentExecution, probe workflow.ProcessProbe) workflow.AttachedExecutionInspection {
	if execution == nil {
		return workflow.AttachedExecutionInspection{Condition: workflow.AttachedExecutionRecoverable, Reason: "resolver_not_started"}
	}
	return workflow.InspectAttachedExecution(*execution, probe)
}

func diagnoseImplementationExecutions(
	result *Result,
	store taskstate.Store,
	env map[string]string,
	repo registry.Repo,
	taskState taskstate.TaskState,
	executionDirs []string,
	fix bool,
) error {
	for index, run := range taskState.Runs {
		boundary, boundaryFailure := nextResumedUsageBoundary(taskState.Runs, index)
		ref := executionRef{
			repoID:                repo.ID,
			taskID:                taskState.TaskID,
			activity:              "implementation",
			attempt:               run.Attempt,
			step:                  "-",
			execution:             run.Execution,
			executionDirs:         executionDirs,
			resumeBoundary:        boundary,
			resumeBoundaryFailure: boundaryFailure,
		}
		if err := appendDiagnosticRow(result, store, env, ref, fix); err != nil {
			return err
		}
	}
	return nil
}

func nextResumedUsageBoundary(
	runs []taskstate.RunAttempt,
	index int,
) (*agent.ResumedUsageBoundary, string) {
	if index < 0 || index >= len(runs) {
		return nil, ""
	}
	execution := runs[index].Execution
	launch := execution.Launch
	if launch == nil || launch.Mode != taskstate.AgentLaunchResumed || launch.SourceSession == nil {
		return nil, ""
	}

	for laterIndex := index + 1; laterIndex < len(runs); laterIndex++ {
		laterExecution := runs[laterIndex].Execution
		laterLaunch := laterExecution.Launch
		if laterLaunch == nil || laterLaunch.Mode != taskstate.AgentLaunchResumed || laterLaunch.SourceSession == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(execution.Harness), strings.TrimSpace(laterExecution.Harness)) {
			continue
		}
		matching, err := agent.SameCanonicalSession(launch.SourceSession, laterLaunch.SourceSession)
		if err != nil {
			return nil, fmt.Sprintf(
				"resumed_session_reuse_boundary_unsafe_at_run_%d: %v",
				runs[laterIndex].Attempt,
				err,
			)
		}
		if !matching {
			continue
		}
		if laterLaunch.UsageBaseline == nil {
			return nil, fmt.Sprintf(
				"resumed_session_reused_at_run_%d_without_usage_upper_bound",
				runs[laterIndex].Attempt,
			)
		}
		return &agent.ResumedUsageBoundary{
			Session:      laterLaunch.SourceSession,
			Usage:        laterLaunch.UsageBaseline,
			CostMicroUSD: laterLaunch.CostBaseline,
		}, ""
	}
	return nil, ""
}

func diagnoseReviewExecutions(
	result *Result,
	store taskstate.Store,
	env map[string]string,
	repo registry.Repo,
	taskState taskstate.TaskState,
	executionDirs []string,
	fix bool,
) error {
	for _, reviewAttempt := range taskState.Reviews {
		for _, step := range reviewAttempt.Steps {
			if step.Execution == nil {
				continue
			}
			ref := executionRef{
				repoID:        repo.ID,
				taskID:        taskState.TaskID,
				activity:      "review-agent",
				attempt:       reviewAttempt.Attempt,
				step:          step.Name,
				reviewAttempt: reviewAttempt.Attempt,
				execution:     *step.Execution,
				executionDirs: executionDirs,
			}
			if err := appendDiagnosticRow(result, store, env, ref, fix); err != nil {
				return err
			}
		}
	}
	return nil
}

func diagnoseSyncConflictExecutions(
	result *Result,
	store taskstate.Store,
	env map[string]string,
	repo registry.Repo,
	taskState taskstate.TaskState,
	fix bool,
) error {
	for _, event := range taskState.Events {
		if !isTerminalSyncConflictEvent(event) || event.Execution == nil {
			continue
		}
		ref := executionRef{
			repoID:             repo.ID,
			taskID:             taskState.TaskID,
			activity:           "sync-conflict-resolution",
			attempt:            event.Attempt,
			step:               "-",
			execution:          *event.Execution,
			executionDirGroups: syncConflictExecutionDirGroups(repo, taskState, event),
			event:              event,
		}
		if err := appendDiagnosticRow(result, store, env, ref, fix); err != nil {
			return err
		}
	}
	return nil
}

func appendDiagnosticRow(
	result *Result,
	store taskstate.Store,
	env map[string]string,
	ref executionRef,
	fix bool,
) error {
	row, err := diagnoseExecution(store, env, ref, fix)
	if err != nil {
		return err
	}
	appendRow(result, row)
	return nil
}

func diagnoseExecution(
	store taskstate.Store,
	env map[string]string,
	ref executionRef,
	fix bool,
) (*Row, error) {
	if !needsUsageDiagnostic(ref.execution) {
		return nil, nil
	}
	if row, handled, err := diagnoseStoredCodexCostEstimate(store, ref, fix); handled || err != nil {
		return row, err
	}
	if len(executionDirGroups(ref)) == 0 {
		return unknownRow(ref, "missing_task_execution_directory"), nil
	}
	if ref.execution.StartedAt.IsZero() {
		return unknownRow(ref, "missing_execution_started_at"), nil
	}
	if ref.resumeBoundaryFailure != "" {
		return unknownRow(ref, ref.resumeBoundaryFailure), nil
	}
	usageOpts := captureUsageWithDirectoryPriority(env, ref)
	usageOpts = preserveStoredUsageCost(ref.execution, usageOpts)
	if usageOpts.UsageCapture.Status != taskstate.UsageCaptureCaptured ||
		usageOpts.Session == nil ||
		usageOpts.Usage == nil {
		return unresolvedRow(ref, usageOpts.UsageCapture, usageOpts.Candidates), nil
	}
	if missingRequiredPiUsageCost(ref.execution, usageOpts) {
		return unresolvedRow(ref, taskstate.AgentUsageCapture{
			Status:         taskstate.UsageCaptureUnknown,
			Reason:         "matching_pi_session_has_no_reported_cost",
			CandidateCount: usageOpts.UsageCapture.CandidateCount,
		}, usageOpts.Candidates), nil
	}

	row := recoveredRow(ref, usageOpts, fix)
	if !fix {
		return &row, nil
	}
	if err := persistRecovery(store, ref, usageOpts); err != nil {
		return nil, err
	}
	return &row, nil
}

func needsUsageDiagnostic(execution taskstate.AgentExecution) bool {
	switch strings.TrimSpace(execution.Harness) {
	case "codex", "pi":
	default:
		return false
	}
	if execution.Usage == nil || execution.Session == nil {
		return true
	}
	if strings.TrimSpace(execution.Session.ID) == "" && strings.TrimSpace(execution.Session.LogPath) == "" {
		return true
	}
	if strings.TrimSpace(execution.Model) == "" {
		return true
	}
	if execution.UsageCost == nil && strings.TrimSpace(execution.Harness) == "pi" {
		return true
	}
	if missingCodexCostEstimate(execution) {
		return true
	}
	return execution.UsageCapture.Status != taskstate.UsageCaptureCaptured
}

func missingCodexCostEstimate(execution taskstate.AgentExecution) bool {
	return strings.TrimSpace(execution.Harness) == "codex" &&
		execution.Usage != nil &&
		strings.TrimSpace(execution.Model) != "" &&
		execution.UsageCost == nil
}

// diagnoseStoredCodexCostEstimate repairs a missing Codex estimate directly
// from already persisted final model and execution-attributed usage. It must
// not re-correlate session logs because historical logs may no longer exist.
func diagnoseStoredCodexCostEstimate(
	store taskstate.Store,
	ref executionRef,
	fix bool,
) (*Row, bool, error) {
	if !missingCodexCostEstimate(ref.execution) {
		return nil, false, nil
	}
	if !agent.HasBillableUsage(*ref.execution.Usage) {
		return unknownRow(ref, agent.UsageCostUnknownBillableUsageMissing), true, nil
	}
	cost, ok := agent.EstimateCodexUsageCostAt(ref.execution.Model, *ref.execution.Usage, ref.execution.StartedAt)
	if !ok {
		return unknownRow(ref, agent.UsageCostUnknownPricingMetadataMissing), true, nil
	}
	usageOpts := taskstate.RecordRunUsageOptions{
		Session:      ref.execution.Session,
		Usage:        ref.execution.Usage,
		UsageCost:    cost,
		UsageCapture: ref.execution.UsageCapture,
		Model:        ref.execution.Model,
	}
	row := recoveredRow(ref, usageOpts, fix)
	if !fix {
		return &row, true, nil
	}
	if err := persistRecovery(store, ref, usageOpts); err != nil {
		return nil, true, err
	}
	return &row, true, nil
}

func preserveStoredUsageCost(
	execution taskstate.AgentExecution,
	usageOpts taskstate.RecordRunUsageOptions,
) taskstate.RecordRunUsageOptions {
	if strings.TrimSpace(execution.Harness) == "codex" &&
		execution.UsageCost != nil &&
		strings.TrimSpace(execution.UsageCost.Kind) == agent.UsageCostKindEstimatedAPIEquivalent {
		usageOpts.UsageCost = execution.UsageCost
	}
	return usageOpts
}

func missingRequiredPiUsageCost(
	execution taskstate.AgentExecution,
	usageOpts taskstate.RecordRunUsageOptions,
) bool {
	return strings.TrimSpace(execution.Harness) == "pi" &&
		execution.UsageCost == nil &&
		usageOpts.UsageCost == nil &&
		!hasRecoverableUsageDetails(execution)
}

func hasRecoverableUsageDetails(execution taskstate.AgentExecution) bool {
	if execution.Usage == nil || execution.Session == nil {
		return true
	}
	if strings.TrimSpace(execution.Session.ID) == "" && strings.TrimSpace(execution.Session.LogPath) == "" {
		return true
	}
	if strings.TrimSpace(execution.Model) == "" {
		return true
	}
	return execution.UsageCapture.Status != taskstate.UsageCaptureCaptured
}

func taskExecutionDirs(repo registry.Repo, taskState taskstate.TaskState) []string {
	dirs := make([]string, 0, 2)
	if facts, ok := taskstate.GitFactsFor(taskState); ok {
		dirs = appendTaskExecutionDir(dirs, facts.Worktree)
	}
	if len(dirs) == 0 {
		dirs = appendTaskExecutionDir(dirs, repo.Path)
	}
	return dirs
}

func syncConflictExecutionDirGroups(
	repo registry.Repo,
	taskState taskstate.TaskState,
	event taskstate.Event,
) [][]string {
	primary := appendTaskExecutionDir(nil, event.Worktree)
	fallbacks := make([]string, 0, 2)
	if facts, ok := taskstate.GitFactsFor(taskState); ok {
		fallbacks = appendTaskExecutionDir(fallbacks, facts.Worktree)
	}
	fallbacks = appendTaskExecutionDir(fallbacks, repo.Path)
	fallbacks = removeTaskExecutionDirs(fallbacks, primary)

	groups := make([][]string, 0, 2)
	if len(primary) > 0 {
		groups = append(groups, primary)
	}
	if len(fallbacks) > 0 {
		groups = append(groups, fallbacks)
	}
	return groups
}

func isTerminalSyncConflictEvent(event taskstate.Event) bool {
	return event.Type == taskstate.EventSyncConflictFinished ||
		event.Type == taskstate.EventSyncConflictFailed
}

func appendTaskExecutionDir(dirs []string, dir string) []string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return dirs
	}
	for _, existing := range dirs {
		if existing == dir {
			return dirs
		}
	}
	return append(dirs, dir)
}

func removeTaskExecutionDirs(dirs []string, removed []string) []string {
	if len(dirs) == 0 || len(removed) == 0 {
		return dirs
	}
	kept := dirs[:0]
	for _, dir := range dirs {
		if !containsTaskExecutionDir(removed, dir) {
			kept = append(kept, dir)
		}
	}
	return kept
}

func containsTaskExecutionDir(dirs []string, dir string) bool {
	for _, existing := range dirs {
		if existing == dir {
			return true
		}
	}
	return false
}

func executionDirGroups(ref executionRef) [][]string {
	if len(ref.executionDirGroups) > 0 {
		return ref.executionDirGroups
	}
	if len(ref.executionDirs) == 0 {
		return nil
	}
	return [][]string{ref.executionDirs}
}

func captureUsageWithDirectoryPriority(
	env map[string]string,
	ref executionRef,
) taskstate.RecordRunUsageOptions {
	var firstUnknown *taskstate.RecordRunUsageOptions
	for _, dirs := range executionDirGroups(ref) {
		if len(dirs) == 0 {
			continue
		}
		usageOpts := agent.CaptureUsage(agent.UsageCaptureOptions{
			Harness:        ref.execution.Harness,
			ExecutionDirs:  dirs,
			SessionName:    ref.execution.SessionName,
			StartedAt:      ref.execution.StartedAt,
			Env:            env,
			Launch:         ref.execution.Launch,
			ResumeBoundary: ref.resumeBoundary,
		})
		if usageCaptureHasCandidate(usageOpts) {
			return usageOpts
		}
		if firstUnknown == nil {
			captured := usageOpts
			firstUnknown = &captured
		}
	}
	if firstUnknown != nil {
		return *firstUnknown
	}
	return taskstate.RecordRunUsageOptions{
		UsageCapture: taskstate.AgentUsageCapture{
			Status: taskstate.UsageCaptureUnknown,
			Reason: "missing_task_execution_directory",
		},
	}
}

func usageCaptureHasCandidate(usageOpts taskstate.RecordRunUsageOptions) bool {
	return usageOpts.UsageCapture.Status == taskstate.UsageCaptureCaptured ||
		usageOpts.UsageCapture.Status == taskstate.UsageCaptureAmbiguous ||
		usageOpts.Session != nil ||
		usageOpts.UsageCapture.CandidateCount > 0 ||
		len(usageOpts.Candidates) > 0
}

func unresolvedRow(
	ref executionRef,
	capture taskstate.AgentUsageCapture,
	candidates []taskstate.UsageCaptureCandidate,
) *Row {
	outcome := OutcomeUnknown
	if capture.Status == taskstate.UsageCaptureAmbiguous {
		outcome = OutcomeAmbiguous
	}
	reason := strings.TrimSpace(capture.Reason)
	if reason == "" {
		reason = "usage_not_recorded"
	}
	return &Row{
		RepoID:         ref.repoID,
		TaskID:         ref.taskID,
		Activity:       ref.activity,
		Attempt:        ref.attempt,
		Step:           ref.step,
		Outcome:        outcome,
		Reason:         reason,
		CandidateCount: capture.CandidateCount,
		Candidates:     candidates,
	}
}

func unknownRow(ref executionRef, reason string) *Row {
	return unresolvedRow(ref, taskstate.AgentUsageCapture{
		Status: taskstate.UsageCaptureUnknown,
		Reason: reason,
	}, nil)
}

func recoveredRow(ref executionRef, usageOpts taskstate.RecordRunUsageOptions, fix bool) Row {
	outcome := OutcomeWouldRecover
	if fix {
		outcome = OutcomeRecovered
	}
	row := Row{
		RepoID:         ref.repoID,
		TaskID:         ref.taskID,
		Activity:       ref.activity,
		Attempt:        ref.attempt,
		Step:           ref.step,
		Outcome:        outcome,
		Reason:         usageOpts.UsageCapture.Reason,
		CandidateCount: usageOpts.UsageCapture.CandidateCount,
		Model:          usageOpts.Model,
	}
	if usageOpts.Session != nil {
		row.SessionID = usageOpts.Session.ID
		row.LogPath = usageOpts.Session.LogPath
	}
	if usageOpts.Usage != nil {
		row.TotalTokens = usageOpts.Usage.TotalTokens
	}
	if usageOpts.UsageCost != nil {
		row.CostMicroUSD = usageOpts.UsageCost.AmountMicroUSD
	}
	return row
}

func persistRecovery(
	store taskstate.Store,
	ref executionRef,
	usageOpts taskstate.RecordRunUsageOptions,
) error {
	switch ref.activity {
	case "implementation":
		if _, err := store.RecordRunUsage(ref.repoID, ref.taskID, ref.attempt, usageOpts); err != nil {
			return fmt.Errorf("doctor recover implementation usage for %s/%s attempt %d: %w",
				ref.repoID,
				ref.taskID,
				ref.attempt,
				err,
			)
		}
	case "review-agent":
		if _, err := store.RecordReviewStepUsage(
			ref.repoID,
			ref.taskID,
			ref.reviewAttempt,
			ref.step,
			usageOpts,
		); err != nil {
			return fmt.Errorf("doctor recover review usage for %s/%s attempt %d step %s: %w",
				ref.repoID,
				ref.taskID,
				ref.attempt,
				ref.step,
				err,
			)
		}
	case "sync-conflict-resolution":
		if _, err := store.RecordSyncConflictResolutionUsage(ref.repoID, ref.taskID, ref.event, usageOpts); err != nil {
			return fmt.Errorf("doctor recover sync-conflict usage for %s/%s: %w",
				ref.repoID,
				ref.taskID,
				err,
			)
		}
	default:
		return fmt.Errorf("doctor recover usage for %s/%s: unsupported activity %q", ref.repoID, ref.taskID, ref.activity)
	}
	return nil
}

func appendRow(result *Result, row *Row) {
	if row == nil {
		return
	}
	result.Rows = append(result.Rows, *row)
	result.Summary.Checked++
	switch row.Outcome {
	case OutcomeWouldRecover:
		result.Summary.Recoverable++
	case OutcomeRecovered:
		result.Summary.Recoverable++
		result.Summary.Recovered++
	case OutcomeAmbiguous:
		result.Summary.Ambiguous++
	default:
		result.Summary.UnresolvedUnknowns++
	}
}
