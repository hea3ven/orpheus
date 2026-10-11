//go:build integration

package cli_test

import (
	"context"
	"errors"
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowTaskRunRejectsProjectedProblemsUntilResolved(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		relations taskmodel.RelationSummary
		related   []taskmodel.Task
		resolved  []taskmodel.Task
		reason    string
	}{
		{
			name:      "open dependency",
			relations: taskmodel.RelationSummary{DependencyIDs: []string{"op-dependency"}},
			related:   []taskmodel.Task{anOpenTask("op-dependency")},
			resolved:  []taskmodel.Task{aClosedTask("op-dependency")},
			reason:    "blocked by op-dependency",
		},
		{
			name:      "in-progress dependency",
			relations: taskmodel.RelationSummary{DependencyIDs: []string{"op-dependency"}},
			related:   []taskmodel.Task{anInProgressTask("op-dependency")},
			resolved:  []taskmodel.Task{aClosedTask("op-dependency")},
			reason:    "blocked by op-dependency",
		},
		{
			name:      "missing dependency",
			relations: taskmodel.RelationSummary{DependencyIDs: []string{"op-dependency"}},
			resolved:  []taskmodel.Task{aClosedTask("op-dependency")},
			reason:    "missing dependency op-dependency",
		},
		{
			name:      "missing dependency details",
			relations: taskmodel.RelationSummary{BlockedByCount: 1},
			reason:    "dependency details missing for 1 blocker(s)",
		},
		{
			name:      "open parent",
			relations: taskmodel.RelationSummary{ParentID: "op-parent"},
			related:   []taskmodel.Task{anOpenEpic("op-parent")},
			resolved:  []taskmodel.Task{anInProgressEpic("op-parent")},
			reason:    "immediate parent epic op-parent is open",
		},
		{
			name:      "closed parent",
			relations: taskmodel.RelationSummary{ParentID: "op-parent"},
			related:   []taskmodel.Task{{ID: "op-parent", IssueType: taskmodel.IssueTypeEpic, Status: taskmodel.StatusClosed}},
			resolved:  []taskmodel.Task{anInProgressEpic("op-parent")},
			reason:    "immediate parent epic op-parent is closed",
		},
		{
			name:      "missing parent",
			relations: taskmodel.RelationSummary{ParentID: "op-parent"},
			resolved:  []taskmodel.Task{anInProgressEpic("op-parent")},
			reason:    "immediate parent epic op-parent is missing",
		},
		{
			name:      "non-epic parent",
			relations: taskmodel.RelationSummary{ParentID: "op-parent"},
			related:   []taskmodel.Task{anInProgressTask("op-parent")},
			resolved:  []taskmodel.Task{anInProgressEpic("op-parent")},
			reason:    "immediate parent op-parent has issue_type=task",
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			item := anOpenTask("op-gated")
			item.Relations = scenario.relations
			fixture := newTaskWorkflowFixture(t, append([]taskmodel.Task{item}, scenario.related...)...)
			before, beforeTask := fixture.loadFinalTask(item.ID)
			statusOutput, _, err := fixture.execute("status", "--full", "--no-truncate")
			require.NoError(t, err)

			stdout, stderr, err := fixture.execute("task", "run", item.ID)

			require.ErrorContains(t, err, scenario.reason)
			assert.Contains(t, statusOutput, scenario.reason)
			assert.Empty(t, stdout)
			assert.Empty(t, stderr)
			rejected, rejectedTask := fixture.loadFinalTask(item.ID)
			assert.Equal(t, before, rejected)
			assert.Equal(t, beforeTask, rejectedTask)
			fixture.assertDispatchDidNotStart(rejected)

			for _, related := range scenario.resolved {
				fixture.backend.tasks[related.ID] = related
			}
			item.Relations.BlockedByCount = 0
			fixture.backend.tasks[item.ID] = item
			fixture.withAgentExitingWithoutCompletion(1)
			_, stderr, err = fixture.execute("task", "run", item.ID)

			require.NoError(t, err, "stderr: %s", stderr)
			started, startedTask := fixture.loadFinalTask(item.ID)
			require.Len(t, started.Runs, 1)
			assert.Equal(t, taskmodel.StatusInProgress, startedTask.Status)
			assert.Len(t, fixture.agent.launches, 1)
		})
	}
}

func TestIntegrationWorkflowTaskRunRejectsUnavailableRelationshipContextBeforeSetup(t *testing.T) {
	item := anOpenTask("op-unavailable")
	item.Relations.DependencyIDs = []string{"op-dependency"}
	fixture := newTaskWorkflowFixture(t, item)
	readFailure := errors.New("dependency source unavailable")
	fixture.options.Dependencies.TaskBackendFactory = func(taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		return eligibilityReadFailureBackend{memoryTaskBackend: fixture.backend, err: readFailure}, nil
	}

	stdout, stderr, err := fixture.execute("task", "run", item.ID)

	require.Error(t, err)
	assert.Contains(t, stderr+err.Error(), readFailure.Error())
	assert.Empty(t, stdout)
	state, finalTask := fixture.loadFinalTask(item.ID)
	fixture.assertDispatchDidNotStart(state)
	assert.Equal(t, item, finalTask)
}

type eligibilityReadFailureBackend struct {
	*memoryTaskBackend
	err error
}

