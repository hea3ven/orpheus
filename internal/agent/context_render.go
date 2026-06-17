package agent

import (
	"fmt"
	"strings"
)

// RenderActiveContext renders backend-neutral instructions for the active agent.
func RenderActiveContext(ctx ActiveContext) string {
	var builder strings.Builder

	builder.WriteString("# Orpheus Agent Context\n\n")

	builder.WriteString("Task:\n")
	appendPromptLine(&builder, "- ID", ctx.Task.ID)
	appendPromptLine(&builder, "- Title", ctx.Task.Title)
	appendPromptBlock(&builder, "- Description", ctx.Task.Description)
	appendPromptBlock(&builder, "- Acceptance criteria", ctx.Task.AcceptanceCriteria)

	builder.WriteString("\nRepository:\n")
	appendPromptLine(&builder, "- ID", ctx.Repository.ID)
	appendPromptLine(&builder, "- Name", ctx.Repository.Name)
	appendPromptLine(&builder, "- Registered root", ctx.Repository.Root)
	appendPromptLine(&builder, "- Registered default branch", ctx.Repository.DefaultBranch)

	builder.WriteString("\nExecution target:\n")
	appendPromptLine(&builder, "- Workflow", ctx.Target.Kind.DisplayName())
	appendPromptLine(&builder, "- Branch", ctx.Target.Branch)
	appendPromptLine(&builder, "- Path", ctx.Target.Path)
	appendPromptLine(&builder, "- Current directory", ctx.Target.CurrentDirectory)
	appendPromptLine(&builder, "- Run attempt", fmt.Sprintf("%d", ctx.Run.Attempt))
	if strings.TrimSpace(ctx.Run.Agent) != "" {
		appendPromptLine(&builder, "- Agent", ctx.Run.Agent)
	}

	builder.WriteString("\nExecution contract:\n")
	switch ctx.Target.Kind {
	case ExecutionTargetWorktree:
		builder.WriteString("- You are running in the deterministic task worktree and task branch.\n")
		builder.WriteString("- Keep implementation work inside the execution target path.\n")
		appendAgentDoneContract(&builder)
		builder.WriteString("- After `orpheus agent done`, Orpheus will record local-review-ready completion data.\n")
		builder.WriteString("- The human operator will later run `orpheus task done ")
		builder.WriteString(ctx.Task.ID)
		builder.WriteString("` after review; do not run it yourself unless explicitly asked.\n")
	case ExecutionTargetMain:
		builder.WriteString("- You are running in the registered repository root on the registered default branch.\n")
		builder.WriteString("- Keep implementation work inside the execution target path.\n")
		appendAgentDoneContract(&builder)
		builder.WriteString("- After `orpheus agent done`, Orpheus will record local-review-ready completion data.\n")
		builder.WriteString("- The human operator will later run `orpheus task done ")
		builder.WriteString(ctx.Task.ID)
		builder.WriteString("` after review; do not run it yourself unless explicitly asked.\n")
	default:
		builder.WriteString("- The execution target is unknown; stop and ask the human operator for help.\n")
	}

	return builder.String()
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
