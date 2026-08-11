package agent_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/stretchr/testify/assert"
)

func TestRenderBootstrapPromptTellsAgentToFetchContext(t *testing.T) {
	is := assert.New(t)

	prompt := agent.RenderBootstrapPrompt()

	is.Contains(prompt, "You are an agent dispatched by Orpheus.")
	is.Contains(prompt, "Run `orpheus agent context` now")
	is.Contains(prompt, "task instructions")
	is.Contains(prompt, "execution contract")
	is.NotContains(prompt, "Task:")
	is.NotContains(prompt, "Repository:")
	is.NotContains(prompt, "Summary:")
	is.NotContains(prompt, "Beads")
	is.NotContains(prompt, "bd")
}

func TestRenderEffectivePromptAppendsSupplementalInstructions(t *testing.T) {
	is := assert.New(t)

	blank := agent.RenderEffectivePrompt(" \n\t ")
	is.Equal(agent.RenderBootstrapPrompt(), blank)

	prompt := agent.RenderEffectivePrompt("Review architecture boundaries.\nCheck dependency direction.")
	is.Contains(prompt, agent.RenderBootstrapPrompt())
	is.Contains(prompt, "\nSupplemental instructions:\n")
	is.Contains(prompt, "Review architecture boundaries.\nCheck dependency direction.\n")
	is.Less(
		strings.Index(prompt, "Run `orpheus agent context` now"),
		strings.Index(prompt, "Supplemental instructions:"),
	)
}

func TestRenderActiveContextIncludesWorktreeContract(t *testing.T) {
	is := assert.New(t)

	ctx := sampleWorktreeActiveContext()
	output := agent.RenderActiveContext(ctx)

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
		"- Current branch: orpheus/op-1",
		"- Work Directory: /worktrees/op-1",
		"- Current directory: /worktrees/op-1/internal",
		"- Run attempt: 2",
		"- Agent: recorder",
		"deterministic task worktree and task branch",
		"orpheus agent done",
		"one commit-style summary line, 80 characters or fewer",
		"<type(fix,feat,test,chore,conf,etc)>: <description>",
		"do not include the task/bead ID",
		"do not mention tests even if included",
		"--technical-explanation",
		"one technical explanation source",
		"implementation rationale and notable code changes",
		"one-time completion handoff for this Orpheus run attempt",
		"not once per reusable harness session",
		"call it exactly once after finishing the current attempt's work",
		"whether this harness session is fresh or resumed",
		"successful `orpheus agent done` visible in resumed session history belongs to an earlier",
		"does not satisfy the current attempt",
		"repeated same-attempt calls are no-ops",
		"first handoff remains authoritative",
		"PR-ready completion data for feature-branch publication",
		"The human operator will later run `orpheus task run op-1` to review and publish the feature branch as a pull request",
	} {
		is.Contains(output, want)
	}
	is.NotContains(output, "Beads")
	is.NotContains(output, "bd")
	is.NotContains(output, "Interaction guidance:")
	is.Equal(output, agent.RenderActiveContextWithOptions(ctx, agent.ActiveContextRenderOptions{}))
	is.Equal(output, agent.RenderActiveContextWithOptions(
		ctx,
		agent.ActiveContextRenderOptions{InteractionMode: agent.AgentInteractionModeUnspecified},
	))
}

