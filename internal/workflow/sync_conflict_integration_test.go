//go:build integration

package workflow_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

var errInjectedSyncConflictPhase = errors.New("injected sync conflict phase failure")

type integrationSyncConflictStore struct {
	taskstate.Store
	failPhase taskstate.SyncConflictPhase
	failed    bool
	phases    []taskstate.SyncConflictPhase
}

func (s *integrationSyncConflictStore) BeginSyncConflictOperation(
	repoID string,
	taskID string,
	operation taskstate.SyncConflictOperation,
) (taskstate.SyncConflictOperation, error) {
	if s.shouldFail(operation.Phase) {
		return taskstate.SyncConflictOperation{}, errInjectedSyncConflictPhase
	}
	started, err := s.Store.BeginSyncConflictOperation(repoID, taskID, operation)
	if err == nil {
		s.phases = append(s.phases, started.Phase)
	}
	return started, err
}

func (s *integrationSyncConflictStore) UpdateSyncConflictOperation(
	repoID string,
	taskID string,
	operationID string,
	update func(*taskstate.SyncConflictOperation) error,
) (taskstate.SyncConflictOperation, error) {
	state, err := s.Store.Load(repoID, taskID)
	if err != nil {
		return taskstate.SyncConflictOperation{}, err
	}
	if state.ActiveSyncConflict == nil {
		return s.Store.UpdateSyncConflictOperation(repoID, taskID, operationID, update)
	}
	candidate := *state.ActiveSyncConflict
	if err := update(&candidate); err != nil {
		return taskstate.SyncConflictOperation{}, err
	}
	if s.shouldFail(candidate.Phase) {
		return taskstate.SyncConflictOperation{}, errInjectedSyncConflictPhase
	}
	updated, err := s.Store.UpdateSyncConflictOperation(repoID, taskID, operationID, update)
	if err == nil {
		s.phases = append(s.phases, updated.Phase)
	}
	return updated, err
}

func (s *integrationSyncConflictStore) shouldFail(phase taskstate.SyncConflictPhase) bool {
	if s.failed || phase != s.failPhase {
		return false
	}
	s.failed = true
	return true
}

type integrationSyncConflictGit struct {
	store       *integrationSyncConflictStore
	syncCalls   int
	beginCalls  int
	commitCalls int
	pushCalls   int
	remoteCalls int
	rollback    int
	remoteHead  string
	localHead   string
}

func (g *integrationSyncConflictGit) SyncTaskBranchWithDefault(
	context.Context,
	gitmeta.TaskBranchSyncOptions,
) (gitmeta.TaskBranchSyncResult, error) {
	g.syncCalls++
	if g.syncCalls == 1 {
		return gitmeta.TaskBranchSyncResult{}, fmt.Errorf("%w: conflict.txt", gitmeta.ErrMergeConflict)
	}
	return gitmeta.TaskBranchSyncResult{Status: gitmeta.TaskBranchSyncAlreadyCurrent, Head: g.localHead}, nil
}

func (g *integrationSyncConflictGit) BeginTaskBranchConflictResolution(
	_ context.Context,
	opts gitmeta.TaskBranchSyncOptions,
) (gitmeta.TaskBranchSyncResult, error) {
	if err := g.requirePhase(taskstate.SyncConflictPhasePrepared); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	g.beginCalls++
	return gitmeta.TaskBranchSyncResult{
		Status:        gitmeta.TaskBranchSyncConflicted,
		Branch:        opts.Branch,
		DefaultBranch: opts.DefaultBranch,
		Head:          "local-before",
		ConflictFiles: []string{"conflict.txt"},
	}, nil
}

func (g *integrationSyncConflictGit) CompleteTaskBranchConflictResolution(
	context.Context,
	gitmeta.TaskBranchSyncOptions,
	[]string,
) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.TaskBranchSyncResult{}, errors.New("unexpected combined conflict completion")
}

func (g *integrationSyncConflictGit) CommitTaskBranchConflictResolution(
	_ context.Context,
	_ gitmeta.TaskBranchSyncOptions,
	_ []string,
) (gitmeta.TaskBranchSyncResult, error) {
	if err := g.requirePhase(taskstate.SyncConflictPhaseResolving); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	g.commitCalls++
	g.localHead = "local-after"
	return gitmeta.TaskBranchSyncResult{Status: gitmeta.TaskBranchSyncUpdated, Head: g.localHead}, nil
}

