package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/status"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

func TestTaskSyncHelpExplainsBranchUpdatePolicies(t *testing.T) {
	command := newTaskSyncCommand(&rootOptions{})
	for _, want := range []string{
		"task sync <task-id> incorporates the integration branch",
		"task sync --all leaves conflict-free branches unchanged",
	} {
		if !strings.Contains(command.Long, want) {
			t.Fatalf("task sync help = %q, want %q", command.Long, want)
		}
	}
}

func TestTaskListDocumentsRepositoryFilter(t *testing.T) {
	flag := newTaskListCommand(&rootOptions{}).Flags().Lookup("repo")
	if flag == nil {
		t.Fatal("task list --repo flag is not registered")
	}
	if flag.Usage != "limit to registered repository id, name, or Beads prefix" {
		t.Fatalf("task list --repo help = %q", flag.Usage)
	}
}

func TestTaskListOptionsRejectUnsupportedStatusAndDate(t *testing.T) {
	for _, options := range []taskListOptions{
		{statuses: []string{"ready"}},
		{types: []string{"bug"}},
		{createdAfter: "not-a-date"},
		{createdAfter: "2026-06-05", createdBefore: "2026-06-04"},
	} {
		if _, err := options.normalized(); err == nil {
			t.Fatalf("normalized %#v successfully, want validation error", options)
		}
	}
}

func TestTaskClosureOutputReportsAuditFailureAfterWorktreeRemoval(t *testing.T) {
	cleanup := &workflow.WorktreeCleanupResult{
		Outcome:  workflow.WorktreeCleanupRemoved,
		Worktree: "/worktrees/op-1",
		Reason:   "removed, but could not record local cleanup history: disk full",
	}

	t.Run("done", func(t *testing.T) {
		var output bytes.Buffer
		command := &cobra.Command{}
		command.SetOut(&output)
		if err := renderTaskDoneResult(command, workflow.FinalizationResult{
			Task:            taskmodel.Task{ID: "op-1"},
			Finalization:    taskstate.Finalization{Commit: "abc123"},
			Branch:          "main",
			WorktreeCleanup: cleanup,
		}); err != nil {
			t.Fatalf("render task done result: %v", err)
		}
		if !strings.Contains(output.String(), cleanup.Reason) {
			t.Fatalf("task done output = %q, want audit failure %q", output.String(), cleanup.Reason)
		}
	})

	t.Run("sync", func(t *testing.T) {
		var output bytes.Buffer
		if err := renderTaskSyncResult(&output, workflow.SyncResult{
			Status:          workflow.SyncStatusPRMerged,
			Task:            taskmodel.Task{ID: "op-1"},
			PRURL:           "https://github.test/org/repo/pull/1",
			WorktreeCleanup: cleanup,
		}); err != nil {
			t.Fatalf("render task sync result: %v", err)
		}
		if !strings.Contains(output.String(), cleanup.Reason) {
			t.Fatalf("task sync output = %q, want audit failure %q", output.String(), cleanup.Reason)
		}
	})
}

func TestTaskClosureOutputReportsUnsafeWorktreePath(t *testing.T) {
	cleanup := &workflow.WorktreeCleanupResult{
		Outcome:  workflow.WorktreeCleanupUnsafe,
		Worktree: "/worktrees/op-1",
		Reason:   "reload local task state after closure: state storage unavailable",
	}

	t.Run("done", func(t *testing.T) {
		var output bytes.Buffer
		command := &cobra.Command{}
		command.SetOut(&output)
		if err := renderTaskDoneResult(command, workflow.FinalizationResult{
			Task:            taskmodel.Task{ID: "op-1"},
			Finalization:    taskstate.Finalization{Commit: "abc123"},
			Branch:          "main",
			WorktreeCleanup: cleanup,
		}); err != nil {
			t.Fatalf("render task done result: %v", err)
		}
		if !strings.Contains(output.String(), cleanup.Worktree) || !strings.Contains(output.String(), cleanup.Reason) {
			t.Fatalf("task done output = %q, want worktree and reload failure", output.String())
		}
	})

	t.Run("sync", func(t *testing.T) {
		var output bytes.Buffer
		if err := renderTaskSyncResult(&output, workflow.SyncResult{
			Status:          workflow.SyncStatusPRMerged,
			Task:            taskmodel.Task{ID: "op-1"},
			PRURL:           "https://github.test/org/repo/pull/1",
			WorktreeCleanup: cleanup,
		}); err != nil {
			t.Fatalf("render task sync result: %v", err)
		}
		if !strings.Contains(output.String(), cleanup.Worktree) || !strings.Contains(output.String(), cleanup.Reason) {
			t.Fatalf("task sync output = %q, want worktree and reload failure", output.String())
		}
	})
}

