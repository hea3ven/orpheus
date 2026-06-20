package agent_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/stretchr/testify/assert"
)

func TestRenderBootstrapPromptTellsAgentToFetchContext(t *testing.T) {
	is := assert.New(t)

	prompt := agent.RenderBootstrapPrompt()

	is.Contains(prompt, "You are an attached implementation agent dispatched by Orpheus.")
	is.Contains(prompt, "Run `orpheus agent context` now")
	is.Contains(prompt, "task instructions")
	is.Contains(prompt, "execution contract")
	is.NotContains(prompt, "Task:")
	is.NotContains(prompt, "Repository:")
	is.NotContains(prompt, "Summary:")
	is.NotContains(prompt, "Beads")
	is.NotContains(prompt, "bd")
}

func TestRenderActiveContextIncludesWorktreeContract(t *testing.T) {
	is := assert.New(t)

	output := agent.RenderActiveContext(agent.ActiveContext{
		Repository: agent.ContextRepository{
			ID:            "alpha",
			Name:          "Alpha Repo",
			Root:          "/repo/alpha",
			DefaultBranch: "main",
		},
		Task: agent.ContextTask{
			ID:                 "op-1",
			Title:              "Implement context",
			Description:        "Resolve the active run.",
			AcceptanceCriteria: "Context renders only for running attempts.",
		},
		Run: agent.ContextRun{
			Attempt: 2,
			Agent:   "recorder",
		},
		Target: agent.ContextTarget{
			Kind:             agent.ExecutionTargetWorktree,
			Branch:           "orpheus/op-1",
			Path:             "/worktrees/op-1",
			CurrentDirectory: "/worktrees/op-1/internal",
		},
	})

	for _, want := range []string{
		"# Orpheus Agent Context",
		"- ID: op-1",
		"- Title: Implement context",
		"- Description: Resolve the active run.",
		"- Acceptance criteria: Context renders only for running attempts.",
		"- ID: alpha",
		"- Name: Alpha Repo",
		"- Registered root: /repo/alpha",
		"- Registered default branch: main",
		"- Workflow: worktree/team",
		"- Branch: orpheus/op-1",
		"- Path: /worktrees/op-1",
		"- Current directory: /worktrees/op-1/internal",
		"- Run attempt: 2",
		"- Agent: recorder",
		"deterministic task worktree and task branch",
		"orpheus agent done",
		"one commit-style summary line, 80 characters or fewer",
		"<type(fix,feat,test,chore,conf,etc)>: <description>",
		"do not include the task/bead ID",
		"do not mention tests even if included",
		"one-time completion handoff",
		"run it at most once",
		"do not run it again after it succeeds",
		"PR-ready completion data for feature-branch publication",
		"The human operator will later run `orpheus task done op-1` to publish the feature branch as a pull request",
	} {
		is.Contains(output, want)
	}
	is.NotContains(output, "Beads")
	is.NotContains(output, "bd")
}

func TestRenderActiveContextIncludesMainContract(t *testing.T) {
	is := assert.New(t)

	output := agent.RenderActiveContext(agent.ActiveContext{
		Repository: agent.ContextRepository{
			ID:            "alpha",
			Name:          "Alpha Repo",
			Root:          "/repo/alpha",
			DefaultBranch: "main",
		},
		Task: agent.ContextTask{ID: "op-main", Title: "Main target"},
		Run:  agent.ContextRun{Attempt: 1},
		Target: agent.ContextTarget{
			Kind:             agent.ExecutionTargetMain,
			Branch:           "main",
			Path:             "/repo/alpha",
			CurrentDirectory: "/repo/alpha",
		},
	})

	for _, want := range []string{
		"- Workflow: main/solo",
		"registered repository root on the registered default branch",
		"one-time completion handoff",
		"one commit-style summary line, 80 characters or fewer",
		"<type(fix,feat,test,chore,conf,etc)>: <description>",
		"do not include the task/bead ID",
		"do not mention tests even if included",
		"run it at most once",
		"do not run it again after it succeeds",
		"Orpheus will record local-review-ready completion data",
		"The human operator will later run `orpheus task done op-main`",
		"do not run it yourself unless explicitly asked",
	} {
		is.Contains(output, want)
	}
	is.NotContains(output, "Beads")
	is.NotContains(output, "bd")
}

func TestRenderActiveContextIncludesRepoRootTaskBranchContract(t *testing.T) {
	is := assert.New(t)

	output := agent.RenderActiveContext(agent.ActiveContext{
		Repository: agent.ContextRepository{
			ID:            "alpha",
			Name:          "Alpha Repo",
			Root:          "/repo/alpha",
			DefaultBranch: "main",
		},
		Task: agent.ContextTask{ID: "op-root", Title: "Repo root"},
		Run:  agent.ContextRun{Attempt: 1},
		Target: agent.ContextTarget{
			Kind:             agent.ExecutionTargetRepoRoot,
			Branch:           "orpheus/op-root",
			Path:             "/repo/alpha",
			CurrentDirectory: "/repo/alpha/internal",
		},
	})

	for _, want := range []string{
		"- Workflow: repo-root/team",
		"- Branch: orpheus/op-root",
		"- Path: /repo/alpha",
		"registered repository root on the task branch",
		"orpheus agent done",
		"PR-ready completion data for feature-branch publication",
		"The human operator will later run `orpheus task done op-root` to publish the feature branch as a pull request",
	} {
		is.Contains(output, want)
	}
}
