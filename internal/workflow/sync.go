package workflow

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

const syncLockOperation = "task sync"

// SyncBackendFactory creates a sync-capable backend for one repository.
type SyncBackendFactory func(task.RepositorySource) (task.SyncBackend, error)

// SyncScanBackendFactory creates a read backend for batch sync candidate scanning.
type SyncScanBackendFactory func(task.RepositorySource) (task.ReadBackend, error)

// SyncRunStore records local audit events produced by sync reconciliation.
type SyncRunStore interface {
	RecordTaskClosed(repoID, taskID string, opts taskstate.TaskClosedOptions) (taskstate.Event, error)
}

// SyncService reconciles backend task state from recorded pull request state.
type SyncService struct {
	Paths          state.Paths
	Sources        []task.RepositorySource
	BackendFactory SyncBackendFactory
	ScanFactory    SyncScanBackendFactory
	RunStore       SyncRunStore
	PRProvider     pullrequest.Provider
}

// SyncOptions are the CLI-provided sync controls.
type SyncOptions struct {
	TaskID string
}

// SyncStatus describes the outcome of a single-task sync.
type SyncStatus string

const (
	// SyncStatusAlreadyInReview means the task's recorded PR is still open.
	SyncStatusAlreadyInReview SyncStatus = "already_in_review"

	// SyncStatusPRMerged means the task's recorded PR is merged.
	SyncStatusPRMerged SyncStatus = "pr_merged"

	// SyncStatusSkipped means the task was resolvable but had no PR state to reconcile.
	SyncStatusSkipped SyncStatus = "skipped"
)

// SyncResult reports the resolved task and sync outcome.
type SyncResult struct {
	Repository task.Repository
	Task       task.Task
	LatestRun  taskstate.RunAttempt
	Status     SyncStatus
	Reason     string
	Branch     string
	Worktree   string
	PRURL      string
}

// SyncAllFailure is a per-repository or per-task batch sync failure.
type SyncAllFailure struct {
	Repository task.Repository
	TaskID     string
	Operation  string
	Err        error
}

// SyncAllResult reports grouped outcomes from a best-effort batch sync.
type SyncAllResult struct {
	Results  []SyncResult
	Failures []SyncAllFailure
}

// HasFailures reports whether any repository or candidate failed.
func (r SyncAllResult) HasFailures() bool {
	return len(r.Failures) > 0
}

type syncTarget struct {
	source  task.RepositorySource
	backend task.SyncBackend
	task    task.Task
}

type syncAllCandidate struct {
	source task.RepositorySource
	taskID string
}

