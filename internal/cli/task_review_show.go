package cli

import (
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/spf13/cobra"
)

func newTaskReviewShowCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <task-id> [review-attempt] [finding-number]",
		Short: "Inspect persisted review history, an attempt, or an authoritative finding",
		Long: "This is the inspection surface for review state, including blocking/advisory/separate-task findings, " +
			"autonomous budget exhaustion, and interrupted automated blocker decisions. It never advances workflow state. " +
			"With only a task ID, show provides concise cross-attempt history. Add a review attempt number for its detailed view, " +
			"then a finding number for one authoritative finding.",
		Args: reviewShowArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskReviewShow(command, opts, args)
		},
	}
	return cmd
}

type reviewShowScope struct {
	reviewAttempt int
	findingNumber int
}

func reviewShowArgs(_ *cobra.Command, args []string) error {
	_, err := parseReviewShowScope(args)
	return err
}

func parseReviewShowScope(args []string) (reviewShowScope, error) {
	if len(args) < 1 || len(args) > 3 {
		return reviewShowScope{}, fmt.Errorf("accepts a task ID, optional positive review attempt, and optional positive finding number")
	}
	scope := reviewShowScope{}
	if len(args) == 1 {
		return scope, nil
	}
	attempt, err := positiveReviewShowNumber("review attempt", args[1])
	if err != nil {
		return reviewShowScope{}, err
	}
	scope.reviewAttempt = attempt
	if len(args) == 2 {
		return scope, nil
	}
	finding, err := positiveReviewShowNumber("finding number", args[2])
	if err != nil {
		return reviewShowScope{}, err
	}
	scope.findingNumber = finding
	return scope, nil
}

func positiveReviewShowNumber(label string, raw string) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", label, raw)
	}
	return value, nil
}

func runTaskReviewShow(command *cobra.Command, opts *rootOptions, args []string) error {
	taskID := args[0]
	scope, err := parseReviewShowScope(args)
	if err != nil {
		return err
	}
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "task_review_show"),
	)
	logger.DebugContext(command.Context(), "loading registered repos for task review show")

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	resolvedCtx, err := resolveTaskContextWithScope(command, deps, "task review show", taskID, false)
	if err != nil {
		return err
	}
	taskState, err := deps.taskStateStore.Load(
		resolvedCtx.Resolved.Source.Repository.ID,
		resolvedCtx.Resolved.TaskID,
	)
	if err != nil {
		return fmt.Errorf(
			"task review show %s: load local task-state for repo %s: %w",
			resolvedCtx.Resolved.TaskID,
			resolvedCtx.Resolved.Source.Repository.ID,
			err,
		)
	}

	logger.DebugContext(
		command.Context(),
		"loaded review state",
		slog.String("repo_id", resolvedCtx.Resolved.Source.Repository.ID),
		slog.String("task_id", resolvedCtx.Resolved.TaskID),
		slog.Int("review_count", len(taskState.Reviews)),
	)
	return renderTaskReviewShow(
		command.OutOrStdout(),
		resolvedCtx.Resolved.Source.Repository.ID,
		resolvedCtx.Resolved.TaskID,
		taskState,
		scope,
	)
}

func renderTaskReviewShow(
	output io.Writer,
	repoID string,
	taskID string,
	taskState taskstate.TaskState,
	scope reviewShowScope,
) error {
	if _, err := fmt.Fprintf(output, "Review state for %s (repo %s)\n", taskID, repoID); err != nil {
		return err
	}

	latest, ok := taskstate.LatestReview(taskState)
	if !ok {
		if scope.reviewAttempt > 0 {
			return fmt.Errorf("review attempt %d was not found for task %s: no review attempts are recorded", scope.reviewAttempt, taskID)
		}
		if _, err := fmt.Fprintf(output, "\nNo review attempts recorded for %s.\n", taskID); err != nil {
			return err
		}
		_, err := fmt.Fprintf(output, "Next step: run `orpheus task run %s` after task work is ready.\n", taskID)
		return err
	}

	if scope.reviewAttempt == 0 {
		if err := renderReviewHistory(output, taskState); err != nil {
			return err
		}
		return renderReviewNextStep(output, taskID, taskState, latest)
	}

	review, ok := reviewAttemptByNumber(taskState, scope.reviewAttempt)
	if !ok {
		return fmt.Errorf("review attempt %d was not found for task %s", scope.reviewAttempt, taskID)
	}
	if scope.findingNumber == 0 {
		if err := renderReviewAttemptDetail(output, taskState, review); err != nil {
			return err
		}
		if review.Attempt == latest.Attempt {
			return renderReviewNextStep(output, taskID, taskState, latest)
		}
		return nil
	}
	return renderAuthoritativeFinding(output, taskState, review, scope.findingNumber)
}