func (g *integrationSyncConflictGit) PushCommittedTaskBranchConflictResolution(
	context.Context,
	gitmeta.TaskBranchSyncOptions,
) (gitmeta.TaskBranchSyncResult, error) {
	if err := g.requirePhase(taskstate.SyncConflictPhasePushIntent); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	g.pushCalls++
	g.remoteHead = g.localHead
	return gitmeta.TaskBranchSyncResult{Status: gitmeta.TaskBranchSyncUpdated, Head: g.localHead}, nil
}

func (g *integrationSyncConflictGit) InspectTaskBranchConflictCheckpoint(
	context.Context,
	gitmeta.TaskBranchSyncOptions,
) (gitmeta.TaskBranchConflictCheckpoint, error) {
	return gitmeta.TaskBranchConflictCheckpoint{
		LocalHead:   "local-before",
		RemoteHead:  "remote-before",
		MergeSource: "default-head",
	}, nil
}

func (g *integrationSyncConflictGit) InspectRemoteTaskBranchHead(
	context.Context,
	gitmeta.TaskBranchSyncOptions,
) (string, error) {
	g.remoteCalls++
	return g.remoteHead, nil
}

func (g *integrationSyncConflictGit) InspectTaskBranchConflictRollbackEligibility(
	context.Context,
	gitmeta.TaskBranchSyncOptions,
	gitmeta.TaskBranchConflictCheckpoint,
	string,
) error {
	return nil
}

func (g *integrationSyncConflictGit) RollbackTaskBranchConflictResolution(
	context.Context,
	gitmeta.TaskBranchSyncOptions,
	gitmeta.TaskBranchConflictCheckpoint,
) error {
	g.rollback++
	return nil
}

func (g *integrationSyncConflictGit) VerifyTaskBranchConflictRollback(
	context.Context,
	gitmeta.TaskBranchSyncOptions,
	gitmeta.TaskBranchConflictCheckpoint,
) error {
	return errors.New("rollback has not run")
}

func (g *integrationSyncConflictGit) requirePhase(want taskstate.SyncConflictPhase) error {
	state, err := g.store.Load("alpha", "op-1")
	if err != nil {
		return err
	}
	if state.ActiveSyncConflict == nil {
		return fmt.Errorf("active sync conflict phase is missing, want %s", want)
	}
	if state.ActiveSyncConflict.Phase != want {
		return fmt.Errorf("active sync conflict phase = %s, want %s", state.ActiveSyncConflict.Phase, want)
	}
	return nil
}

type integrationSyncConflictResolver struct {
	store        *integrationSyncConflictStore
	resolveCalls int
}

func (r *integrationSyncConflictResolver) PrepareSyncConflictResolution(
	_ context.Context,
	_ workflow.SyncConflictResolutionOptions,
) (workflow.PreparedSyncConflictResolution, error) {
	return workflow.PreparedSyncConflictResolution{
		Execution: taskstate.AgentExecution{
			Purpose: taskstate.AgentExecutionPurposeSyncConflictResolution,
			Status:  taskstate.RunStatusRunning,
			Agent:   "integration-resolver",
			Profile: "integration-resolver",
			Harness: "integration",
			Command: "integration-resolver",
		},
		Resolve: func(context.Context) error {
			state, err := r.store.Load("alpha", "op-1")
			if err != nil {
				return err
			}
			if state.ActiveSyncConflict == nil || state.ActiveSyncConflict.Phase != taskstate.SyncConflictPhaseResolving {
				return fmt.Errorf("resolver started before the resolving phase was durable")
			}
			r.resolveCalls++
			return nil
		},
	}, nil
}

func TestIntegrationSyncConflictStopsExternalMutationsWhenPhasePersistenceFails(t *testing.T) {
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
			service, store, gitState, resolver := newIntegrationSyncConflictService(t, tt.phase)

			_, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
			if !errors.Is(err, errInjectedSyncConflictPhase) {
				t.Fatalf("sync error = %v, want injected %s persistence failure", err, tt.phase)
			}
			if gitState.beginCalls != tt.wantBegin || resolver.resolveCalls != tt.wantResolve ||
				gitState.commitCalls != tt.wantCommit || gitState.pushCalls != tt.wantPush ||
				gitState.rollback != tt.wantRollback {
				t.Fatalf(
					"external calls = begin:%d resolve:%d commit:%d push:%d rollback:%d, want begin:%d resolve:%d commit:%d push:%d rollback:%d; phases: %v",
					gitState.beginCalls,
					resolver.resolveCalls,
					gitState.commitCalls,
					gitState.pushCalls,
					gitState.rollback,
					tt.wantBegin,
					tt.wantResolve,
					tt.wantCommit,
					tt.wantPush,
					tt.wantRollback,
					store.phases,
				)
			}
		})
	}
}

