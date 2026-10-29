package agent_test

import (
	"context"
	"errors"
	"fmt"
	workmodel "github.com/hea3ven/orpheus/internal/work"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeContextBackend struct {
	task taskmodel.Task
	err  error
}

func (b fakeContextBackend) Get(ctx context.Context, id string) (taskmodel.Task, error) {
	if b.err != nil {
		return taskmodel.Task{}, b.err
	}
	return b.task, nil
}

type contextMutation func(
	fixture *activeContextFixture,
	worktree string,
	taskItem *taskmodel.Task,
	env map[string]string,
	cwd *string,
)

func TestActiveContextResolverResolvesWorktreeTarget(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	fixture.repo.SummaryGuidance = "Use sentence-case summaries without a type prefix."
	worktree := fixture.expectedWorktree(t, "op-1")
	cwd := filepath.Join(worktree, "internal", "agent")
	must.NoError(testMkdirAll(cwd))

	taskItem := taskmodel.Task{
		ID:                 "op-1",
		Title:              "Resolve context",
		ExternalRef:        "TREX-1234",
		Description:        "Render active context.",
		AcceptanceCriteria: "Only active runs render.",
		Status:             taskmodel.StatusInProgress,
		Metadata: taskmodel.Metadata{
			taskmodel.MetadataBranch:   "orpheus/op-1",
			taskmodel.MetadataWorktree: worktree,
		},
	}
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: worktree, TaskBranch: "orpheus/op-1", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
		Agent:    "recorder",
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)

	resolver := fixture.resolver(taskItem, map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  "op-1",
		"ORPHEUS_WORKTREE": worktree,
		"ORPHEUS_BRANCH":   "orpheus/op-1",
	}, cwd)

	got, err := resolver.Resolve(context.Background())

	must.NoError(err)
	is.Equal("alpha", got.Repository.ID)
	is.Equal("Alpha Repo", got.Repository.Name)
	is.Equal(fixture.repoPath, got.Repository.Root)
	is.Equal("main", got.Repository.DefaultBranch)
	is.Equal(fixture.repo.SummaryGuidance, got.Repository.SummaryGuidance)
	is.Equal(registry.SummaryGuidanceStyleTyped, got.Repository.SummaryGuidanceStyle)
	is.Equal("op-1", got.Task.ID)
	is.Equal("Resolve context", got.Task.Title)
	is.Equal("TREX-1234", got.Task.ExternalRef)
	is.Equal(1, got.Run.Attempt)
	is.Equal("recorder", got.Run.Agent)
	is.Equal(agent.ExecutionTargetWorktree, got.Target.Kind)
	is.Equal("orpheus/op-1", got.Target.Branch)
	is.Equal(worktree, got.Target.Path)
	is.Equal(cwd, got.Target.CurrentDirectory)
	is.Equal(workmodel.AttemptRef{RepositoryID: "alpha", TaskID: "op-1", Phase: 1}, got.Run.Address)
	before, err := fixture.store.Load("alpha", "op-1")
	must.NoError(err)
	for _, invalid := range []struct{ name, value string }{
		{"ORPHEUS_WORKFLOW", ""}, {"ORPHEUS_WORKFLOW", "null"}, {"ORPHEUS_WORKFLOW", "-1"}, {"ORPHEUS_WORKFLOW", "1"},
		{"ORPHEUS_PHASE", ""}, {"ORPHEUS_PHASE", "0"}, {"ORPHEUS_STEP_INDEX", "0"}, {"ORPHEUS_BRANCH_SYNC_INDEX", "0"},
	} {
		t.Run(invalid.name+"="+invalid.value, func(t *testing.T) {
			stale := resolver
			stale.Env = make(map[string]string, len(resolver.Env))
			for name, value := range resolver.Env {
				stale.Env[name] = value
			}
			stale.Env[invalid.name] = invalid.value
			_, err := stale.Resolve(context.Background())
			require.Error(t, err, "omission/null cannot become a real zero index, and wrong-kind children cannot attach")
			after, err := fixture.store.Load("alpha", "op-1")
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
}

func TestActiveContextResolverUsesGlobalPublicationPolicy(t *testing.T) {
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-global-policy")
	must.NoError(testutil.WriteConfigYAML(fixture.paths, "config.yaml", map[string]any{
		"publication": map[string]any{
			"summary_guidance":       "Write a concise release note.",
			"summary_guidance_style": registry.SummaryGuidanceStyleCapitalized,
			"title_template":         "[GLOBAL] {{summary}}",
		},
	}))
	worktree := fixture.expectedWorktree(t, "op-global-policy")
	must.NoError(testMkdirAll(worktree))
	_, err := fixture.store.StartRun("alpha", "op-global-policy", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: worktree, TaskBranch: "orpheus/op-global-policy", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
		Agent:    "recorder",
		Branch:   "orpheus/op-global-policy",
		Worktree: worktree,
	})
	must.NoError(err)

	resolver := fixture.resolver(fixture.worktreeTask("op-global-policy", worktree), map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  "op-global-policy",
		"ORPHEUS_WORKTREE": worktree,
		"ORPHEUS_BRANCH":   "orpheus/op-global-policy",
	}, worktree)
	got, err := resolver.Resolve(context.Background())

	must.NoError(err)
	must.Equal("Write a concise release note.", got.Repository.SummaryGuidance)
	must.Equal(registry.SummaryGuidanceStyleCapitalized, got.Repository.SummaryGuidanceStyle)
	prompt := agent.RenderActiveContext(got)
	must.Contains(prompt, "Write `--summary` following this repository guidance")
	must.Contains(prompt, "Write a concise release note.")
	must.NotContains(prompt, "Use one capitalized plain-English summary line")
}

