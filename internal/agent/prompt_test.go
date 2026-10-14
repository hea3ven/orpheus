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

func TestRenderActiveContextDescribesTargetAndCompletionContract(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		kind                    agent.ExecutionTarget
		branch, path, directory string
		wants                   []string
	}{
		{"worktree", agent.ExecutionTargetWorktree, "orpheus/op-1", "/worktrees/op-1", "/worktrees/op-1/internal", []string{"deterministic task worktree and task branch", "to review and publish the feature branch as a pull request"}},
		{"main", agent.ExecutionTargetMain, "main", "/repo/alpha", "/repo/alpha", []string{"registered repository root on the registered default branch", "do not run `orpheus agent done` again", "do not run it yourself unless explicitly asked"}},
		{"repo root", agent.ExecutionTargetRepoRoot, "orpheus/op-1", "/repo/alpha", "/repo/alpha/internal", []string{"registered repository root on the task branch", "to review and publish the feature branch as a pull request"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := sampleWorktreeActiveContext()
			ctx.Task.ExternalRef = "TREX-1234"
			ctx.Target.Kind = tc.kind
			ctx.Target.Branch = tc.branch
			ctx.Target.Path = tc.path
			ctx.Target.CurrentDirectory = tc.directory

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
				"- Run attempt: 2",
				"- Agent: recorder",
				"orpheus agent done",
				"Do not create Git commits yourself. Leave completed changes uncommitted; Orpheus owns commit creation at the appropriate workflow stage.",
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
				"- External reference: TREX-1234",
			} {
				assert.Contains(t, output, want)
			}
			for _, want := range tc.wants {
				assert.Contains(t, output, want)
			}
			assert.Contains(t, output, "- Current branch: "+tc.branch)
			assert.Contains(t, output, "- Work Directory: "+tc.path)
			assert.Contains(t, output, "- Current directory: "+tc.directory)
			assert.Contains(t, output, "The human operator will later run `orpheus task run op-1`")
			assert.NotContains(t, output, "Beads")
			assert.NotContains(t, output, "bd")
			assert.NotContains(t, output, "Interaction guidance:")
			assert.Equal(t, output, agent.RenderActiveContextWithOptions(ctx, agent.ActiveContextRenderOptions{}))
			assert.Equal(t, output, agent.RenderActiveContextWithOptions(ctx, agent.ActiveContextRenderOptions{InteractionMode: agent.AgentInteractionModeUnspecified}))
		})
	}
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
		"Do not run `orpheus agent done`, `orpheus task run`, or `orpheus task done`",
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

func TestRenderActiveContextInteractionGuidance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mode     agent.AgentInteractionMode
		wants    []string
		excludes string
	}{
		{"interactive", agent.AgentInteractionModeInteractive, []string{
			"Interaction guidance:",
			"attached interactive implementation session",
			"may ask the human operator for clarification or decisions",
			"Minimize interruptions",
			"ask only for critical ambiguity or major product/architecture decisions",
			"Make low-risk, low-level implementation decisions independently",
		}, "do not ask the human operator for clarification or decisions"},
		{"noninteractive", agent.AgentInteractionModeNonInteractive, []string{
			"Interaction guidance:",
			"non-interactive implementation session",
			"do not ask the human operator for clarification or decisions",
			"Decide independently when a reasonable, low-risk path exists",
			"fail clearly",
			"missing information",
			"summarize significant decisions in the visible terminal/session output",
		}, "may ask the human operator for clarification or decisions"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := agent.RenderActiveContextWithOptions(sampleWorktreeActiveContext(), agent.ActiveContextRenderOptions{InteractionMode: tc.mode})

			for _, want := range tc.wants {
				assert.Contains(t, output, want)
			}
			assert.NotContains(t, output, tc.excludes)
		})
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
	} {
		assert.Contains(t, output, want)
	}
}

func TestRenderActiveContextSummaryGuidancePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, custom    string
		style           string
		wants, excludes []string
	}{
		{"custom overrides style", "Use sentence-case summaries without a type prefix.", registry.SummaryGuidanceStyleCapitalized,
			[]string{"Write `--summary` following this repository guidance: Use sentence-case summaries without a type prefix."},
			[]string{"one commit-style summary line, 80 characters or fewer", "<type(fix,feat,test,chore,conf,etc)>: <description>", "capitalized plain-English summary"}},
		{"capitalized", "", registry.SummaryGuidanceStyleCapitalized,
			[]string{"capitalized plain-English summary line", "with no task type prefix"},
			[]string{"<type(fix,feat,test,chore,conf,etc)>: <description>"}},
		{"default", "", "",
			[]string{"one commit-style summary line, 80 characters or fewer", "<type(fix,feat,test,chore,conf,etc)>: <description>"},
			[]string{"capitalized plain-English summary"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := sampleWorktreeActiveContext()
			ctx.Repository.SummaryGuidance = tc.custom
			ctx.Repository.SummaryGuidanceStyle = tc.style

			output := agent.RenderActiveContext(ctx)

			for _, want := range tc.wants {
				assert.Contains(t, output, want)
			}
			for _, unwanted := range tc.excludes {
				assert.NotContains(t, output, unwanted)
			}
		})
	}
}
