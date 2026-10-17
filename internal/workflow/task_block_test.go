package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockSafetyFixture struct {
	item          task.Task
	state         taskstate.TaskState
	loadError     error
	mutationError error
	mutations     int
}

func (f *blockSafetyFixture) Get(context.Context, string) (task.Task, error) {
	return f.item.Clone(), nil
}
func (f *blockSafetyFixture) Load(string, string) (taskstate.TaskState, error) {
	return f.state, f.loadError
}
func (f *blockSafetyFixture) Block(context.Context, string) error {
	f.mutations++
	return f.mutationError
}
func (f *blockSafetyFixture) Unblock(ctx context.Context, id string) error { return f.Block(ctx, id) }

func TestTaskBlockingSafetyPreservesHistoryAndRefusesUnsafeWork(t *testing.T) {
	failed := taskstate.RunAttempt{Attempt: 1, Status: taskstate.RunStatusFailed, Execution: taskstate.AgentExecution{Status: taskstate.RunStatusFailed}}
	running := taskstate.RunAttempt{Attempt: 2, Status: taskstate.RunStatusRunning, Execution: taskstate.AgentExecution{Status: taskstate.RunStatusRunning, SupervisorPID: 10, ChildPID: 11}}
	completed := failed
	completed.Completion = &taskstate.Completion{Summary: "feat: completed"}
	repair := failed
	repair.Attempt = 3
	repair.ReviewFollowUp = &taskstate.ReviewFollowUp{ReviewAttempt: 1}
	storageError := errors.New("unreadable history")
	writeError := errors.New("source unavailable")
	for _, tc := range []struct {
		name                     string
		status                   task.Status
		metadata                 task.Metadata
		runs                     []taskstate.RunAttempt
		reviews                  []taskstate.ReviewAttempt
		finalization             *taskstate.Finalization
		conflict                 *taskstate.SyncConflictOperation
		probe                    workflow.ProcessProbe
		loadError, mutationError error
		want                     string
		cause                    error
	}{
		{name: "native in-progress without executions"},
		{name: "failed implementation", runs: []taskstate.RunAttempt{failed}},
		{name: "proven inactive interrupted implementation", runs: []taskstate.RunAttempt{running}},
		{name: "failed targeted repair retains prior review", runs: []taskstate.RunAttempt{completed, repair}, reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusBlocked}}},
		{name: "live supervisor", runs: []taskstate.RunAttempt{running}, probe: func(int) (agentexec.ProcessLiveness, error) { return agentexec.ProcessLive, nil }, want: "active or unverifiable execution"},
		{name: "live child", runs: []taskstate.RunAttempt{running}, probe: func(pid int) (agentexec.ProcessLiveness, error) {
			if pid == 11 {
				return agentexec.ProcessLive, nil
			}
			return agentexec.ProcessAbsent, nil
		}, want: "child_pid_live"},
		{name: "legacy execution", runs: []taskstate.RunAttempt{{Attempt: 1, Status: taskstate.RunStatusRunning}}, want: "missing_supervisor_pid"},
		{name: "probe failure", runs: []taskstate.RunAttempt{running}, probe: func(int) (agentexec.ProcessLiveness, error) {
			return agentexec.ProcessUnknown, errors.New("permission denied")
		}, want: "probe_failed"},
		{name: "older active execution cannot be hidden by a newer failed run", runs: []taskstate.RunAttempt{running, repair}, probe: func(int) (agentexec.ProcessLiveness, error) { return agentexec.ProcessLive, nil }, want: "active or unverifiable execution"},
		{name: "handoff before process exit", runs: []taskstate.RunAttempt{completed}, want: "review boundary"},
		{name: "waiting manual review", runs: []taskstate.RunAttempt{completed}, reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusWaitingForManual}}, want: "review boundary"},
		{name: "running review without run", reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusRunning}}, want: "review boundary"},
		{name: "live reviewer", reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusRunning, Steps: []taskstate.ReviewStep{{Execution: &running.Execution}}}}, probe: func(int) (agentexec.ProcessLiveness, error) { return agentexec.ProcessLive, nil }, want: "active or unverifiable execution"},
		{name: "passed review", runs: []taskstate.RunAttempt{failed}, reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusPassed}}, want: "review boundary"},
		{name: "review gate cannot be hidden by incomplete repair", runs: []taskstate.RunAttempt{repair}, reviews: []taskstate.ReviewAttempt{{Attempt: 1, Status: taskstate.ReviewStatusWaitingForAutomatedDecision}}, want: "review boundary"},
		{name: "published PR", metadata: task.Metadata{task.MetadataPRURL: "https://example.invalid/pr/1"}, want: "publication boundary"},
		{name: "publication in progress", finalization: &taskstate.Finalization{Commit: "abc123"}, want: "publication boundary"},
		{name: "sync conflict", conflict: &taskstate.SyncConflictOperation{}, want: "publication boundary"},
		{name: "lost execution history", metadata: task.Metadata{task.MetadataBranch: "work/block", task.MetadataWorktree: "/fixture/worktree"}, want: "no run history"},
		{name: "closed work", status: task.StatusClosed, want: "only open or inactive in-progress"},
		{name: "unknown native lifecycle", status: task.Status("cancelled"), want: "only open or inactive in-progress"},
		{name: "unreadable history", loadError: storageError, want: "cannot verify execution history", cause: storageError},
		{name: "failed source write", mutationError: writeError, want: "inspect `orpheus task show op-item`", cause: writeError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, blocked := range []bool{true, false} {
				status := tc.status
				if status == "" {
					status = task.StatusInProgress
				}
				if !blocked && (tc.status == task.StatusClosed || tc.status == task.Status("cancelled")) {
					continue
				}
				if !blocked {
					status = task.StatusBlocked
				}
				fixture := &blockSafetyFixture{
					item:      task.Task{ID: "op-item", IssueType: task.IssueTypeTask, Status: status, Metadata: tc.metadata},
					state:     taskstate.TaskState{Runs: tc.runs, Reviews: tc.reviews, Finalization: tc.finalization, ActiveSyncConflict: tc.conflict},
					loadError: tc.loadError, mutationError: tc.mutationError,
				}
				assertBlockingSafety(t, fixture, tc.probe, blocked, tc.want, tc.cause)
			}
		})
	}
}

func assertBlockingSafety(t *testing.T, fixture *blockSafetyFixture, probe workflow.ProcessProbe, blocked bool, want string, cause error) {
	t.Helper()
	before, err := json.Marshal(fixture.state)
	require.NoError(t, err)
	if probe == nil {
		probe = func(int) (agentexec.ProcessLiveness, error) { return agentexec.ProcessAbsent, nil }
	}
	changed, err := (workflow.BlockService{Store: fixture, Probe: probe}).SetBlocked(t.Context(), fixture, "alpha", "op-item", blocked)
	if want == "" {
		require.NoError(t, err)
		assert.True(t, changed)
		assert.Equal(t, 1, fixture.mutations)
	} else {
		require.ErrorContains(t, err, want)
		assert.False(t, changed)
		if cause != nil {
			assert.ErrorIs(t, err, cause)
		}
		if fixture.mutationError == nil {
			assert.Zero(t, fixture.mutations)
		}
	}
	after, marshalErr := json.Marshal(fixture.state)
	require.NoError(t, marshalErr)
	assert.Equal(t, before, after, "blocking never rewrites execution or review history")
}
