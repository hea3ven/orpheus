package implementation_test

import (
	"context"
	"errors"
	"github.com/hea3ven/orpheus/internal/workfixture"
	reviewphase "github.com/hea3ven/orpheus/internal/workflow/review"
	"path/filepath"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/implementation"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newImplementationPolicy(t *testing.T, initial work.State) (workfixture.Store, workstate.Store, phaseexec.Runner) {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
	raw := workstate.NewStore(paths)
	initial.Version = 0
	_, err = raw.Save(initial, 0)
	require.NoError(t, err)
	return workfixture.NewStore(paths), raw, phaseexec.Runner{Store: raw, Paths: paths, RepositoryID: initial.RepositoryID, TaskID: initial.TaskID}
}

func policyTime(seconds int) *time.Time {
	at := time.Date(2026, 7, 1, 0, 0, seconds, 0, time.UTC)
	return &at
}

func recordPolicyChange(runner phaseexec.Runner, change phaseexec.Change) (taskstate.RunAttempt, error) {
	current, err := runner.Commit(context.Background(), change)
	if err != nil {
		return taskstate.RunAttempt{}, err
	}
	view, err := taskstate.Project(current)
	if err != nil {
		return taskstate.RunAttempt{}, err
	}
	run, ok := taskstate.LatestRun(view)
	if !ok {
		return taskstate.RunAttempt{}, errors.New("no implementation recorded")
	}
	return run, nil
}