func TestIntegrationSyncConflictRecoversPushAfterPushedPhasePersistenceFails(t *testing.T) {
	service, store, gitState, resolver := newIntegrationSyncConflictService(t, taskstate.SyncConflictPhasePushed)

	_, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if !errors.Is(err, errInjectedSyncConflictPhase) {
		t.Fatalf("first sync error = %v, want pushed phase persistence failure", err)
	}
	state, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load interrupted operation: %v", err)
	}
	if state.ActiveSyncConflict == nil || state.ActiveSyncConflict.Phase != taskstate.SyncConflictPhasePushIntent {
		t.Fatalf("active operation = %#v, want durable push intent", state.ActiveSyncConflict)
	}
	operationID := state.ActiveSyncConflict.ID
	store.failPhase = ""
	if _, err := store.UpdateSyncConflictOperation("alpha", "op-1", operationID, func(active *taskstate.SyncConflictOperation) error {
		active.Execution = nil
		return nil
	}); err != nil {
		t.Fatalf("simulate stopped resolver supervisor: %v", err)
	}

	result, err := service.Sync(context.Background(), workflow.SyncOptions{TaskID: "op-1"})
	if err != nil {
		t.Fatalf("recovery sync: %v", err)
	}
	if result.Status != workflow.SyncStatusAlreadyInReview {
		t.Fatalf("recovery result = %#v, want already-in-review after recovered push", result)
	}
	if gitState.beginCalls != 1 || resolver.resolveCalls != 1 || gitState.commitCalls != 1 || gitState.pushCalls != 1 {
		t.Fatalf(
			"external calls after recovery = begin:%d resolve:%d commit:%d push:%d, want no repeated mutation",
			gitState.beginCalls,
			resolver.resolveCalls,
			gitState.commitCalls,
			gitState.pushCalls,
		)
	}
	if gitState.remoteCalls != 2 {
		t.Fatalf("remote-head inspections = %d, want post-push verification and recovery verification", gitState.remoteCalls)
	}
	state, err = store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load recovered operation: %v", err)
	}
	if state.ActiveSyncConflict != nil {
		t.Fatalf("active operation = %#v, want cleared after remote-head recovery", state.ActiveSyncConflict)
	}
	if len(state.Events) != 2 ||
		state.Events[0].Type != taskstate.EventSyncConflictStarted ||
		state.Events[1].Type != taskstate.EventSyncConflictFinished ||
		state.Events[1].Commit != "local-after" {
		t.Fatalf("conflict audit events = %#v, want started and recovered finish at local-after", state.Events)
	}
	if len(store.phases) == 0 || store.phases[len(store.phases)-1] != taskstate.SyncConflictPhasePushed {
		t.Fatalf("durable phases = %v, want recovered pushed phase", store.phases)
	}
}

func newIntegrationSyncConflictService(
	t *testing.T,
	failPhase taskstate.SyncConflictPhase,
) (workflow.SyncService, *integrationSyncConflictStore, *integrationSyncConflictGit, *integrationSyncConflictResolver) {
	t.Helper()
	paths, source, targets := newSyncTestSource(t, "/fixture/repo", "op-1")
	taskItem := task.Task{
		ID:     "op-1",
		Status: task.StatusInProgress,
		Metadata: task.Metadata{
			task.MetadataBranch:   targets.WorktreeTeam.Branch,
			task.MetadataWorktree: targets.WorktreeTeam.Worktree,
			task.MetadataPRURL:    "https://github.test/org/repo/pull/42",
		},
	}
	backend := &fakeSyncBackend{tasks: []task.Task{taskItem}}
	provider := &fakePRProvider{status: pullrequest.PullRequestStatus{
		URL:   "https://github.test/org/repo/pull/42",
		State: pullrequest.StateOpen,
	}}
	store := &integrationSyncConflictStore{
		Store:     taskstate.NewStore(paths),
		failPhase: failPhase,
	}
	gitState := &integrationSyncConflictGit{
		store:      store,
		remoteHead: "remote-before",
		localHead:  "local-before",
	}
	resolver := &integrationSyncConflictResolver{store: store}
	service := workflow.SyncService{
		Paths:   paths,
		Sources: []task.RepositorySource{source},
		BackendFactory: func(task.RepositorySource) (task.SyncBackend, error) {
			return backend, nil
		},
		RunStore:         store,
		Git:              gitState,
		ConflictResolver: resolver,
		PRProvider:       provider,
	}
	return service, store, gitState, resolver
}
