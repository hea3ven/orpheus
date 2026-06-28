package agent_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
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
		"The human operator will later run `orpheus task review op-1` to review and publish the feature branch as a pull request",
	} {
		is.Contains(output, want)
	}
	is.NotContains(output, "Beads")
	is.NotContains(output, "bd")
}

func TestRenderActiveContextIncludesExternalReference(t *testing.T) {
	output := agent.RenderActiveContext(agent.ActiveContext{
		Task: agent.ContextTask{ExternalRef: "TREX-1234"},
	})

	assert.Contains(t, output, "- External reference: TREX-1234")
}

func TestRenderActiveContextIncludesReviewFollowUpContract(t *testing.T) {
	output := agent.RenderActiveContext(agent.ActiveContext{
		Task: agent.ContextTask{ID: "op-1", Title: "Follow up"},
		Run:  agent.ContextRun{Attempt: 2},
		Target: agent.ContextTarget{
			Kind:             agent.ExecutionTargetMain,
			Branch:           "main",
			Path:             "/repo/alpha",
			CurrentDirectory: "/repo/alpha",
		},
		FollowUp: &agent.ContextFollowUp{
			ReviewAttempt: 1,
			Findings: []agent.ContextReviewFinding{
				{
					Index:           0,
					Title:           "Fix panic",
					Description:     "The command panics on empty input.",
					SuggestedAction: "Add input validation.",
				},
			},
		},
	})

	for _, want := range []string{
		"Review follow-up:",
		"- Review attempt: 1",
		"This is a continuation of completed work.",
		"Do not reimplement the original task.",
		"Address only the listed review findings.",
		"Preserve the current task branch and worktree target.",
		"Blocking findings:",
		"- Finding 1 title: Fix panic",
		"Description: The command panics on empty input.",
		"Suggested action: Add input validation.",
		"This run must address only the listed review findings",
	} {
		assert.Contains(t, output, want)
	}
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
		"The human operator will later run `orpheus task review op-main`",
		"do not run it yourself unless explicitly asked",
	} {
		is.Contains(output, want)
	}
	is.NotContains(output, "Beads")
	is.NotContains(output, "bd")
}

func TestRenderActiveContextUsesCustomSummaryGuidance(t *testing.T) {
	is := assert.New(t)
	guidance := "Use sentence-case summaries without a type prefix."

	output := agent.RenderActiveContext(agent.ActiveContext{
		Repository: agent.ContextRepository{
			ID:                   "alpha",
			Name:                 "Alpha Repo",
			Root:                 "/repo/alpha",
			DefaultBranch:        "main",
			SummaryGuidance:      guidance,
			SummaryGuidanceStyle: registry.SummaryGuidanceStyleCapitalized,
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

	is.Contains(output, "Write `--summary` following this repository guidance: "+guidance)
	is.NotContains(output, "one commit-style summary line, 80 characters or fewer")
	is.NotContains(output, "<type(fix,feat,test,chore,conf,etc)>: <description>")
	is.NotContains(output, "capitalized plain-English summary")
}

func TestRenderActiveContextUsesCapitalizedSummaryGuidance(t *testing.T) {
	output := agent.RenderActiveContext(agent.ActiveContext{
		Repository: agent.ContextRepository{
			SummaryGuidanceStyle: registry.SummaryGuidanceStyleCapitalized,
		},
		Target: agent.ContextTarget{Kind: agent.ExecutionTargetMain},
	})

	assert.Contains(t, output, "capitalized plain-English summary line")
	assert.Contains(t, output, "with no task type prefix")
	assert.Contains(t, output, "Replaced the config for abc")
	assert.NotContains(t, output, "<type(fix,feat,test,chore,conf,etc)>: <description>")
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
		"The human operator will later run `orpheus task review op-root` to review and publish the feature branch as a pull request",
	} {
		is.Contains(output, want)
	}
}