func TestRenderActiveContextsSafelyTransportCompletionText(t *testing.T) {
	contexts := map[string]agent.ActiveContext{
		"implementation": sampleWorktreeActiveContext(),
		"follow-up": {
			Task:   agent.ContextTask{ID: "op-1"},
			Run:    agent.ContextRun{Attempt: 2},
			Target: agent.ContextTarget{Kind: agent.ExecutionTargetMain},
			FollowUp: &agent.ContextFollowUp{
				ReviewAttempt: 1,
			},
		},
	}

	for name, ctx := range contexts {
		t.Run(name, func(t *testing.T) {
			output := agent.RenderActiveContext(ctx)

			for _, want := range []string{
				"Never place generated prose inside a double-quoted shell argument.",
				"JSON string escaping is not Bash quoting",
				"backticks and `$()` command substitutions",
				"Prefer the existing file flags for multiline or Markdown content.",
				"Do not place arbitrary raw text in a fixed-delimiter heredoc",
				"base64-encode generated file contents",
				"`'O'\\''Brien'`",
				"printf '%s' '",
				"| base64 --decode >\"$report_dir/pr-body.md\"",
				"--summary 'fix: preserve O'\\''Brien reporting'",
				"--description 'Preserve O'\\''Brien reporting text.'",
				"--detailed-description-file \"$report_dir/pr-body.md\"",
				"--technical-explanation-file \"$report_dir/technical-explanation.md\"",
				"Verify every reporting command succeeded before exiting or retrying it.",
			} {
				assert.Contains(t, output, want)
			}
			assertBase64PayloadCarriesStandaloneDelimiterLine(t, output, "IyMgUHJlc2VydmUgbGl0ZXJhbCBNYXJrZG93bgoKS2VlcCBgYmFja3RpY2tzYCwgJChjb21tYW5kcyksIGEgc3RhbmRhbG9uZSBkZWxpbWl0ZXI6CkVPRgphbmQgTydCcmllbiBhcyB3cml0dGVuLgo=")
			for _, unsafe := range []string{
				"--summary \"",
				"--description \"",
				"--detailed-description \"",
				"--technical-explanation \"",
			} {
				assert.NotContains(t, output, unsafe)
			}
		})
	}
}

func assertBase64PayloadCarriesStandaloneDelimiterLine(t *testing.T, output string, payload string) {
	t.Helper()

	assert.Contains(t, output, payload)
	assert.NotContains(t, payload, "'")
	decoded, err := base64.StdEncoding.DecodeString(payload)
	assert.NoError(t, err)
	assert.Contains(t, string(decoded), "\nEOF\n")
	assert.NotContains(t, output, "<<'EOF'")
}

func TestRenderConflictResolutionContextConstrainsAgentScope(t *testing.T) {
	output := agent.RenderConflictResolutionContext(agent.ConflictResolutionContext{
		Repository: agent.ContextRepository{
			ID:            "alpha",
			Name:          "Alpha Repo",
			Root:          "/repo/alpha",
			DefaultBranch: "main",
		},
		Task: agent.ContextTask{
			ID:          "op-1",
			Title:       "Resolve sync",
			Description: "Original task context.",
		},
		Target: agent.ContextTarget{
			Kind:             agent.ExecutionTargetWorktree,
			Branch:           "orpheus/op-1",
			Path:             "/worktrees/op-1",
			CurrentDirectory: "/worktrees/op-1",
		},
		PRURL:         "https://github.test/org/repo/pull/42",
		ConflictFiles: []string{"conflict.txt", "pkg/service.go"},
	})

	for _, want := range []string{
		"# Orpheus Sync Conflict Resolution Context",
		"- ID: op-1",
		"- Pull request: https://github.test/org/repo/pull/42",
		"- Registered default branch: main",
		"- Current branch: orpheus/op-1",
		"Resolve only the merge conflicts",
		"Do not implement unrelated task changes",
		"  - conflict.txt",
		"  - pkg/service.go",
		"non-interactive sync conflict-resolution session",
		"Do not run `orpheus agent done`, `orpheus task run`, `orpheus task review`, or `orpheus task done`",
		"Do not create commits, push branches",
		"Leave the merge in progress",
		"Orpheus sync will commit and push after you exit",
	} {
		assert.Contains(t, output, want)
	}
	assert.NotContains(t, output, "one-time completion handoff")
	assert.NotContains(t, output, "PR-ready completion data")
}

func sampleWorktreeActiveContext() agent.ActiveContext {
	return agent.ActiveContext{
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
	}
}