// Sync resolves one task, skips non-eligible states, and pushes eligible task branches.
func (s SyncService) Sync(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.validate(); err != nil {
		return SyncResult{}, err
	}

	var result SyncResult
	err := state.WithGlobalMutationLock(s.Paths, syncLockOperation, func() error {
		synced, err := s.syncLocked(ctx, opts)
		if err != nil {
			return err
		}
		result = synced
		return nil
	})
	if err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

// SyncAll scans all registered repositories and syncs tasks already at a PR boundary.
func (s SyncService) SyncAll(ctx context.Context) (SyncAllResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.validate(); err != nil {
		return SyncAllResult{}, err
	}

	var result SyncAllResult
	err := state.WithGlobalMutationLock(s.Paths, syncLockOperation, func() error {
		candidates, failures := s.scanSyncAllCandidates(ctx)
		result.Failures = append(result.Failures, failures...)

		for _, candidate := range candidates {
			synced, err := s.syncLocked(ctx, SyncOptions{TaskID: candidate.taskID})
			if err != nil {
				result.Failures = append(result.Failures, SyncAllFailure{
					Repository: candidate.source.Repository,
					TaskID:     candidate.taskID,
					Operation:  "sync",
					Err:        err,
				})
				continue
			}
			result.Results = append(result.Results, synced)
		}
		return nil
	})
	if err != nil {
		return SyncAllResult{}, err
	}
	return result, nil
}

func (s SyncService) validate() error {
	if s.BackendFactory == nil {
		return errors.New("task sync backend factory is required")
	}
	if s.RunStore == nil {
		return errors.New("task sync run store is required")
	}
	if s.PRProvider == nil {
		return errors.New("task sync PR provider is required")
	}
	return nil
}

func (s SyncService) scanSyncAllCandidates(ctx context.Context) ([]syncAllCandidate, []SyncAllFailure) {
	candidates := make([]syncAllCandidate, 0)
	failures := make([]SyncAllFailure, 0)
	for _, source := range s.Sources {
		backend, err := s.syncScanBackend(source)
		if err != nil {
			failures = append(failures, SyncAllFailure{
				Repository: source.Repository,
				Operation:  "create_scan_backend",
				Err:        err,
			})
			continue
		}

		tasks, err := backend.List(ctx)
		if err != nil {
			failures = append(failures, SyncAllFailure{
				Repository: source.Repository,
				Operation:  "scan_tasks",
				Err:        err,
			})
			continue
		}

		repoCandidates, repoFailures := s.syncAllCandidatesForTasks(source, tasks)
		candidates = append(candidates, repoCandidates...)
		failures = append(failures, repoFailures...)
	}
	return candidates, failures
}

func (s SyncService) syncScanBackend(source task.RepositorySource) (task.ReadBackend, error) {
	if s.ScanFactory != nil {
		return s.ScanFactory(source)
	}

	backend, err := s.BackendFactory(source)
	if err != nil {
		return nil, err
	}
	readBackend, ok := backend.(task.ReadBackend)
	if !ok {
		return nil, errors.New("task sync scan backend must support list")
	}
	return readBackend, nil
}

func (s SyncService) syncAllCandidatesForTasks(
	source task.RepositorySource,
	tasks []task.Task,
) ([]syncAllCandidate, []SyncAllFailure) {
	candidates := make([]syncAllCandidate, 0)
	failures := make([]SyncAllFailure, 0)
	for _, taskItem := range tasks {
		if !isSyncAllRunnableTask(taskItem) {
			continue
		}

		metadata := taskItem.OrpheusMetadata()
		if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
			candidates = append(candidates, syncAllCandidate{source: source, taskID: taskItem.ID})
		}
	}
	return candidates, failures
}

func isSyncAllRunnableTask(taskItem task.Task) bool {
	if strings.TrimSpace(taskItem.ID) == "" || taskItem.Status == task.StatusClosed {
		return false
	}
	return taskItem.IssueType != task.IssueTypeEpic
}

func (s SyncService) syncLocked(ctx context.Context, opts SyncOptions) (SyncResult, error) {
	target, err := s.resolveTarget(ctx, opts)
	if err != nil {
		return SyncResult{}, err
	}

	if target.task.Status == task.StatusClosed {
		return s.skip(target, taskstate.RunAttempt{}, "task is closed"), nil
	}

	if result, ok, err := s.pollExistingPR(ctx, target); ok || err != nil {
		if err != nil {
			return SyncResult{}, err
		}
		return result, nil
	}

	return s.skip(target, taskstate.RunAttempt{}, task.MetadataPRURL+" is not set"), nil
}

