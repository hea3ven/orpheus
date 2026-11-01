//nolint:testpackage // Exercises phase selection beside the controller's policy helpers.
package workflow

import (
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectTaskRunRouteUsesLastTypedPhase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*work.Workflow)
		want   TaskRunAction
	}{
		{name: "established preparation", change: func(w *work.Workflow) { w.Phases = w.Phases[:1] }, want: TaskRunActionStartImplementation},
		{name: "successful own handoff", want: TaskRunActionStartReview},
		{name: "active handoff is not reviewable", change: func(w *work.Workflow) {
			p := w.Phases[1].(*work.ImplementationPhase)
			p.Output = nil
			p.Execution.Process = &work.RunningProcess{}
		}, want: TaskRunActionImplementationActive},
		{name: "failed handoff is not reviewable", change: func(w *work.Workflow) {
			p := w.Phases[1].(*work.ImplementationPhase)
			p.Output = nil
			p.Execution.Process = &work.ExitedProcess{At: routeAt(5), Code: routeNumber(1)}
		}, want: TaskRunActionStartImplementation},
		{name: "interrupted handoff is not reviewable", change: func(w *work.Workflow) {
			p := w.Phases[1].(*work.ImplementationPhase)
			p.Output = nil
			p.Execution.Process = &work.InterruptedProcess{At: routeAt(5), Reason: "lost supervisor"}
		}, want: TaskRunActionStartImplementation},
		{name: "successful exit without handoff retries implementation", change: func(w *work.Workflow) {
			p := w.Phases[1].(*work.ImplementationPhase)
			p.Output = nil
			p.Completion = nil
		}, want: TaskRunActionStartImplementation},
		{name: "pending review is active", change: func(w *work.Workflow) { appendRouteReview(w, false) }, want: TaskRunActionReviewActive},
		{name: "manual assessment resumes", change: func(w *work.Workflow) {
			p := appendRouteReview(w, true)
			p.Steps = []work.ReviewStep{&work.ManualReviewStep{Slot: 0}}
		}, want: TaskRunActionResumeReview},
		{name: "manual changes request repairs", change: func(w *work.Workflow) { appendRouteBlocker(w) }, want: TaskRunActionTargetedRepair},
		{name: "automated blocker requires a decision", change: func(w *work.Workflow) {
			p := appendRouteReview(w, false)
			p.Steps = []work.ReviewStep{&work.CheckStep{Slot: 0, Result: &work.CheckResult{ExitCode: routeNumber(1), FinishedAt: routeAt(7)}, Findings: []work.Finding{routeFinding()}}}
		}, want: TaskRunActionResumeReview},
		{name: "paused automated decision resumes", change: func(w *work.Workflow) {
			p := appendRouteReview(w, false)
			p.Steps = []work.ReviewStep{&work.CheckStep{Slot: 0, Result: &work.CheckResult{ExitCode: routeNumber(1), FinishedAt: routeAt(7)}, Findings: []work.Finding{routeFinding()}, DecisionPauses: []work.AutomatedDecisionPause{{At: routeAt(8), Kind: work.DecisionPaused}}}}
		}, want: TaskRunActionResumeReview},
		{name: "aborted review restarts", change: func(w *work.Workflow) {
			p := appendRouteReview(w, true)
			p.Abort = &work.AbortedReview{At: routeAt(7), Reason: "operator stopped"}
		}, want: TaskRunActionStartReview},
		{name: "failed review restarts", change: func(w *work.Workflow) {
			p := appendRouteReview(w, false)
			p.ExecutionFailure = &work.ReviewExecutionFailure{At: routeAt(7), Reason: "check failed to start"}
		}, want: TaskRunActionStartReview},
		{name: "passed review enters publication not closure", change: func(w *work.Workflow) {
			p := appendRouteReview(w, false)
			p.Steps = []work.ReviewStep{&work.CheckStep{Slot: 0, Result: &work.CheckResult{ExitCode: routeNumber(0), FinishedAt: routeAt(7)}}}
		}, want: TaskRunActionPreparePublication},
		{name: "failed repair retains selected repair action", change: func(w *work.Workflow) { appendRouteBlocker(w); appendRouteRepair(w, false) }, want: TaskRunActionTargetedRepair},
		{name: "completed repair starts review rather than old blockers", change: func(w *work.Workflow) { appendRouteBlocker(w); appendRouteRepair(w, true) }, want: TaskRunActionStartReview},
		{name: "newer blockers are not replaced by older successful repair", change: func(w *work.Workflow) {
			appendRouteBlocker(w)
			appendRouteRepair(w, true)
			p := appendRouteBlocker(w)
			p.Scope.Output = work.OutputRef{Phase: 3}
		}, want: TaskRunActionTargetedRepair},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := testutil.ReviewableWork("alpha", "op-1", "/fixture/repo", "orpheus/op-1")
			if tc.change != nil {
				tc.change(&snapshot.Workflows[0])
			}
			view, err := taskstate.Project(snapshot)
			require.NoError(t, err)
			view.Runs = append(view.Runs, taskstate.RunAttempt{Attempt: 999, Status: taskstate.RunStatusRunning})
			view.Reviews = append(view.Reviews, taskstate.ReviewAttempt{Attempt: 999, Status: taskstate.ReviewStatusPassed})
			// A PR URL and conflicting task-wide projections are adversarial inputs:
			// neither may independently override the selected occurrence.
			item := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataPRURL: "https://example.test/pr/old"}}
			route, err := SelectTaskRunRoute(item, view)
			require.NoError(t, err)
			assert.Equal(t, tc.want, route.Action)
			require.NotNil(t, route.Owner)
			assert.Equal(t, len(snapshot.Workflows[0].Phases)-1, route.Owner.Phase)
			assert.Empty(t, route.PRURL)
		})
	}
}

func TestSelectTaskRunRoutePublicationAcceptanceAndClosureHaveSeparateOwners(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(map[bool]string{false: "PR", true: "direct"}[direct], func(t *testing.T) {
			snapshot, err := testutil.DeliveredPRWork("alpha", "op-1", "/fixture/repo", "orpheus/op-1", "https://example.test/pr/recorded")
			require.NoError(t, err)
			w := &snapshot.Workflows[0]
			w.Phases = w.Phases[:4]
			publication := w.Phases[3].(*work.PublicationPhase)
			publication.Consent = nil
			publication.Delivery = nil
			if direct {
				publication.Plan.Target = &work.DirectMergeTarget{}
			}
			item := task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataPRURL: "https://example.test/pr/unrelated"}}
			route := assertRoute(t, snapshot, item, TaskRunActionAwaitDecision)
			assert.Equal(t, 3, route.Owner.Phase)
			require.NoError(t, snapshot.AuthorizePublication(0, work.PublicationRef{Phase: 3}, routeAt(10), "Operator consent for this exact plan"))
			var accepted work.Acceptance
			if direct {
				publication.Delivery = &work.DirectDelivery{PublicCommit: "fixture-output", Destination: work.CodeIdentity{Commit: "merged", Tree: "fixture-tree"}, ObservedAt: routeAt(11), Evidence: "Observed destination push"}
				assertRoute(t, snapshot, item, TaskRunActionPublish)
				accepted = &work.DirectAcceptance{Publication: work.PublicationRef{Phase: 3}}
			} else {
				publication.Delivery = &work.PullRequestDelivery{PullRequest: work.PullRequestRef{URL: "https://example.test/pr/recorded"}, PublicCommit: "fixture-output", Head: "fixture-output", Tree: "fixture-tree", ObservedAt: routeAt(11), Evidence: "Observed PR delivery"}
				w.Phases = append(w.Phases, &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: 3}})
				route = assertRoute(t, snapshot, item, TaskRunActionOpenPR)
				assert.Equal(t, "https://example.test/pr/recorded", route.PRURL)
				accepted = &work.PRAcceptance{ExternalReview: 4, ObservedAt: routeAt(12), Evidence: "Provider observed merged"}
			}
			final := &work.FinalizationPhase{Acceptance: accepted}
			w.Phases = append(w.Phases, final)
			assertRoute(t, snapshot, item, TaskRunActionRetryFinalization)
			final.Closure = &work.TaskClosure{At: routeAt(13), Reason: "Delivered and accepted", Evidence: "Source observed closed"}
			assertRoute(t, snapshot, item, TaskRunActionCompleted)
			item.Status = task.StatusClosed
			view, err := taskstate.Project(snapshot)
			require.NoError(t, err)
			_, err = SelectTaskRunRoute(item, view)
			require.ErrorContains(t, err, "task op-1 is closed")
		})
	}
	// An empty current workflow cannot fall back to earlier output or approval.
	history, err := testutil.DeliveredPRWork("alpha", "op-1", "/fixture/repo", "orpheus/op-1", "https://example.test/pr/old")
	require.NoError(t, err)
	history.Workflows = append(history.Workflows, work.Workflow{})
	route := assertRoute(t, history, task.Task{ID: "op-1", Status: task.StatusInProgress, Metadata: task.Metadata{task.MetadataPRURL: "https://example.test/pr/old"}}, TaskRunActionStartImplementation)
	assert.Nil(t, route.Owner)
	// Without local history a native PR remains advice, not invented local work.
	route, err = SelectTaskRunRoute(task.Task{ID: "op-1", Metadata: task.Metadata{task.MetadataPRURL: "https://example.test/pr/import"}}, taskstate.TaskState{})
	require.NoError(t, err)
	assert.Equal(t, TaskRunActionOpenPR, route.Action)
	route, err = SelectTaskRunRoute(task.Task{ID: "op-1"}, taskstate.TaskState{})
	require.NoError(t, err)
	assert.Equal(t, TaskRunActionStartImplementation, route.Action)
}

func assertRoute(t *testing.T, snapshot work.State, item task.Task, want TaskRunAction) TaskRunRoute {
	t.Helper()
	view, err := taskstate.Project(snapshot)
	require.NoError(t, err)
	route, err := SelectTaskRunRoute(item, view)
	require.NoError(t, err)
	assert.Equal(t, want, route.Action)
	return route
}
func routeAt(seconds int) time.Time { return time.Date(2026, 1, 1, 0, 0, seconds, 0, time.UTC) }
func routeFinding() work.Finding {
	return work.Finding{Classification: work.FindingBlocking, Title: "Fix output", Description: "Required correction", Dispositions: []work.FindingDisposition{&work.RepairRequired{}}}
}
func appendRouteReview(w *work.Workflow, manual bool) *work.ReviewPhase {
	kind := work.ReviewCheck
	if manual {
		kind = work.ReviewManual
	}
	contract := len(w.ReviewContracts)
	w.ReviewContracts = append(w.ReviewContracts, work.ReviewContract{PipelineKey: "quality", Slots: []work.ReviewSlot{{Kind: kind, ConfigKey: "inspect"}}})
	output, _ := w.LatestOutput()
	started := routeAt(6)
	if output.Phase > 1 {
		started = routeAt(12)
	}
	p := &work.ReviewPhase{Contract: contract, StartedAt: started, Scope: work.ReviewScope{Requirements: "Task intent", Output: output, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "destination"}}}
	w.Phases = append(w.Phases, p)
	return p
}
func appendRouteBlocker(w *work.Workflow) *work.ReviewPhase {
	p := appendRouteReview(w, true)
	at := routeAt(8)
	if len(w.Phases) > 3 {
		at = routeAt(14)
	}
	p.Steps = []work.ReviewStep{&work.ManualReviewStep{Slot: 0, Findings: []work.Finding{routeFinding()}, Decision: &work.RequestedChanges{At: at}}}
	return p
}
func appendRouteRepair(w *work.Workflow, complete bool) {
	input := work.OutputRef{Phase: 1}
	p := &work.ImplementationPhase{Input: &input, TargetFindings: []work.FindingRef{{Phase: 2, Step: 0, Finding: 0}}, Execution: work.AgentExecution{StartedAt: routeAt(9), Process: &work.ExitedProcess{At: routeAt(11), Code: routeNumber(0)}}}
	if complete {
		p.Completion = &work.Completion{Summary: "fix: repair", Description: "Repair rationale", DetailedDescription: "Repair details", CompletedAt: routeAt(10)}
		p.Output = &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: "R2", Tree: "T2"}, RetainedRef: "refs/orpheus/repair"}
	}
	w.Phases = append(w.Phases, p)
}

func routeNumber(n int) *int { return &n }