func TestActiveContextResolverResolvesRepoRootTargets(t *testing.T) {
	for _, tt := range []struct {
		name       string
		taskID     string
		branch     string
		cwdRel     string
		targetKind agent.ExecutionTarget
	}{
		{
			name:       "main",
			taskID:     "op-main",
			branch:     "main",
			cwdRel:     filepath.Join("cmd", "orpheus"),
			targetKind: agent.ExecutionTargetMain,
		},
		{
			name:       "task branch",
			taskID:     "op-root",
			branch:     "orpheus/op-root",
			cwdRel:     filepath.Join("internal", "cli"),
			targetKind: agent.ExecutionTargetRepoRoot,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newActiveContextFixture(t, tt.taskID)
			cwd := filepath.Join(fixture.repoPath, tt.cwdRel)
			must.NoError(testMkdirAll(cwd))

			taskItem := taskmodel.Task{
				ID:     tt.taskID,
				Status: taskmodel.StatusInProgress,
				Metadata: taskmodel.Metadata{
					taskmodel.MetadataBranch:   tt.branch,
					taskmodel.MetadataWorktree: fixture.repoPath,
				},
			}
			_, err := fixture.store.StartRun("alpha", tt.taskID, taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: fixture.repoPath, TaskBranch: tt.branch, CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
				Agent:    "recorder",
				Branch:   tt.branch,
				Worktree: fixture.repoPath,
			})
			must.NoError(err)

			resolver := fixture.resolver(taskItem, map[string]string{
				"ORPHEUS_REPO_ID":  "alpha",
				"ORPHEUS_TASK_ID":  tt.taskID,
				"ORPHEUS_WORKTREE": fixture.repoPath,
				"ORPHEUS_BRANCH":   tt.branch,
			}, cwd)

			got, err := resolver.Resolve(context.Background())

			must.NoError(err)
			is.Equal(tt.targetKind, got.Target.Kind)
			is.Equal(tt.branch, got.Target.Branch)
			is.Equal(fixture.repoPath, got.Target.Path)
			is.Equal(cwd, got.Target.CurrentDirectory)
		})
	}
}

func TestActiveContextResolverRejectsMetadataTaskstateTargetMismatch(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	taskItem := taskmodel.Task{
		ID:     "op-1",
		Status: taskmodel.StatusInProgress,
		Metadata: taskmodel.Metadata{
			taskmodel.MetadataBranch:   "main",
			taskmodel.MetadataWorktree: fixture.repoPath,
		},
	}
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: worktree, TaskBranch: "orpheus/op-1", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)

	resolver := fixture.resolver(taskItem, map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  "op-1",
		"ORPHEUS_WORKTREE": worktree,
		"ORPHEUS_BRANCH":   "orpheus/op-1",
	}, worktree)

	_, err = resolver.Resolve(context.Background())

	must.Error(err)
	is.Contains(err.Error(), "metadata target")
	is.Contains(err.Error(), "does not match taskstate target")
}

func TestActiveContextResolverRejectsNonCanonicalTaskstateTarget(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	expectedWorktree := fixture.expectedWorktree(t, "op-1")
	manualWorktree := filepath.Join(testutil.CanonicalTempDir(t), "manual-worktree")
	must.NoError(testMkdirAll(manualWorktree))
	taskItem := fixture.worktreeTask("op-1", expectedWorktree)
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: manualWorktree, TaskBranch: "orpheus/op-1", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
		Branch:   "orpheus/op-1",
		Worktree: manualWorktree,
	})
	must.NoError(err)

	resolver := fixture.resolver(taskItem, map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  "op-1",
		"ORPHEUS_WORKTREE": manualWorktree,
		"ORPHEUS_BRANCH":   "orpheus/op-1",
	}, manualWorktree)

	_, err = resolver.Resolve(context.Background())

	must.Error(err)
	is.Contains(err.Error(), "inconsistent taskstate target")
	is.Contains(err.Error(), "does not match an expected workflow target")
}

func TestActiveContextResolverRejectsLatestRunThatIsNotRunning(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	taskItem := fixture.worktreeTask("op-1", worktree)
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: worktree, TaskBranch: "orpheus/op-1", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)
	_, err = fixture.store.FinishObservedRun(workmodel.AttemptRef{RepositoryID: "alpha", TaskID: "op-1", Phase: 1}, &workmodel.ExitedProcess{Code: contextExit(0), At: time.Now().UTC()}, 0)
	must.NoError(err)

	resolver := fixture.resolver(taskItem, map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  "op-1",
		"ORPHEUS_WORKTREE": worktree,
		"ORPHEUS_BRANCH":   "orpheus/op-1",
	}, worktree)

	_, err = resolver.Resolve(context.Background())

	must.Error(err)
	is.Contains(err.Error(), "attempt does not own the active last work")
	is.Contains(err.Error(), "active last work")
}

func TestActiveContextResolverRejectsUnsafeOrInconsistentContext(t *testing.T) {
	for _, tt := range unsafeContextCases() {
		t.Run(tt.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			fixture := newActiveContextFixture(t, "op-1")
			worktree := fixture.expectedWorktree(t, "op-1")
			cwd := worktree
			taskItem := fixture.worktreeTask("op-1", worktree)
			env := map[string]string{
				"ORPHEUS_REPO_ID":  "alpha",
				"ORPHEUS_TASK_ID":  "op-1",
				"ORPHEUS_WORKTREE": worktree,
				"ORPHEUS_BRANCH":   "orpheus/op-1",
			}
			_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: worktree, TaskBranch: "orpheus/op-1", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
				Branch:   "orpheus/op-1",
				Worktree: worktree,
			})
			must.NoError(err)

			tt.mutate(fixture, worktree, &taskItem, env, &cwd)
			resolver := fixture.resolver(taskItem, env, cwd)

			_, err = resolver.Resolve(context.Background())

			must.Error(err)
			is.Contains(err.Error(), tt.wantError)
		})
	}
}

