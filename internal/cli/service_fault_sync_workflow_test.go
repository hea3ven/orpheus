//go:build integration

package cli_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errInjectedSyncReceiptWrite = errors.New("injected sync observation write failure")

// Every mutating boundary independently reloads its semantic prerequisites.
// No presentation status or command-intent journal can authorize an effect.
type observedSyncConflictGit struct {
	*memorySyncGit
	t                                  *testing.T
	store                              taskstate.Store
	begins, commits, pushes, rollbacks int
}

func observeSyncConflictGit(t *testing.T, f *syncWorkflowFixture) *observedSyncConflictGit {
	t.Helper()
	return &observedSyncConflictGit{memorySyncGit: f.syncGit, t: t, store: f.taskStore}
}
func (g *observedSyncConflictGit) recordedSync() *work.BranchSync {
	g.t.Helper()
	view, err := g.store.Load("alpha", "op-sync")
	require.NoError(g.t, err)
	require.NotNil(g.t, view.ActiveSyncConflict, "external mutation requires a recorded native checkpoint")
	ref := view.ActiveSyncConflict.Address
	require.NotNil(g.t, ref.BranchSync)
	return &view.Work.Workflows[ref.Workflow].Phases[ref.Phase].(*work.ExternalReviewPhase).BranchSyncs[*ref.BranchSync]
}
func (g *observedSyncConflictGit) BeginTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	observed := g.recordedSync()
	require.Equal(g.t, g.head, observed.Checkpoint.LocalHead)
	require.Equal(g.t, g.remote[opts.Branch], observed.Checkpoint.RemoteHead)
	require.Equal(g.t, "default-head", observed.Checkpoint.MergeSource)
	require.Nil(g.t, observed.ConflictResolution)
	require.Nil(g.t, observed.LocalCommit)
	g.begins++
	return g.memorySyncGit.BeginTaskBranchConflictResolution(ctx, opts)
}
func (g *observedSyncConflictGit) CommitTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, files []string) (gitmeta.TaskBranchSyncResult, error) {
	observed := g.recordedSync()
	require.NotNil(g.t, observed.ConflictResolution)
	native, ok := observed.ConflictResolution.Execution.Process.(*work.ExitedProcess)
	require.True(g.t, ok, "an aggregate status cannot substitute for a reaped resolver exit")
	require.Equal(g.t, 0, *native.Code)
	require.Nil(g.t, observed.LocalCommit)
	require.Nil(g.t, observed.Push)
	g.commits++
	return g.memorySyncGit.CommitTaskBranchConflictResolution(ctx, opts, files)
}
func (g *observedSyncConflictGit) PushCommittedTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	observed := g.recordedSync()
	require.NotNil(g.t, observed.LocalCommit)
	require.Equal(g.t, g.head, observed.LocalCommit.Head)
	require.Nil(g.t, observed.Push, "a local commit is not an observed remote push")
	g.pushes++
	return g.memorySyncGit.PushCommittedTaskBranchConflictResolution(ctx, opts)
}
func (g *observedSyncConflictGit) RollbackTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint) error {
	observed := g.recordedSync()
	require.Equal(g.t, checkpoint, gitmeta.TaskBranchConflictCheckpoint(observed.Checkpoint))
	require.Nil(g.t, observed.Push)
	g.rollbacks++
	return g.memorySyncGit.RollbackTaskBranchConflictResolution(ctx, opts, checkpoint)
}

func TestIntegrationWorkflowTaskSyncConflictStopsExternalMutationsWhenObservationPersistenceFails(t *testing.T) {
	cases := []struct {
		name                                         string
		begins, launches, commits, pushes, rollbacks int
		guarded                                      bool
	}{
		{name: "checkpoint"},
		{name: "resolver admission", begins: 1, rollbacks: 1},
		{name: "process outcome", begins: 1, launches: 1, guarded: true},
		{name: "resolver usage", begins: 1, launches: 1, rollbacks: 1},
		{name: "local result", begins: 1, launches: 1, commits: 1, guarded: true},
		{name: "remote observation", begins: 1, launches: 1, commits: 1, pushes: 1, guarded: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSyncFixture(t)
			observed := observeSyncConflictGit(t, f)
			f.options.Dependencies.SyncGit = observed
			f.syncGit.behind = true
			f.syncGit.conflicts = []string{"conflict.txt"}
			if tc.launches > 0 {
				f.resolvingAgent(nil)
			} else {
				configureSyncConflictResolverProfile(t, f)
			}
			if tc.name == "resolver usage" {
				f.options.Dependencies.CaptureUsage = func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
					return taskstate.RecordRunUsageOptions{Usage: &taskstate.AgentUsage{TotalTokens: 333}, UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "fixture native usage"}}
				}
			}
			consumed := injectSyncObservationWriteFailure(t, f.paths, tc.name)
			before := f.publication.remote["orpheus/op-sync"]

			stdout, stderr, err := f.runError("", "task", "sync", "op-sync")

			require.ErrorIs(t, err, errInjectedSyncReceiptWrite)
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
			require.True(t, *consumed, "the intended actual observation must reach the atomic boundary")
			assert.Equal(t, tc.begins, observed.begins)
			assert.Len(t, f.agent.launches, tc.launches)
			assert.Equal(t, tc.commits, observed.commits)
			assert.Equal(t, tc.pushes, observed.pushes)
			assert.Equal(t, tc.rollbacks, observed.rollbacks)
			view, item := f.loadFinalTask("op-sync")
			assert.Equal(t, tc.guarded, view.ActiveSyncConflict != nil, "uncertain effects must remain guarded, not erased")
			assert.Equal(t, f.initial.TaskID, item.ID)
			assert.Empty(t, f.tasks.closed)
			assert.Empty(t, f.pr.created)
			require.Len(t, view.Work.Workflows[0].Phases, 5, "branch maintenance cannot invent task review, implementation output or consent")
			syncs := view.Work.Workflows[0].Phases[4].(*work.ExternalReviewPhase).BranchSyncs
			if tc.name == "checkpoint" {
				assert.Empty(t, syncs)
				return
			}
			require.Len(t, syncs, 1)
			owned := syncs[0]
			if tc.pushes == 0 {
				assert.Equal(t, before, f.publication.remote["orpheus/op-sync"])
			} else {
				assert.Equal(t, f.candidate.head, f.publication.remote["orpheus/op-sync"])
				assert.Nil(t, owned.Push, "failed receipt persistence cannot claim remote delivery")
			}
			if tc.rollbacks > 0 {
				require.NotNil(t, owned.Rollback)
				assert.Equal(t, before, owned.Rollback.LocalHead)
				assert.Equal(t, before, f.candidate.head)
			}
			if tc.name == "resolver usage" {
				require.NotNil(t, owned.ConflictResolution)
				exit := owned.ConflictResolution.Execution.Process.(*work.ExitedProcess)
				assert.Equal(t, 0, *exit.Code, "usage failure cannot overwrite or suppress the real native exit")
				assert.Nil(t, owned.ConflictResolution.Execution.Usage)
			}
		})
	}
}

