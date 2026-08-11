package agent

import (
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/registry"
)

// ActiveContextRenderOptions controls optional implementation-agent context sections.
type ActiveContextRenderOptions struct {
	InteractionMode AgentInteractionMode
}

// AgentInteractionMode controls how implementation agents should handle
// human-facing decisions during a run.
type AgentInteractionMode string

const (
	// AgentInteractionModeUnspecified omits profile-controlled interaction guidance.
	AgentInteractionModeUnspecified AgentInteractionMode = ""

	// AgentInteractionModeInteractive tells the agent it may ask the operator
	// for critical clarifications.
	AgentInteractionModeInteractive AgentInteractionMode = "interactive"

	// AgentInteractionModeNonInteractive tells the agent to proceed autonomously.
	AgentInteractionModeNonInteractive AgentInteractionMode = "non-interactive"
)

// RenderActiveContext renders backend-neutral instructions for the active agent.
func RenderActiveContext(ctx ActiveContext) string {
	return RenderActiveContextWithOptions(ctx, ActiveContextRenderOptions{})
}

// RenderActiveContextWithOptions renders backend-neutral instructions for the active agent.
func RenderActiveContextWithOptions(ctx ActiveContext, opts ActiveContextRenderOptions) string {
	var builder strings.Builder

	appendContextHeader(&builder, ctx)
	appendRepositoryContext(&builder, ctx.Repository)
	appendExecutionTargetContext(&builder, ctx)
	appendFollowUpContext(&builder, ctx.FollowUp)
	appendAgentInteractionGuidance(&builder, opts)
	appendExecutionContract(&builder, ctx)

	return builder.String()
}

// RenderConflictResolutionContext renders instructions for a sync conflict agent.
func RenderConflictResolutionContext(ctx ConflictResolutionContext) string {
	var builder strings.Builder

	appendConflictResolutionContextHeader(&builder, ctx)
	appendRepositoryContext(&builder, ctx.Repository)
	appendConflictResolutionTargetContext(&builder, ctx)
	appendConflictResolutionGuidance(&builder, ctx)
	appendConflictResolutionContract(&builder)

	return builder.String()
}

func appendAgentInteractionGuidance(builder *strings.Builder, opts ActiveContextRenderOptions) {
	switch opts.InteractionMode {
	case AgentInteractionModeInteractive:
		appendInteractiveAgentGuidance(builder)
	case AgentInteractionModeNonInteractive:
		appendNonInteractiveAgentGuidance(builder)
	}
}

func appendInteractiveAgentGuidance(builder *strings.Builder) {
	builder.WriteString("\nInteraction guidance:\n")
	builder.WriteString("- This is an attached interactive implementation session; ")
	builder.WriteString("you may ask the human operator for clarification or decisions.\n")
	builder.WriteString("- Minimize interruptions: ask only for critical ambiguity ")
	builder.WriteString("or major product/architecture decisions.\n")
	builder.WriteString("- Make low-risk, low-level implementation decisions independently.\n")
}

func appendNonInteractiveAgentGuidance(builder *strings.Builder) {
	builder.WriteString("\nInteraction guidance:\n")
	builder.WriteString("- This is a non-interactive implementation session; ")
	builder.WriteString("do not ask the human operator for clarification or decisions.\n")
	builder.WriteString("- Decide independently when a reasonable, low-risk path exists.\n")
	builder.WriteString("- If continuation is impossible without human input, fail clearly ")
	builder.WriteString("and explain the blocking decision or missing information.\n")
	builder.WriteString("- Before finishing, summarize significant decisions in the visible ")
	builder.WriteString("terminal/session output.\n")
}

func appendFollowUpContext(builder *strings.Builder, followUp *ContextFollowUp) {
	if followUp == nil {
		return
	}

	builder.WriteString("\nReview follow-up:\n")
	appendPromptLine(builder, "- Review attempt", fmt.Sprintf("%d", followUp.ReviewAttempt))
	builder.WriteString("- This is a continuation of completed work.\n")
	builder.WriteString("- Do not reimplement the original task.\n")
	builder.WriteString("- Fix every required blocking finding before completing this run.\n")
	builder.WriteString("- Consider advisory opportunities only when they remain applicable, task-scoped, and safe.\n")
	builder.WriteString("- Advisory work is best-effort: leaving an advisory unresolved does not fail this run or block publication.\n")
	builder.WriteString("- Preserve the current task branch and worktree target.\n")
	builder.WriteString("- This Orpheus run attempt is a new completion boundary; after the repair, call `orpheus agent done` exactly once for the current attempt.\n")
	builder.WriteString("- A successful `orpheus agent done` visible in resumed session history belongs to an earlier attempt and does not complete this follow-up.\n")
	appendFollowUpFindings(builder, "Required blocking findings", followUp.RequiredFindings)
	appendFollowUpFindings(builder, "Advisory opportunities", followUp.AdvisoryFindings)
}