func TestFilteredTaskInventoryAppliesProjectedStatusAfterHidingContext(t *testing.T) {
	repository := taskmodel.Repository{ID: "alpha"}
	projection := status.Projection{Groups: []status.Group{
		{
			ID: status.GroupReadyToRun,
			Entries: []status.Entry{
				{Kind: status.EntryTask, Repository: repository, Task: taskmodel.Task{ID: "a-selected"}},
				{Kind: status.EntryTask, Repository: repository, Task: taskmodel.Task{ID: "a-context"}},
			},
		},
		{
			ID: status.GroupDoneClosed,
			Entries: []status.Entry{
				{Kind: status.EntryTask, Repository: repository, Task: taskmodel.Task{ID: "a-closed", Status: taskmodel.StatusClosed}},
			},
		},
	}}
	candidates := []taskmodel.RepoTask{
		{Repository: repository, Task: taskmodel.Task{ID: "a-selected"}},
		{Repository: repository, Task: taskmodel.Task{ID: "a-closed", Status: taskmodel.StatusClosed}},
	}

	defaultInventory := filteredTaskInventory(projection, candidates, nil)
	if got := taskIDsInInventory(defaultInventory); !equalStrings(got, []string{"a-selected"}) {
		t.Fatalf("default task-list rows = %v, want selected non-closed row only", got)
	}
	closedInventory := filteredTaskInventory(projection, candidates, map[status.GroupID]struct{}{status.GroupDoneClosed: {}})
	if got := taskIDsInInventory(closedInventory); !equalStrings(got, []string{"a-closed"}) {
		t.Fatalf("closed task-list rows = %v, want selected closed row only", got)
	}
}

func TestFilteredTaskInventoryProjectsExcludedRelationshipContext(t *testing.T) {
	repository := taskmodel.Repository{ID: "alpha"}
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{{
		Repository: repository,
		Tasks: []taskmodel.Task{
			{ID: "a-epic", Title: "selected epic", IssueType: taskmodel.IssueTypeEpic, Status: taskmodel.StatusOpen, Relations: taskmodel.RelationSummary{ChildCount: 1}},
			{ID: "a-child", Title: "excluded completed child", IssueType: taskmodel.IssueTypeTask, Status: taskmodel.StatusClosed, Relations: taskmodel.RelationSummary{ParentID: "a-epic"}},
			{ID: "a-selected-child", Title: "selected child", IssueType: taskmodel.IssueTypeTask, Status: taskmodel.StatusOpen, Relations: taskmodel.RelationSummary{ParentID: "a-parent"}},
			{ID: "a-parent", Title: "excluded parent", IssueType: taskmodel.IssueTypeEpic, Status: taskmodel.StatusOpen},
			{ID: "a-selected-dependent", Title: "selected dependent", IssueType: taskmodel.IssueTypeTask, Status: taskmodel.StatusOpen, Relations: taskmodel.RelationSummary{DependencyIDs: []string{"a-dependency"}}},
			{ID: "a-dependency", Title: "excluded dependency", IssueType: taskmodel.IssueTypeTask, Status: taskmodel.StatusOpen},
		},
	}}}
	candidates := []taskmodel.RepoTask{
		{Repository: repository, Task: taskmodel.Task{ID: "a-epic"}},
		{Repository: repository, Task: taskmodel.Task{ID: "a-selected-child"}},
		{Repository: repository, Task: taskmodel.Task{ID: "a-selected-dependent"}},
	}
	inventory := filteredTaskInventory(status.Project(snapshot), candidates, nil)
	if got := taskIDsInInventory(inventory); !equalStrings(got, []string{"a-epic", "a-selected-child", "a-selected-dependent"}) {
		t.Fatalf("task-list rows = %v, want selected rows without context", got)
	}

	entries := inventoryEntries(inventory)
	if epic := entries["a-epic"]; epic.EpicProgress.Completed != 1 || epic.EpicProgress.Total != 1 {
		t.Fatalf("epic progress = %#v, want completed excluded child counted", epic.EpicProgress)
	}
	if child := entries["a-selected-child"]; child.SemanticDetail.Kind != status.DetailParentNotReady {
		t.Fatalf("child detail = %#v, want excluded parent gate", child.SemanticDetail)
	}
	if dependent := entries["a-selected-dependent"]; dependent.SemanticDetail.Kind != status.DetailBlockedDependency {
		t.Fatalf("dependent detail = %#v, want excluded dependency block", dependent.SemanticDetail)
	}
}

func TestFilteredTaskInventoryDistinguishesFailedRelationLookupFromMissingRelation(t *testing.T) {
	repository := taskmodel.Repository{ID: "alpha"}
	selected := taskmodel.Task{
		ID:        "a-selected",
		IssueType: taskmodel.IssueTypeTask,
		Status:    taskmodel.StatusOpen,
		Relations: taskmodel.RelationSummary{DependencyIDs: []string{"a-dependency"}},
	}
	snapshot := taskmodel.SnapshotResult{Repositories: []taskmodel.RepositorySnapshot{{
		Repository:                  repository,
		Tasks:                       []taskmodel.Task{selected},
		RelationshipContextFailures: []taskmodel.RelationshipContextFailure{{TaskID: selected.ID, ReferenceID: "a-dependency"}},
	}}}
	inventory := filteredTaskInventory(
		status.Project(snapshot),
		[]taskmodel.RepoTask{{Repository: repository, Task: selected}},
		nil,
	)
	entry := inventoryEntries(inventory)[selected.ID]
	if entry.SemanticDetail.Kind != status.DetailRelationshipContextUnavailable {
		t.Fatalf("detail = %#v, want unavailable relationship context", entry.SemanticDetail)
	}
	if strings.Contains(entry.Detail, "missing dependency") {
		t.Fatalf("detail = %q, must not report failed lookup as missing", entry.Detail)
	}
}

func inventoryEntries(projection status.Projection) map[string]status.Entry {
	entries := make(map[string]status.Entry)
	for _, group := range projection.Groups {
		for _, entry := range group.Entries {
			if entry.Kind == status.EntryTask {
				entries[entry.Task.ID] = entry
			}
		}
	}
	return entries
}

func taskIDsInInventory(projection status.Projection) []string {
	var ids []string
	for _, group := range projection.Groups {
		for _, entry := range group.Entries {
			if entry.Kind == status.EntryTask {
				ids = append(ids, entry.Task.ID)
			}
		}
	}
	return ids
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func TestTaskEditRecognizesExplicitlyEmptyPlanningInput(t *testing.T) {
	t.Parallel()

	t.Run("flag", func(t *testing.T) {
		opts := buildTaskEditUpdateOptions("op-1", taskEditOptions{titleSet: true})
		if opts.Title == nil || *opts.Title != "" {
			t.Fatalf("title option = %#v, want explicit empty value", opts.Title)
		}
	})

	t.Run("file", func(t *testing.T) {
		path := filepath.Join(testutil.CanonicalTempDir(t), "acceptance.md")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		loaded, err := loadTaskEditFileInputs(taskEditOptions{acceptanceFile: path}, "op-1")
		if err != nil {
			t.Fatalf("loadTaskEditFileInputs() error = %v", err)
		}
		opts := buildTaskEditUpdateOptions("op-1", loaded)
		if opts.AcceptanceCriteria == nil || *opts.AcceptanceCriteria != "" {
			t.Fatalf("acceptance option = %#v, want explicit empty value", opts.AcceptanceCriteria)
		}
	})
}

func TestSyncConflictAgentUsageOptionsUnsupportedHarnessUsesStableReason(t *testing.T) {
	tests := []struct {
		name    string
		harness string
		want    string
	}{
		{name: "blank", want: "unsupported_harness:unknown"},
		{name: "trimmed", harness: " local ", want: "unsupported_harness:local"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := syncConflictAgentUsageOptions(
				agent.CommandSnapshot{Harness: tt.harness},
				testutil.CanonicalTempDir(t),
			)(taskstate.AgentExecution{}, nil)

			if options.UsageCapture.Status != taskstate.UsageCaptureUnknown {
				t.Fatalf("status = %q, want %q", options.UsageCapture.Status, taskstate.UsageCaptureUnknown)
			}
			if options.UsageCapture.Reason != tt.want {
				t.Fatalf("reason = %q, want %q", options.UsageCapture.Reason, tt.want)
			}
		})
	}
}

func TestSyncConflictAgentResolverUsesEffectivePromptInCommandAndEnvironment(t *testing.T) {
	t.Parallel()

	promptAppend := "Resolve only conflict markers.\nKeep unrelated task work unchanged."
	wantPrompt := agent.RenderEffectivePrompt(promptAppend)
	paths := syncConflictPromptTestPaths(t, promptAppend)
	var gotPromptArg string
	var gotEnvPrompt string
	resolver := syncConflictPromptTestResolver(paths, &gotPromptArg, &gotEnvPrompt)

	prepared, err := resolver.PrepareSyncConflictResolution(context.Background(), workflow.SyncConflictResolutionOptions{
		Repository:    taskmodel.Repository{ID: "alpha"},
		Task:          taskmodel.Task{ID: "op-1"},
		Branch:        "orpheus/op-1",
		Worktree:      testutil.CanonicalTempDir(t),
		ConflictFiles: []string{"conflict.go"},
	})
	if err != nil {
		t.Fatalf("prepare conflict resolution: %v", err)
	}

	execution := prepared.Execution
	if execution.Harness != "pi" || execution.Model != "openai-codex/gpt-5.4-mini" {
		t.Fatalf("execution harness/model = %q/%q, want pi/openai-codex/gpt-5.4-mini", execution.Harness, execution.Model)
	}
	if got := execution.Args[len(execution.Args)-1]; got != wantPrompt {
		t.Fatalf("recorded prompt arg = %q, want %q", got, wantPrompt)
	}
	if err := prepared.Resolve(context.Background()); err != nil {
		t.Fatalf("resolve conflict: %v", err)
	}
	if gotPromptArg != wantPrompt {
		t.Fatalf("launch prompt arg = %q, want %q", gotPromptArg, wantPrompt)
	}
	if gotEnvPrompt != wantPrompt {
		t.Fatalf("env prompt = %q, want %q", gotEnvPrompt, wantPrompt)
	}
}

func syncConflictPromptTestPaths(t *testing.T, promptAppend string) state.Paths {
	t.Helper()

	paths, err := state.NewPaths(testutil.CanonicalTempDir(t), testutil.CanonicalTempDir(t))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	err = paths.WriteConfigYAML(agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer":            "impl",
				"sync_conflict_resolver": "sync-pi",
			},
			"profiles": map[string]any{
				"impl": map[string]any{"command": "impl"},
				"sync-pi": map[string]any{
					"harness":       "pi",
					"model":         "openai-codex/gpt-5.4-mini",
					"interactive":   false,
					"prompt_append": promptAppend,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("write config: %v", err)
	}
	return paths
}

func syncConflictPromptTestResolver(paths state.Paths, gotPromptArg *string, gotEnvPrompt *string) syncConflictAgentResolver {
	return syncConflictAgentResolver{
		paths: paths,
		launcher: syncConflictLauncherFunc(func(
			ctx context.Context,
			command agentexec.Command,
			opts agentexec.LaunchOptions,
		) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			*gotPromptArg = command.Args[len(command.Args)-1]
			*gotEnvPrompt = envValue(opts.Env, "ORPHEUS_AGENT_PROMPT")
			return nil
		}),
	}
}

type syncConflictLauncherFunc func(context.Context, agentexec.Command, agentexec.LaunchOptions) error

func (f syncConflictLauncherFunc) Run(ctx context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
	return f(ctx, command, opts)
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}
