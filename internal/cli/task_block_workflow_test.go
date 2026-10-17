//go:build integration

package cli_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/cli"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func (b *memoryTaskBackend) Block(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	item := b.tasks[id]
	if item.Status == taskmodel.StatusBlocked {
		return nil
	}
	if item.Metadata == nil {
		item.Metadata = make(taskmodel.Metadata)
	}
	item.Metadata[taskmodel.MetadataPreBlockStatus] = string(item.Status)
	item.Status = taskmodel.StatusBlocked
	b.tasks[id] = item
	return nil
}

func (b *memoryTaskBackend) Unblock(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	item := b.tasks[id]
	if item.Status != taskmodel.StatusBlocked {
		return nil
	}
	item.Status = taskmodel.StatusOpen
	if item.Metadata[taskmodel.MetadataPreBlockStatus] == "in_progress" {
		item.Status = taskmodel.StatusInProgress
	}
	delete(item.Metadata, taskmodel.MetadataPreBlockStatus)
	b.tasks[id] = item
	return nil
}

func (b *memoryTaskBackend) StartEpic(_ context.Context, id string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	item, ok := b.tasks[id]
	if !ok {
		return taskmodel.ErrNotFound
	}
	item.Status = taskmodel.StatusInProgress
	b.tasks[id] = item
	return nil
}

func TestIntegrationWorkflowTaskBlockingAcrossSourcesPreservesExecutionsAndWorkflowGates(t *testing.T) {
	parent := anInProgressEpic("be-parent")
	waiting := anOpenChildTask("op-waiting", parent.ID)
	waiting.Relations.DependencyIDs = []string{"be-prerequisite"}
	racing := anOpenChildTask("op-racing", parent.ID)
	nested := anOpenEpic("op-nested")
	nested.Relations.ParentID = parent.ID
	fixture := newTaskWorkflowFixture(t, waiting, racing, nested, anOpenChildTask("op-running", parent.ID))
	beta := newMemoryTaskBackend([]taskmodel.Task{parent, anOpenTask("be-prerequisite")})
	betaRepo := crossRepositoryOwner()
	betaRepo.TaskSource, betaRepo.TaskMode, betaRepo.TaskPrefix = "gig", "managed", "be"
	betaRepo.BeadsMode, betaRepo.BeadsPrefix = "", ""
	fixture.withRegisteredRepos(taskWorkflowRepository(), betaRepo)
	fixture.options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		if source.Repository.ID == "beta" {
			assert.Equal(t, "gig", source.Kind)
			return beta, nil
		}
		assert.Equal(t, "alpha", source.Repository.ID)
		assert.Equal(t, "beads", source.Kind)
		return fixture.backend, nil
	}
	fixture.withAgentExitingWithoutCompletion(1)
	assertConcurrentEpicBlockRefusesInitialDispatch(t, fixture, beta, racing)
	fixture.mustExecute("task", "unblock", parent.ID)
	assertConcurrentEpicBlockCannotOvertakeActivation(t, fixture, beta, nested)
	fixture.mustExecute("task", "unblock", parent.ID)
	fixture.options.Dependencies.ProcessProbe = func(int) (agentexec.ProcessLiveness, error) { return agentexec.ProcessLive, nil }
	fixture.agent.afterStart = func() error {
		before, beforeTask := fixture.loadFinalTask("op-running")
		_, _, err := fixture.execute("task", "block", "op-running")
		require.ErrorContains(t, err, "active or unverifiable execution")
		fixture.mustExecute("task", "block", parent.ID)
		after, afterTask := fixture.loadFinalTask("op-running")
		assert.Equal(t, before, after, "blocking an epic does not stop or change child executions")
		assert.Equal(t, beforeTask, afterTask)
		stdout, _ := fixture.mustExecute("status", "--full", "--no-truncate")
		assert.Contains(t, tableRowForTask(stdout, "op-waiting"), "Blocked")
		assert.Contains(t, tableRowForTask(stdout, "op-running"), "Working")
		_, _, err = fixture.execute("task", "run", "op-waiting")
		require.ErrorContains(t, err, "parent epic be-parent is blocked")
		return nil
	}

	fixture.mustExecute("task", "run", "op-running")
	fixture.agent.afterStart = nil
	before, beforeTask := fixture.loadFinalTask("op-running")
	fixture.mustExecute("task", "block", "op-running")
	stdout, _ := fixture.mustExecute("task", "block", "op-running")
	assert.Contains(t, stdout, "already blocked")
	_, _, err := fixture.execute("task", "run", "op-running")
	require.ErrorContains(t, err, "status blocked")
	fixture.mustExecute("task", "unblock", "op-running")
	after, afterTask := fixture.loadFinalTask("op-running")
	assert.Equal(t, before, after, "block/unblock retains run, review and usage history")
	assert.Equal(t, beforeTask, afterTask, "explicit unblock restores in-progress lifecycle and Git facts")
	fixture.withAgentExitingWithoutCompletion(1)
	fixture.mustExecute("task", "run", "op-running")
	assert.Len(t, fixture.agent.launches, 2, "existing retries remain allowed with a blocked parent")

	fixture.mustExecute("task", "unblock", parent.ID)
	assert.Equal(t, taskmodel.StatusInProgress, beta.tasks[parent.ID].Status)
	fixture.mustExecute("task", "block", waiting.ID)
	fixture.mustExecute("task", "unblock", waiting.ID)
	_, _, err = fixture.execute("task", "run", waiting.ID)
	require.ErrorContains(t, err, "blocked by be-prerequisite")
	assert.Equal(t, []string{"be-prerequisite"}, fixture.backend.tasks[waiting.ID].Relations.DependencyIDs)
	fixture.mustExecute("task", "block", "be-prerequisite")
	fixture.mustExecute("task", "unblock", "be-prerequisite")
	assert.Equal(t, taskmodel.StatusOpen, beta.tasks["be-prerequisite"].Status)
	stdout, _ = fixture.mustExecute("task", "unblock", "be-prerequisite")
	assert.Contains(t, stdout, "already unblocked")
	assert.Len(t, fixture.git.setups, 2)

	holdMutationLock(t, fixture.paths)
	_, _, err = fixture.execute("task", "block", "be-prerequisite")
	require.Error(t, err)
	assert.Equal(t, taskmodel.StatusOpen, beta.tasks["be-prerequisite"].Status, "lock contention prevents source mutation")
}

