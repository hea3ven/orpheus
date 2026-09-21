//go:build integration

package cli_test

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/cli"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskRunRejectsClosedTaskWithoutChangingRecordedState(t *testing.T) {
	item := aClosedTask("op-closed")
	fixture := newTaskWorkflowFixture(t, item)
	fixture.withCompletedRunAndPassedReview(item.ID)
	before, beforeTask := fixture.loadFinalTask(item.ID)
	priorSetups := len(fixture.git.setups)

	stdout, stderr, err := fixture.execute("task", "run", item.ID)

	require.ErrorContains(t, err, "task is closed")
	assert.NotContains(t, err.Error(), "task done")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	final, finalTask := fixture.loadFinalTask(item.ID)
	assert.Equal(t, before, final)
	assert.Equal(t, beforeTask, finalTask)
	assert.Len(t, fixture.git.setups, priorSetups)
	assert.Empty(t, fixture.agent.launches)
	assert.Empty(t, fixture.backend.markCalls)
}

func TestIntegrationWorkflowTaskRunMissingExternalReferenceBlocksSetup(t *testing.T) {
	for _, source := range []string{"repository", "global"} {
		t.Run(source, func(t *testing.T) {
			fixture := newTaskWorkflowFixture(t, anOpenTask("op-reference"))
			template := "[{{external_ref}}] {{summary}}"
			if source == "global" {
				fixture.setConfig("publication", map[string]any{"title_template": template})
			} else {
				repo := taskWorkflowRepository()
				repo.TitleTemplate = template
				fixture.withRegisteredRepos(repo)
			}

			statusOutput, statusStderr, statusErr := fixture.execute("status", "--no-truncate")
			require.NoError(t, statusErr)
			stdout, stderr, err := fixture.execute("task", "run", "op-reference")

			assert.Contains(t, statusOutput, "op-reference")
			assert.Contains(t, statusOutput, "missing required external reference")
			assert.Empty(t, statusStderr)
			assert.ErrorContains(t, err, "publication title template requires a task external reference")
			assert.ErrorContains(t, err, "orpheus task edit op-reference --external-ref <reference>")
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
			final, _ := fixture.loadFinalTask("op-reference")
			fixture.assertDispatchDidNotStart(final)
		})
	}
}

func TestIntegrationWorkflowTaskRunRejectsChildOfInactiveEpicBeforeSetup(t *testing.T) {
	parent := anOpenEpic("op-parent")
	fixture := newTaskWorkflowFixture(t, parent, anOpenChildTask("op-child", parent.ID))

	stdout, stderr, err := fixture.execute("task", "run", "op-child")

	assert.ErrorContains(t, err, "immediate parent epic op-parent is open")
	assert.ErrorContains(t, err, "immediate parent epic must be in_progress")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	final, _ := fixture.loadFinalTask("op-child")
	fixture.assertDispatchDidNotStart(final)
}