func unsafeContextCases() []struct {
	name      string
	mutate    contextMutation
	wantError string
} {
	return []struct {
		name      string
		mutate    contextMutation
		wantError string
	}{
		{
			name:      "environment worktree mismatch",
			mutate:    mutateEnvWorktreeMismatch,
			wantError: "ORPHEUS_WORKTREE",
		},
		{
			name:      "cwd outside target",
			mutate:    mutateCWDOutsideTarget,
			wantError: "outside the worktree/team execution target",
		},
		{
			name:      "closed task",
			mutate:    mutateClosedTask,
			wantError: "task op-1 is closed",
		},
		{
			name:      "task already has pull request URL",
			mutate:    mutateTaskWithPRURL,
			wantError: "already has a pull request URL recorded",
		},
		{
			name:      "metadata mismatch",
			mutate:    mutateMetadataMismatch,
			wantError: "inconsistent Orpheus metadata",
		},
	}
}

func mutateEnvWorktreeMismatch(
	fixture *activeContextFixture,
	_ string,
	_ *taskmodel.Task,
	env map[string]string,
	_ *string,
) {
	env["ORPHEUS_WORKTREE"] = fixture.repoPath
}

func mutateCWDOutsideTarget(
	fixture *activeContextFixture,
	_ string,
	_ *taskmodel.Task,
	_ map[string]string,
	cwd *string,
) {
	*cwd = filepath.Dir(fixture.repoPath)
}

func mutateClosedTask(
	_ *activeContextFixture,
	_ string,
	taskItem *taskmodel.Task,
	_ map[string]string,
	_ *string,
) {
	taskItem.Status = taskmodel.StatusClosed
}

func mutateTaskWithPRURL(
	_ *activeContextFixture,
	_ string,
	taskItem *taskmodel.Task,
	_ map[string]string,
	_ *string,
) {
	taskItem.Metadata[taskmodel.MetadataPRURL] = "https://example.test/pr/1"
}

func mutateMetadataMismatch(
	_ *activeContextFixture,
	_ string,
	taskItem *taskmodel.Task,
	_ map[string]string,
	_ *string,
) {
	taskItem.Metadata[taskmodel.MetadataBranch] = "other"
}

func TestActiveContextResolverWrapsBackendErrors(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Context: &workmodel.WorkContext{Directory: worktree, TaskBranch: "orpheus/op-1", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}},
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)

	cause := errors.New("backend unavailable")
	resolver := fixture.resolverWithBackend(fakeContextBackend{err: cause}, map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  "op-1",
		"ORPHEUS_WORKTREE": worktree,
		"ORPHEUS_BRANCH":   "orpheus/op-1",
	}, worktree)

	_, err = resolver.Resolve(context.Background())

	must.Error(err)
	is.Contains(err.Error(), "load task op-1 in repo alpha")
	is.ErrorIs(err, cause)
}

func TestRenderReviewContextRendersCompactPriorAuthoritativeFindings(t *testing.T) {
	context := reviewContextRenderFixture()
	context.Review.Attempt = 3
	context.Review.PriorFindings = []agent.ContextPriorReviewFinding{
		{Attempt: 1, Number: 1, Step: "ai-review", Type: taskstate.FindingTypeBlocking, Disposition: "waived", Title: "Known limitation"},
		{Attempt: 2, Number: 2, Step: "checks", Type: taskstate.FindingTypeSeparateTask, Disposition: "created task op-42", Title: "Extract helper"},
	}

	got := agent.RenderReviewContext(context)
	for _, want := range []string{
		"Prior authoritative findings:",
		"`1/1` · ai-review · blocking · waived · Known limitation",
		"`2/2` · checks · separate_task · created task op-42 · Extract helper",
		"`orpheus task show review op-1 <review-attempt> <finding-number>`",
		"do not repeat an unchanged accepted disposition",
		"newly applicable or its material circumstances changed",
	} {
		assert.Contains(t, got, want)
	}
	assert.NotContains(t, got, "waiver reason")
}

func TestRenderReviewContextCollapsesMultilinePriorFindingFields(t *testing.T) {
	context := reviewContextRenderFixture()
	context.Review.PriorFindings = []agent.ContextPriorReviewFinding{{
		Attempt:     1,
		Number:      1,
		Step:        "ai-review\nspoofed-step",
		Type:        taskstate.FindingType("blocking\nspoofed-type"),
		Disposition: "waived\nspoofed-disposition",
		Title:       "Known limitation\n- spoofed finding",
	}}

	got := agent.RenderReviewContext(context)
	assert.Contains(t, got, "`1/1` · ai-review spoofed-step · blocking spoofed-type · waived spoofed-disposition · Known limitation - spoofed finding")
	assert.NotContains(t, got, "\n- spoofed finding")
}

func TestRenderReviewContextLegacyModesRetainCompleteReviewInstructions(t *testing.T) {
	for _, toggle := range []string{"", "0"} {
		t.Run("toggle="+toggle, func(t *testing.T) {
			is := assert.New(t)
			t.Setenv("ORPHEUS_EXHAUSTIVE_REVIEW_CONTEXT", toggle)

			got := agent.RenderReviewContext(reviewContextRenderFixture())

			for _, want := range []string{
				"Review the complete change set before exiting, even if you find an issue early.",
				"Do not stop after the first issue; continue reviewing for additional distinct findings.",
				"- Technical explanation: Explains the implementation rationale for review.",
				"Record each distinct finding with its own `orpheus agent review add` call",
				"When multiple findings exist, run `orpheus agent review add` multiple times, once per finding.",
			} {
				is.Contains(got, want)
			}
			is.NotContains(got, "Follow this staged procedure before exiting:")
			is.NotContains(got, "Calling `orpheus agent review add` before completing the initial inspection")

			is.NotContains(got, "Exhaustive coverage is required within your assigned reviewer scope.")
		})
	}
}