func renderReviewHistory(output io.Writer, taskState taskstate.TaskState) error {
	if _, err := fmt.Fprintln(output, "\nAuthoritative review history:"); err != nil {
		return err
	}
	for _, review := range taskState.Reviews {
		if _, err := fmt.Fprintf(output, "  Attempt %d: %s (%d authoritative finding(s))\n", review.Attempt, formatReviewValue(string(review.Status)), authoritativeFindingCount(review)); err != nil {
			return err
		}
		for index, finding := range review.Findings {
			if taskstate.InterruptedPrimaryReviewFinding(review, finding) {
				continue
			}
			step := compactReviewText(finding.Step)
			if step == "" {
				step = "-"
			}
			if _, err := fmt.Fprintf(output, "    - %d/%d · %s · %s · %s · %s\n", review.Attempt, index+1, step, compactReviewText(string(finding.Type)), compactReviewText(compactReviewFindingDisposition(taskState, finding)), compactReviewText(finding.Title)); err != nil {
				return err
			}
		}
	}
	_, err := fmt.Fprintln(output, "\nInspect an attempt with `orpheus task review show <task-id> <review-attempt>` or an authoritative finding with `orpheus task review show <task-id> <review-attempt> <finding-number>`.")
	return err
}

func authoritativeFindingCount(review taskstate.ReviewAttempt) int {
	count := 0
	for _, finding := range review.Findings {
		if !taskstate.InterruptedPrimaryReviewFinding(review, finding) {
			count++
		}
	}
	return count
}

func compactReviewText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func reviewAttemptByNumber(taskState taskstate.TaskState, attempt int) (taskstate.ReviewAttempt, bool) {
	for _, review := range taskState.Reviews {
		if review.Attempt == attempt {
			return review, true
		}
	}
	return taskstate.ReviewAttempt{}, false
}

func renderReviewAttemptDetail(output io.Writer, taskState taskstate.TaskState, review taskstate.ReviewAttempt) error {
	if _, err := fmt.Fprintf(output, "\nAuthoritative review attempt %d:\n", review.Attempt); err != nil {
		return err
	}
	rows := []string{
		fmt.Sprintf("  Attempt: %d", review.Attempt),
		fmt.Sprintf("  Status: %s", formatReviewValue(string(review.Status))),
		fmt.Sprintf("  Pipeline: %s", formatReviewValue(review.Pipeline)),
		fmt.Sprintf("  Current step: %s", formatReviewValue(review.Step)),
	}
	if review.AutonomousBudgetExhausted {
		rows = append(rows, "  Autonomous review: attempt budget exhausted")
	}
	if review.AutomatedBlockerDecisionInterrupted {
		rows = append(rows, "  Automated blocker decisions: interrupted")
	}
	for _, row := range rows {
		if _, err := fmt.Fprintln(output, row); err != nil {
			return err
		}
	}

	if err := renderReviewSteps(output, review.Steps); err != nil {
		return err
	}
	if err := renderReviewFindings(output, taskState, review); err != nil {
		return err
	}
	if err := renderReviewFollowUpRunsForAttempt(output, taskState, review.Attempt); err != nil {
		return err
	}
	return renderCreatedReviewFollowUpsForAttempt(output, taskState, review.Attempt)
}