func appendFollowUpFindings(builder *strings.Builder, heading string, findings []ContextReviewFinding) {
	builder.WriteString("\n")
	builder.WriteString(heading)
	builder.WriteString(":\n")
	if len(findings) == 0 {
		builder.WriteString("- (none)\n")
		return
	}
	for _, finding := range findings {
		appendPromptLine(builder, fmt.Sprintf("- Finding %d title", finding.Index+1), finding.Title)
		appendPromptBlock(builder, "  Description", finding.Description)
		appendPromptBlock(builder, "  Suggested action", finding.SuggestedAction)
	}
}

func appendContextHeader(builder *strings.Builder, ctx ActiveContext) {
	builder.WriteString("# Orpheus Agent Context\n\n")

	builder.WriteString("Task:\n")
	appendPromptLine(builder, "- ID", ctx.Task.ID)
	appendPromptLine(builder, "- Title", ctx.Task.Title)
	appendPromptLine(builder, "- External reference", ctx.Task.ExternalRef)
	appendPromptBlock(builder, "- Description", ctx.Task.Description)
	appendPromptBlock(builder, "- Acceptance criteria", ctx.Task.AcceptanceCriteria)
}

func appendConflictResolutionContextHeader(builder *strings.Builder, ctx ConflictResolutionContext) {
	builder.WriteString("# Orpheus Sync Conflict Resolution Context\n\n")

	builder.WriteString("Task:\n")
	appendPromptLine(builder, "- ID", ctx.Task.ID)
	appendPromptLine(builder, "- Title", ctx.Task.Title)
	appendPromptLine(builder, "- External reference", ctx.Task.ExternalRef)
	appendPromptBlock(builder, "- Description", ctx.Task.Description)
	appendPromptBlock(builder, "- Acceptance criteria", ctx.Task.AcceptanceCriteria)
	appendPromptLine(builder, "- Pull request", ctx.PRURL)
}

func appendRepositoryContext(builder *strings.Builder, repo ContextRepository) {
	builder.WriteString("\nRepository:\n")
	appendPromptLine(builder, "- ID", repo.ID)
	appendPromptLine(builder, "- Name", repo.Name)
	appendPromptLine(builder, "- Registered root", repo.Root)
	appendPromptLine(builder, "- Registered default branch", repo.DefaultBranch)
}

func appendExecutionTargetContext(builder *strings.Builder, ctx ActiveContext) {
	builder.WriteString("\nExecution target:\n")
	appendPromptLine(builder, "- Work Directory", ctx.Target.Path)
	appendPromptLine(builder, "- Current branch", ctx.Target.Branch)
	appendPromptLine(builder, "- Current directory", ctx.Target.CurrentDirectory)
	appendPromptLine(builder, "- Run attempt", fmt.Sprintf("%d", ctx.Run.Attempt))
	if strings.TrimSpace(ctx.Run.Agent) != "" {
		appendPromptLine(builder, "- Agent", ctx.Run.Agent)
	}
}

func appendConflictResolutionTargetContext(builder *strings.Builder, ctx ConflictResolutionContext) {
	builder.WriteString("\nExecution target:\n")
	appendPromptLine(builder, "- Work Directory", ctx.Target.Path)
	appendPromptLine(builder, "- Current branch", ctx.Target.Branch)
	appendPromptLine(builder, "- Current directory", ctx.Target.CurrentDirectory)
}

func appendConflictResolutionGuidance(builder *strings.Builder, ctx ConflictResolutionContext) {
	builder.WriteString("\nConflict resolution scope:\n")
	builder.WriteString("- Resolve only the merge conflicts from syncing the registered default branch into this open PR branch.\n")
	builder.WriteString("- Do not implement unrelated task changes, refactor unrelated code, or address review feedback.\n")
	builder.WriteString("- Inspect `git status` and the conflicted files, edit only what is needed to resolve those conflicts, and stage the resolved conflict files.\n")
	if len(ctx.ConflictFiles) == 0 {
		builder.WriteString("- Conflicted files: inspect `git status --short` in the execution target.\n")
		return
	}
	builder.WriteString("- Conflicted files:\n")
	for _, file := range ctx.ConflictFiles {
		builder.WriteString("  - ")
		builder.WriteString(strings.TrimSpace(file))
		builder.WriteString("\n")
	}
}