func (s SyncService) pollExistingPR(ctx context.Context, target syncTarget) (SyncResult, bool, error) {
	metadata := target.task.OrpheusMetadata()
	prURL := strings.TrimSpace(metadata.PRURL)
	if !metadata.HasPRURL || prURL == "" {
		return SyncResult{}, false, nil
	}

	status, err := s.PRProvider.StatusByURL(ctx, pullrequest.StatusByURLRequest{URL: prURL})
	if err != nil {
		return SyncResult{}, true, err
	}
	observedURL := strings.TrimSpace(status.URL)
	if observedURL == "" {
		observedURL = prURL
	}

	result := SyncResult{
		Repository: target.source.Repository,
		Task:       target.task.Clone(),
		Branch:     strings.TrimSpace(metadata.Branch),
		Worktree:   strings.TrimSpace(target.task.OrpheusMetadata().Worktree),
		PRURL:      observedURL,
	}

	switch status.State {
	case pullrequest.StateOpen:
		result.Status = SyncStatusAlreadyInReview
		result.Reason = "PR is still open for review"
		return result, true, nil
	case pullrequest.StateMerged:
		result.Status = SyncStatusPRMerged
		result.Reason = "PR is merged; backend task was closed"
		if err := target.backend.Close(ctx, target.task.ID); err != nil {
			return SyncResult{}, true, fmt.Errorf("close backend task %s after merged PR %s: %w", target.task.ID, observedURL, err)
		}
		if _, err := s.RunStore.RecordTaskClosed(
			target.source.Repository.ID,
			target.task.ID,
			taskstate.TaskClosedOptions{
				Reason:          taskstate.CloseReasonPRMerged,
				PRURL:           observedURL,
				ObservedPRState: string(pullrequest.StateMerged),
			},
		); err != nil {
			return SyncResult{}, true, fmt.Errorf(
				"backend task %s was closed after merged PR %s but local task-state audit event failed: %w",
				target.task.ID,
				observedURL,
				err,
			)
		}
		result.Task.Status = task.StatusClosed
		return result, true, nil
	case pullrequest.StateClosed:
		return SyncResult{}, true, fmt.Errorf("task %s PR %s is closed without merge; no backend state was changed", target.task.ID, observedURL)
	default:
		return SyncResult{}, true, fmt.Errorf("task %s PR %s has unsupported provider state %q", target.task.ID, observedURL, status.State)
	}
}

func (s SyncService) resolveTarget(ctx context.Context, opts SyncOptions) (syncTarget, error) {
	resolved, err := task.ResolveTaskSource(s.Sources, opts.TaskID)
	if err != nil {
		return syncTarget{}, err
	}
	backend, err := s.BackendFactory(resolved.Source)
	if err != nil {
		return syncTarget{}, fmt.Errorf(
			"task sync %s: create backend for repo %s (%s; prefix %s): %w",
			resolved.TaskID,
			resolved.Source.Repository.ID,
			resolved.Source.Repository.Name,
			resolved.Source.Repository.TaskIDPrefix,
			err,
		)
	}
	taskItem, err := backend.Get(ctx, resolved.TaskID)
	if err != nil {
		if errors.Is(err, task.ErrNotFound) {
			return syncTarget{}, fmt.Errorf(
				"task sync %s: task was not found in repo %s (%s; prefix %s): %w",
				resolved.TaskID,
				resolved.Source.Repository.ID,
				resolved.Source.Repository.Name,
				resolved.Source.Repository.TaskIDPrefix,
				err,
			)
		}
		return syncTarget{}, fmt.Errorf(
			"task sync %s: query repo %s (%s; prefix %s): %w",
			resolved.TaskID,
			resolved.Source.Repository.ID,
			resolved.Source.Repository.Name,
			resolved.Source.Repository.TaskIDPrefix,
			err,
		)
	}
	return syncTarget{source: resolved.Source, backend: backend, task: taskItem}, nil
}

func (s SyncService) skip(target syncTarget, latest taskstate.RunAttempt, reason string) SyncResult {
	metadata := target.task.OrpheusMetadata()
	return SyncResult{
		Repository: target.source.Repository,
		Task:       target.task.Clone(),
		LatestRun:  latest,
		Status:     SyncStatusSkipped,
		Reason:     reason,
		Branch:     strings.TrimSpace(metadata.Branch),
		Worktree:   strings.TrimSpace(metadata.Worktree),
	}
}

// PullRequestContent is the generated title/body for a pull request.
type PullRequestContent struct {
	Title string
	Body  string
}

// BuildSyncPullRequestContent returns default PR text from the completion handoff.
func BuildSyncPullRequestContent(taskItem task.Task, latest taskstate.RunAttempt) (PullRequestContent, error) {
	return BuildPublicationPullRequestContent("", taskItem, latest)
}

