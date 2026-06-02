package agent

import (
	"fmt"
	"strings"
)

// DispatchPromptContext is the backend-neutral context rendered into an M3 agent prompt.
type DispatchPromptContext struct {
	TaskID                 string
	TaskTitle              string
	TaskDescription        string
	TaskAcceptanceCriteria string

	RepositoryID   string
	RepositoryName string
	ExecutionDir   string
	WorktreePath   string
	Branch         string
}

// RenderDispatchPrompt renders the built-in M3 prompt for an attached agent run.
func RenderDispatchPrompt(ctx DispatchPromptContext) string {
	var builder strings.Builder

	builder.WriteString("You are an attached implementation agent dispatched by Orpheus.\n\n")
	builder.WriteString("Task:\n")
	appendPromptLine(&builder, "- ID", ctx.TaskID)
	appendPromptLine(&builder, "- Title", ctx.TaskTitle)
	appendPromptBlock(&builder, "- Description", ctx.TaskDescription)
	if strings.TrimSpace(ctx.TaskAcceptanceCriteria) != "" {
		appendPromptBlock(&builder, "- Acceptance criteria", ctx.TaskAcceptanceCriteria)
	}

	builder.WriteString("\nRepository:\n")
	appendPromptLine(&builder, "- ID", ctx.RepositoryID)
	appendPromptLine(&builder, "- Name", ctx.RepositoryName)
	appendPromptLine(&builder, "- Current execution directory", ctx.ExecutionDir)
	appendPromptLine(&builder, "- Deterministic worktree", ctx.WorktreePath)
	appendPromptLine(&builder, "- Deterministic branch", ctx.Branch)

	builder.WriteString("\nInstructions:\n")
	builder.WriteString("- Work in the current repository directory, which is the deterministic task worktree.\n")
	builder.WriteString("- Do not commit manually; leave changes in the working tree for the human operator and later Orpheus workflow steps.\n")
	builder.WriteString("- When you are finished, report back to the human operator using exactly this format:\n\n")
	builder.WriteString("Summary:\n")
	builder.WriteString("- One commit-style summary line, 80 characters or fewer, ")
	builder.WriteString("formatted as \"<type(fix,feat,test,chore,conf,etc)>: <description>\"; ")
	builder.WriteString("do not include the task/bead ID; do not mention tests even if included.\n\n")
	builder.WriteString("Details:\n")
	builder.WriteString("- Bullet list of important implementation details.\n\n")
	builder.WriteString("Checks:\n")
	builder.WriteString("- Commands you ran and whether they passed.\n")
	builder.WriteString("- If you did not run checks, say so.\n\n")
	builder.WriteString("Follow-ups:\n")
	builder.WriteString("- Anything the human should know before review.\n")
	builder.WriteString("- If none, say \"None\".\n")

	return builder.String()
}

func appendPromptLine(builder *strings.Builder, label string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "-"
	}
	_, _ = fmt.Fprintf(builder, "%s: %s\n", label, value)
}

func appendPromptBlock(builder *strings.Builder, label string, value string) {
	value = strings.TrimRight(strings.ReplaceAll(value, "\r\n", "\n"), "\r\n")
	if strings.TrimSpace(value) == "" {
		_, _ = fmt.Fprintf(builder, "%s: -\n", label)
		return
	}
	if !strings.Contains(value, "\n") {
		_, _ = fmt.Fprintf(builder, "%s: %s\n", label, value)
		return
	}

	_, _ = fmt.Fprintf(builder, "%s:\n", label)
	for _, line := range strings.Split(value, "\n") {
		_, _ = fmt.Fprintf(builder, "  %s\n", line)
	}
}
