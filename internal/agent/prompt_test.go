package agent_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/stretchr/testify/assert"
)

func TestRenderDispatchPromptIncludesTaskRepositoryAndReportFormat(t *testing.T) {
	is := assert.New(t)

	prompt := agent.RenderDispatchPrompt(agent.DispatchPromptContext{
		TaskID:                 "op-9xs.10",
		TaskTitle:              "Minimal attached agent execution",
		TaskDescription:        "Resolve the task.\nRun the agent.",
		TaskAcceptanceCriteria: "Agent receives backend-neutral context.",
		RepositoryID:           "orpheus",
		RepositoryName:         "Orpheus",
		ExecutionDir:           "/tmp/orpheus",
	})

	for _, want := range []string{
		"Task:",
		"- ID: op-9xs.10",
		"- Title: Minimal attached agent execution",
		"Resolve the task.",
		"Run the agent.",
		"- Acceptance criteria: Agent receives backend-neutral context.",
		"Repository:",
		"- ID: orpheus",
		"- Name: Orpheus",
		"- Current execution directory: /tmp/orpheus",
		"Do not commit manually",
		"M3 WIP limitation",
		"Summary:",
		"One commit-style summary line, 80 characters or fewer",
		"<type(fix,feat,test,chore,conf,etc)>: <description>",
		"do not include the task/bead ID",
		"do not mention tests even if included",
		"Details:",
		"Checks:",
		"Follow-ups:",
		"If none, say \"None\".",
	} {
		is.Contains(prompt, want)
	}
	is.NotContains(prompt, "Beads")
}
