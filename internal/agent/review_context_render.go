package agent

import (
	"fmt"
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
	appendReviewCompletionContext(&builder, ctx.Review.Completion)
	appendReviewContract(&builder)

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
	appendPromptLine(builder, "- Workflow", ctx.Target.Kind.DisplayName())
	appendPromptLine(builder, "- Branch", ctx.Target.Branch)
	appendPromptLine(builder, "- Path", ctx.Target.Path)
	appendPromptLine(builder, "- Current directory", ctx.Target.CurrentDirectory)
	appendPromptLine(builder, "- Review attempt", fmt.Sprintf("%d", ctx.Review.Attempt))
	appendPromptLine(builder, "- Review step", ctx.Review.Step)
	if strings.TrimSpace(ctx.Review.EnvStep) != "" && ctx.Review.EnvStep != ctx.Review.Step {
		appendPromptLine(builder, "- Informational env review step", ctx.Review.EnvStep)
	}
}

func appendReviewCompletionContext(builder *strings.Builder, completion taskstate.Completion) {
	builder.WriteString("\nLatest completion:\n")
	appendPromptLine(builder, "- Summary", completion.Summary)
	appendPromptBlock(builder, "- Description", completion.Description)
	appendPromptBlock(builder, "- Detailed description", completion.DetailedDescription)
	if strings.TrimSpace(completion.Commit) != "" {
		appendPromptLine(builder, "- Commit", completion.Commit)
	}
}

func appendReviewContract(builder *strings.Builder) {
	builder.WriteString("\nReview contract:\n")
	builder.WriteString("- You are reviewing the current working-tree changes for the task above.\n")
	builder.WriteString("- Use Git commands such as `git status --short`, `git diff`, and `git log` as needed.\n")
	builder.WriteString("- This is a strict read-only review step. Do not edit files, stage changes, commit, run formatters that write files, or otherwise mutate the worktree.\n")
	builder.WriteString("- Record each finding with `orpheus agent review add`; there is no `orpheus agent review done` command.\n")
	builder.WriteString("- Exit 0 after recording all findings. Exit non-zero only for an operational review failure.\n")

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