func appendConflictResolutionContract(builder *strings.Builder) {
	builder.WriteString("\nExecution contract:\n")
	builder.WriteString("- This is a non-interactive sync conflict-resolution session; do not ask the human operator for clarification or decisions.\n")
	builder.WriteString("- Do not run `orpheus agent done`, `orpheus task run`, `orpheus task review`, or `orpheus task done`.\n")
	builder.WriteString("- Do not create commits, push branches, merge pull requests, close tasks, or change task metadata.\n")
	builder.WriteString("- Leave the merge in progress after staging the resolved conflict files; Orpheus sync will commit and push after you exit.\n")
	builder.WriteString("- If the conflicts cannot be resolved safely, exit nonzero and explain the blocker in the visible terminal/session output.\n")
}

func appendExecutionContract(builder *strings.Builder, ctx ActiveContext) {
	builder.WriteString("\nExecution contract:\n")
	if ctx.FollowUp != nil {
		builder.WriteString("- Fix the required blocking findings; advisories are best-effort and must be addressed only when still applicable, task-scoped, and safe. Do not reimplement the original task.\n")
		builder.WriteString("- Preserve the current task branch/worktree target.\n")
	}
	switch ctx.Target.Kind {
	case ExecutionTargetWorktree:
		builder.WriteString("- You are running in the deterministic task worktree and task branch.\n")
		appendFeatureBranchExecutionContract(builder, ctx.Task.ID, ctx.Repository.SummaryGuidance, ctx.Repository.SummaryGuidanceStyle)
	case ExecutionTargetRepoRoot:
		builder.WriteString("- You are running in the registered repository root on the task branch.\n")
		appendFeatureBranchExecutionContract(builder, ctx.Task.ID, ctx.Repository.SummaryGuidance, ctx.Repository.SummaryGuidanceStyle)
	case ExecutionTargetMain:
		builder.WriteString("- You are running in the registered repository root on the registered default branch.\n")
		builder.WriteString("- The deterministic task branch is created only after review, immediately before publication.\n")
		appendFeatureBranchExecutionContract(builder, ctx.Task.ID, ctx.Repository.SummaryGuidance, ctx.Repository.SummaryGuidanceStyle)
	default:
		builder.WriteString("- The execution target is unknown; stop and ask the human operator for help.\n")
	}
}

func appendFeatureBranchExecutionContract(
	builder *strings.Builder,
	taskID string,
	summaryGuidance string,
	summaryGuidanceStyle string,
) {
	builder.WriteString("- Keep implementation work inside the execution target path.\n")
	appendAgentDoneContract(builder, summaryGuidance, summaryGuidanceStyle)
	builder.WriteString("- After `orpheus agent done`, Orpheus will record PR-ready completion data for feature-branch publication.\n")
	builder.WriteString("- The human operator will later run `orpheus task run ")
	builder.WriteString(taskID)
	builder.WriteString("` to review and publish the feature branch as a pull request; do not run it yourself unless explicitly asked.\n")
}

func appendAgentDoneContract(builder *strings.Builder, summaryGuidance string, summaryGuidanceStyle string) {
	builder.WriteString("- When implementation and checks are complete, write the Markdown fields to files and finish with `orpheus agent done --summary '<summary>' --description '<description>' --detailed-description-file <path> --technical-explanation-file <path>`.\n")
	appendSummaryGuidanceContract(builder, summaryGuidance, summaryGuidanceStyle)
	builder.WriteString("- Use `--description` for a concise, plain one-paragraph commit body.\n")
	builder.WriteString("- Use exactly one detailed PR body source. Prefer `--detailed-description-file` for Markdown; `--detailed-description` is only for a safely quoted inline value.\n")
	builder.WriteString("- Use exactly one technical explanation source. Prefer `--technical-explanation-file` for Markdown; `--technical-explanation` is only for a safely quoted inline value. Explain implementation rationale and notable code changes without replacing the PR body.\n")
	appendCompletionTextTransportGuidance(builder)
	builder.WriteString("- `orpheus agent done` is a one-time completion handoff for this Orpheus run attempt ")
	builder.WriteString("(see Run attempt above), not once per reusable harness session: call it exactly once after ")
	builder.WriteString("finishing the current attempt's work, whether this harness session is fresh or resumed.\n")
	builder.WriteString("- A successful `orpheus agent done` visible in resumed session history belongs to an earlier ")
	builder.WriteString("Orpheus run attempt and does not satisfy the current attempt.\n")
	builder.WriteString("- After this attempt successfully records completion, do not run `orpheus agent done` again ")
	builder.WriteString("even if this interactive session continues; repeated same-attempt calls are no-ops and the ")
	builder.WriteString("first handoff remains authoritative.\n")
}

func appendCompletionTextTransportGuidance(builder *strings.Builder) {
	builder.WriteString("\nSafe reporting text:\n")
	builder.WriteString("- Never place generated prose inside a double-quoted shell argument. JSON string escaping is not Bash quoting; apply shell quoting when running a shell command.\n")
	builder.WriteString("- In Bash, double quotes still expand backticks and `$()` command substitutions (and `$variable` expansions), so generated Markdown can execute commands or be changed before Orpheus receives it.\n")
	builder.WriteString("- Prefer the existing file flags for multiline or Markdown content. Do not place arbitrary raw text in a fixed-delimiter heredoc: a line equal to its delimiter ends it. Instead, base64-encode generated file contents and decode each payload from a single-quoted shell literal; standard base64 data contains no apostrophes.\n")
	builder.WriteString("- For unavoidable inline plain-text fields, use a single-quoted shell literal. To embed an apostrophe, close the quote, write `\\'`, and reopen it: `'O'\\''Brien'`.\n")
	builder.WriteString("\nExample:\n```bash\n")
	builder.WriteString("report_dir=$(mktemp -d /tmp/orpheus-completion.XXXXXX)\n")
	appendBase64ReportFile(builder, "$report_dir/pr-body.md", "IyMgUHJlc2VydmUgbGl0ZXJhbCBNYXJrZG93bgoKS2VlcCBgYmFja3RpY2tzYCwgJChjb21tYW5kcyksIGEgc3RhbmRhbG9uZSBkZWxpbWl0ZXI6CkVPRgphbmQgTydCcmllbiBhcyB3cml0dGVuLgo=")
	appendBase64ReportFile(builder, "$report_dir/technical-explanation.md", "VGhlIGZpbGUgdHJhbnNwb3J0IHByZXNlcnZlcyBnZW5lcmF0ZWQgTWFya2Rvd24gdmVyYmF0aW0uCg==")
	builder.WriteString("orpheus agent done \\\n")
	builder.WriteString("  --summary 'fix: preserve O'\\''Brien reporting' \\\n")
	builder.WriteString("  --description 'Preserve O'\\''Brien reporting text.' \\\n")
	builder.WriteString("  --detailed-description-file \"$report_dir/pr-body.md\" \\\n")
	builder.WriteString("  --technical-explanation-file \"$report_dir/technical-explanation.md\"\n")
	builder.WriteString("```\n")
	builder.WriteString("- Verify every reporting command succeeded before exiting or retrying it. If its result is ambiguous, inspect recorded state; do not blindly retry a command that may already have recorded completion.\n")
}

func appendBase64ReportFile(builder *strings.Builder, path string, payload string) {
	builder.WriteString("printf '%s' '")
	builder.WriteString(payload)
	builder.WriteString("' | base64 --decode >\"")
	builder.WriteString(path)
	builder.WriteString("\"\n")
}

func appendSummaryGuidanceContract(builder *strings.Builder, summaryGuidance string, summaryGuidanceStyle string) {
	summaryGuidance = strings.TrimSpace(summaryGuidance)
	if summaryGuidance != "" {
		appendPromptBlock(builder, "- Write `--summary` following this repository guidance", summaryGuidance)
		return
	}

	if strings.TrimSpace(summaryGuidanceStyle) == registry.SummaryGuidanceStyleCapitalized {
		builder.WriteString("- Use one capitalized plain-English summary line, 80 characters or fewer, ")
		builder.WriteString("with no task type prefix, for example \"Replaced the config for abc\"; ")
		builder.WriteString("do not include the task/bead ID; do not mention tests even if included.\n")
		return
	}

	builder.WriteString("- Use one commit-style summary line, 80 characters or fewer, ")
	builder.WriteString("formatted as \"<type(fix,feat,test,chore,conf,etc)>: <description>\"; ")
	builder.WriteString("do not include the task/bead ID; do not mention tests even if included.\n")
}
