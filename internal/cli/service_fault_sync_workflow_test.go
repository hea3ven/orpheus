//go:build integration

package cli_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var errInjectedSyncConflictPhaseWrite = errors.New("injected sync conflict phase write failure")

type observedSyncConflictGit struct {
	*memorySyncGit
	t             *testing.T
	store         taskstate.Store
	beginCalls    int
	commitCalls   int
	pushCalls     int
	remoteCalls   int
	rollbackCalls int
}

func observeSyncConflictGit(t *testing.T, f *syncWorkflowFixture) *observedSyncConflictGit {
	t.Helper()
	return &observedSyncConflictGit{memorySyncGit: f.syncGit, t: t, store: f.taskStore}
}

func (g *observedSyncConflictGit) BeginTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	if err := g.requireActivePhase(taskstate.SyncConflictPhasePrepared); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	g.beginCalls++
	return g.memorySyncGit.BeginTaskBranchConflictResolution(ctx, opts)
}

func (g *observedSyncConflictGit) CommitTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, files []string) (gitmeta.TaskBranchSyncResult, error) {
	if err := g.requireActivePhase(taskstate.SyncConflictPhaseResolving); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	g.commitCalls++
	return g.memorySyncGit.CommitTaskBranchConflictResolution(ctx, opts, files)
}

func (g *observedSyncConflictGit) PushCommittedTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	if err := g.requireActivePhase(taskstate.SyncConflictPhasePushIntent); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	g.pushCalls++
	return g.memorySyncGit.PushCommittedTaskBranchConflictResolution(ctx, opts)
}

func (g *observedSyncConflictGit) InspectRemoteTaskBranchHead(ctx context.Context, opts gitmeta.TaskBranchSyncOptions) (string, error) {
	g.remoteCalls++
	return g.memorySyncGit.InspectRemoteTaskBranchHead(ctx, opts)
}

func (g *observedSyncConflictGit) RollbackTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint) error {
	if err := g.requireActivePhase(taskstate.SyncConflictPhaseConflicted, taskstate.SyncConflictPhaseResolving); err != nil {
		return err
	}
	g.rollbackCalls++
	return g.memorySyncGit.RollbackTaskBranchConflictResolution(ctx, opts, checkpoint)
}

func (g *observedSyncConflictGit) requireActivePhase(phases ...taskstate.SyncConflictPhase) error {
	g.t.Helper()
	taskState, err := g.store.Load("alpha", "op-sync")
	if err != nil {
		return err
	}
	if taskState.ActiveSyncConflict == nil {
		return fmt.Errorf("external sync mutation started without durable active conflict; want one of %v", phases)
	}
	for _, phase := range phases {
		if taskState.ActiveSyncConflict.Phase == phase {
			return nil
		}
	}
	return fmt.Errorf("external sync mutation saw durable phase %s, want one of %v", taskState.ActiveSyncConflict.Phase, phases)
}

func TestIntegrationWorkflowTaskSyncConflictStopsExternalMutationsWhenPhasePersistenceFails(t *testing.T) {
	tests := []struct {
		name         string
		phase        taskstate.SyncConflictPhase
		wantBegin    int
		wantResolve  int
		wantCommit   int
		wantPush     int
		wantRollback int
	}{
		{name: "prepared", phase: taskstate.SyncConflictPhasePrepared},
		{name: "conflicted", phase: taskstate.SyncConflictPhaseConflicted, wantBegin: 1},
		{name: "resolving", phase: taskstate.SyncConflictPhaseResolving, wantBegin: 1, wantRollback: 1},
		{name: "local completed", phase: taskstate.SyncConflictPhaseLocalCompleted, wantBegin: 1, wantResolve: 1, wantCommit: 1},
		{name: "push intent", phase: taskstate.SyncConflictPhasePushIntent, wantBegin: 1, wantResolve: 1, wantCommit: 1},
		{name: "pushed", phase: taskstate.SyncConflictPhasePushed, wantBegin: 1, wantResolve: 1, wantCommit: 1, wantPush: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := assert.New(t)
			must := require.New(t)
			f := newSyncFixture(t)
			git := observeSyncConflictGit(t, f)
			f.options.Dependencies.SyncGit = git
			f.syncGit.behind = true
			f.syncGit.conflicts = []string{"conflict.txt"}
			if tt.wantResolve > 0 {
				f.resolvingAgent(nil)
			} else if tt.phase == taskstate.SyncConflictPhaseResolving {
				configureSyncConflictResolverProfile(t, f)
			}
			failed := injectSyncConflictPhaseWriteFailure(t, f.paths, tt.phase)

			stdout, stderr, err := f.runError("", "task", "sync", "op-sync")

			must.Error(err)
			is.ErrorIs(err, errInjectedSyncConflictPhaseWrite)
			is.Empty(stdout)
			is.Empty(stderr)
			must.True(*failed, "phase %s write fault was not consumed", tt.phase)
			is.Equal(tt.wantBegin, git.beginCalls, "begin requests")
			is.Len(f.agent.launches, tt.wantResolve, "resolver launches")
			is.Equal(tt.wantCommit, git.commitCalls, "commit requests")
			is.Equal(tt.wantPush, git.pushCalls, "push requests")
			is.Equal(tt.wantRollback, git.rollbackCalls, "rollback requests")
		})
	}
}

