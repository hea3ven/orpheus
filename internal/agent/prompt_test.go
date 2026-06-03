package agent_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/stretchr/testify/assert"
)

func TestRenderDispatchPromptIncludesRepoRootModeContext(t *testing.T) {
	is := assert.New(t)

	prompt := agent.RenderDispatchPrompt(agent.DispatchPromptContext{
		TaskID:         "op-main",
		TaskTitle:      "Manual repo-root review",
		RepositoryID:   "orpheus",
		RepositoryName: "Orpheus",
		ExecutionDir:   "/tmp/orpheus",
		WorktreePath:   "/tmp/orpheus",
		Branch:         "main",
		RepoRootMode:   true,
	})

	for _, want := range []string{
		"- Current execution directory: /tmp/orpheus",
		"- Registered repo root: /tmp/orpheus",
		"- Registered default branch: main",
		"Work in the current repository directory, which is the registered repo root on the registered default branch.",
	} {
		is.Contains(prompt, want)
	}
	is.NotContains(prompt, "- Deterministic worktree")
	is.NotContains(prompt, "- Deterministic branch")
}

func TestRenderDispatchPromptIncludesTaskRepositoryAndReportFormat(t *testing.T) {
	is := assert.New(t)

	prompt := agent.RenderDispatchPrompt(agent.DispatchPromptContext{
		TaskID:                 "op-9xs.10",
		TaskTitle:              "Minimal attached agent execution",
		TaskDescription:        "Resolve the task.\nRun the agent.",
		TaskAcceptanceCriteria: "Agent receives backend-neutral context.",
		RepositoryID:           "orpheus",
		RepositoryName:         "Orpheus",
		ExecutionDir:           "/tmp/orpheus-worktree",
		WorktreePath:           "/tmp/orpheus-worktree",
		Branch:                 "orpheus/op-9xs.10",
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
		"- Current execution directory: /tmp/orpheus-worktree",
		"- Deterministic worktree: /tmp/orpheus-worktree",
		"- Deterministic branch: orpheus/op-9xs.10",
		"Work in the current repository directory, which is the deterministic task worktree.",
		"Do not commit manually",
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
	is.NotContains(prompt, "no isolated task worktree")
}