func TestRenderReviewContextUsesStagedExhaustiveReviewWhenToggleEnabled(t *testing.T) {
	is := assert.New(t)
	t.Setenv("ORPHEUS_EXHAUSTIVE_REVIEW_CONTEXT", "1")

	got := agent.RenderReviewContext(reviewContextRenderFixture())

	for _, want := range []string{
		"Exhaustive coverage is required within your assigned reviewer scope.",
		"architecture reviewers must review the full relevant architectural change set without broadening into general code review",
		"1. Inventory the complete changed surface and the task acceptance criteria in scope.",
		"2. Inspect the relevant changes, tests, callers, error paths, and cross-cutting effects for that scope.",
		"3. Accumulate candidate findings privately. Do not call `orpheus agent review add` during this initial inspection.",
		"4. Perform a final coverage sweep against the inventory",
		"5. Only after the initial inspection and final coverage sweep are complete, record findings with `orpheus agent review add`.",
		"Calling `orpheus agent review add` before completing the initial inspection and final coverage sweep is prohibited.",
		"Record every collected distinct finding before exit, with a separate `orpheus agent review add` call for each finding.",
		"- Technical explanation: Explains the implementation rationale for review.",
	} {
		is.Contains(got, want)
	}
	is.NotContains(got, "Review the complete change set before exiting, even if you find an issue early.")
}

func TestRenderReviewContextsSafelyTransportFindingText(t *testing.T) {
	for _, tt := range []struct {
		name       string
		exhaustive string
	}{
		{name: "legacy", exhaustive: ""},
		{name: "exhaustive", exhaustive: "1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ORPHEUS_EXHAUSTIVE_REVIEW_CONTEXT", tt.exhaustive)
			output := agent.RenderReviewContext(reviewContextRenderFixture())

			for _, want := range []string{
				"Never place generated prose inside a double-quoted shell argument.",
				"JSON string escaping is not Bash quoting",
				"backticks and `$()` command substitutions",
				"Always use `--description-file` for a generated finding description.",
				"`--task-description-file` and `--task-acceptance-criteria-file`",
				"outside the candidate worktree",
				"Do not place arbitrary raw text in a fixed-delimiter heredoc",
				"base64-encode generated file contents",
				"`'O'\\''Brien'`",
				"printf '%s' '",
				"| base64 --decode >\"$report_dir/finding.md\"",
				"--title 'Missing validation for O'\\''Brien IDs'",
				"--suggested-action 'Validate O'\\''Brien IDs before saving'",
				"--task-title 'Extract O'\\''Brien validation helper'",
				"Verify every reporting command succeeded before exiting or retrying it.",
				"do not blindly retry a finding",
			} {
				assert.Contains(t, output, want)
			}
			for _, unsafe := range []string{
				"--title \"",
				"--suggested-action \"",
				"--task-title \"",
			} {
				assert.NotContains(t, output, unsafe)
			}

			separateTaskExample := renderedSeparateTaskExample(t, output)
			for _, want := range []string{
				"report_dir=$(mktemp -d /tmp/orpheus-review.XXXXXX)", // orpheus:allow-absolute-tmp-path -- verifies the operator-visible completion command, not a fixture path.
				"printf '%s' '",
				"| base64 --decode >\"$report_dir/finding.md\"",
				"| base64 --decode >\"$report_dir/task-description.md\"",
				"| base64 --decode >\"$report_dir/acceptance.md\"",
				"--description-file \"$report_dir/finding.md\"",
			} {
				assert.Contains(t, separateTaskExample, want)
			}
			assertBase64PayloadCarriesStandaloneDelimiterLine(t, separateTaskExample, "VGhlIHZhbGlkYXRpb24gaGVscGVyIGR1cGxpY2F0ZXMgYGV4aXN0aW5nIGJlaGF2aW9yYCBmb3IgTydCcmllbiB2YWx1ZXMuCkVPRgpUaGUgcmVwb3J0IHN0aWxsIHJlbWFpbnMgbGl0ZXJhbC4K")
		})
	}
}

func renderedSeparateTaskExample(t *testing.T, output string) string {
	t.Helper()

	findingOffset := strings.Index(output, "  --type separate-task \\\n")
	require.GreaterOrEqual(t, findingOffset, 0)
	startOffset := strings.LastIndex(output[:findingOffset], "```bash\n")
	require.GreaterOrEqual(t, startOffset, 0)
	endOffset := strings.Index(output[findingOffset:], "```\n")
	require.GreaterOrEqual(t, endOffset, 0)

	return output[startOffset : findingOffset+endOffset+len("```\n")]
}

func reviewContextRenderFixture() agent.ReviewContext {
	return agent.ReviewContext{
		Repository: agent.ContextRepository{
			ID:            "alpha",
			Name:          "Alpha Repo",
			Root:          "/repo",
			DefaultBranch: "main",
		},
		Task: agent.ContextTask{
			ID:    "op-1",
			Title: "Review context",
		},
		Target: agent.ContextTarget{
			Kind:             agent.ExecutionTargetMain,
			Branch:           "main",
			Path:             "/repo",
			CurrentDirectory: "/repo",
		},
		Review: agent.ContextReview{
			Attempt: 1,
			Step:    "ai-review",
			Completion: taskstate.Completion{
				Summary:              "Complete review context",
				Description:          "Implementation is ready for review.",
				DetailedDescription:  "PR body for review.",
				TechnicalExplanation: "Explains the implementation rationale for review.",
			},
		},
	}
}

