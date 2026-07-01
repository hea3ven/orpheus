package agent

import (
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/registry"
)

// ActiveContextRenderOptions controls optional implementation-agent context sections.
type ActiveContextRenderOptions struct {
	InteractiveAgentGuidance bool
}

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
	appendInteractiveAgentGuidance(&builder, opts)
	appendExecutionContract(&builder, ctx)

	return builder.String()
}

func appendInteractiveAgentGuidance(builder *strings.Builder, opts ActiveContextRenderOptions) {
	if !opts.InteractiveAgentGuidance {
		return
	}

	builder.WriteString("\nInteraction guidance:\n")
	builder.WriteString("- This is an attached interactive implementation session; ")
	builder.WriteString("you may ask the human operator for clarification or decisions.\n")
	builder.WriteString("- Minimize interruptions: ask only for critical ambiguity ")
	builder.WriteString("or major product/architecture decisions.\n")
	builder.WriteString("- Make low-risk, low-level implementation decisions independently.\n")
}

func appendFollowUpContext(builder *strings.Builder, followUp *ContextFollowUp) {
	if followUp == nil {
		return
	}

	builder.WriteString("\nReview follow-up:\n")
	appendPromptLine(builder, "- Review attempt", fmt.Sprintf("%d", followUp.ReviewAttempt))
	builder.WriteString("- This is a continuation of completed work.\n")
	builder.WriteString("- Do not reimplement the original task.\n")
	builder.WriteString("- Address only the listed review findings.\n")
	builder.WriteString("- Preserve the current task branch and worktree target.\n")
	builder.WriteString("\nBlocking findings:\n")
	for _, finding := range followUp.Findings {
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

func appendRepositoryContext(builder *strings.Builder, repo ContextRepository) {
	builder.WriteString("\nRepository:\n")
	appendPromptLine(builder, "- ID", repo.ID)
	appendPromptLine(builder, "- Name", repo.Name)
	appendPromptLine(builder, "- Registered root", repo.Root)
	appendPromptLine(builder, "- Registered default branch", repo.DefaultBranch)
}

func appendExecutionTargetContext(builder *strings.Builder, ctx ActiveContext) {
	builder.WriteString("\nExecution target:\n")
	appendPromptLine(builder, "- Workflow", ctx.Target.Kind.DisplayName())
	appendPromptLine(builder, "- Branch", ctx.Target.Branch)
	appendPromptLine(builder, "- Path", ctx.Target.Path)
	appendPromptLine(builder, "- Current directory", ctx.Target.CurrentDirectory)
	appendPromptLine(builder, "- Run attempt", fmt.Sprintf("%d", ctx.Run.Attempt))
	if strings.TrimSpace(ctx.Run.Agent) != "" {
		appendPromptLine(builder, "- Agent", ctx.Run.Agent)
	}
}

func appendExecutionContract(builder *strings.Builder, ctx ActiveContext) {
	builder.WriteString("\nExecution contract:\n")
	if ctx.FollowUp != nil {
		builder.WriteString("- This run must address only the listed review findings; do not reimplement the original task.\n")
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
		builder.WriteString("- Keep implementation work inside the execution target path.\n")
		appendAgentDoneContract(builder, ctx.Repository.SummaryGuidance, ctx.Repository.SummaryGuidanceStyle)
		builder.WriteString("- After `orpheus agent done`, Orpheus will record local-review-ready completion data.\n")
		builder.WriteString("- The human operator will later run `orpheus task review ")
		builder.WriteString(ctx.Task.ID)
		builder.WriteString("` after review; do not run it yourself unless explicitly asked.\n")
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
	builder.WriteString("- The human operator will later run `orpheus task review ")
	builder.WriteString(taskID)
	builder.WriteString("` to review and publish the feature branch as a pull request; do not run it yourself unless explicitly asked.\n")
}

func appendAgentDoneContract(builder *strings.Builder, summaryGuidance string, summaryGuidanceStyle string) {
	builder.WriteString("- When implementation and checks are complete, finish with ")
	builder.WriteString("`orpheus agent done --summary \"<summary>\" --description \"<description>\" ")
	builder.WriteString("--detailed-description \"<markdown-pr-body>\"` ")
	builder.WriteString("or `orpheus agent done --summary \"<summary>\" --description \"<description>\" ")
	builder.WriteString("--detailed-description-file <path>`.\n")
	appendSummaryGuidanceContract(builder, summaryGuidance, summaryGuidanceStyle)
	builder.WriteString("- Use `--description` for a concise, plain one-paragraph commit body.\n")
	builder.WriteString("- Use exactly one detailed PR body source: inline `--detailed-description` ")
	builder.WriteString("or `--detailed-description-file`; markdown is allowed.\n")
	builder.WriteString("- `orpheus agent done` is a one-time completion handoff for this Orpheus run: ")
	builder.WriteString("run it at most once, and do not run it again after it succeeds ")
	builder.WriteString("even if this interactive session continues.\n")
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
