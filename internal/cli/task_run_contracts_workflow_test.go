//go:build integration

package cli_test

import (
	"context"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationTaskRunConfiguredDefaultPreservesLaunchContextAndAuditFacts(t *testing.T) {
	is := assert.New(t)
	item := anOpenTask("op-profile")
	item.Title = "Add search filters"
	fixture := newTaskWorkflowFixture(t, item)
	fixture.options.Environment["XDG_CONFIG_HOME"] = "/fixture/ignored-config"
	fixture.options.Environment["XDG_DATA_HOME"] = "/fixture/ignored-data"
	fixture.withCompletingAgent(aCompletion())
	fixture.configureAgentProfiles(agent.AgentDefaults{Implementer: "selected"}, map[string]agent.Profile{
		"decoy": {Command: "must-not-run", Interactive: false},
		"selected": {
			Command: "unused-agent", Interactive: true,
			Args: []string{"--literal", "keep this argument", "--name", "{{session_name}}", "--prompt", "{{prompt}}"},
		},
	})
	fixture.withSuppliedManualReview()
	const worktree = taskWorkflowDataRoot + "/repos/alpha/worktrees/op-profile"
	const branch = "orpheus/op-profile"
	const session = "Implementing op-profile Add search filters"
	wantArgs := []string{"--literal", "keep this argument", "--name", session, "--prompt", agent.RenderBootstrapPrompt()}

	_, stderr, err := fixture.execute("task", "run", "op-profile")
	require.NoError(t, err, "task run; stderr: %s", stderr)

	launch := fixture.onlyAgentLaunch()
	is.Equal("selected", launch.command.Name)
	is.Equal("unused-agent", launch.command.Command)
	is.Equal(worktree, launch.dir)
	require.Equal(t, wantArgs, launch.command.Args)
	for key, want := range map[string]string{
		"ORPHEUS_REPO_ID": "alpha", "ORPHEUS_TASK_ID": "op-profile",
		"ORPHEUS_BRANCH": branch, "ORPHEUS_WORKTREE": worktree,
		"ORPHEUS_AGENT_PROMPT": agent.RenderBootstrapPrompt(),
		"XDG_CONFIG_HOME":      "/fixture/task-workflow/xdg-config",
		"XDG_DATA_HOME":        "/fixture/task-workflow/xdg-data",
	} {
		is.Equal(want, launch.environment[key], "environment variable %s", key)
	}
	is.Equal("/fixture/ignored-config", fixture.options.Environment["XDG_CONFIG_HOME"], "caller environment is unchanged")
	is.Equal("/fixture/ignored-data", fixture.options.Environment["XDG_DATA_HOME"], "caller environment is unchanged")
	assertBootstrapPromptOmitsTaskDetails(t, launch.command.Args[5], item.Title)
	fixture.assertIsTaskInAgentContext("op-profile")
	fixture.assertAgentContextTarget(branch, worktree)
	finalState, finalTask := fixture.loadFinalTask("op-profile")
	assertPersistedTaskTarget(t, finalState, finalTask, branch, worktree)

	require.Len(t, finalState.Runs, 1)
	execution := finalState.Runs[0].Execution
	is.Equal("selected", execution.Agent)
	is.Equal("selected", execution.Profile)
	is.True(execution.Interactive)
	is.Equal("unused-agent", execution.Command)
	is.Equal(session, execution.SessionName)
	is.Equal(wantArgs, execution.Args)
	is.Equal(taskstate.RunStatusSucceeded, execution.Status)
	is.Equal(4242, execution.ChildPID)
	is.Positive(execution.SupervisorPID)
	is.False(execution.StartedAt.IsZero())
	is.NotNil(execution.FinishedAt)

	events := finalState.Events
	wantEvents := []struct {
		kind    taskstate.EventType
		attempt int
		status  taskstate.RunStatus
	}{
		{kind: taskstate.EventWorktreeCreated},
		{kind: taskstate.EventRunStarted, attempt: 1, status: taskstate.RunStatusRunning},
		{kind: taskstate.EventCompletionRecorded, attempt: 1, status: taskstate.RunStatusRunning},
		{kind: taskstate.EventRunFinished, attempt: 1, status: taskstate.RunStatusSucceeded},
	}
	require.Len(t, events, len(wantEvents))
	for i, want := range wantEvents {
		is.Equal(want.kind, events[i].Type, "event %d", i)
		is.Equal(want.attempt, events[i].Attempt, "event %d", i)
		is.Equal(want.status, events[i].Status, "event %d", i)
	}
}

func TestIntegrationTaskRunDispatchesChildOfInProgressEpic(t *testing.T) {
	is := assert.New(t)
	parent := anInProgressEpic("op-parent")
	fixture := newTaskWorkflowFixture(t, parent, anOpenChildTask("op-child", parent.ID))
	fixture.withAgentExitingWithoutCompletion(1)

	_, stderr, err := fixture.execute("task", "run", "op-child")
	require.NoError(t, err, "task run; stderr: %s", stderr)

	childState, child := fixture.loadFinalTask("op-child")
	parentState, finalParent := fixture.loadFinalTask("op-parent")
	require.Len(t, childState.Runs, 1)
	is.Equal(taskstate.RunStatusSucceeded, childState.Runs[0].Status)
	is.Equal(taskmodel.StatusInProgress, child.Status)
	is.Equal(parent.ID, child.Relations.ParentID)
	is.Equal(parent, finalParent, "dispatching a child must not mutate its parent")
	is.Empty(parentState.Runs, "only the child should be dispatched")
}

// The backend is a dispatch-only stub, not a reusable Beads emulator. Its supported
// mutation follows the same cases as the Beads MarkInProgress unit contracts.
func TestIntegrationDispatchStubRejectsConflictingTaskStateWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   taskmodel.Status
		metadata taskmodel.Metadata
		conflict bool
	}{
		{name: "open", status: taskmodel.StatusOpen},
		{name: "matching retry", status: taskmodel.StatusInProgress, metadata: taskmodel.Metadata{taskmodel.MetadataBranch: "branch", taskmodel.MetadataWorktree: "/fixture/worktree"}},
		{name: "missing retry target", status: taskmodel.StatusInProgress, conflict: true},
		{name: "conflicting branch", status: taskmodel.StatusInProgress, metadata: taskmodel.Metadata{taskmodel.MetadataBranch: "different", taskmodel.MetadataWorktree: "/fixture/worktree"}, conflict: true},
		{name: "conflicting worktree", status: taskmodel.StatusInProgress, metadata: taskmodel.Metadata{taskmodel.MetadataBranch: "branch", taskmodel.MetadataWorktree: "/fixture/different"}, conflict: true},
		{name: "closed", status: taskmodel.StatusClosed, conflict: true},
		{name: "published", status: taskmodel.StatusOpen, metadata: taskmodel.Metadata{taskmodel.MetadataPRURL: "https://example.test/pr/1"}, conflict: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial := taskmodel.Task{ID: "op-1", Status: tc.status, Metadata: tc.metadata}
			backend := newMemoryTaskBackend([]taskmodel.Task{initial})

			err := backend.MarkInProgress(context.Background(), "op-1", "branch", "/fixture/worktree")

			got, getErr := backend.Get(context.Background(), "op-1")
			require.NoError(t, getErr)
			if tc.conflict {
				assert.ErrorIs(t, err, taskmodel.ErrMutationConflict)
				assert.Equal(t, initial, got, "rejected mutation must leave the task unchanged")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, taskmodel.StatusInProgress, got.Status)
			assert.Equal(t, "branch", got.OrpheusMetadata().Branch)
			assert.Equal(t, "/fixture/worktree", got.OrpheusMetadata().Worktree)
		})
	}
}

func TestIntegrationDispatchStubRejectsUnsupportedMutations(t *testing.T) {
	is := assert.New(t)
	initial := anOpenTask("op-1")
	backend := newMemoryTaskBackend([]taskmodel.Task{initial})
	ctx := context.Background()

	is.Error(backend.UpdateGitFacts(ctx, "op-1", "branch", "/fixture/worktree"), "target relocation is unsupported")
	is.Error(backend.SetPRURL(ctx, "op-1", "https://example.test/pr/1"), "publication is unsupported")
	is.Error(backend.Close(ctx, "op-1"), "closure is unsupported")
	_, createErr := backend.Create(ctx, taskmodel.CreateOptions{Title: "Unrelated task"})
	is.Error(createErr, "creation is unsupported")

	got, err := backend.List(ctx)
	require.NoError(t, err)
	is.Equal([]taskmodel.Task{initial}, got)
}

func TestIntegrationScriptedAgentRejectsUnexpectedOrUnspecifiedLaunch(t *testing.T) {
	for _, tc := range []struct {
		name     string
		outcomes []semanticAgentOutcome
	}{
		{name: "unexpected launch"},
		{name: "unspecified outcome", outcomes: []semanticAgentOutcome{{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			launcher := semanticAgentLauncher{outcomes: tc.outcomes}

			err := launcher.Run(context.Background(), agentexec.Command{Name: "unexpected"}, agentexec.LaunchOptions{})

			assert.Error(t, err, "agent launch needs an explicit outcome")
		})
	}
}