type activeContextFixture struct {
	paths    state.Paths
	repoPath string
	repo     registry.Repo
	source   taskmodel.RepositorySource
	store    taskstate.Store
}

func newActiveContextFixture(t *testing.T, taskID string) *activeContextFixture {
	t.Helper()
	must := require.New(t)

	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(
		filepath.Join(root, "config", state.AppName),
		filepath.Join(root, "data", state.AppName),
	)
	must.NoError(err)
	repoPath := filepath.Join(root, "repo")
	must.NoError(testMkdirAll(repoPath))
	repo := registry.Repo{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}
	source := taskmodel.RepositorySource{
		Repository: taskmodel.Repository{
			ID:            repo.ID,
			Name:          repo.Name,
			TaskIDPrefix:  repo.BeadsPrefix,
			Path:          repo.Path,
			DefaultBranch: repo.DefaultBranch,
		},
		BackendDir: repo.Path,
	}

	return &activeContextFixture{
		paths:    paths,
		repoPath: repoPath,
		repo:     repo,
		source:   source,
		store:    taskstate.NewStore(paths),
	}
}

func (f *activeContextFixture) expectedWorktree(t *testing.T, taskID string) string {
	t.Helper()
	path, err := f.paths.DataPath(filepath.Join("repos", f.repo.ID, "worktrees", taskID))
	require.NoError(t, err)
	return path
}

func (f *activeContextFixture) worktreeTask(taskID string, worktree string) taskmodel.Task {
	return taskmodel.Task{
		ID:     taskID,
		Title:  "Worktree target",
		Status: taskmodel.StatusInProgress,
		Metadata: taskmodel.Metadata{
			taskmodel.MetadataBranch:   "orpheus/" + taskID,
			taskmodel.MetadataWorktree: worktree,
		},
	}
}

func (f *activeContextFixture) resolver(
	taskItem taskmodel.Task,
	env map[string]string,
	cwd string,
) agent.ActiveContextResolver {
	return f.resolverWithBackend(fakeContextBackend{task: taskItem}, env, cwd)
}

func (f *activeContextFixture) resolverWithBackend(
	backend fakeContextBackend,
	env map[string]string,
	cwd string,
) agent.ActiveContextResolver {
	seedContextOwnerEnvironment(f, env)
	return agent.ActiveContextResolver{
		Paths:          f.paths,
		Registry:       registry.Registry{Repos: []registry.Repo{f.repo}},
		Sources:        []taskmodel.RepositorySource{f.source},
		BackendFactory: func(source taskmodel.RepositorySource) (agent.ContextBackend, error) { return backend, nil },
		RunStore:       f.store,
		Env:            env,
		CWD:            cwd,
	}
}

func seedContextOwnerEnvironment(f *activeContextFixture, env map[string]string) {
	// These fixtures describe an actual dispatch. Ownership-refusal cases can
	// override or delete these entries after constructing the resolver.
	snapshot, err := f.store.Load("alpha", env["ORPHEUS_TASK_ID"])
	if err != nil || snapshot.Work == nil || len(snapshot.Work.Workflows) == 0 {
		return
	}
	wi := len(snapshot.Work.Workflows) - 1
	w := &snapshot.Work.Workflows[wi]
	if _, supplied := env["ORPHEUS_WORKFLOW"]; !supplied {
		env["ORPHEUS_WORKFLOW"] = fmt.Sprint(wi)
	}
	if _, supplied := env["ORPHEUS_PHASE"]; !supplied {
		env["ORPHEUS_PHASE"] = fmt.Sprint(len(w.Phases) - 1)
	}
	if review, ok := w.LastPhase().(*workmodel.ReviewPhase); ok && len(review.Steps) > 0 {
		if _, supplied := env["ORPHEUS_STEP_INDEX"]; !supplied {
			env["ORPHEUS_STEP_INDEX"] = fmt.Sprint(len(review.Steps) - 1)
		}
	}
	if external, ok := w.LastPhase().(*workmodel.ExternalReviewPhase); ok && len(external.BranchSyncs) > 0 {
		env["ORPHEUS_BRANCH_SYNC_INDEX"] = fmt.Sprint(len(external.BranchSyncs) - 1)
	}
}

func testMkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}

func contextExit(code int) *int { return &code }

// Context tests seed explicit synthetic history; native Git/process evidence has
// its own adapter owners. No application API infers success from a status here.
func contextFixtureHistory(directory string) workmodel.State {
	at := time.Now().UTC().Add(-time.Hour)
	return workmodel.State{RepositoryID: "alpha", TaskID: "op-1", Workflows: []workmodel.Workflow{{Phases: []workmodel.Phase{
		&workmodel.PreparationPhase{EstablishedAt: &at, Context: &workmodel.WorkContext{Directory: directory, TaskBranch: "orpheus/op-1", CheckpointRef: "refs/orpheus/fixture-baseline", Baseline: workmodel.CodeIdentity{Commit: "initial-commit", Tree: "initial-tree"}}},
		&workmodel.ImplementationPhase{Execution: workmodel.AgentExecution{Profile: "implementer", Harness: "pi", Command: "fixture-agent", StartedAt: at.Add(time.Second), Process: &workmodel.ExitedProcess{Code: contextExit(0), At: at.Add(3 * time.Second)}}, Completion: &workmodel.Completion{Summary: "Complete work", Description: "Implemented the requested work.", DetailedDescription: "Detailed PR body.", CompletedAt: at.Add(2 * time.Second)}, Output: &workmodel.CodeOutput{CodeIdentity: workmodel.CodeIdentity{Commit: "R1", Tree: "T1"}, RetainedRef: "refs/orpheus/fixture-output"}},
	}}}}
}
func saveContextHistory(t *testing.T, fixture *activeContextFixture, history workmodel.State) {
	t.Helper()
	_, err := workstate.NewStore(fixture.paths).Save(history, history.Version)
	require.NoError(t, err)
}
func contextReviewScope() workmodel.ReviewScope {
	return workmodel.ReviewScope{Requirements: "Implement the task safely", Output: workmodel.OutputRef{Phase: 1}, Destination: &workmodel.Destination{Remote: "origin", Branch: "main", Commit: "D"}}
}