func TestIntegrationWorkflowTaskRunRepoRootDispatchLocksTargetAndRejectsModeOverrideOnRetry(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-root"))
	fixture.withAgentExitingWithoutCompletion(1)

	_, stderr, err := fixture.execute("task", "run", "--repo-root", "op-root")
	require.NoError(t, err, "stderr: %s", stderr)
	before, item := fixture.loadFinalTask("op-root")
	_, _, retryErr := fixture.execute("task", "run", "--repo-root", "op-root")

	assert.ErrorContains(t, retryErr, "already has target branch")
	assert.ErrorContains(t, retryErr, "retry without --repo-root")
	final, finalTask := fixture.loadFinalTask("op-root")
	assert.Equal(t, before, final)
	assert.Equal(t, item, finalTask)
	assertPersistedTaskTarget(t, final, finalTask, "main", taskWorkflowRepoRoot)
	require.Len(t, final.Runs, 1)
	assert.Equal(t, taskstate.RunStatusSucceeded, final.Runs[0].Status)
	assertEventTypes(t, final.Events, taskstate.EventWorktreeReused, taskstate.EventRunStarted, taskstate.EventRunFinished)
	require.Len(t, fixture.git.repoRootSetups, 1)
	assert.Equal(t, "main", fixture.git.repoRootSetups[0].DefaultBranch)
	assert.Equal(t, taskWorkflowRepoRoot, fixture.git.repoRootSetups[0].RepoPath)
	assert.Empty(t, fixture.git.worktreeSetups)
	launch := fixture.onlyAgentLaunch()
	assert.Equal(t, taskWorkflowRepoRoot, launch.dir)
	assert.Equal(t, "main", launch.environment["ORPHEUS_BRANCH"])
	assert.Equal(t, taskWorkflowRepoRoot, launch.environment["ORPHEUS_WORKTREE"])
	assert.Equal(t, "alpha", launch.environment["ORPHEUS_REPO_ID"])
	assert.Equal(t, "op-root", launch.environment["ORPHEUS_TASK_ID"])
	assertBootstrapPromptOmitsTaskDetails(t, launch.environment["ORPHEUS_AGENT_PROMPT"], "Task op-root")
}

func TestIntegrationWorkflowTaskRunBackendOwnedTargetWithoutLocalHistory(t *testing.T) {
	for _, mode := range []string{"worktree", "repo-root"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newTaskWorkflowFixture(t, anInProgressTask("op-owned"))
			branch, dir := fixture.expectedTarget("op-owned")
			args := []string{"task", "run", "op-owned"}
			if mode == "repo-root" {
				branch, dir = "main", taskWorkflowRepoRoot
				args = append(args, "--repo-root")
			}
			fixture.withBackendTarget("op-owned", branch, dir)
			fixture.withAgentExitingWithoutCompletion(1)
			before, taskBefore := fixture.loadFinalTask("op-owned")
			require.Empty(t, before.Runs)

			_, stderr, err := fixture.execute(args...)

			require.NoError(t, err, "stderr: %s", stderr)
			final, finalTask := fixture.loadFinalTask("op-owned")
			require.Len(t, final.Runs, 1)
			assert.Equal(t, taskstate.RunStatusSucceeded, final.Runs[0].Status)
			assert.Equal(t, taskBefore, finalTask)
			assertPersistedTaskTarget(t, final, finalTask, branch, dir)
		})
	}
}

func TestIntegrationWorkflowTaskRunBackendRepoRootMetadataRequiresExplicitMode(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anInProgressTask("op-root"))
	fixture.withBackendTarget("op-root", "main", taskWorkflowRepoRoot)

	stdout, stderr, err := fixture.execute("task", "run", "op-root")

	assert.ErrorContains(t, err, "repository-root metadata")
	assert.ErrorContains(t, err, "retry with `orpheus task run --repo-root op-root`")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	final, _ := fixture.loadFinalTask("op-root")
	fixture.assertDispatchDidNotStart(final)
}

func TestIntegrationWorkflowTaskRunOtherRepoRootOwnerDoesNotBlockWorktreeDispatch(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anInProgressTask("op-owner"), anOpenTask("op-next"))
	fixture.withBackendTarget("op-owner", "main", taskWorkflowRepoRoot)
	fixture.withAgentExitingWithoutCompletion(1)

	stdout, stderr, err := fixture.execute("task", "run", "--repo-root", "op-next")
	require.ErrorContains(t, err, "already has non-closed task op-owner owning repo-root metadata")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	final, _ := fixture.loadFinalTask("op-next")
	fixture.assertDispatchDidNotStart(final)
	_, stderr, err = fixture.execute("task", "run", "op-next")

	require.NoError(t, err, "stderr: %s", stderr)
	final, _ = fixture.loadFinalTask("op-next")
	require.Len(t, final.Runs, 1)
	assert.Equal(t, taskstate.RunStatusSucceeded, final.Runs[0].Status)
	assert.Empty(t, fixture.git.repoRootSetups)
	assert.Len(t, fixture.git.worktreeSetups, 1)
}