func (b eligibilityReadFailureBackend) Get(ctx context.Context, id string) (taskmodel.Task, error) {
	if id == "op-dependency" {
		return taskmodel.Task{}, b.err
	}
	return b.memoryTaskBackend.Get(ctx, id)
}

func TestIntegrationWorkflowTaskRunRejectsOpenTaskWithLocalRunHistory(t *testing.T) {
	fixture := newTaskWorkflowFixture(t, anOpenTask("op-reopened"))
	fixture.withAgentExitingWithoutCompletion(1)
	_, stderr, err := fixture.execute("task", "run", "op-reopened")
	require.NoError(t, err, "stderr: %s", stderr)
	item := fixture.backend.tasks["op-reopened"]
	item.Status = taskmodel.StatusOpen
	fixture.backend.tasks[item.ID] = item
	before, beforeTask := fixture.loadFinalTask(item.ID)
	setups, mutations := len(fixture.git.setups), len(fixture.backend.markCalls)

	stdout, stderr, err := fixture.execute("task", "run", item.ID)

	require.ErrorContains(t, err, "backend status is open but local run attempt 1 succeeded")
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
	after, afterTask := fixture.loadFinalTask(item.ID)
	assert.Equal(t, before, after)
	assert.Equal(t, beforeTask, afterTask)
	assert.Len(t, fixture.git.setups, setups)
	assert.Len(t, fixture.backend.markCalls, mutations)
	assert.Len(t, fixture.agent.launches, 1)
}

func TestIntegrationWorkflowTaskRunRetriesAfterActivationIgnoreNewEligibilityProblems(t *testing.T) {
	for _, problem := range []string{"open parent", "closed parent", "missing parent", "new dependency"} {
		t.Run(problem, func(t *testing.T) {
			parent := anInProgressEpic("op-parent")
			fixture := newTaskWorkflowFixture(t, parent, anOpenChildTask("op-retry", parent.ID))
			failure := errors.New("implementation failed")
			fixture.withFailingAgent(failure)
			_, _, err := fixture.execute("task", "run", "op-retry")
			require.ErrorIs(t, err, failure)
			before, item := fixture.loadFinalTask("op-retry")
			switch problem {
			case "open parent":
				fixture.backend.tasks[parent.ID] = anOpenEpic(parent.ID)
			case "closed parent":
				parent.Status = taskmodel.StatusClosed
				fixture.backend.tasks[parent.ID] = parent
			case "missing parent":
				delete(fixture.backend.tasks, parent.ID)
			case "new dependency":
				item.Relations.DependencyIDs = []string{"op-dependency"}
				fixture.backend.tasks[item.ID] = item
				fixture.backend.tasks["op-dependency"] = anOpenTask("op-dependency")
			}
			fixture.withAgentExitingWithoutCompletion(1)

			_, stderr, err := fixture.execute("task", "run", item.ID)

			require.NoError(t, err, "stderr: %s", stderr)
			after, afterTask := fixture.loadFinalTask(item.ID)
			require.Len(t, after.Runs, 2)
			assert.Equal(t, before.Runs[0], after.Runs[0])
			assert.Equal(t, taskstate.RunStatusSucceeded, after.Runs[1].Status)
			assert.Equal(t, taskmodel.StatusInProgress, afterTask.Status)
			assert.Len(t, fixture.agent.launches, 2)
		})
	}
}

func TestIntegrationWorkflowTaskRunReviewsAndRepairsIgnoreNewEligibilityProblems(t *testing.T) {
	parent := anInProgressEpic("op-parent")
	fixture := newTaskWorkflowFixture(t, parent, anOpenChildTask("op-repair", parent.ID))
	fixture.withCompletingAgent(aCompletion(), aRepairCompletion())
	fixture.withRealKeptBlockerThenManualReviewPipeline(aBlockingFinding())
	fixture.agent.afterStart = func() error {
		fixture.backend.tasks[parent.ID] = anOpenEpic(parent.ID)
		item := fixture.backend.tasks["op-repair"]
		item.Relations.DependencyIDs = []string{"op-dependency"}
		fixture.backend.tasks[item.ID] = item
		fixture.backend.tasks["op-dependency"] = anOpenTask("op-dependency")
		return nil
	}

	_, stderr, err := fixture.executeWithInput("k\n", "task", "run", "op-repair")

	require.NoError(t, err, "stderr: %s", stderr)
	state, item := fixture.loadFinalTask("op-repair")
	require.Len(t, state.Runs, 2)
	assert.NotNil(t, state.Runs[1].ReviewFollowUp)
	latest, ok := taskstate.LatestReview(state)
	require.True(t, ok)
	assert.Equal(t, taskstate.ReviewStatusWaitingForManual, latest.Status)
	assert.Equal(t, taskmodel.StatusInProgress, item.Status)
	launches := len(fixture.agent.launches)

	_, stderr, err = fixture.execute("task", "run", item.ID)

	require.NoError(t, err, "resume review; stderr: %s", stderr)
	resumed, _ := fixture.loadFinalTask(item.ID)
	latest, ok = taskstate.LatestReview(resumed)
	require.True(t, ok)
	assert.Equal(t, taskstate.ReviewStatusWaitingForManual, latest.Status)
	assert.Len(t, resumed.Runs, 2)
	assert.Len(t, fixture.agent.launches, launches)
}