func TestActiveContextResolverResolvesConflictResolutionContext(t *testing.T) {
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	cwd := filepath.Join(worktree, "internal")
	require.NoError(t, testMkdirAll(cwd))
	history := contextFixtureHistory(worktree)
	w := &history.Workflows[0]
	at := w.Phases[1].(*workmodel.ImplementationPhase).Execution.StartedAt
	w.ReviewContracts = []workmodel.ReviewContract{{PipelineKey: "review", Slots: []workmodel.ReviewSlot{{ConfigKey: "reviewer", Kind: workmodel.ReviewAgent}}}}
	w.Phases = append(w.Phases, &workmodel.ReviewPhase{Contract: 0, Scope: contextReviewScope(), StartedAt: at.Add(4 * time.Second), Steps: []workmodel.ReviewStep{&workmodel.AgentReviewStep{Slot: 0, Execution: workmodel.AgentExecution{StartedAt: at.Add(5 * time.Second), Process: &workmodel.ExitedProcess{At: at.Add(6 * time.Second), Code: contextExit(0)}}}}}, &workmodel.PublicationPhase{Plan: workmodel.PublicationPlan{Review: workmodel.ReviewRef{Phase: 2}, Target: &workmodel.PullRequestTarget{}, Content: "Reviewed task\n\nPR body"}})
	require.NoError(t, history.AuthorizePublication(0, workmodel.PublicationRef{Phase: 3}, at.Add(7*time.Second), "Explicit fixture grant"))
	w.Phases[3].(*workmodel.PublicationPhase).Delivery = &workmodel.PullRequestDelivery{PullRequest: workmodel.PullRequestRef{URL: "https://github.test/org/repo/pull/42"}, PublicCommit: "R1", Head: "R1", Tree: "T1", ObservedAt: at.Add(8 * time.Second), Evidence: "Observed fixture PR and remote head"}
	w.Phases = append(w.Phases, &workmodel.ExternalReviewPhase{Publication: workmodel.PublicationRef{Phase: 3}, BranchSyncs: []workmodel.BranchSync{{StartedAt: at.Add(9 * time.Second), Checkpoint: workmodel.BranchSyncCheckpoint{LocalHead: "R1", RemoteHead: "R1", MergeSource: "D2"}, ConflictResolution: &workmodel.ConflictResolution{Files: []string{"conflict.txt", "pkg/service.go"}, Execution: workmodel.AgentExecution{StartedAt: at.Add(10 * time.Second), Process: &workmodel.RunningProcess{}}}}}})
	saveContextHistory(t, fixture, history)
	taskItem := fixture.worktreeTask("op-1", worktree)
	taskItem.Metadata[taskmodel.MetadataPRURL] = "https://github.test/org/repo/pull/42"
	resolver := fixture.resolver(taskItem, map[string]string{"ORPHEUS_REPO_ID": "alpha", "ORPHEUS_TASK_ID": "op-1", "ORPHEUS_WORKTREE": worktree, "ORPHEUS_BRANCH": "orpheus/op-1", "ORPHEUS_CONFLICT_FILES": "conflict.txt\npkg/service.go\n"}, cwd)

	got, err := resolver.ResolveConflictResolution(context.Background())

	require.NoError(t, err)
	assert.Equal(t, "alpha", got.Repository.ID)
	assert.Equal(t, "op-1", got.Task.ID)
	assert.Equal(t, agent.ExecutionTargetWorktree, got.Target.Kind)
	assert.Equal(t, "orpheus/op-1", got.Target.Branch)
	assert.Equal(t, worktree, got.Target.Path)
	assert.Equal(t, cwd, got.Target.CurrentDirectory)
	assert.Equal(t, "https://github.test/org/repo/pull/42", got.PRURL)
	assert.Equal(t, []string{"conflict.txt", "pkg/service.go"}, got.ConflictFiles)
	assert.Equal(t, workmodel.AttemptRef{RepositoryID: "alpha", TaskID: "op-1", Phase: 4, BranchSync: contextExit(0)}, got.Address)
	resolver.Env["ORPHEUS_BRANCH_SYNC_INDEX"] = "1"
	_, err = resolver.ResolveConflictResolution(context.Background())
	require.Error(t, err, "a resolver cannot attach to a nonexistent newer sync")
	resolver.Env["ORPHEUS_BRANCH_SYNC_INDEX"] = "0"
	resolver.Env["ORPHEUS_PHASE"] = "3"
	_, err = resolver.ResolveConflictResolution(context.Background())
	require.Error(t, err, "publication is not a conflict-resolver execution")
}