func TestIntegrationWorkflowTaskRunRepoRootSetupFailureDoesNotBlockWorktreeDispatch(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-dirty"))
	fixture.withAgentExitingWithoutCompletion(1)
	setupErr := errors.New("uncommitted changes")
	fixture.git.repoRootError = setupErr

	stdout, stderr, err := fixture.execute("task", "run", "--repo-root", "op-dirty")
	require.ErrorIs(t, err, setupErr)
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	rejected, item := fixture.loadFinalTask("op-dirty")
	assert.Empty(t, rejected.Runs)
	assert.Equal(t, taskmodel.StatusOpen, item.Status)
	assert.Empty(t, fixture.agent.launches)
	_, stderr, err = fixture.execute("task", "run", "op-dirty")

	require.NoError(t, err, "stderr: %s", stderr)
	final, _ := fixture.loadFinalTask("op-dirty")
	require.Len(t, final.Runs, 1)
	assert.Equal(t, taskstate.RunStatusSucceeded, final.Runs[0].Status)
	assert.Len(t, fixture.git.worktreeSetups, 1)
}

func TestIntegrationWorkflowTaskRunMutationConflictPreventsAttemptAndLaunch(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-race"))
	fixture.configureImplementer("recorder", agent.Profile{Command: "unused-agent"})
	conflict := taskmodel.MutationConflictError{TaskID: "op-race", Reason: "orpheus.branch is missing"}
	fixture.backend.markError = conflict

	stdout, stderr, err := fixture.execute("task", "run", "op-race")

	assert.ErrorIs(t, err, taskmodel.ErrMutationConflict)
	assert.ErrorContains(t, err, "mark task in progress")
	assert.ErrorContains(t, err, "orpheus.branch is missing")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	final, item := fixture.loadFinalTask("op-race")
	assert.Empty(t, final.Runs)
	assert.Empty(t, final.Events)
	assert.Equal(t, taskmodel.StatusOpen, item.Status)
	assert.Empty(t, fixture.agent.launches)
	assert.Equal(t, []string{"op-race"}, fixture.backend.markCalls)
}

func TestIntegrationWorkflowTaskRunHeldMutationLockPreventsSetup(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-locked"))
	fixture.configureImplementer("recorder", agent.Profile{Command: "unused-agent"})
	holdMutationLock(t, fixture.paths)

	stdout, stderr, err := fixture.execute("task", "run", "op-locked")

	assert.ErrorIs(t, err, os.ErrExist)
	assert.ErrorContains(t, err, "failed to acquire lock for task run setup:")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	final, _ := fixture.loadFinalTask("op-locked")
	fixture.assertDispatchDidNotStart(final)
}

func TestIntegrationWorkflowTaskRunReleasesMutationLockDuringAgentAndReacquiresForFinish(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-lock"))
	fixture.withAgentExitingWithoutCompletion(1)
	fixture.agent.afterStart = func() error {
		// Acquiring here also verifies the launch/child-PID lock has been released.
		holdMutationLock(t, fixture.paths)
		return nil
	}

	_, stderr, err := fixture.execute("task", "run", "op-lock")

	assert.ErrorIs(t, err, os.ErrExist)
	assert.ErrorContains(t, err, "record run finish")
	assert.ErrorContains(t, err, "task run finalization")
	assert.Contains(t, stderr, "== Agent run: implementation (run attempt 1) ==")
	final, _ := fixture.loadFinalTask("op-lock")
	require.Len(t, final.Runs, 1)
	assert.Equal(t, taskstate.RunStatusRunning, final.Runs[0].Status)
	assert.Equal(t, 4242, final.Runs[0].Execution.ChildPID)
	assertEventTypes(t, final.Events, taskstate.EventWorktreeCreated, taskstate.EventRunStarted)
}

