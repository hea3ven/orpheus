package agent

import (
	"fmt"
	"os"
	"strings"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

// RenderReviewContext renders backend-neutral instructions for the active review agent.
func RenderReviewContext(ctx ReviewContext) string {
	var builder strings.Builder

	builder.WriteString("# Orpheus Review Agent Context\n\n")
	appendReviewTaskContext(&builder, ctx)
	appendRepositoryContext(&builder, ctx.Repository)
	appendReviewTargetContext(&builder, ctx)
	appendReviewCompletionContext(&builder, ctx.Review)
	if exhaustiveReviewContextEnabled() {
		appendExhaustiveReviewContract(&builder)
	} else {
		appendLegacyReviewContract(&builder)
	}

	return builder.String()
}

func appendReviewTaskContext(builder *strings.Builder, ctx ReviewContext) {
	builder.WriteString("Task:\n")
	appendPromptLine(builder, "- ID", ctx.Task.ID)
	appendPromptLine(builder, "- Title", ctx.Task.Title)
	appendPromptLine(builder, "- External reference", ctx.Task.ExternalRef)
	appendPromptBlock(builder, "- Description", ctx.Task.Description)
	appendPromptBlock(builder, "- Acceptance criteria", ctx.Task.AcceptanceCriteria)
}

func appendReviewTargetContext(builder *strings.Builder, ctx ReviewContext) {
	builder.WriteString("\nReview target:\n")
	appendPromptLine(builder, "- Work Directory", ctx.Target.Path)
	appendPromptLine(builder, "- Current branch", ctx.Target.Branch)
	appendPromptLine(builder, "- Current directory", ctx.Target.CurrentDirectory)
	appendPromptLine(builder, "- Review attempt", fmt.Sprintf("%d", ctx.Review.Attempt))
	appendPromptLine(builder, "- Review step", ctx.Review.Step)
	if strings.TrimSpace(ctx.Review.EnvStep) != "" && ctx.Review.EnvStep != ctx.Review.Step {
		appendPromptLine(builder, "- Informational env review step", ctx.Review.EnvStep)
	}
}

func appendReviewCompletionContext(builder *strings.Builder, review ContextReview) {
	if review.OriginalCompletion != nil && review.LatestFixCompletion != nil {
		appendReviewCompletionBlock(builder, "Original completion", *review.OriginalCompletion)
		appendReviewCompletionBlock(builder, "Latest fix completion", *review.LatestFixCompletion)
		return
	}
	appendReviewCompletionBlock(builder, "Latest completion", review.Completion)
}

func appendReviewCompletionBlock(builder *strings.Builder, label string, completion taskstate.Completion) {
	builder.WriteString("\n")
	builder.WriteString(label)
	builder.WriteString(":\n")
	appendPromptLine(builder, "- Summary", completion.Summary)
	appendPromptBlock(builder, "- Description", completion.Description)
	appendPromptBlock(builder, "- Detailed description", completion.DetailedDescription)
	appendPromptBlock(builder, "- Technical explanation", completion.TechnicalExplanation)
	if strings.TrimSpace(completion.Commit) != "" {
		appendPromptLine(builder, "- Commit", completion.Commit)
	}
}

func exhaustiveReviewContextEnabled() bool {
	return strings.TrimSpace(os.Getenv(envExhaustiveReviewContext)) == "1"
}

func appendLegacyReviewContract(builder *strings.Builder) {
	builder.WriteString("\nReview contract:\n")
	builder.WriteString("- You are reviewing the current working-tree changes for the task above.\n")
	builder.WriteString("- Use Git commands such as `git status --short`, `git diff`, and `git log` as needed.\n")
	builder.WriteString("- Review the complete change set before exiting, even if you find an issue early.\n")
	builder.WriteString("- Do not stop after the first issue; continue reviewing for additional distinct findings.\n")
	builder.WriteString("- This is a strict read-only review step. Do not edit files, stage changes, commit, run formatters that write files, or otherwise mutate the worktree.\n")
	builder.WriteString("- Record each distinct finding with its own `orpheus agent review add` call; there is no `orpheus agent review done` command.\n")
	builder.WriteString("- When multiple findings exist, run `orpheus agent review add` multiple times, once per finding.\n")
	builder.WriteString("- Exit 0 after recording all findings. Exit non-zero only for an operational review failure.\n")

	appendReviewFindingInstructions(builder)
}

func appendExhaustiveReviewContract(builder *strings.Builder) {
	builder.WriteString("\nReview contract:\n")
	builder.WriteString("- You are reviewing the current working-tree changes for the task above.\n")
	builder.WriteString("- This is a strict read-only review step. Do not edit files, stage changes, commit, run formatters that write files, or otherwise mutate the worktree.\n")
	builder.WriteString("- Exhaustive coverage is required within your assigned reviewer scope. Preserve any profile or supplemental review focus; for example, architecture reviewers must review the full relevant architectural change set without broadening into general code review.\n")
	builder.WriteString("- Follow this staged procedure before exiting:\n")
	builder.WriteString("  1. Inventory the complete changed surface and the task acceptance criteria in scope.\n")
	builder.WriteString("  2. Inspect the relevant changes, tests, callers, error paths, and cross-cutting effects for that scope.\n")
	builder.WriteString("  3. Accumulate candidate findings privately. Do not call `orpheus agent review add` during this initial inspection.\n")
	builder.WriteString("  4. Perform a final coverage sweep against the inventory to look for missed files, missed criteria, and duplicate or overlapping findings.\n")
	builder.WriteString("  5. Only after the initial inspection and final coverage sweep are complete, record findings with `orpheus agent review add`.\n")
	builder.WriteString("- Calling `orpheus agent review add` before completing the initial inspection and final coverage sweep is prohibited.\n")
	builder.WriteString("- Record every collected distinct finding before exit, with a separate `orpheus agent review add` call for each finding.\n")
	builder.WriteString("- Exit 0 after recording all findings. Exit non-zero only for an operational review failure.\n")

	appendReviewFindingInstructions(builder)
}

func appendReviewFindingInstructions(builder *strings.Builder) {
	builder.WriteString("\nFinding examples:\n")
	builder.WriteString("```bash\n")
	builder.WriteString("orpheus agent review add \\\n")
	builder.WriteString("  --type blocking \\\n")
	builder.WriteString("  --title \"Missing validation for empty ID\" \\\n")
	builder.WriteString("  --description-file finding.md \\\n")
	builder.WriteString("  --suggested-action \"Add validation and tests\"\n")
	builder.WriteString("```\n\n")
	builder.WriteString("```bash\n")
	builder.WriteString("orpheus agent review add \\\n")
	builder.WriteString("  --type separate-task \\\n")
	builder.WriteString("  --title \"Duplicate validation helper\" \\\n")
	builder.WriteString("  --description-file finding.md \\\n")
	builder.WriteString("  --task-title \"Extract shared validation helper\" \\\n")
	builder.WriteString("  --task-description-file task.md \\\n")
	builder.WriteString("  --task-acceptance-criteria-file acceptance.md\n")
	builder.WriteString("```\n")

	builder.WriteString("\nFinding validation rules:\n")
	builder.WriteString("- `--type` must be exactly one of `blocking`, `advisory`, or `separate-task`.\n")
	builder.WriteString("- `--title` is required.\n")
	builder.WriteString("- Use exactly one of `--description` or `--description-file`.\n")
	builder.WriteString("- Blocking findings require `--suggested-action`.\n")
	builder.WriteString("- Separate-task findings require `--task-title`, exactly one task description source, and exactly one task acceptance criteria source.\n")
	builder.WriteString("- Invalid or stale calls fail without writing task state.\n")
	builder.WriteString("- Blocking findings from a successful review-agent process stop the pipeline.\n")
	builder.WriteString("- Advisory and separate-task findings do not stop the pipeline.\n")
	builder.WriteString("- Do not call `orpheus agent done`; implementation completion has already been recorded.\n")
}
