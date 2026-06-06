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
		builder.WriteString("- When implementation and checks are complete, finish with ")
		builder.WriteString("`orpheus agent done --summary \"<summary>\" --details \"<details>\"`.\n")
		builder.WriteString("- After `orpheus agent done`, Orpheus will create the pull request in a later workflow step.\n")
	case ExecutionTargetMain:
		builder.WriteString("- You are running in the registered repository root on the registered default branch.\n")
		builder.WriteString("- Keep implementation work inside the execution target path.\n")
		builder.WriteString("- When implementation and checks are complete, finish with ")
		builder.WriteString("`orpheus agent done --summary \"<summary>\" --details \"<details>\"`.\n")
		builder.WriteString("- After `orpheus agent done`, Orpheus will record local-review-ready completion data.\n")
		builder.WriteString("- The human operator will later run `orpheus task done ")
		builder.WriteString(ctx.Task.ID)
		builder.WriteString("` after review; do not run it yourself unless explicitly asked.\n")
	default:
		builder.WriteString("- The execution target is unknown; stop and ask the human operator for help.\n")
	}

	return builder.String()
}