// BuildPublicationPullRequestContent returns PR text using an optional title template.
func BuildPublicationPullRequestContent(titleTemplate string, taskItem task.Task, latest taskstate.RunAttempt) (PullRequestContent, error) {
	if strings.TrimSpace(taskItem.ID) == "" {
		return PullRequestContent{}, errors.New("task id is required")
	}
	if latest.Completion == nil {
		return PullRequestContent{}, errors.New("completion is required")
	}
	renderedTitle, err := publication.RenderTitle(titleTemplate, latest.Completion.Summary, taskItem.ExternalRef)
	if err != nil {
		return PullRequestContent{}, err
	}
	title := singleLine(renderedTitle)
	if title == "" {
		return PullRequestContent{}, errors.New("completion summary is required")
	}
	body := latest.Completion.DetailedDescription
	if strings.TrimSpace(body) == "" {
		return PullRequestContent{}, errors.New("completion detailed description is required")
	}
	return PullRequestContent{
		Title: title,
		Body:  body,
	}, nil
}

// BuildPublicationPullRequestContentFromState returns PR text from the
// canonical implementation completion plus any recorded review process.
func BuildPublicationPullRequestContentFromState(
	titleTemplate string,
	taskItem task.Task,
	state taskstate.TaskState,
) (PullRequestContent, error) {
	run, err := publicationRun(state)
	if err != nil {
		return PullRequestContent{}, err
	}
	content, err := BuildPublicationPullRequestContent(titleTemplate, taskItem, run)
	if err != nil {
		return PullRequestContent{}, err
	}
	content.Body = appendReviewProcess(content.Body, state)
	return content, nil
}

func publicationRun(state taskstate.TaskState) (taskstate.RunAttempt, error) {
	var selected taskstate.RunAttempt
	for _, run := range state.Runs {
		if run.Completion == nil || run.ReviewFollowUp != nil {
			continue
		}
		if selected.Attempt == 0 || run.Attempt > selected.Attempt {
			selected = run
		}
	}
	if selected.Attempt == 0 {
		return taskstate.RunAttempt{}, errors.New("original implementation completion is required")
	}
	return selected, nil
}

func appendReviewProcess(body string, state taskstate.TaskState) string {
	if len(state.Reviews) == 0 {
		return body
	}

	var builder strings.Builder
	builder.WriteString(strings.TrimRight(body, "\n"))
	builder.WriteString("\n\n## Review process\n")
	for _, review := range state.Reviews {
		appendReviewAttempt(&builder, review, state.Runs)
	}
	return builder.String()
}

func appendReviewAttempt(builder *strings.Builder, review taskstate.ReviewAttempt, runs []taskstate.RunAttempt) {
	builder.WriteString("\n### Review attempt ")
	builder.WriteString(strconv.Itoa(review.Attempt))
	builder.WriteString(" — ")
	builder.WriteString(reviewProcessStatus(review.Status))
	builder.WriteString("\n\n")

	for _, stepName := range reviewStepNames(review) {
		builder.WriteString("- ")
		builder.WriteString(reviewStepIcon(review, stepName))
		builder.WriteString(" `")
		builder.WriteString(stepName)
		builder.WriteString("`\n")
		for _, finding := range review.Findings {
			if findingStepName(finding, review.Step) == stepName {
				appendReviewFinding(builder, finding)
			}
		}
	}
	appendFixRuns(builder, review, runs)
}

func reviewProcessStatus(status taskstate.ReviewStatus) string {
	statusText := strings.TrimSpace(string(status))
	if statusText == "" {
		return "unknown"
	}
	return statusText
}

