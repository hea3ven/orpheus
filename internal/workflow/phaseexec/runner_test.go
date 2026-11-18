package phaseexec_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnerCommitsIntentAndPreservesIndependentHandoffAfterCanceledEffect(t *testing.T) {
	runner, current := runnerFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	observedFailure := errors.New("effect failed after observing exit")
	op := phaseexec.Operation{Name: "attached effort", Prepare: func(candidate *work.State) (phaseexec.Prepared, error) {
		execution(candidate).SupervisorPID = 33
		return phaseexec.Prepared{Changed: true, Scope: phaseexec.ReleaseForEffect, Run: func(ctx context.Context) phaseexec.Result {
			reserved, err := runner.Store.Load("repo", "task")
			require.NoError(t, err)
			require.Equal(t, work.StateVersion(2), reserved.Version)
			assert.Equal(t, 33, execution(&reserved).SupervisorPID)
			_, err = runner.Commit(ctx, func(child *work.State) (bool, error) {
				execution(child).ChildPID = 44
				child.Workflows[0].Phases[1].(*work.ImplementationPhase).Completion = &work.Completion{CommitSubject: "feat: completed work", CommitBody: "Implemented outcome", TechnicalExplanation: "Reasoning", CompletedAt: at(2)}
				return true, nil
			})
			require.NoError(t, err)
			cancel()
			return phaseexec.Result{Err: observedFailure, Update: func(parent *work.State) (bool, error) {
				require.NotNil(t, parent.Workflows[0].Phases[1].(*work.ImplementationPhase).Completion)
				zero := 0
				execution(parent).Process = &work.ExitedProcess{At: at(3), Code: &zero}
				return true, nil
			}}
		}}, nil
	}}
	saved, err := runner.Execute(ctx, current, op)
	require.ErrorIs(t, err, observedFailure)
	require.Equal(t, work.StateVersion(4), saved.Version)
	assert.Equal(t, 44, execution(&saved).ChildPID)
	assert.IsType(t, &work.ExitedProcess{}, execution(&saved).Process)
	assert.Zero(t, execution(&current).SupervisorPID, "preparation must not alias the caller's committed snapshot")
	persisted, err := runner.Store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, saved, persisted)
}

func TestRunnerFailuresReturnCommittedStateAndNeverExecuteUnsavedIntent(t *testing.T) {
	for _, stage := range []string{"preparation", "intent save", "result save"} {
		t.Run(stage, func(t *testing.T) {
			runner, current := runnerFixture(t)
			failed := errors.New("injected boundary failure")
			store := &failingStore{Store: runner.Store, fail: failed}
			if stage == "intent save" {
				store.failAt = 1
			}
			if stage == "result save" {
				store.failAt = 2
			}
			runner.Store = store
			effects := 0
			saved, err := runner.Execute(context.Background(), current, phaseexec.Operation{Name: "reserve then observe", Prepare: func(candidate *work.State) (phaseexec.Prepared, error) {
				execution(candidate).SupervisorPID = 33
				if stage == "preparation" {
					return phaseexec.Prepared{}, failed
				}
				return phaseexec.Prepared{Changed: true, Run: func(context.Context) phaseexec.Result {
					effects++
					return phaseexec.Result{Update: func(result *work.State) (bool, error) { execution(result).ChildPID = 44; return true, nil }}
				}}, nil
			}})
			require.ErrorIs(t, err, failed)
			assert.Zero(t, execution(&current).SupervisorPID)
			expectedVersion := work.StateVersion(1)
			if stage == "result save" {
				expectedVersion = 2
				assert.Equal(t, 1, effects)
				assert.Equal(t, 33, execution(&saved).SupervisorPID)
			} else {
				assert.Zero(t, effects)
			}
			assert.Equal(t, expectedVersion, saved.Version)
			assert.Zero(t, execution(&saved).ChildPID, "unsaved ordinary observations cannot escape")
			persisted, err := store.Load("repo", "task")
			require.NoError(t, err)
			assert.Equal(t, persisted, saved)
		})
	}
}

func TestAgentResultRecordingAlwaysAttemptsBothIndependentWritesAfterCancellation(t *testing.T) {
	for _, usageFirst := range []bool{true, false} {
		t.Run(map[bool]string{true: "usage then process", false: "process then usage"}[usageFirst], func(t *testing.T) {
			runner, _ := runnerFixture(t)
			firstFailure := errors.New("first write failed")
			secondFailure := errors.New("second write failed")
			calls := []string{}
			usage := func(*work.State) (bool, error) {
				calls = append(calls, "usage")
				if usageFirst {
					return false, firstFailure
				}
				return false, secondFailure
			}
			process := func(*work.State) (bool, error) {
				calls = append(calls, "process")
				if !usageFirst {
					return false, firstFailure
				}
				return false, secondFailure
			}
			recorder := phaseexec.AgentResults{Commit: runner.Commit}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var firstErr, secondErr error
			if usageFirst {
				_, firstErr, secondErr = recorder.UsageThenProcess(ctx, usage, process)
				assert.Equal(t, []string{"usage", "process"}, calls)
			} else {
				_, firstErr, secondErr = recorder.ProcessThenUsage(ctx, process, usage)
				assert.Equal(t, []string{"process", "usage"}, calls)
			}
			assert.ErrorIs(t, firstErr, firstFailure)
			assert.ErrorIs(t, secondErr, secondFailure)
		})
	}
}

type failingStore struct {
	phaseexec.Store
	fail          error
	failAt, calls int
}

func (s *failingStore) Save(candidate work.State, version work.StateVersion) (work.StateVersion, error) {
	s.calls++
	if s.calls == s.failAt {
		return 0, s.fail
	}
	return s.Store.Save(candidate, version)
}
func execution(s *work.State) *work.AgentExecution {
	return &s.Workflows[0].Phases[1].(*work.ImplementationPhase).Execution
}
func at(minute int) time.Time { return time.Date(2026, 1, 1, 0, minute, 0, 0, time.UTC) }
func runnerFixture(t *testing.T) (phaseexec.Runner, work.State) {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
	current := work.State{RepositoryID: "repo", TaskID: "task", Workflows: []work.Workflow{{Phases: []work.Phase{
		&work.PreparationPhase{Context: &work.WorkContext{Directory: "/fixture/work", TaskBranch: "task-branch", CheckpointRef: "refs/orpheus/baseline", Baseline: work.CodeIdentity{Commit: "B", Tree: "TB"}}, EstablishedAt: pointer(at(0))},
		&work.ImplementationPhase{Execution: work.AgentExecution{Profile: "implementer", Command: "pi", StartedAt: at(1), Process: &work.RunningProcess{}}},
	}}}}
	store := workstate.NewStore(paths)
	current.Version, err = store.Save(current, 0)
	require.NoError(t, err)
	return phaseexec.Runner{Paths: paths, Store: store, RepositoryID: "repo", TaskID: "task"}, current
}
func pointer[T any](v T) *T { return &v }
