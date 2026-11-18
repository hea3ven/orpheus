package delivery

import (
	"errors"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"strconv"
	"strings"
)

// PullRequestContent is the generated title/body for a pull request.
type PullRequestContent struct {
	Title string
	Body  string
}

// PublicationOptions controls generated pull-request publication content.
type PublicationOptions struct {
	TitleTemplate        string
	IncludeReviewProcess bool
}

// ResolvePublicationOptions applies global and repository publication policy.
func ResolvePublicationOptions(paths state.Paths, repo task.Repository) (PublicationOptions, error) {
	reviewConfig, err := review.LoadConfig(paths)
	if err != nil {
		return PublicationOptions{}, err
	}
	includeReviewProcess := reviewConfig.IncludePRReviewProcess
	if repo.IncludePRReviewProcess != nil {
		includeReviewProcess = *repo.IncludePRReviewProcess
	}
	return PublicationOptions{
		TitleTemplate:        repo.TitleTemplate,
		IncludeReviewProcess: includeReviewProcess,
	}, nil
}

// BuildPublicationPullRequestContent derives the PR title from the workflow's
// publication subject. The commit body never supplies the PR description.
func BuildPublicationPullRequestContent(titleTemplate string, taskItem task.Task, authored *work.PublicationContent) (PullRequestContent, error) {
	if strings.TrimSpace(taskItem.ID) == "" {
		return PullRequestContent{}, errors.New("task id is required")
	}
	if authored == nil {
		return PullRequestContent{}, errors.New("publication content is required")
	}
	renderedTitle, err := publication.RenderTitle(titleTemplate, authored.CommitSubject, taskItem.ExternalRef)
	if err != nil {
		return PullRequestContent{}, err
	}
	title := singleLine(renderedTitle)
	if title == "" {
		return PullRequestContent{}, errors.New("publication commit subject is required")
	}
	if strings.TrimSpace(authored.PRDescription) == "" {
		return PullRequestContent{}, errors.New("PR description is required")
	}
	return PullRequestContent{Title: title, Body: authored.PRDescription}, nil
}

// BuildPublicationPullRequestContentFromState returns PR text from the
// workflow publication content plus any recorded review process.
func BuildPublicationPullRequestContentFromState(
	titleTemplate string,
	taskItem task.Task,
	state taskstate.TaskState,
) (PullRequestContent, error) {
	return BuildPublicationPullRequestContentFromStateWithOptions(PublicationOptions{
		TitleTemplate:        titleTemplate,
		IncludeReviewProcess: true,
	}, taskItem, state)
}

// BuildPublicationPullRequestContentFromStateWithOptions returns PR text from
// workflow publication content and optional review-process history.
func BuildPublicationPullRequestContentFromStateWithOptions(
	options PublicationOptions,
	taskItem task.Task,
	state taskstate.TaskState,
) (PullRequestContent, error) {
	if state.Work == nil || state.Work.ActiveWorkflow() == nil {
		return PullRequestContent{}, errors.New("workflow publication content is required")
	}
	content, err := BuildPublicationPullRequestContent(options.TitleTemplate, taskItem, state.Work.ActiveWorkflow().Publication)
	if err != nil {
		return PullRequestContent{}, err
	}
	if options.IncludeReviewProcess {
		content.Body = appendReviewProcess(content.Body, state)
	}
	return content, nil
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
		if taskstate.IsOpenBlockingReviewFinding(finding) {
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

	switch taskstate.ResolveReviewFinding(finding) {
	case taskstate.ReviewFindingResolutionAddressedManually:
		builder.WriteString("    - Addressed manually.\n")
		return
	case taskstate.ReviewFindingResolutionWaived:
		builder.WriteString("    - Waived.\n")
		return
	case taskstate.ReviewFindingResolutionDowngraded:
		builder.WriteString("    - Downgraded to advisory.\n")
		return
	case taskstate.ReviewFindingResolutionTargetedByRun:
		appendBlockingFindingResolution(builder, finding)
		return
	}

	if taskstate.IsOpenBlockingReviewFinding(finding) {
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
	switch taskstate.ResolveReviewFinding(finding) {
	case taskstate.ReviewFindingResolutionTargetedByRun:
		builder.WriteString("    - Fixed by run attempt ")
		builder.WriteString(strconv.Itoa(finding.TargetedByRunAttempt))
		builder.WriteString("\n")
		return
	case taskstate.ReviewFindingResolutionOpen:
		builder.WriteString("    - No targeted fix run recorded.\n")
	}
}

func reviewFindingLabel(finding taskstate.ReviewFinding) string {
	switch taskstate.ResolveReviewFinding(finding) {
	case taskstate.ReviewFindingResolutionAddressedManually:
		return "Blocking (addressed manually)"
	case taskstate.ReviewFindingResolutionWaived:
		return "Blocking (waived)"
	case taskstate.ReviewFindingResolutionDowngraded:
		return "Advisory (downgraded)"
	}

	switch finding.Type {
	case taskstate.FindingTypeBlocking:
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
		builder.WriteString(singleLine(run.Completion.CommitSubject))
		builder.WriteString("`\n")
		builder.WriteString("  - Description: ")
		builder.WriteString(strings.TrimSpace(run.Completion.CommitBody))
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