func TestIntegrationWorkflowTaskSyncConflictRecoversPushAfterPushedPhasePersistenceFails(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	f := newSyncFixture(t)
	f.resolvingAgent(nil)
	git := observeSyncConflictGit(t, f)
	f.options.Dependencies.SyncGit = git
	failed := injectSyncConflictPhaseWriteFailure(t, f.paths, taskstate.SyncConflictPhasePushed)

	stdout, stderr, err := f.runError("", "task", "sync", "op-sync")

	must.Error(err)
	is.ErrorIs(err, errInjectedSyncConflictPhaseWrite)
	is.Empty(stdout)
	is.Empty(stderr)
	must.True(*failed, "pushed phase write fault was not consumed")
	is.Equal(1, git.beginCalls)
	is.Len(f.agent.launches, 1)
	is.Equal(1, git.commitCalls)
	is.Equal(1, git.pushCalls)
	is.Equal(1, git.remoteCalls)
	is.Zero(git.rollbackCalls)
	stateBeforeRetry, _ := f.loadFinalTask("op-sync")
	must.NotNil(stateBeforeRetry.ActiveSyncConflict)
	is.Equal(taskstate.SyncConflictPhasePushIntent, stateBeforeRetry.ActiveSyncConflict.Phase)
	is.Equal(f.candidate.head, stateBeforeRetry.ActiveSyncConflict.LocalHead)
	is.Equal(f.candidate.head, f.publication.remote["orpheus/op-sync"])
	must.Len(stateBeforeRetry.Events, 1)
	is.Equal(taskstate.EventSyncConflictStarted, stateBeforeRetry.Events[0].Type)
	operationID := stateBeforeRetry.ActiveSyncConflict.ID
	must.NoError(state.SetMemoryDataWriteError(f.paths, nil))
	_, err = f.taskStore.UpdateSyncConflictOperation("alpha", "op-sync", operationID, func(active *taskstate.SyncConflictOperation) error {
		active.Execution = nil
		return nil
	})
	must.NoError(err)

	stdout, stderr, err = f.runError("", "task", "sync", "op-sync")

	must.NoError(err)
	is.Empty(stderr)
	is.Contains(stdout, "PR "+f.pr.url+" is still open for review")
	is.Equal(1, git.beginCalls, "begin must not repeat")
	is.Len(f.agent.launches, 1, "resolver launch must not repeat")
	is.Equal(1, git.commitCalls, "commit must not repeat")
	is.Equal(1, git.pushCalls, "push must not repeat")
	is.Equal(2, git.remoteCalls, "retry must verify the remote push")
	is.Zero(git.rollbackCalls)
	stateAfterRetry, _ := f.loadFinalTask("op-sync")
	is.Nil(stateAfterRetry.ActiveSyncConflict)
	must.Len(stateAfterRetry.Events, 2)
	is.Equal(taskstate.EventSyncConflictStarted, stateAfterRetry.Events[0].Type)
	is.Equal(taskstate.EventSyncConflictFinished, stateAfterRetry.Events[1].Type)
	is.Equal(f.candidate.head, stateAfterRetry.Events[1].Commit)
}

func configureSyncConflictResolverProfile(t *testing.T, f *syncWorkflowFixture) {
	t.Helper()
	f.configureAgentProfiles(agent.AgentDefaults{Implementer: "impl-codex", SyncConflictResolver: "sync-codex"}, map[string]agent.Profile{
		"impl-codex": {Harness: "codex", Model: "gpt-5"},
		"sync-codex": {Harness: "codex", Model: "gpt-5"},
	})
}

func injectSyncConflictPhaseWriteFailure(t *testing.T, paths state.Paths, phase taskstate.SyncConflictPhase) *bool {
	t.Helper()
	failed := false
	relTaskPath := filepath.ToSlash(filepath.Join("repos", "alpha", "tasks", "op-sync.yaml"))
	require.NoError(t, state.SetMemoryDataWriteError(paths, func(relativeDataPath string, proposedYAML []byte) error {
		if failed || filepath.ToSlash(relativeDataPath) != relTaskPath {
			return nil
		}
		var proposed taskstate.TaskState
		if err := yaml.Unmarshal(proposedYAML, &proposed); err != nil {
			return err
		}
		if proposed.ActiveSyncConflict != nil && proposed.ActiveSyncConflict.Phase == phase {
			failed = true
			return errInjectedSyncConflictPhaseWrite
		}
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, state.SetMemoryDataWriteError(paths, nil)) })
	return &failed
}
