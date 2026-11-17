package review_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconcileReviewExecutionRecordsFailedReview(t *testing.T) {
	for _, failedReceipt := range []bool{false, true} {
		name := "recorded"
		if failedReceipt {
			name = "failed receipt then fresh invocation"
		}
		t.Run(name, func(t *testing.T) {
			paths := newTestPaths(t)
			raw := workstate.NewStore(paths)
			initial := runningReviewHistory()
			var err error
			initial.Version, err = raw.Save(initial, 0)
			require.NoError(t, err)
			store := &manualSaveStore{Store: raw}
			runner := phaseexec.Runner{Paths: paths, Store: store, RepositoryID: "repo", TaskID: "task"}
			injected := errors.New("recovery receipt unavailable")
			if failedReceipt {
				store.failure = injected
			}
			var inspected []int
			probe := func(pid int) (agentexec.ProcessLiveness, error) {
				inspected = append(inspected, pid)
				return agentexec.ProcessAbsent, nil
			}
			phase := review.ExecutionRecovery{ExpectedAttempt: 1, Trigger: "task_run", Probe: probe}
			selection, err := phase.Next(initial)
			require.NoError(t, err)
			assert.Empty(t, inspected, "selection does not inspect a process")
			op, ok := selection.Operation()
			require.True(t, ok)

			current, err := runner.Execute(context.Background(), initial, op)

			after, loadErr := raw.Load("repo", "task")
			require.NoError(t, loadErr)
			assert.Equal(t, []int{10, 11}, inspected)
			assert.Equal(t, 1, store.saves, "no automatic receipt or process retry")
			if failedReceipt {
				require.ErrorIs(t, err, injected)
				assert.Equal(t, initial, current, "an unsaved interruption cannot select subsequent work")
				assert.Equal(t, initial, after)
				store.failure = nil
				inspection, retryErr := review.ReconcileExecution(context.Background(), runner, 1, "task_run", probe)
				require.NoError(t, retryErr)
				assert.Equal(t, phaseexec.AttachedExecutionRecoverable, inspection.Condition)
				assert.Equal(t, "supervisor_and_child_pids_absent", inspection.Reason)
				assert.Equal(t, []int{10, 11, 10, 11}, inspected, "a fresh invocation observes process facts again")
				current, err = raw.Load("repo", "task")
				require.NoError(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, current, after)
				selection, err = phase.Next(current)
				require.NoError(t, err)
				inspection, ok := selection.Outcome()
				require.True(t, ok)
				assert.Equal(t, phaseexec.AttachedExecutionRecoverable, inspection.Condition)
				assert.Equal(t, "supervisor_and_child_pids_absent", inspection.Reason)
				assert.Equal(t, []int{10, 11}, inspected)
			}
			assert.Equal(t, initial.Version+1, current.Version)
			assert.Equal(t, initial.Workflows[0].Phases[:2], current.Workflows[0].Phases[:2])
			p := current.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
			before := initial.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
			assert.Equal(t, before.Scope, p.Scope)
			step := p.Steps[0].(*work.AgentReviewStep)
			assert.Equal(t, before.Steps[0].(*work.AgentReviewStep).Report, step.Report)
			assert.Equal(t, 10, step.Execution.SupervisorPID)
			assert.Equal(t, 11, step.Execution.ChildPID)
			recovered, ok := step.Execution.Process.(*work.InterruptedProcess)
			require.True(t, ok)
			assert.Equal(t, "supervisor_and_child_pids_absent", recovered.Reason)
			assert.Equal(t, "task_run", recovered.Trigger)
			assert.False(t, recovered.At.IsZero())
			projected, err := taskstate.Project(current)
			require.NoError(t, err)
			latest, ok := taskstate.LatestReview(projected)
			require.True(t, ok)
			assert.Equal(t, taskstate.ReviewStatusFailed, latest.Status)
			assert.True(t, taskstate.ReviewExecutionInterrupted(latest))
			assert.Empty(t, taskstate.UntargetedBlockingFindingIndexes(latest))
			assert.True(t, taskstate.AuditOnlyReviewFinding(latest, latest.Findings[0]))
		})
	}
}

func runningReviewHistory() work.State {
	current := testutil.ReviewableWork("repo", "task", "/fixture/work", "task-branch")
	w := current.ActiveWorkflow()
	w.ReviewContracts = []work.ReviewContract{{PipelineKey: "ai", Slots: []work.ReviewSlot{{Kind: work.ReviewAgent, ConfigKey: "ai-review"}}}}
	at := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	w.Phases = append(w.Phases, &work.ReviewPhase{StartedAt: at, Scope: work.ReviewScope{Requirements: "Fixture task requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}}, Steps: []work.ReviewStep{
		&work.AgentReviewStep{Slot: 0, Execution: work.AgentExecution{Profile: "reviewer", Harness: "pi", Command: "fixture-reviewer", StartedAt: at, SupervisorPID: 10, ChildPID: 11, Process: &work.RunningProcess{}}, Report: &work.ReviewReport{Findings: []work.Finding{{Classification: work.FindingBlocking, Title: "Unfinished finding", Description: "Retain for audit", Dispositions: []work.FindingDisposition{&work.RepairRequired{}}}}}},
	}})
	return current
}