func TestActiveContextResolverSeparatesRequiredAndAdvisoryFollowUpFindings(t *testing.T) {
	fixture := newActiveContextFixture(t, "op-1")
	directory := fixture.expectedWorktree(t, "op-1")
	require.NoError(t, testMkdirAll(directory))
	history := contextFixtureHistory(directory)
	w := &history.Workflows[0]
	at := w.Phases[1].(*workmodel.ImplementationPhase).Execution.StartedAt.Add(5 * time.Second)
	w.ReviewContracts = []workmodel.ReviewContract{{PipelineKey: "standard", Slots: []workmodel.ReviewSlot{{Kind: workmodel.ReviewManual, ConfigKey: "inspect"}}}}
	w.Phases = append(w.Phases, &workmodel.ReviewPhase{Contract: 0, Scope: contextReviewScope(), StartedAt: at, Steps: []workmodel.ReviewStep{&workmodel.ManualReviewStep{Slot: 0, Findings: []workmodel.Finding{
		{Classification: workmodel.FindingBlocking, Title: "Required repair", Description: "Fix the regression.", SuggestedAction: "Add the guard."},
		{Classification: workmodel.FindingAdvisory, Title: "Optional cleanup", Description: "Simplify the path.", SuggestedAction: "Extract a helper."},
		{Classification: workmodel.FindingBlocking, Title: "Downgraded opportunity", Description: "Still worth considering.", Dispositions: []workmodel.FindingDisposition{&workmodel.DowngradedFinding{Waiver: workmodel.Waiver{At: at.Add(time.Second), Reason: "Not required for this task."}}}},
	}, Decision: &workmodel.RequestedChanges{At: at.Add(2 * time.Second)}}}})
	saveContextHistory(t, fixture, history)
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1, FindingIndexes: []int{0}, AdvisoryFindingIndexes: []int{1, 2}}})
	require.NoError(t, err)
	resolver := fixture.resolver(fixture.worktreeTask("op-1", directory), worktreeEnv("op-1", directory), directory)

	resolved, err := resolver.Resolve(context.Background())

	require.NoError(t, err)
	require.NotNil(t, resolved.FollowUp)
	assert.Equal(t, []agent.ContextReviewFinding{{Index: 0, Title: "Required repair", Description: "Fix the regression.", SuggestedAction: "Add the guard."}}, resolved.FollowUp.RequiredFindings)
	assert.Equal(t, []agent.ContextReviewFinding{{Index: 1, Title: "Optional cleanup", Description: "Simplify the path.", SuggestedAction: "Extract a helper."}, {Index: 2, Title: "Downgraded opportunity", Description: "Still worth considering."}}, resolved.FollowUp.AdvisoryFindings)
	rendered := agent.RenderActiveContext(resolved)
	for _, text := range []string{"Required blocking findings:", "Advisory opportunities:", "Optional cleanup", "Consider advisory opportunities only when they remain applicable, task-scoped, and safe."} {
		assert.Contains(t, rendered, text)
	}
}

func TestActiveContextResolverRejectsReviewContextWhenLatestRunHasNoCompletion(t *testing.T) {
	fixture := newActiveContextFixture(t, "op-1")
	directory := fixture.expectedWorktree(t, "op-1")
	require.NoError(t, testMkdirAll(directory))
	history := contextFixtureHistory(directory)
	history.Workflows[0].Phases = append(history.Workflows[0].Phases, &workmodel.ImplementationPhase{Input: &workmodel.OutputRef{Phase: 1}, Execution: workmodel.AgentExecution{StartedAt: time.Now().UTC(), Process: &workmodel.RunningProcess{}}})
	saveContextHistory(t, fixture, history)
	resolver := fixture.resolver(fixture.worktreeTask("op-1", directory), map[string]string{"ORPHEUS_REPO_ID": "alpha", "ORPHEUS_TASK_ID": "op-1", "ORPHEUS_WORKTREE": directory, "ORPHEUS_BRANCH": "orpheus/op-1", "ORPHEUS_AGENT_PURPOSE": "review", "ORPHEUS_REVIEW_ATTEMPT": "1", "ORPHEUS_REVIEW_STEP": "ai-review", "ORPHEUS_STEP_INDEX": "0"}, directory)
	before, err := fixture.store.Load("alpha", "op-1")
	require.NoError(t, err)

	_, err = resolver.ResolveReview(context.Background())

	require.ErrorContains(t, err, "current implementation completion is required")
	after, err := fixture.store.Load("alpha", "op-1")
	require.NoError(t, err)
	assert.Equal(t, before, after, "context refusal cannot fabricate an approval or borrow an older handoff")
}

