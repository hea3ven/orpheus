package cli

import (
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/spf13/cobra"
)

func newTaskReviewShowCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "show <task-id>",
		Short: "Show persisted review findings and follow-up tasks for a task",
		Long: "Show persisted review findings and follow-up tasks for a task.\n\n" +
			"This is the inspection surface for review state. It shows the latest " +
			"authoritative review attempt, executed steps, blocking/advisory/separate-task " +
			"findings, autonomous budget exhaustion, interrupted automated blocker " +
			"decisions, created follow-up Beads, and the next command, such as task run " +
			"for any workflow advancement after inspection.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskReviewShow(command, opts, args[0])
		},
	}
	return cmd
}

func runTaskReviewShow(command *cobra.Command, opts *rootOptions, taskID string) error {
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
	)
}

func renderTaskReviewShow(
	output io.Writer,
	repoID string,
	taskID string,
	taskState taskstate.TaskState,
) error {
	if _, err := fmt.Fprintf(output, "Review state for %s (repo %s)\n", taskID, repoID); err != nil {
		return err
	}

	latest, ok := taskstate.LatestReview(taskState)
	if !ok {
		if _, err := fmt.Fprintf(output, "\nNo review attempts recorded for %s.\n", taskID); err != nil {
			return err
		}
		_, err := fmt.Fprintf(output, "Next step: run `orpheus task run %s` after task work is ready.\n", taskID)
		return err
	}

	if err := renderLatestReviewAttempt(output, taskState, latest); err != nil {
		return err
	}
	if err := renderReviewFollowUpRuns(output, taskState); err != nil {
		return err
	}
	if err := renderCreatedReviewFollowUps(output, taskState); err != nil {
		return err
	}
	return renderReviewNextStep(output, taskID, taskState, latest)
}

func renderLatestReviewAttempt(output io.Writer, taskState taskstate.TaskState, review taskstate.ReviewAttempt) error {
	if _, err := fmt.Fprintln(output, "\nLatest authoritative review attempt:"); err != nil {
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
	return renderReviewFindings(output, taskState, review)
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
		if step.Comparison != nil {
			if err := renderReviewExecution(output, "Primary", step.Execution); err != nil {
				return err
			}
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
	if _, err := fmt.Fprintf(output, "    %s execution:\n      Profile: %s\n      Model: %s\n      Harness: %s\n      Thinking: %s\n      Status: %s\n      Agent: %s\n      Session name: %s\n      Command: %s\n      Args: %s\n      Started: %s\n      Finished: %s\n      Duration: %dms\n", label, formatReviewValue(execution.Profile), formatReviewValue(execution.Model), formatReviewValue(execution.Harness), formatReviewValue(execution.Thinking), execution.Status, formatReviewValue(execution.Agent), formatReviewValue(execution.SessionName), formatReviewValue(execution.Command), formatReviewValue(strings.Join(execution.Args, " ")), execution.StartedAt.UTC().Format(time.RFC3339), formatExecutionFinishedAt(execution), execution.DurationMillis); err != nil {
		return err
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

	for _, group := range groupReviewFindingsByStep(review.Findings) {
		if _, err := fmt.Fprintf(output, "  Step: %s\n", group.step); err != nil {
			return err
		}
		for _, finding := range group.findings {
			if err := renderReviewFinding(output, taskState, finding); err != nil {
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

func renderReviewFinding(output io.Writer, taskState taskstate.TaskState, indexed indexedReviewFinding) error {
	finding := indexed.finding
	lines := []string{
		fmt.Sprintf("    Finding %d:", indexed.index+1),
		fmt.Sprintf("      Type: %s", formatReviewValue(string(finding.Type))),
		fmt.Sprintf("      Title: %s", formatReviewValue(finding.Title)),
		fmt.Sprintf("      Description: %s", formatReviewValue(finding.Description)),
		fmt.Sprintf("      Resolution: %s", reviewFindingResolution(taskState, finding)),
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

func renderReviewFollowUpRuns(output io.Writer, taskState taskstate.TaskState) error {
	if _, err := fmt.Fprintln(output, "\nFollow-up runs:"); err != nil {
		return err
	}
	found := false
	for _, run := range taskState.Runs {
		if run.ReviewFollowUp == nil {
			continue
		}
		found = true
		if _, err := fmt.Fprintf(
			output,
			"  - Run attempt %d: %s (review attempt %d, findings %s)\n",
			run.Attempt,
			formatReviewValue(string(run.Status)),
			run.ReviewFollowUp.ReviewAttempt,
			formatReviewFindingIndexes(run.ReviewFollowUp.FindingIndexes),
		); err != nil {
			return err
		}
	}
	if !found {
		_, err := fmt.Fprintln(output, "  (none recorded)")
		return err
	}
	return nil
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

func renderCreatedReviewFollowUps(output io.Writer, taskState taskstate.TaskState) error {
	followUps := createdReviewFollowUps(taskState)
	if _, err := fmt.Fprintln(output, "\nCreated follow-up Beads:"); err != nil {
		return err
	}
	if len(followUps) == 0 {
		_, err := fmt.Fprintln(output, "  (none recorded)")
		return err
	}
	for _, followUp := range followUps {
		line := fmt.Sprintf(
			"  - %s (review attempt %d, finding %d",
			followUp.createdTaskID,
			followUp.reviewAttempt,
			followUp.findingIndex+1,
		)
		if strings.TrimSpace(followUp.step) != "" {
			line += ", step " + followUp.step
		}
		line += ")"
		if strings.TrimSpace(followUp.title) != "" {
			line += ": " + followUp.title
		}
		if _, err := fmt.Fprintln(output, line); err != nil {
			return err
		}
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
