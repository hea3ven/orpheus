// Package doctor contains local diagnostics and safe repair routines.
package doctor

import (
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
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
}

// Result summarizes one doctor diagnostics run.
type Result struct {
	Rows    []Row
	Summary Summary
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
	repoID             string
	taskID             string
	activity           string
	attempt            int
	step               string
	reviewAttempt      int
	execution          taskstate.AgentExecution
	executionDirs      []string
	executionDirGroups [][]string
	event              taskstate.Event
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
			if err := diagnoseTask(&result, store, env, repo, taskState, opts.Fix); err != nil {
				return Result{}, err
			}
		}
	}
	return result, nil
}

func diagnoseTask(
	result *Result,
	store taskstate.Store,
	env map[string]string,
	repo registry.Repo,
	taskState taskstate.TaskState,
	fix bool,
) error {
	executionDirs := taskExecutionDirs(repo, taskState)
	if err := diagnoseImplementationExecutions(result, store, env, repo, taskState, executionDirs, fix); err != nil {
		return err
	}
	if err := diagnoseReviewExecutions(result, store, env, repo, taskState, executionDirs, fix); err != nil {
		return err
	}
	return diagnoseSyncConflictExecutions(result, store, env, repo, taskState, fix)
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
	for _, run := range taskState.Runs {
		ref := executionRef{
			repoID:        repo.ID,
			taskID:        taskState.TaskID,
			activity:      "implementation",
			attempt:       run.Attempt,
			step:          "-",
			execution:     run.Execution,
			executionDirs: executionDirs,
		}
		if err := appendDiagnosticRow(result, store, env, ref, fix); err != nil {
			return err
		}
	}
	return nil
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
	if len(executionDirGroups(ref)) == 0 {
		return unknownRow(ref, "missing_task_execution_directory"), nil
	}
	if ref.execution.StartedAt.IsZero() {
		return unknownRow(ref, "missing_execution_started_at"), nil
	}
	usageOpts := captureUsageWithDirectoryPriority(env, ref)
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
	if strings.TrimSpace(execution.Harness) == "pi" && execution.UsageCost == nil {
		return true
	}
	return execution.UsageCapture.Status != taskstate.UsageCaptureCaptured
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
	dirs = appendTaskExecutionDir(dirs, taskState.Target.Worktree)
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
	fallbacks = appendTaskExecutionDir(fallbacks, taskState.Target.Worktree)
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
			Harness:       ref.execution.Harness,
			ExecutionDirs: dirs,
			SessionName:   ref.execution.SessionName,
			StartedAt:     ref.execution.StartedAt,
			Env:           env,
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
