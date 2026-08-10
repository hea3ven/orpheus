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
	appendPriorReviewFindings(&builder, ctx.Task.ID, ctx.Review.PriorFindings)
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

func appendPriorReviewFindings(builder *strings.Builder, taskID string, findings []ContextPriorReviewFinding) {
	if len(findings) == 0 {
		return
	}

	builder.WriteString("\nPrior authoritative findings:\n")
	for _, finding := range findings {
		step := compactReviewText(finding.Step)
		if step == "" {
			step = "(unspecified)"
		}
		fmt.Fprintf(
			builder,
			"- `%d/%d` · %s · %s · %s · %s\n",
			finding.Attempt,
			finding.Number,
			step,
			compactReviewText(string(finding.Type)),
			compactReviewText(finding.Disposition),
			compactReviewText(finding.Title),
		)
	}
	fmt.Fprintf(builder, "Inspect a finding with `orpheus task review show %s <review-attempt> <finding-number>`; for example, `orpheus task review show %s %d %d`.\n", taskID, taskID, findings[0].Attempt, findings[0].Number)
	builder.WriteString("Prior decisions are context, not a prohibition: do not repeat an unchanged accepted disposition, but report a defect if it is newly applicable or its material circumstances changed.\n")
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
	appendReviewTextTransportGuidance(builder)
	builder.WriteString("\nFinding examples:\n")
	appendBlockingFindingExample(builder)
	appendSeparateTaskFindingExample(builder)
	appendReviewFindingValidationRules(builder)
}

func appendReviewTextTransportGuidance(builder *strings.Builder) {
	builder.WriteString("\nSafe reporting text:\n")
	builder.WriteString("- Never place generated prose inside a double-quoted shell argument. JSON string escaping is not Bash quoting; apply shell quoting when running a shell command.\n")
	builder.WriteString("- In Bash, double quotes still expand backticks and `$()` command substitutions (and `$variable` expansions), so generated Markdown can execute commands or be changed before Orpheus receives it.\n")
	builder.WriteString("- Always use `--description-file` for a generated finding description. Use `--task-description-file` and `--task-acceptance-criteria-file` for generated separate-task Markdown.\n")
	builder.WriteString("- Keep temporary review-report files outside the candidate worktree. Do not place arbitrary raw text in a fixed-delimiter heredoc: a line equal to its delimiter ends it. Instead, base64-encode generated file contents and decode each payload from a single-quoted shell literal; standard base64 data contains no apostrophes.\n")
	builder.WriteString("- For unavoidable inline fields such as titles and suggested actions, use a single-quoted shell literal. To embed an apostrophe, close the quote, write `\\'`, and reopen it: `'O'\\''Brien'`.\n")
}

func appendBlockingFindingExample(builder *strings.Builder) {
	builder.WriteString("```bash\n")
	builder.WriteString("report_dir=$(mktemp -d /tmp/orpheus-review.XXXXXX)\n")
	appendBase64ReportFile(builder, "$report_dir/finding.md", "VGhlIHBhcnNlciBkcm9wcyBgbGl0ZXJhbCB0ZXh0YCwgJChjb21tYW5kcyksIGFuZCBPJ0JyaWVuIHZhbHVlcy4K")
	builder.WriteString("orpheus agent review add \\\n")
	builder.WriteString("  --type blocking \\\n")
	builder.WriteString("  --title 'Missing validation for O'\\''Brien IDs' \\\n")
	builder.WriteString("  --description-file \"$report_dir/finding.md\" \\\n")
	builder.WriteString("  --suggested-action 'Validate O'\\''Brien IDs before saving'\n")
	builder.WriteString("```\n\n")
}

func appendSeparateTaskFindingExample(builder *strings.Builder) {
	builder.WriteString("```bash\n")
	builder.WriteString("report_dir=$(mktemp -d /tmp/orpheus-review.XXXXXX)\n")
	appendBase64ReportFile(builder, "$report_dir/finding.md", "VGhlIHZhbGlkYXRpb24gaGVscGVyIGR1cGxpY2F0ZXMgYGV4aXN0aW5nIGJlaGF2aW9yYCBmb3IgTydCcmllbiB2YWx1ZXMuCkVPRgpUaGUgcmVwb3J0IHN0aWxsIHJlbWFpbnMgbGl0ZXJhbC4K")
	appendBase64ReportFile(builder, "$report_dir/task-description.md", "RXh0cmFjdCB0aGUgaGVscGVyIHdpdGhvdXQgY2hhbmdpbmcgYGV4aXN0aW5nIGNhbGxlcnNgLgo=")
	appendBase64ReportFile(builder, "$report_dir/acceptance.md", "LSBUaGUgaGVscGVyIHByZXNlcnZlcyAkKGV4aXN0aW5nIGJlaGF2aW9yKS4K")
	builder.WriteString("orpheus agent review add \\\n")
	builder.WriteString("  --type separate-task \\\n")
	builder.WriteString("  --title 'Duplicate validation helper' \\\n")
	builder.WriteString("  --description-file \"$report_dir/finding.md\" \\\n")
	builder.WriteString("  --task-title 'Extract O'\\''Brien validation helper' \\\n")
	builder.WriteString("  --task-description-file \"$report_dir/task-description.md\" \\\n")
	builder.WriteString("  --task-acceptance-criteria-file \"$report_dir/acceptance.md\"\n")
	builder.WriteString("```\n")
}

func appendReviewFindingValidationRules(builder *strings.Builder) {
	builder.WriteString("\nFinding validation rules:\n")
	builder.WriteString("- `--type` must be exactly one of `blocking`, `advisory`, or `separate-task`.\n")
	builder.WriteString("- `--title` is required.\n")
	builder.WriteString("- Use exactly one of `--description` or `--description-file`.\n")
	builder.WriteString("- Blocking findings require `--suggested-action`.\n")
	builder.WriteString("- Separate-task findings require `--task-title`, exactly one task description source, and exactly one task acceptance criteria source.\n")
	builder.WriteString("- Invalid or stale calls fail without writing task state.\n")
	builder.WriteString("- Verify every reporting command succeeded before exiting or retrying it. If its result is ambiguous, inspect recorded state; do not blindly retry a finding that may already be recorded.\n")
	builder.WriteString("- Blocking findings from a successful review-agent process stop the pipeline.\n")
	builder.WriteString("- Advisory and separate-task findings do not stop the pipeline.\n")
	builder.WriteString("- Do not call `orpheus agent done`; implementation completion has already been recorded.\n")
}