func TestActiveContextResolverProvidesEarlierAuthoritativeFindingsOnly(t *testing.T) {
	fixture := newActiveContextFixture(t, "op-1")
	directory := fixture.expectedWorktree(t, "op-1")
	require.NoError(t, testMkdirAll(directory))
	history := contextFixtureHistory(directory)
	w := &history.Workflows[0]
	at := w.Phases[1].(*workmodel.ImplementationPhase).Execution.StartedAt.Add(5 * time.Second)
	w.ReviewContracts = []workmodel.ReviewContract{{PipelineKey: "standard", Slots: []workmodel.ReviewSlot{{Kind: workmodel.ReviewAgent, ConfigKey: "ai-review"}}}}
	titles := []string{"Prior blocker", "Interrupted audit finding", "Current review finding"}
	for i, title := range titles {
		start := at.Add(time.Duration(i*10) * time.Second)
		var process workmodel.ProcessResult = &workmodel.RunningProcess{}
		if i == 0 {
			process = &workmodel.ExitedProcess{At: start.Add(time.Second), Code: contextExit(0)}
		}
		if i == 1 {
			process = &workmodel.InterruptedProcess{At: start.Add(time.Second), Reason: "supervisor disappeared", Trigger: "recovery"}
		}
		finding := workmodel.Finding{Classification: workmodel.FindingBlocking, Title: title, Description: "Reported defect."}
		if i == 0 {
			finding.Dispositions = []workmodel.FindingDisposition{&workmodel.WaivedFinding{Waiver: workmodel.Waiver{At: start.Add(2 * time.Second), Reason: "Accepted before the new review."}}}
		}
		w.Phases = append(w.Phases, &workmodel.ReviewPhase{Contract: 0, Scope: contextReviewScope(), StartedAt: start, Steps: []workmodel.ReviewStep{&workmodel.AgentReviewStep{Slot: 0, Execution: workmodel.AgentExecution{StartedAt: start, Process: process}, Report: &workmodel.ReviewReport{Findings: []workmodel.Finding{finding}}}}})
	}
	saveContextHistory(t, fixture, history)
	resolver := fixture.resolver(fixture.worktreeTask("op-1", directory), map[string]string{"ORPHEUS_REPO_ID": "alpha", "ORPHEUS_TASK_ID": "op-1", "ORPHEUS_WORKTREE": directory, "ORPHEUS_BRANCH": "orpheus/op-1", "ORPHEUS_AGENT_PURPOSE": "review", "ORPHEUS_REVIEW_ATTEMPT": "3", "ORPHEUS_REVIEW_STEP": "ai-review"}, directory)

	resolved, err := resolver.ResolveReview(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []agent.ContextPriorReviewFinding{{Attempt: 1, Number: 1, Step: "ai-review", Type: taskstate.FindingTypeBlocking, Disposition: "waived", Title: "Prior blocker"}}, resolved.Review.PriorFindings)
	rendered := agent.RenderReviewContext(resolved)
	assert.NotContains(t, rendered, "Interrupted audit finding")
	assert.NotContains(t, rendered, "Current review finding")

	t.Run("reset ordinals cannot borrow archived repair status", func(t *testing.T) {
		fixture := newActiveContextFixture(t, "op-1")
		directory := fixture.expectedWorktree(t, "op-1")
		require.NoError(t, testMkdirAll(directory))
		history := contextFixtureHistory(directory)
		old := history.Workflows[0]
		original := old.Phases[1].(*workmodel.ImplementationPhase)
		failed := *original
		failed.Output, failed.Completion = nil, nil
		failed.Input = &workmodel.OutputRef{Phase: 1}
		failed.Execution.StartedAt = original.Execution.StartedAt.Add(time.Minute)
		failed.Execution.Process = &workmodel.ExitedProcess{At: failed.Execution.StartedAt.Add(time.Second), Code: contextExit(1)}
		old.Phases = append(old.Phases, &failed)
		current := contextFixtureHistory(directory).Workflows[0]
		current.ReviewContracts = []workmodel.ReviewContract{{PipelineKey: "standard", Slots: []workmodel.ReviewSlot{{Kind: workmodel.ReviewAgent, ConfigKey: "ai-review"}}}}
		start := current.Phases[1].(*workmodel.ImplementationPhase).Execution.StartedAt.Add(5 * time.Second)
		prior := &workmodel.ReviewPhase{Contract: 0, Scope: contextReviewScope(), StartedAt: start, Steps: []workmodel.ReviewStep{
			&workmodel.AgentReviewStep{Slot: 0, Execution: workmodel.AgentExecution{StartedAt: start, Process: &workmodel.ExitedProcess{At: start.Add(time.Second), Code: contextExit(0)}}, Report: &workmodel.ReviewReport{Findings: []workmodel.Finding{{Classification: workmodel.FindingBlocking, Title: "Current workflow blocker", Description: "Repair this defect."}}}},
		}}
		repair := *current.Phases[1].(*workmodel.ImplementationPhase)
		completion := *repair.Completion
		completion.Summary = "fix: current work"
		completion.CompletedAt = start.Add(11 * time.Second)
		repair.Completion = &completion
		repair.Input = &workmodel.OutputRef{Phase: 1}
		repair.TargetFindings = []workmodel.FindingRef{{Phase: 2, Step: 0, Finding: 0}}
		repair.Execution.StartedAt = start.Add(10 * time.Second)
		repair.Execution.Process = &workmodel.ExitedProcess{At: start.Add(12 * time.Second), Code: contextExit(0)}
		repair.Output = &workmodel.CodeOutput{CodeIdentity: workmodel.CodeIdentity{Commit: "R2", Tree: "T2"}, RetainedRef: "refs/orpheus/current-repair"}
		scope := contextReviewScope()
		scope.Output, scope.DeltaFrom = workmodel.OutputRef{Phase: 3}, &workmodel.OutputRef{Phase: 1}
		current.Phases = append(current.Phases, prior, &repair, &workmodel.ReviewPhase{Contract: 0, Scope: scope, StartedAt: start.Add(20 * time.Second), Steps: []workmodel.ReviewStep{&workmodel.AgentReviewStep{Slot: 0, Execution: workmodel.AgentExecution{StartedAt: start.Add(20 * time.Second), Process: &workmodel.RunningProcess{}}}}})
		history.Workflows = []workmodel.Workflow{old, current}
		saveContextHistory(t, fixture, history)
		resolver := fixture.resolver(fixture.worktreeTask("op-1", directory), map[string]string{"ORPHEUS_REPO_ID": "alpha", "ORPHEUS_TASK_ID": "op-1", "ORPHEUS_WORKTREE": directory, "ORPHEUS_BRANCH": "orpheus/op-1", "ORPHEUS_AGENT_PURPOSE": "review", "ORPHEUS_REVIEW_ATTEMPT": "2", "ORPHEUS_REVIEW_STEP": "ai-review"}, directory)

		resolved, err := resolver.ResolveReview(context.Background())

		require.NoError(t, err)
		assert.Equal(t, []agent.ContextPriorReviewFinding{{Attempt: 1, Number: 1, Step: "ai-review", Type: taskstate.FindingTypeBlocking, Disposition: "follow-up run 2 succeeded", Title: "Current workflow blocker"}}, resolved.Review.PriorFindings)
		require.NotNil(t, resolved.Review.OriginalCompletion)
		assert.Equal(t, "Complete work", resolved.Review.OriginalCompletion.Summary)
		require.NotNil(t, resolved.Review.LatestFixCompletion)
		assert.Equal(t, "fix: current work", resolved.Review.LatestFixCompletion.Summary)
	})
}