// pauseParentSnapshot retains an already-read lifecycle while a separate command
// attempts to block the epic. Subsequent parent reads remain authoritative.
type pauseParentSnapshot struct {
	*memoryTaskBackend
	parentID string
	read     chan taskmodel.Task
	resume   chan struct{}
	paused   atomic.Bool
}

func (b *pauseParentSnapshot) Get(ctx context.Context, id string) (taskmodel.Task, error) {
	item, err := b.memoryTaskBackend.Get(ctx, id)
	if err != nil || id != b.parentID || !b.paused.CompareAndSwap(false, true) {
		return item, err
	}
	b.read <- item.Clone()
	select {
	case <-b.resume:
		return item, nil
	case <-ctx.Done():
		return taskmodel.Task{}, ctx.Err()
	}
}

func assertConcurrentEpicBlockRefusesInitialDispatch(t *testing.T, fixture *taskWorkflowFixture, beta *memoryTaskBackend, child taskmodel.Task) {
	t.Helper()
	before, beforeTask := fixture.loadFinalTask(child.ID)
	resume := pauseCommandAtParentRead(t, fixture, beta, child.Relations.ParentID, "task", "run", child.ID)

	fixture.mustExecute("task", "block", child.Relations.ParentID)
	blocked, err := beta.Get(t.Context(), child.Relations.ParentID)
	require.NoError(t, err)
	require.Equal(t, taskmodel.StatusBlocked, blocked.Status)
	assert.Empty(t, fixture.agent.launches)
	assert.Empty(t, fixture.git.setups)
	require.ErrorContains(t, resume(), "parent epic be-parent is blocked")
	after, afterTask := fixture.loadFinalTask(child.ID)
	assert.Equal(t, before, after, "refused initial dispatch writes no execution history")
	assert.Equal(t, beforeTask, afterTask, "refused initial dispatch changes no lifecycle or Git facts")
	assert.Empty(t, fixture.backend.markCalls)
	assert.Empty(t, fixture.agent.launches)
	assert.Empty(t, fixture.git.setups, "stale eligibility must be refused before Git setup")
}

func assertConcurrentEpicBlockCannotOvertakeActivation(t *testing.T, fixture *taskWorkflowFixture, beta *memoryTaskBackend, child taskmodel.Task) {
	t.Helper()
	before, beforeTask := fixture.loadFinalTask(child.ID)
	parentBefore, err := beta.Get(t.Context(), child.Relations.ParentID)
	require.NoError(t, err)
	resume := pauseCommandAtParentRead(t, fixture, beta, child.Relations.ParentID, "task", "start", child.ID)

	_, _, err = fixture.execute("task", "block", child.Relations.ParentID)
	var locked *state.LockAcquisitionError
	require.ErrorAs(t, err, &locked, "epic activation must hold the shared lock while inspecting its parent")
	parentAfter, err := beta.Get(t.Context(), child.Relations.ParentID)
	require.NoError(t, err)
	assert.Equal(t, parentBefore, parentAfter, "failed concurrent blocking leaves the parent active")
	pausedState, pausedTask := fixture.loadFinalTask(child.ID)
	assert.Equal(t, before, pausedState)
	assert.Equal(t, beforeTask, pausedTask, "child activation has not happened during parent inspection")

	require.NoError(t, resume())
	after, afterTask := fixture.loadFinalTask(child.ID)
	assert.Equal(t, before, after, "epic activation writes no execution history")
	assert.Equal(t, taskmodel.StatusInProgress, afterTask.Status)
	fixture.mustExecute("task", "block", child.Relations.ParentID)
	stillActive, err := fixture.backend.Get(t.Context(), child.ID)
	require.NoError(t, err)
	assert.Equal(t, afterTask, stillActive, "blocking after activation preserves the already-active child")
	assert.Empty(t, fixture.agent.launches)
	assert.Empty(t, fixture.git.setups)
}

func pauseCommandAtParentRead(t *testing.T, fixture *taskWorkflowFixture, beta *memoryTaskBackend, parentID string, args ...string) func() error {
	t.Helper()
	paused := &pauseParentSnapshot{memoryTaskBackend: beta, parentID: parentID, read: make(chan taskmodel.Task, 1), resume: make(chan struct{})}
	options := fixture.options
	factory := options.Dependencies.TaskBackendFactory
	options.Dependencies.TaskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		if source.Repository.ID == "beta" {
			return paused, nil
		}
		return factory(source)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	command := cli.NewRootCommandWithOptions(options)
	command.SetContext(ctx)
	finished := make(chan error, 1)
	t.Cleanup(func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(5 * time.Second):
			t.Error("paused command did not stop after cancellation")
		}
	})
	go func() {
		defer close(finished)
		_, _, err := executeRootCommand(command, args...)
		finished <- err
	}()
	select {
	case snapshot := <-paused.read:
		require.Equal(t, taskmodel.StatusInProgress, snapshot.Status, "activation inspected an active epic")
	case <-ctx.Done():
		t.Fatal("command did not reach its parent eligibility read")
	}
	return func() error {
		close(paused.resume)
		select {
		case err := <-finished:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