func renderReviewSteps(output io.Writer, steps []taskstate.ReviewStep) error {
	if _, err := fmt.Fprintln(output, "\nSteps:"); err != nil {
		return err
	}
	if len(steps) == 0 {
		_, err := fmt.Fprintln(output, "  (none recorded)")
		return err
	}
	for _, step := range steps {
		line := fmt.Sprintf("  - %s", formatReviewValue(step.Name))
		if strings.TrimSpace(step.Kind) != "" {
			line += fmt.Sprintf(" (%s)", step.Kind)
		}
		if step.ExitCode != nil {
			line += fmt.Sprintf(", exit code %d", *step.ExitCode)
		}
		if _, err := fmt.Fprintln(output, line); err != nil {
			return err
		}
		if err := renderReviewExecution(output, "Primary", step.Execution); err != nil {
			return err
		}
		if step.Comparison != nil {
			if err := renderReviewComparison(output, step.Comparison); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderReviewComparison(output io.Writer, comparison *taskstate.ReviewComparison) error {
	if comparison == nil {
		return nil
	}
	if err := renderReviewExecution(output, "Alternate", comparison.AlternateExecution); err != nil {
		return err
	}
	if strings.TrimSpace(comparison.Failure) != "" {
		if _, err := fmt.Fprintf(output, "    Alternate failure: %s\n", comparison.Failure); err != nil {
			return err
		}
	}
	if comparison.InputInterrupted {
		if _, err := fmt.Fprintln(output, "    Alternate comparison: input interrupted (fresh review required)"); err != nil {
			return err
		}
	}
	if len(comparison.AlternateFindings) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(output, "    Raw alternate findings:"); err != nil {
		return err
	}
	for index, alternate := range comparison.AlternateFindings {
		if err := renderRawAlternateFinding(output, index, alternate); err != nil {
			return err
		}
	}
	return nil
}

func renderReviewExecution(output io.Writer, label string, execution *taskstate.AgentExecution) error {
	if execution == nil {
		return nil
	}
	if _, err := fmt.Fprintf(output, "    %s execution:\n      Profile: %s\n      Model: %s\n      Harness: %s\n      Thinking: %s\n      Status: %s\n      Agent: %s\n      Session name: %s\n      Command: %s\n      Args: %s\n      Supervisor PID: %s\n      Child PID: %s\n      Started: %s\n      Finished: %s\n      Duration: %dms\n", label, formatReviewValue(execution.Profile), formatReviewValue(execution.Model), formatReviewValue(execution.Harness), formatReviewValue(execution.Thinking), execution.Status, formatReviewValue(execution.Agent), formatReviewValue(execution.SessionName), formatReviewValue(execution.Command), formatReviewValue(strings.Join(execution.Args, " ")), formatExecutionPID(execution.SupervisorPID), formatExecutionPID(execution.ChildPID), execution.StartedAt.UTC().Format(time.RFC3339), formatExecutionFinishedAt(execution), execution.DurationMillis); err != nil {
		return err
	}
	if execution.InterruptionReason != "" {
		if _, err := fmt.Fprintf(output, "      Interrupted: %s (trigger: %s)\n", execution.InterruptionReason, formatReviewValue(execution.InterruptionTrigger)); err != nil {
			return err
		}
	}
	if execution.Session != nil {
		if _, err := fmt.Fprintf(output, "      Session: id=%s log=%s\n", formatReviewValue(execution.Session.ID), formatReviewValue(execution.Session.LogPath)); err != nil {
			return err
		}
	}
	if execution.Usage != nil {
		if _, err := fmt.Fprintf(output, "      Usage: input=%d cached_input=%d output=%d reasoning_output=%d total=%d\n", execution.Usage.InputTokens, execution.Usage.CachedInputTokens, execution.Usage.OutputTokens, execution.Usage.ReasoningOutputTokens, execution.Usage.TotalTokens); err != nil {
			return err
		}
	}
	if execution.UsageCost != nil {
		if _, err := fmt.Fprintf(output, "      Usage cost: %d micro-USD (%s; %s; %s)\n", execution.UsageCost.AmountMicroUSD, formatReviewValue(execution.UsageCost.Kind), formatReviewValue(execution.UsageCost.Currency), formatReviewValue(execution.UsageCost.Source)); err != nil {
			return err
		}
	}
	if !execution.UsageCapture.IsZero() {
		if _, err := fmt.Fprintf(output, "      Usage capture: %s (%s; candidates=%d)\n", execution.UsageCapture.Status, formatReviewValue(execution.UsageCapture.Reason), execution.UsageCapture.CandidateCount); err != nil {
			return err
		}
	}
	return nil
}

func formatExecutionPID(pid int) string {
	if pid <= 0 {
		return "-"
	}
	return fmt.Sprintf("%d", pid)
}

func formatExecutionFinishedAt(execution *taskstate.AgentExecution) string {
	if execution.FinishedAt == nil {
		return "-"
	}
	return execution.FinishedAt.UTC().Format(time.RFC3339)
}

func renderRawAlternateFinding(output io.Writer, index int, alternate taskstate.AlternateReviewFinding) error {
	finding := alternate.Finding
	lines := []string{
		fmt.Sprintf("      Alternate finding %d [%s]:", index+1, formatAlternateClassification(alternate)),
		fmt.Sprintf("        Type: %s", formatReviewValue(string(finding.Type))),
		fmt.Sprintf("        Title: %s", formatReviewValue(finding.Title)),
		fmt.Sprintf("        Description: %s", formatReviewValue(finding.Description)),
	}
	if finding.SuggestedAction != "" {
		lines = append(lines, "        Suggested action: "+finding.SuggestedAction)
	}
	if !finding.TaskProposal.IsZero() {
		lines = append(lines, "        Task title: "+finding.TaskProposal.Title, "        Task description: "+finding.TaskProposal.Description, "        Task acceptance criteria: "+finding.TaskProposal.AcceptanceCriteria)
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(output, line); err != nil {
			return err
		}
	}
	return nil
}

func formatAlternateClassification(alternate taskstate.AlternateReviewFinding) string {
	classification := formatReviewValue(string(alternate.Classification))
	if alternate.Classification == taskstate.AlternateFindingDuplicate {
		return classification + fmt.Sprintf(" of primary finding %d", alternate.DuplicateOf+1)
	}
	return classification
}

func renderReviewFindings(output io.Writer, taskState taskstate.TaskState, review taskstate.ReviewAttempt) error {
	if _, err := fmt.Fprintln(output, "\nFindings by step:"); err != nil {
		return err
	}
	if len(review.Findings) == 0 {
		_, err := fmt.Fprintln(output, "  (none recorded)")
		return err
	}
	auditOnly := taskstate.PrimaryReviewExecutionInterrupted(review)
	if auditOnly {
		if _, err := fmt.Fprintln(output, "  Findings from the interrupted primary reviewer are retained for audit only and do not drive follow-up work."); err != nil {
			return err
		}
	}

	for _, group := range groupReviewFindingsByStep(review.Findings) {
		if _, err := fmt.Fprintf(output, "  Step: %s\n", group.step); err != nil {
			return err
		}
		for _, finding := range group.findings {
			if err := renderReviewFinding(output, taskState, finding, taskstate.InterruptedPrimaryReviewFinding(review, finding.finding)); err != nil {
				return err
			}
		}
	}
	return nil
}

type reviewFindingGroup struct {
	step     string
	findings []indexedReviewFinding
}

type indexedReviewFinding struct {
	index   int
	finding taskstate.ReviewFinding
}

func groupReviewFindingsByStep(findings []taskstate.ReviewFinding) []reviewFindingGroup {
	groups := make([]reviewFindingGroup, 0)
	indexByStep := map[string]int{}
	for index, finding := range findings {
		step := strings.TrimSpace(finding.Step)
		if step == "" {
			step = "(unspecified)"
		}
		groupIndex, ok := indexByStep[step]
		if !ok {
			groupIndex = len(groups)
			indexByStep[step] = groupIndex
			groups = append(groups, reviewFindingGroup{step: step, findings: []indexedReviewFinding{}})
		}
		groups[groupIndex].findings = append(groups[groupIndex].findings, indexedReviewFinding{
			index:   index,
			finding: finding,
		})
	}
	return groups
}

func renderReviewFinding(output io.Writer, taskState taskstate.TaskState, indexed indexedReviewFinding, auditOnly bool) error {
	finding := indexed.finding
	resolution := reviewFindingResolution(taskState, finding)
	if auditOnly {
		resolution = "audit-only (interrupted primary reviewer)"
	}
	lines := []string{
		fmt.Sprintf("    Finding %d:", indexed.index+1),
		fmt.Sprintf("      Type: %s", formatReviewValue(string(finding.Type))),
		fmt.Sprintf("      Title: %s", formatReviewValue(finding.Title)),
		fmt.Sprintf("      Description: %s", formatReviewValue(finding.Description)),
		fmt.Sprintf("      Resolution: %s", resolution),
	}
	if strings.TrimSpace(finding.Reviewer) != "" {
		lines = append(lines, fmt.Sprintf("      Reviewer: %s", finding.Reviewer))
	}
	if strings.TrimSpace(finding.SuggestedAction) != "" {
		lines = append(lines, fmt.Sprintf("      Suggested action: %s", finding.SuggestedAction))
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(output, line); err != nil {
			return err
		}
	}
	return nil
}

func reviewFindingResolution(taskState taskstate.TaskState, finding taskstate.ReviewFinding) string {
	switch taskstate.ResolveReviewFindingInState(taskState, finding) {
	case taskstate.ReviewFindingResolutionAddressedManually:
		return "addressed manually: " + strings.TrimSpace(finding.AddressedManually)
	case taskstate.ReviewFindingResolutionWaived:
		return "waived: " + strings.TrimSpace(finding.Waiver)
	case taskstate.ReviewFindingResolutionDowngraded:
		return "downgraded to advisory: " + strings.TrimSpace(finding.DowngradeReason)
	case taskstate.ReviewFindingResolutionCreatedTask:
		return "converted/created task " + strings.TrimSpace(finding.CreatedTaskID)
	case taskstate.ReviewFindingResolutionTargetedByRun:
		return fmt.Sprintf("targeted by follow-up run attempt %d", finding.TargetedByRunAttempt)
	case taskstate.ReviewFindingResolutionOpen:
		return "open"
	case taskstate.ReviewFindingResolutionSeparateTask:
		return "open separate-task proposal"
	default:
		return "advisory/non-blocking"
	}
}

func compactReviewFindingDisposition(taskState taskstate.TaskState, finding taskstate.ReviewFinding) string {
	switch taskstate.ResolveReviewFindingInState(taskState, finding) {
	case taskstate.ReviewFindingResolutionAddressedManually:
		return "addressed manually"
	case taskstate.ReviewFindingResolutionWaived:
		return "waived"
	case taskstate.ReviewFindingResolutionDowngraded:
		return "downgraded to advisory"
	case taskstate.ReviewFindingResolutionCreatedTask:
		return "created task " + formatReviewValue(finding.CreatedTaskID)
	case taskstate.ReviewFindingResolutionTargetedByRun:
		return followUpRunDisposition(taskState, finding.TargetedByRunAttempt)
	case taskstate.ReviewFindingResolutionOpen:
		if finding.TargetedByRunAttempt > 0 {
			return followUpRunDisposition(taskState, finding.TargetedByRunAttempt)
		}
		return "open"
	case taskstate.ReviewFindingResolutionSeparateTask:
		return "separate-task proposal"
	default:
		return "advisory"
	}
}

func followUpRunDisposition(taskState taskstate.TaskState, attempt int) string {
	for _, run := range taskState.Runs {
		if run.Attempt == attempt {
			return fmt.Sprintf("follow-up run %d %s", attempt, run.Status)
		}
	}
	return fmt.Sprintf("follow-up run %d", attempt)
}

func renderAuthoritativeFinding(output io.Writer, taskState taskstate.TaskState, review taskstate.ReviewAttempt, number int) error {
	if number <= 0 || number > len(review.Findings) {
		return fmt.Errorf("finding number %d is out of range for review attempt %d (has %d persisted finding(s))", number, review.Attempt, len(review.Findings))
	}
	finding := review.Findings[number-1]
	auditOnly := taskstate.InterruptedPrimaryReviewFinding(review, finding)
	label := "Authoritative"
	disposition := reviewFindingResolution(taskState, finding)
	if auditOnly {
		label = "Audit-only"
		disposition = "audit-only (interrupted primary reviewer)"
	}
	if _, err := fmt.Fprintf(output, "\n%s finding %d/%d:\n", label, review.Attempt, number); err != nil {
		return err
	}
	lines := []string{
		"  Step: " + formatReviewValue(finding.Step),
		"  Type: " + formatReviewValue(string(finding.Type)),
		"  Title: " + formatReviewValue(finding.Title),
		"  Description: " + formatReviewValue(finding.Description),
		"  Disposition: " + disposition,
	}
	if auditOnly {
		lines = append(lines, "  Audit-only: interrupted primary reviewer")
	}
	if strings.TrimSpace(finding.Reviewer) != "" {
		lines = append(lines, "  Reviewer: "+finding.Reviewer)
	}
	if strings.TrimSpace(finding.SuggestedAction) != "" {
		lines = append(lines, "  Suggested action: "+finding.SuggestedAction)
	}
	if !finding.TaskProposal.IsZero() {
		lines = append(lines,
			"  Proposed task title: "+finding.TaskProposal.Title,
			"  Proposed task description: "+finding.TaskProposal.Description,
			"  Proposed task acceptance criteria: "+finding.TaskProposal.AcceptanceCriteria,
		)
	}
	if strings.TrimSpace(finding.CreatedTaskID) != "" {
		lines = append(lines, "  Created follow-up task: "+finding.CreatedTaskID)
	}
	if finding.CreatedTaskAt != nil {
		lines = append(lines, "  Created follow-up task at: "+finding.CreatedTaskAt.UTC().Format(time.RFC3339))
	}
	if finding.TargetedByRunAttempt > 0 {
		lines = append(lines, fmt.Sprintf("  Targeted by follow-up run attempt: %d", finding.TargetedByRunAttempt))
	}
	for _, line := range lines {
		if _, err := fmt.Fprintln(output, line); err != nil {
			return err
		}
	}
	return renderReviewFollowUpRunsForFinding(output, taskState, review.Attempt, number-1)
}

func renderReviewFollowUpRunsForAttempt(output io.Writer, taskState taskstate.TaskState, reviewAttempt int) error {
	if _, err := fmt.Fprintln(output, "\nFollow-up runs:"); err != nil {
		return err
	}
	found := false
	for _, run := range taskState.Runs {
		if run.ReviewFollowUp == nil || run.ReviewFollowUp.ReviewAttempt != reviewAttempt {
			continue
		}
		found = true
		if _, err := fmt.Fprintf(output, "  - Run attempt %d: %s (findings %s)\n", run.Attempt, formatReviewValue(string(run.Status)), formatReviewFindingIndexes(run.ReviewFollowUp.FindingIndexes)); err != nil {
			return err
		}
	}
	if !found {
		_, err := fmt.Fprintln(output, "  (none recorded)")
		return err
	}
	return nil
}

func renderReviewFollowUpRunsForFinding(output io.Writer, taskState taskstate.TaskState, reviewAttempt, findingIndex int) error {
	if _, err := fmt.Fprintln(output, "\nAssociated follow-up runs:"); err != nil {
		return err
	}
	found := false
	for _, run := range taskState.Runs {
		if run.ReviewFollowUp == nil || run.ReviewFollowUp.ReviewAttempt != reviewAttempt || !containsFindingIndex(run.ReviewFollowUp.FindingIndexes, findingIndex) {
			continue
		}
		found = true
		completion := "no completion"
		if run.Completion != nil {
			completion = "completion recorded"
		}
		if _, err := fmt.Fprintf(output, "  - Run attempt %d: %s (%s)\n", run.Attempt, formatReviewValue(string(run.Status)), completion); err != nil {
			return err
		}
	}
	if !found {
		_, err := fmt.Fprintln(output, "  (none recorded)")
		return err
	}
	return nil
}

func containsFindingIndex(indexes []int, target int) bool {
	for _, index := range indexes {
		if index == target {
			return true
		}
	}
	return false
}

func formatReviewFindingIndexes(indexes []int) string {
	if len(indexes) == 0 {
		return "-"
	}
	formatted := make([]string, 0, len(indexes))
	for _, index := range indexes {
		formatted = append(formatted, fmt.Sprintf("%d", index+1))
	}
	return strings.Join(formatted, ", ")
}

func renderCreatedReviewFollowUpsForAttempt(output io.Writer, taskState taskstate.TaskState, reviewAttempt int) error {
	if _, err := fmt.Fprintln(output, "\nCreated follow-up Beads:"); err != nil {
		return err
	}
	found := false
	for _, followUp := range createdReviewFollowUps(taskState) {
		if followUp.reviewAttempt != reviewAttempt {
			continue
		}
		found = true
		if _, err := fmt.Fprintf(output, "  - %s (finding %d", followUp.createdTaskID, followUp.findingIndex+1); err != nil {
			return err
		}
		if strings.TrimSpace(followUp.step) != "" {
			if _, err := fmt.Fprintf(output, ", step %s", followUp.step); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprint(output, ")"); err != nil {
			return err
		}
		if strings.TrimSpace(followUp.title) != "" {
			if _, err := fmt.Fprintf(output, ": %s", followUp.title); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(output); err != nil {
			return err
		}
	}
	if !found {
		_, err := fmt.Fprintln(output, "  (none recorded)")
		return err
	}
	return nil
}

type createdReviewFollowUp struct {
	createdTaskID string
	reviewAttempt int
	findingIndex  int
	step          string
	title         string
}

func createdReviewFollowUps(taskState taskstate.TaskState) []createdReviewFollowUp {
	followUps := make([]createdReviewFollowUp, 0)
	for _, review := range taskState.Reviews {
		for index, finding := range review.Findings {
			if strings.TrimSpace(finding.CreatedTaskID) == "" {
				continue
			}
			followUps = append(followUps, createdReviewFollowUp{
				createdTaskID: strings.TrimSpace(finding.CreatedTaskID),
				reviewAttempt: review.Attempt,
				findingIndex:  index,
				step:          strings.TrimSpace(finding.Step),
				title:         strings.TrimSpace(finding.Title),
			})
		}
	}
	sort.SliceStable(followUps, func(i, j int) bool {
		if followUps[i].reviewAttempt != followUps[j].reviewAttempt {
			return followUps[i].reviewAttempt < followUps[j].reviewAttempt
		}
		return followUps[i].findingIndex < followUps[j].findingIndex
	})
	return followUps
}

//nolint:funlen // Review lifecycle guidance reads clearest as a single status switch.
func renderReviewNextStep(output io.Writer, taskID string, taskState taskstate.TaskState, review taskstate.ReviewAttempt) error {
	switch review.Status {
	case taskstate.ReviewStatusWaitingForManual:
		_, err := fmt.Fprintf(
			output,
			"\nNext step: run `orpheus task run %s` to resume manual step %s.\n",
			taskID,
			formatReviewValue(review.Step),
		)
		return err
	case taskstate.ReviewStatusBlocked:
		if taskstate.ReviewComparisonInputInterrupted(review) {
			_, err := fmt.Fprintf(output, "\nNext step: alternate comparison input was interrupted; run `orpheus task run %s` to start a fresh review.\n", taskID)
			return err
		}
		if review.AutomatedBlockerDecisionInterrupted {
			_, err := fmt.Fprintf(
				output,
				"\nNext step: automated blocker decisions were interrupted; run `orpheus task run %s` to start a fresh review.\n",
				taskID,
			)
			return err
		}
		if taskstate.HasUnkeptAutomatedBlockingFindingsInState(taskState, review) {
			_, err := fmt.Fprintf(
				output,
				"\nNext step: automated blockers need operator decisions; run `orpheus task run %s` to start a fresh review.\n",
				taskID,
			)
			return err
		}
		if review.AutonomousBudgetExhausted {
			_, err := fmt.Fprintf(
				output,
				"\nNext step: autonomous review attempts are exhausted; run `orpheus task run %s` to continue with a fresh budget.\n",
				taskID,
			)
			return err
		}
		if taskstate.ReviewHasOpenBlockersInState(taskState, review) {
			if taskstate.HasFailedReviewFollowUpTargets(taskState, review) {
				_, err := fmt.Fprintf(
					output,
					"\nNext step: retry `orpheus task run %s` to address open blocking findings; it starts a fresh review after repair.\n",
					taskID,
				)
				return err
			}
			_, err := fmt.Fprintf(
				output,
				"\nNext step: run `orpheus task run %s` to address open blocking findings; it starts a fresh review after repair.\n",
				taskID,
			)
			return err
		}
		_, err := fmt.Fprintf(output, "\nNext step: run `orpheus task run %s` after targeted follow-up work completes.\n", taskID)
		return err
	case taskstate.ReviewStatusFailed, taskstate.ReviewStatusAborted:
		_, err := fmt.Fprintf(output, "\nNext step: run `orpheus task run %s` when ready.\n", taskID)
		return err
	default:
		return nil
	}
}

func formatReviewValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "-"
	}
	return value
}
