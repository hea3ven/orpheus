package agent_test

import (
	"context"
	"errors"
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
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
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
}

func TestActiveContextResolverUsesGlobalPublicationPolicy(t *testing.T) {
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-global-policy")
	must.NoError(fixture.paths.WriteConfigYAML("config.yaml", map[string]any{
		"publication": map[string]any{
			"summary_guidance":       "Write a concise release note.",
			"summary_guidance_style": registry.SummaryGuidanceStyleCapitalized,
			"title_template":         "[GLOBAL] {{summary}}",
		},
	}))
	worktree := fixture.expectedWorktree(t, "op-global-policy")
	must.NoError(testMkdirAll(worktree))
	_, err := fixture.store.StartRun("alpha", "op-global-policy", taskstate.StartRunOptions{
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
			_, err := fixture.store.StartRun("alpha", tt.taskID, taskstate.StartRunOptions{
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

func TestActiveContextResolverResolvesConflictResolutionContext(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	cwd := filepath.Join(worktree, "internal")
	must.NoError(testMkdirAll(cwd))

	taskItem := taskmodel.Task{
		ID:          "op-1",
		Title:       "Conflict task",
		Description: "Original task.",
		Status:      taskmodel.StatusInProgress,
		Metadata: taskmodel.Metadata{
			taskmodel.MetadataBranch:   "orpheus/op-1",
			taskmodel.MetadataWorktree: worktree,
			taskmodel.MetadataPRURL:    "https://github.test/org/repo/pull/42",
		},
	}
	resolver := fixture.resolver(taskItem, map[string]string{
		"ORPHEUS_REPO_ID":        "alpha",
		"ORPHEUS_TASK_ID":        "op-1",
		"ORPHEUS_WORKTREE":       worktree,
		"ORPHEUS_BRANCH":         "orpheus/op-1",
		"ORPHEUS_CONFLICT_FILES": "conflict.txt\npkg/service.go\n",
	}, cwd)

	got, err := resolver.ResolveConflictResolution(context.Background())

	must.NoError(err)
	is.Equal("alpha", got.Repository.ID)
	is.Equal("op-1", got.Task.ID)
	is.Equal(agent.ExecutionTargetWorktree, got.Target.Kind)
	is.Equal("orpheus/op-1", got.Target.Branch)
	is.Equal(worktree, got.Target.Path)
	is.Equal(cwd, got.Target.CurrentDirectory)
	is.Equal("https://github.test/org/repo/pull/42", got.PRURL)
	is.Equal([]string{"conflict.txt", "pkg/service.go"}, got.ConflictFiles)
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
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
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
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
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
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)
	_, err = fixture.store.FinishRun("alpha", "op-1", 1, taskstate.RunStatusSucceeded)
	must.NoError(err)

	resolver := fixture.resolver(taskItem, map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  "op-1",
		"ORPHEUS_WORKTREE": worktree,
		"ORPHEUS_BRANCH":   "orpheus/op-1",
	}, worktree)

	_, err = resolver.Resolve(context.Background())

	must.Error(err)
	is.Contains(err.Error(), "latest Orpheus run attempt 1")
	is.Contains(err.Error(), `expected "running"`)
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
			_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
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
	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Branch:   "orpheus/op-1",
		Worktree: worktree,
	})
	must.NoError(err)

	resolver := fixture.resolverWithBackend(fakeContextBackend{err: errors.New("backend unavailable")}, map[string]string{
		"ORPHEUS_REPO_ID":  "alpha",
		"ORPHEUS_TASK_ID":  "op-1",
		"ORPHEUS_WORKTREE": worktree,
		"ORPHEUS_BRANCH":   "orpheus/op-1",
	}, worktree)

	_, err = resolver.Resolve(context.Background())

	must.Error(err)
	is.Contains(err.Error(), "load task op-1 in repo alpha")
	is.Contains(err.Error(), "backend unavailable")
}

func TestActiveContextResolverSeparatesRequiredAndAdvisoryFollowUpFindings(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	must.NoError(testMkdirAll(worktree))
	taskItem := fixture.worktreeTask("op-1", worktree)
	recordCompletedContextRun(t, fixture, worktree, nil)

	review, err := fixture.store.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{Pipeline: "standard", Step: "inspect"})
	must.NoError(err)
	for _, finding := range []taskstate.ReviewFinding{
		{Type: taskstate.FindingTypeBlocking, Title: "Required repair", Description: "Fix the regression.", SuggestedAction: "Add the guard."},
		{Type: taskstate.FindingTypeAdvisory, Title: "Optional cleanup", Description: "Simplify the path.", SuggestedAction: "Extract a helper."},
		{Type: taskstate.FindingTypeAdvisory, Title: "Downgraded opportunity", Description: "Still worth considering.", DowngradeReason: "Not required for this task."},
	} {
		_, err = fixture.store.RecordReviewFinding("alpha", "op-1", review.Attempt, finding)
		must.NoError(err)
	}
	_, err = fixture.store.FinishReview("alpha", "op-1", review.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)
	followUp, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent: "implementer", Branch: "orpheus/op-1", Worktree: worktree,
		ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: review.Attempt, FindingIndexes: []int{0}, AdvisoryFindingIndexes: []int{1, 2}},
	})
	must.NoError(err)
	_, err = fixture.store.TargetReviewFindings("alpha", "op-1", review.Attempt, []int{0}, followUp.Attempt)
	must.NoError(err)

	resolver := fixture.resolver(taskItem, map[string]string{
		"ORPHEUS_REPO_ID": "alpha", "ORPHEUS_TASK_ID": "op-1", "ORPHEUS_WORKTREE": worktree, "ORPHEUS_BRANCH": "orpheus/op-1",
	}, worktree)
	resolved, err := resolver.Resolve(context.Background())
	must.NoError(err)
	must.NotNil(resolved.FollowUp)
	is.Equal([]agent.ContextReviewFinding{{Index: 0, Title: "Required repair", Description: "Fix the regression.", SuggestedAction: "Add the guard."}}, resolved.FollowUp.RequiredFindings)
	is.Equal([]agent.ContextReviewFinding{
		{Index: 1, Title: "Optional cleanup", Description: "Simplify the path.", SuggestedAction: "Extract a helper."},
		{Index: 2, Title: "Downgraded opportunity", Description: "Still worth considering."},
	}, resolved.FollowUp.AdvisoryFindings)

	rendered := agent.RenderActiveContext(resolved)
	is.Contains(rendered, "Required blocking findings:")
	is.Contains(rendered, "Advisory opportunities:")
	is.Contains(rendered, "Optional cleanup")
	is.Contains(rendered, "Consider advisory opportunities only when they remain applicable, task-scoped, and safe.")
}

func TestActiveContextResolverRejectsReviewContextWhenLatestRunHasNoCompletion(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	must.NoError(testMkdirAll(worktree))
	taskItem := fixture.worktreeTask("op-1", worktree)
	recordCompletedContextRun(t, fixture, worktree, nil)
	recordCompletedContextRun(t, fixture, worktree, &taskstate.ReviewFollowUp{
		ReviewAttempt:  1,
		FindingIndexes: []int{0},
	})

	_, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "implementer",
		Branch:   "orpheus/op-1",
		Worktree: worktree,
		ReviewFollowUp: &taskstate.ReviewFollowUp{
			ReviewAttempt:  2,
			FindingIndexes: []int{0},
		},
	})
	must.NoError(err)
	review, err := fixture.store.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{
		Pipeline: "default",
		Step:     "ai-review",
	})
	must.NoError(err)
	_, err = fixture.store.RecordReviewStep("alpha", "op-1", review.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "agent_review",
		Name: "ai-review",
	})
	must.NoError(err)

	resolver := fixture.resolver(taskItem, map[string]string{
		"ORPHEUS_REPO_ID":        "alpha",
		"ORPHEUS_TASK_ID":        "op-1",
		"ORPHEUS_WORKTREE":       worktree,
		"ORPHEUS_BRANCH":         "orpheus/op-1",
		"ORPHEUS_AGENT_PURPOSE":  "review",
		"ORPHEUS_REVIEW_ATTEMPT": "1",
		"ORPHEUS_REVIEW_STEP":    "ai-review",
	}, worktree)

	_, err = resolver.ResolveReview(context.Background())

	must.Error(err)
	is.Contains(err.Error(), "resolve review completion history")
	is.Contains(err.Error(), "latest run attempt 3 completion is required")
}

func recordCompletedContextRun(
	t *testing.T,
	fixture *activeContextFixture,
	worktree string,
	followUp *taskstate.ReviewFollowUp,
) {
	t.Helper()
	must := require.New(t)

	run, err := fixture.store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:          "implementer",
		Branch:         "orpheus/op-1",
		Worktree:       worktree,
		ReviewFollowUp: followUp,
	})
	must.NoError(err)
	_, err = fixture.store.CompleteRun("alpha", "op-1", run.Attempt, taskstate.CompleteRunOptions{
		Summary:              "Complete work",
		Description:          "Implemented the requested work.",
		DetailedDescription:  "Detailed PR body.",
		TechnicalExplanation: "Technical explanation.",
	})
	must.NoError(err)
	_, err = fixture.store.FinishRun("alpha", "op-1", run.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
}