func reviewStepNames(review taskstate.ReviewAttempt) []string {
	names := make([]string, 0, len(review.Steps)+1)
	seen := map[string]bool{}
	for _, step := range review.Steps {
		name := strings.TrimSpace(step.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	for _, finding := range review.Findings {
		name := findingStepName(finding, review.Step)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	if len(names) == 0 {
		name := strings.TrimSpace(review.Step)
		if name == "" {
			name = "review"
		}
		names = append(names, name)
	}
	return names
}

func findingStepName(finding taskstate.ReviewFinding, fallback string) string {
	name := strings.TrimSpace(finding.Step)
	if name != "" {
		return name
	}
	return strings.TrimSpace(fallback)
}

func reviewStepIcon(review taskstate.ReviewAttempt, stepName string) string {
	for _, finding := range review.Findings {
		if findingStepName(finding, review.Step) != stepName {
			continue
		}
		if finding.Type == taskstate.FindingTypeBlocking && strings.TrimSpace(finding.Waiver) == "" {
			return "❌"
		}
	}
	if review.Status == taskstate.ReviewStatusFailed {
		return "⚠️"
	}
	return "✅"
}

func appendReviewFinding(builder *strings.Builder, finding taskstate.ReviewFinding) {
	title := singleLine(finding.Title)
	if title == "" {
		title = "Finding"
	}
	builder.WriteString("  - **")
	builder.WriteString(reviewFindingLabel(finding))
	builder.WriteString(":** ")
	builder.WriteString(title)
	builder.WriteString("\n")

	if strings.TrimSpace(finding.Waiver) != "" {
		builder.WriteString("    - Waived.\n")
		return
	}
	if finding.Type == taskstate.FindingTypeBlocking {
		appendBlockingFindingResolution(builder, finding)
		return
	}
	if finding.Type == taskstate.FindingTypeSeparateTask {
		createdTaskID := strings.TrimSpace(finding.CreatedTaskID)
		if createdTaskID != "" {
			builder.WriteString("    - Created task: ")
			builder.WriteString(createdTaskID)
			builder.WriteString("\n")
		}
	}
}

func appendBlockingFindingResolution(builder *strings.Builder, finding taskstate.ReviewFinding) {
	if finding.TargetedByRunAttempt > 0 {
		builder.WriteString("    - Fixed by run attempt ")
		builder.WriteString(strconv.Itoa(finding.TargetedByRunAttempt))
		builder.WriteString("\n")
		return
	}
	builder.WriteString("    - No targeted fix run recorded.\n")
}

func reviewFindingLabel(finding taskstate.ReviewFinding) string {
	switch finding.Type {
	case taskstate.FindingTypeBlocking:
		if strings.TrimSpace(finding.Waiver) != "" {
			return "Blocking (waived)"
		}
		return "Blocking"
	case taskstate.FindingTypeAdvisory:
		return "Advisory"
	case taskstate.FindingTypeSeparateTask:
		return "Separate task"
	default:
		return "Finding"
	}
}

func appendFixRuns(builder *strings.Builder, review taskstate.ReviewAttempt, runs []taskstate.RunAttempt) {
	for _, run := range reviewFixRuns(review, runs) {
		if run.Completion == nil {
			continue
		}
		builder.WriteString("\n  **Fix run attempt ")
		builder.WriteString(strconv.Itoa(run.Attempt))
		builder.WriteString("**\n")
		builder.WriteString("  - Summary: `")
		builder.WriteString(singleLine(run.Completion.Summary))
		builder.WriteString("`\n")
		builder.WriteString("  - Description: ")
		builder.WriteString(strings.TrimSpace(run.Completion.Description))
		builder.WriteString("\n")
	}
}

func reviewFixRuns(review taskstate.ReviewAttempt, runs []taskstate.RunAttempt) []taskstate.RunAttempt {
	attempts := make([]int, 0)
	seen := map[int]bool{}
	for _, finding := range review.Findings {
		attempt := finding.TargetedByRunAttempt
		if attempt <= 0 || seen[attempt] {
			continue
		}
		seen[attempt] = true
		attempts = append(attempts, attempt)
	}

	result := make([]taskstate.RunAttempt, 0, len(attempts))
	for _, attempt := range attempts {
		for _, run := range runs {
			if run.Attempt == attempt {
				result = append(result, run)
				break
			}
		}
	}
	return result
}

func singleLine(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) == 0 {
		return ""
	}
	return strings.Join(fields, " ")
}