func TestIntegrationWorkflowTaskRunAgentStartFailureRecordsFailedStartWithoutChildPID(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-start"))
	fixture.configureImplementer("missing", agent.Profile{Command: "missing-agent"})
	startErr := &agentexec.StartError{Name: "missing", Err: errors.New("missing executable")}
	fixture.agent.outcomes = []semanticAgentOutcome{{startFailure: startErr}}

	stdout, stderr, err := fixture.execute("task", "run", "op-start")

	assert.ErrorIs(t, err, startErr)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, "== Agent run: implementation (run attempt 1) ==")
	final, _ := fixture.loadFinalTask("op-start")
	require.Len(t, final.Runs, 1)
	assert.Equal(t, taskstate.RunStatusFailed, final.Runs[0].Status)
	assert.Zero(t, final.Runs[0].Execution.ChildPID)
	assert.NotNil(t, final.Runs[0].Execution.FinishedAt)
	assertEventTypes(t, final.Events, taskstate.EventWorktreeCreated, taskstate.EventRunStarted, taskstate.EventRunStartFailed)
	assert.Contains(t, final.Events[2].Error, "missing executable")
}

func TestIntegrationWorkflowTaskRunHeaderFailurePreventsLaunchAndRecordsFailedStart(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-header"))
	fixture.configureImplementer("recorder", agent.Profile{Command: "unused-agent"})
	command := cli.NewRootCommandWithOptions(fixture.options)
	command.SetIn(bytes.NewReader(nil))
	command.SetOut(new(bytes.Buffer))
	command.SetErr(unavailableOutput{})
	command.SetArgs([]string{"task", "run", "op-header"})

	err := command.Execute()

	assert.ErrorContains(t, err, "render agent run header")
	assert.ErrorContains(t, err, "output unavailable")
	assert.Empty(t, fixture.agent.launches)
	final, _ := fixture.loadFinalTask("op-header")
	require.Len(t, final.Runs, 1)
	assert.Equal(t, taskstate.RunStatusFailed, final.Runs[0].Status)
	assert.NotNil(t, final.Runs[0].Execution.FinishedAt)
	assertEventTypes(t, final.Events, taskstate.EventWorktreeCreated, taskstate.EventRunStarted, taskstate.EventRunStartFailed)
	assert.Contains(t, final.Events[2].Error, "output unavailable")
}

func TestIntegrationWorkflowTaskRunReportsActiveAttemptWithoutAnotherDispatch(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anInProgressTask("op-active"))
	fixture.seedRunningAttempt("op-active", 100, 101)
	fixture.options.Dependencies.ProcessProbe = func(int) (agentexec.ProcessLiveness, error) { return agentexec.ProcessLive, nil }
	before, beforeTask := fixture.loadFinalTask("op-active")
	setups := len(fixture.git.setups)

	stdout, stderr, err := fixture.execute("task", "run", "op-active")

	require.NoError(t, err)
	assert.Contains(t, stdout, "implementation attempt 1 is active")
	assert.Empty(t, stderr)
	final, finalTask := fixture.loadFinalTask("op-active")
	assert.Equal(t, before, final)
	assert.Equal(t, beforeTask, finalTask)
	assert.Len(t, fixture.git.setups, setups)
	assert.Empty(t, fixture.agent.launches)
}

func (f *taskWorkflowFixture) withBackendTarget(id, branch, dir string) {
	f.t.Helper()
	item := f.backend.tasks[id]
	item.Metadata = taskmodel.Metadata{taskmodel.MetadataBranch: branch, taskmodel.MetadataWorktree: dir}
	f.backend.tasks[id] = item
}

func (f *taskWorkflowFixture) assertDispatchDidNotStart(final taskstate.TaskState) {
	f.t.Helper()
	assert.Empty(f.t, final.Runs)
	assert.Empty(f.t, final.Events)
	assert.Empty(f.t, f.git.repoRootSetups)
	assert.Empty(f.t, f.git.worktreeSetups)
	assert.Empty(f.t, f.agent.launches)
	assert.Empty(f.t, f.backend.markCalls)
}

type unavailableOutput struct{}

func (unavailableOutput) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }
