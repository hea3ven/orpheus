package agent

import (
	"fmt"
	"strings"
)

// RenderActiveContext renders backend-neutral instructions for the active agent.
func RenderActiveContext(ctx ActiveContext) string {
	var builder strings.Builder

	appendContextHeader(&builder, ctx)
	appendRepositoryContext(&builder, ctx.Repository)
	appendExecutionTargetContext(&builder, ctx)
	appendExecutionContract(&builder, ctx)

	return builder.String()
}

func appendContextHeader(builder *strings.Builder, ctx ActiveContext) {
	builder.WriteString("# Orpheus Agent Context\n\n")

	builder.WriteString("Task:\n")
	appendPromptLine(builder, "- ID", ctx.Task.ID)
	appendPromptLine(builder, "- Title", ctx.Task.Title)
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
	switch ctx.Target.Kind {
	case ExecutionTargetWorktree:
		builder.WriteString("- You are running in the deterministic task worktree and task branch.\n")
		appendFeatureBranchExecutionContract(builder, ctx.Task.ID)
	case ExecutionTargetRepoRoot:
		builder.WriteString("- You are running in the registered repository root on the task branch.\n")
		appendFeatureBranchExecutionContract(builder, ctx.Task.ID)
	case ExecutionTargetMain:
		builder.WriteString("- You are running in the registered repository root on the registered default branch.\n")
		builder.WriteString("- Keep implementation work inside the execution target path.\n")
		appendAgentDoneContract(builder)
		builder.WriteString("- After `orpheus agent done`, Orpheus will record local-review-ready completion data.\n")
		builder.WriteString("- The human operator will later run `orpheus task done ")
		builder.WriteString(ctx.Task.ID)
		builder.WriteString("` after review; do not run it yourself unless explicitly asked.\n")
	default:
		builder.WriteString("- The execution target is unknown; stop and ask the human operator for help.\n")
	}
}

func appendFeatureBranchExecutionContract(builder *strings.Builder, taskID string) {
	builder.WriteString("- Keep implementation work inside the execution target path.\n")
	appendAgentDoneContract(builder)
	builder.WriteString("- After `orpheus agent done`, Orpheus will record PR-ready completion data for feature-branch publication.\n")
	builder.WriteString("- The human operator will later run `orpheus task done ")
	builder.WriteString(taskID)
	builder.WriteString("` to publish the feature branch as a pull request; do not run it yourself unless explicitly asked.\n")
}

func appendAgentDoneContract(builder *strings.Builder) {
	builder.WriteString("- When implementation and checks are complete, finish with ")
	builder.WriteString("`orpheus agent done --summary \"<summary>\" --description \"<description>\" ")
	builder.WriteString("--detailed-description \"<markdown-pr-body>\"` ")
	builder.WriteString("or `orpheus agent done --summary \"<summary>\" --description \"<description>\" ")
	builder.WriteString("--detailed-description-file <path>`.\n")
	builder.WriteString("- Use one commit-style summary line, 80 characters or fewer, ")
	builder.WriteString("formatted as \"<type(fix,feat,test,chore,conf,etc)>: <description>\"; ")
	builder.WriteString("do not include the task/bead ID; do not mention tests even if included.\n")
	builder.WriteString("- Use `--description` for a concise, plain one-paragraph commit body.\n")
	builder.WriteString("- Use exactly one detailed PR body source: inline `--detailed-description` ")
	builder.WriteString("or `--detailed-description-file`; markdown is allowed.\n")
	builder.WriteString("- `orpheus agent done` is a one-time completion handoff for this Orpheus run: ")
	builder.WriteString("run it at most once, and do not run it again after it succeeds ")
	builder.WriteString("even if this interactive session continues.\n")
}