func TestIntegrationWorkflowTaskSyncConflictRecoversObservedPushWithoutRepeatingRepair(t *testing.T) {
	f := newSyncFixture(t)
	f.resolvingAgent(nil)
	observed := observeSyncConflictGit(t, f)
	f.options.Dependencies.SyncGit = observed
	consumed := injectSyncObservationWriteFailure(t, f.paths, "remote observation")

	_, _, err := f.runError("", "task", "sync", "op-sync")

	require.ErrorIs(t, err, errInjectedSyncReceiptWrite)
	require.True(t, *consumed)
	pending, _ := f.loadFinalTask("op-sync")
	require.NotNil(t, pending.ActiveSyncConflict)
	require.NotNil(t, pending.ActiveSyncConflict.Execution)
	assert.Equal(t, taskstate.RunStatusSucceeded, pending.ActiveSyncConflict.Execution.Status)
	head := f.candidate.head
	assert.Equal(t, head, pending.ActiveSyncConflict.LocalHead)
	assert.Equal(t, head, f.publication.remote["orpheus/op-sync"])
	require.NoError(t, state.SetMemoryDataWriteError(f.paths, nil))

	stdout, stderr := f.run("", "task", "sync", "op-sync")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "is still open for review")
	assert.Len(t, f.agent.launches, 1)
	assert.Equal(t, 1, observed.commits)
	assert.Equal(t, 1, observed.pushes)
	assert.Zero(t, observed.rollbacks)
	settled, _ := f.loadFinalTask("op-sync")
	assert.Nil(t, settled.ActiveSyncConflict)
	first := settled.Work.Workflows[0].Phases[4].(*work.ExternalReviewPhase).BranchSyncs[0]
	require.NotNil(t, first.Push)
	assert.Equal(t, head, first.Push.Head)
	assert.Equal(t, 0, *first.ConflictResolution.Execution.Process.(*work.ExitedProcess).Code)
	assert.Equal(t, head, f.candidate.head)
}

func configureSyncConflictResolverProfile(t *testing.T, f *syncWorkflowFixture) {
	t.Helper()
	f.configureAgentProfiles(agent.AgentDefaults{Implementer: "impl-codex", SyncConflictResolver: "sync-codex"}, map[string]agent.Profile{"impl-codex": {Harness: "codex", Model: "gpt-5"}, "sync-codex": {Harness: "codex", Model: "gpt-5"}})
}

func injectSyncObservationWriteFailure(t *testing.T, paths state.Paths, observation string) *bool {
	t.Helper()
	consumed := false
	require.NoError(t, state.SetMemoryDataWriteError(paths, func(path string, data []byte) error {
		if consumed || filepath.ToSlash(path) != "repos/alpha/tasks/op-sync.yaml" {
			return nil
		}
		proposed, err := workstate.Decode(data)
		if err != nil {
			return err
		}
		w := proposed.ActiveWorkflow()
		if w == nil {
			return nil
		}
		external, ok := w.LastPhase().(*work.ExternalReviewPhase)
		if !ok || len(external.BranchSyncs) == 0 {
			return nil
		}
		owned := external.BranchSyncs[len(external.BranchSyncs)-1]
		match := false
		switch observation {
		case "checkpoint":
			match = owned.ConflictResolution == nil && owned.LocalCommit == nil && owned.Rollback == nil
		case "resolver admission":
			if owned.ConflictResolution != nil {
				_, running := owned.ConflictResolution.Execution.Process.(*work.RunningProcess)
				match = running && owned.ConflictResolution.Execution.ChildPID == 0
			}
		case "process outcome":
			if owned.ConflictResolution != nil {
				_, match = owned.ConflictResolution.Execution.Process.(*work.ExitedProcess)
			}
		case "resolver usage":
			match = owned.ConflictResolution != nil && owned.ConflictResolution.Execution.Usage != nil
		case "local result":
			match = owned.LocalCommit != nil && owned.Push == nil
		case "remote observation":
			match = owned.Push != nil
		}
		if match {
			consumed = true
			return errInjectedSyncReceiptWrite
		}
		return nil
	}))
	t.Cleanup(func() { require.NoError(t, state.SetMemoryDataWriteError(paths, nil)) })
	return &consumed
}