func TestImplementationPolicyKeepsLiveHandoffSeparateFromObservedExitAndRefusesStaleOwners(t *testing.T) {
	prepared := &work.WorkContext{Directory: "/fixture/work", TaskBranch: "task", CheckpointRef: "refs/orpheus/baseline", Baseline: work.CodeIdentity{Commit: "B", Tree: "BT"}}
	store, raw, runner := newImplementationPolicy(t, work.State{RepositoryID: "repo", TaskID: "task", Workflows: []work.Workflow{{Phases: []work.Phase{&work.PreparationPhase{Context: prepared, EstablishedAt: policyTime(0)}}}}})
	run, err := recordPolicyChange(runner, implementation.ReserveChange(implementation.Reservation{Agent: "implementer", Harness: "pi", Command: "fixture-agent", Args: []string{" -- prompt\nwith newlines"}, SessionName: "Original launch label", SupervisorPID: 123}, *policyTime(1)))
	require.NoError(t, err)
	assert.Equal(t, work.AttemptRef{RepositoryID: "repo", TaskID: "task", Phase: 1}, run.Address)
	before, err := store.Load("repo", "task")
	require.NoError(t, err)
	_, err = recordPolicyChange(runner, implementation.ReserveChange(implementation.Reservation{Agent: "another"}, *policyTime(2)))
	require.ErrorIs(t, err, taskstate.ErrActiveRun)
	after, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = recordPolicyChange(runner, implementation.ChildPIDChange(run.Address, 456))
	require.NoError(t, err)
	handoff := taskstate.CompleteRunOptions{CommitSubject: "feat: original intent", CommitBody: "Requested behavior", PRDescription: "Original PR content", TechnicalExplanation: "Why"}
	live, err := (implementation.CompletionRecorder{Store: raw, Paths: runner.Paths}).CompleteImplementation(run.Address, handoff)
	require.NoError(t, err)
	assert.Equal(t, taskstate.RunStatusRunning, live.Status)
	assert.Nil(t, live.Execution.FinishedAt)
	assert.Empty(t, live.Completion.Commit, "handoff cannot authorize capture or a native exit")
	first, err := store.Load("repo", "task")
	require.NoError(t, err)
	_, err = (implementation.CompletionRecorder{Store: raw, Paths: runner.Paths}).CompleteImplementation(run.Address, taskstate.CompleteRunOptions{CommitSubject: "attempt to replace", CommitBody: "Different", PRDescription: "Different"})
	require.NoError(t, err)
	repeated, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, first, repeated, "first handoff is authoritative and repeated calls do not append an independent event")
	_, err = recordPolicyChange(runner, implementation.ProcessChange(run.Address, nil, 456))
	require.Error(t, err)
	zero := 0
	exit := &work.ExitedProcess{At: live.Completion.CompletedAt.Add(time.Second), Code: &zero}
	_, err = recordPolicyChange(runner, implementation.ProcessChange(run.Address, exit, 999))
	require.Error(t, err)
	preserved, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, first, preserved)
	finished, err := recordPolicyChange(runner, implementation.ProcessChange(run.Address, exit, 456))
	require.NoError(t, err)
	assert.Equal(t, taskstate.RunStatusSucceeded, finished.Status)
	assert.Equal(t, 456, finished.Execution.ChildPID)
	assert.Equal(t, "Original launch label", finished.Execution.SessionName)
	captureAt := exit.At.Add(time.Second)
	_, err = recordPolicyChange(runner, implementation.UsageChange(run.Address, taskstate.RecordRunUsageOptions{Session: &taskstate.AgentSession{ID: "native", LogPath: "/fixture/session.jsonl"}, Usage: &taskstate.AgentUsage{TotalTokens: 50}, UsageCost: &taskstate.AgentUsageCost{Kind: "reported", Currency: "USD", AmountMicroUSD: 0}, UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, CapturedAt: &captureAt}, Model: "native-model"}))
	require.NoError(t, err)
	historical, err := store.Load("repo", "task")
	require.NoError(t, err)
	require.NotNil(t, historical.Runs[0].Execution.UsageCost)
	assert.Zero(t, historical.Runs[0].Execution.UsageCost.AmountMicroUSD, "known zero differs from missing cost")
	assert.Equal(t, "native-model", historical.Runs[0].Execution.Model)
	_, err = recordPolicyChange(runner, implementation.UsageChange(run.Address, taskstate.RecordRunUsageOptions{Usage: &taskstate.AgentUsage{TotalTokens: 999}}))
	require.Error(t, err)
	_, err = store.SeedReview("repo", "task", reviewphase.Reservation{Pipeline: "manual", Contract: work.ReviewContract{PipelineKey: "manual", Slots: []work.ReviewSlot{{Kind: work.ReviewManual, ConfigKey: "approval"}}}, Scope: work.ReviewScope{Requirements: "Requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "destination"}}})
	require.Error(t, err, "even a successful process with handoff is not captured output")
	refused, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, historical, refused)
	// Explicit reset is fixture setup: the application never converts or resets
	// operator history implicitly. Its equal indexes must not reuse an old token.
	snapshot, err := raw.Load("repo", "task")
	require.NoError(t, err)
	contextCopy := *prepared
	established := captureAt.Add(time.Second)
	snapshot.Workflows = append(snapshot.Workflows, work.Workflow{Phases: []work.Phase{&work.PreparationPhase{Context: &contextCopy, EstablishedAt: &established}, &work.ImplementationPhase{Execution: work.AgentExecution{StartedAt: established, Process: &work.RunningProcess{}}}}})
	_, err = raw.Save(snapshot, snapshot.Version)
	require.NoError(t, err)
	before, err = store.Load("repo", "task")
	require.NoError(t, err)
	_, err = recordPolicyChange(runner, implementation.ChildPIDChange(run.Address, 777))
	require.Error(t, err)
	_, err = (implementation.CompletionRecorder{Store: raw, Paths: runner.Paths}).CompleteImplementation(run.Address, handoff)
	require.Error(t, err)
	after, err = store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestImplementationResumptionKeepsExactSourceWithinCurrentWorkflow(t *testing.T) {
	history := testutil.ReviewableWork("repo", "task", "/fixture/work", "task")
	current := testutil.ReviewableWork("repo", "task", "/fixture/work", "task").Workflows[0]
	current.Phases = current.Phases[:2]
	current.ReviewContracts = nil
	current.Phases[1].(*work.ImplementationPhase).Execution.Session = &work.Session{ID: "current-session", LogPath: "/fixture/current-session.jsonl"}
	history.Workflows = append(history.Workflows, current)
	store, _, runner := newImplementationPolicy(t, history)
	before, err := store.Load("repo", "task")
	require.NoError(t, err)
	source := before.Runs[len(before.Runs)-1].Address
	historical := before.Runs[0].Address
	foreign := source
	foreign.TaskID = "another-task"
	for _, tc := range []struct {
		name   string
		source *work.AttemptRef
	}{
		{name: "missing exact source"},
		{name: "older workflow with matching attempt", source: &historical},
		{name: "another task", source: &foreign},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := recordPolicyChange(runner, implementation.ReserveChange(implementation.Reservation{Agent: "repairer", Harness: "pi", Launch: &taskstate.AgentLaunch{Mode: taskstate.AgentLaunchResumed, SourceRunAttempt: 1, SourceExecution: tc.source}}, *policyTime(5)))
			require.ErrorContains(t, err, "owning workflow")
			after, err := store.Load("repo", "task")
			require.NoError(t, err)
			assert.Equal(t, before, after)
		})
	}
	baseline := &taskstate.AgentUsage{TotalTokens: 150}
	run, err := recordPolicyChange(runner, implementation.ReserveChange(implementation.Reservation{Agent: "repairer", Harness: "pi", Launch: &taskstate.AgentLaunch{Mode: taskstate.AgentLaunchResumed, SourceRunAttempt: 999, SourceExecution: &source, UsageBaseline: baseline}}, *policyTime(5)))
	require.NoError(t, err)
	assert.Equal(t, &source, run.Execution.Launch.SourceExecution)
	assert.Equal(t, 1, run.Execution.Launch.SourceRunAttempt, "display attempt is derived from exact provenance, never used to select it")
	assert.Equal(t, "current-session", run.Execution.Launch.SourceSession.ID)
	assert.Equal(t, baseline, run.Execution.Launch.UsageBaseline)
	after, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, before.Runs, after.Runs[:len(before.Runs)], "resumption retains historical handoffs, process and usage facts")
}