func TestActiveContextResolverProvidesEarlierAuthoritativeFindingsOnly(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	fixture := newActiveContextFixture(t, "op-1")
	worktree := fixture.expectedWorktree(t, "op-1")
	must.NoError(testMkdirAll(worktree))
	taskItem := fixture.worktreeTask("op-1", worktree)
	recordCompletedContextRun(t, fixture, worktree, nil)

	previous, err := fixture.store.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{Pipeline: "standard", Step: "ai-review"})
	must.NoError(err)
	_, err = fixture.store.RecordReviewFinding("alpha", "op-1", previous.Attempt, taskstate.ReviewFinding{
		Type: taskstate.FindingTypeBlocking, Step: "ai-review", Title: "Prior blocker", Description: "An earlier defect.", Waiver: "Accepted before the new review.",
	})
	must.NoError(err)
	_, err = fixture.store.FinishReview("alpha", "op-1", previous.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)

	interrupted, err := fixture.store.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{Pipeline: "standard", Step: "ai-review"})
	must.NoError(err)
	interruptedPrimary := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, StartedAt: time.Now()}
	_, err = fixture.store.RecordReviewStep("alpha", "op-1", interrupted.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &interruptedPrimary})
	must.NoError(err)
	_, err = fixture.store.RecordReviewFinding("alpha", "op-1", interrupted.Attempt, taskstate.ReviewFinding{
		Type: taskstate.FindingTypeBlocking, Step: "ai-review", Title: "Interrupted audit finding", Description: "Not authoritative.",
	})
	must.NoError(err)
	_, err = fixture.store.InterruptPrimaryReviewExecution("alpha", "op-1", interrupted.Attempt, "ai-review", taskstate.InterruptPrimaryReviewExecutionOptions{Reason: "supervisor disappeared", Trigger: "recovery"})
	must.NoError(err)

	active, err := fixture.store.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{Pipeline: "standard", Step: "ai-review"})
	must.NoError(err)
	primary := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, StartedAt: time.Now()}
	_, err = fixture.store.RecordReviewStep("alpha", "op-1", active.Attempt, taskstate.RecordReviewStepOptions{Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &primary})
	must.NoError(err)
	_, err = fixture.store.RecordReviewFinding("alpha", "op-1", active.Attempt, taskstate.ReviewFinding{
		Type: taskstate.FindingTypeBlocking, Step: "ai-review", Title: "Current primary finding", Description: "Must not be injected.",
	})
	must.NoError(err)
	alternate := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, StartedAt: time.Now()}
	_, err = fixture.store.StartReviewStepComparison("alpha", "op-1", active.Attempt, "ai-review", alternate)
	must.NoError(err)
	_, err = fixture.store.RecordAlternateReviewFinding("alpha", "op-1", active.Attempt, "ai-review", taskstate.ReviewFinding{
		Type: taskstate.FindingTypeAdvisory, Title: "Current alternate finding", Description: "Must not be injected.",
	})
	must.NoError(err)

	resolver := fixture.resolver(taskItem, map[string]string{
		"ORPHEUS_REPO_ID": "alpha", "ORPHEUS_TASK_ID": "op-1", "ORPHEUS_WORKTREE": worktree, "ORPHEUS_BRANCH": "orpheus/op-1",
		"ORPHEUS_AGENT_PURPOSE": "review", "ORPHEUS_REVIEW_ATTEMPT": "3", "ORPHEUS_REVIEW_STEP": "ai-review",
	}, worktree)
	resolved, err := resolver.ResolveReview(context.Background())

	must.NoError(err)
	must.Len(resolved.Review.PriorFindings, 1)
	is.Equal(agent.ContextPriorReviewFinding{Attempt: 1, Number: 1, Step: "ai-review", Type: taskstate.FindingTypeBlocking, Disposition: "waived", Title: "Prior blocker"}, resolved.Review.PriorFindings[0])
	is.NotContains(agent.RenderReviewContext(resolved), "Interrupted audit finding")
	is.NotContains(agent.RenderReviewContext(resolved), "Current primary finding")
	is.NotContains(agent.RenderReviewContext(resolved), "Current alternate finding")
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

func TestRenderReviewContextUsesLegacyMultiFindingReviewByDefault(t *testing.T) {
	is := assert.New(t)
	t.Setenv("ORPHEUS_EXHAUSTIVE_REVIEW_CONTEXT", "")

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
}

func TestRenderReviewContextUsesLegacyMultiFindingReviewWhenToggleDisabled(t *testing.T) {
	is := assert.New(t)
	t.Setenv("ORPHEUS_EXHAUSTIVE_REVIEW_CONTEXT", "0")

	got := agent.RenderReviewContext(reviewContextRenderFixture())

	is.Contains(got, "Review the complete change set before exiting, even if you find an issue early.")
	is.Contains(got, "Record each distinct finding with its own `orpheus agent review add` call")
	is.NotContains(got, "Exhaustive coverage is required within your assigned reviewer scope.")
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
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
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

func testMkdirAll(path string) error {
	return os.MkdirAll(path, 0o755)
}