func TestRenderActiveContextIncludesExternalReference(t *testing.T) {
	output := agent.RenderActiveContext(agent.ActiveContext{
		Task: agent.ContextTask{ExternalRef: "TREX-1234"},
	})

	assert.Contains(t, output, "- External reference: TREX-1234")
}

func TestRenderActiveContextIncludesOptInInteractiveGuidance(t *testing.T) {
	output := agent.RenderActiveContextWithOptions(
		agent.ActiveContext{
			Task:   agent.ContextTask{ID: "op-1"},
			Target: agent.ContextTarget{Kind: agent.ExecutionTargetMain},
		},
		agent.ActiveContextRenderOptions{InteractionMode: agent.AgentInteractionModeInteractive},
	)

	for _, want := range []string{
		"Interaction guidance:",
		"attached interactive implementation session",
		"may ask the human operator for clarification or decisions",
		"Minimize interruptions",
		"ask only for critical ambiguity or major product/architecture decisions",
		"Make low-risk, low-level implementation decisions independently",
	} {
		assert.Contains(t, output, want)
	}
}

func TestRenderActiveContextIncludesNonInteractiveGuidance(t *testing.T) {
	output := agent.RenderActiveContextWithOptions(
		agent.ActiveContext{
			Task:   agent.ContextTask{ID: "op-1"},
			Target: agent.ContextTarget{Kind: agent.ExecutionTargetMain},
		},
		agent.ActiveContextRenderOptions{InteractionMode: agent.AgentInteractionModeNonInteractive},
	)

	for _, want := range []string{
		"Interaction guidance:",
		"non-interactive implementation session",
		"do not ask the human operator for clarification or decisions",
		"Decide independently when a reasonable, low-risk path exists",
		"fail clearly",
		"missing information",
		"summarize significant decisions in the visible terminal/session output",
	} {
		assert.Contains(t, output, want)
	}
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
			RequiredFindings: []agent.ContextReviewFinding{
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
		"Fix every required blocking finding before completing this run.",
		"Consider advisory opportunities only when they remain applicable, task-scoped, and safe.",
		"Advisory work is best-effort",
		"Preserve the current task branch and worktree target.",
		"This Orpheus run attempt is a new completion boundary",
		"call `orpheus agent done` exactly once for the current attempt",
		"successful `orpheus agent done` visible in resumed session history belongs to an earlier attempt",
		"does not complete this follow-up",
		"Required blocking findings:",
		"Advisory opportunities:",
		"- Finding 1 title: Fix panic",
		"Description: The command panics on empty input.",
		"Suggested action: Add input validation.",
		"Fix the required blocking findings; advisories are best-effort",
		"one-time completion handoff for this Orpheus run attempt",
		"not once per reusable harness session",
		"whether this harness session is fresh or resumed",
		"does not satisfy the current attempt",
		"repeated same-attempt calls are no-ops",
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
		"registered repository root on the registered default branch",
		"one-time completion handoff for this Orpheus run attempt",
		"not once per reusable harness session",
		"one commit-style summary line, 80 characters or fewer",
		"<type(fix,feat,test,chore,conf,etc)>: <description>",
		"do not include the task/bead ID",
		"do not mention tests even if included",
		"--technical-explanation",
		"one technical explanation source",
		"call it exactly once after finishing the current attempt's work",
		"whether this harness session is fresh or resumed",
		"do not run `orpheus agent done` again",
		"repeated same-attempt calls are no-ops",
		"Orpheus will record PR-ready completion data for feature-branch publication",
		"The human operator will later run `orpheus task run op-main`",
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
		"- Current branch: orpheus/op-root",
		"- Work Directory: /repo/alpha",
		"registered repository root on the task branch",
		"orpheus agent done",
		"PR-ready completion data for feature-branch publication",
		"The human operator will later run `orpheus task run op-root` to review and publish the feature branch as a pull request",
	} {
		is.Contains(output, want)
	}
}
