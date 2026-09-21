//go:build integration

package cli_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/review"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func anOpenTask(id string) taskmodel.Task {
	return taskmodel.Task{
		ID:        id,
		Title:     "Task " + id,
		Status:    taskmodel.StatusOpen,
		IssueType: taskmodel.IssueTypeTask,
	}
}

func anInProgressTask(id string) taskmodel.Task {
	item := anOpenTask(id)
	item.Status = taskmodel.StatusInProgress
	return item
}

func anInProgressEpic(id string) taskmodel.Task {
	item := anInProgressTask(id)
	item.IssueType = taskmodel.IssueTypeEpic
	return item
}

func anOpenChildTask(id, parentID string) taskmodel.Task {
	item := anOpenTask(id)
	item.Relations.ParentID = parentID
	return item
}

func aCompletion() agent.CompleteOptions {
	return agent.CompleteOptions{
		Summary:              "feat: add search filters",
		Description:          "Filter search results by repository.",
		DetailedDescription:  "## Search filters\n\nUsers can limit results to one repository.",
		TechnicalExplanation: "Apply the repository filter before paginating results.",
	}
}

func aRepairCompletion() agent.CompleteOptions {
	return agent.CompleteOptions{
		Summary:              "fix: handle missing repositories",
		Description:          "Return an empty result for unknown repositories.",
		DetailedDescription:  "## Repair\n\nHandle unknown repositories.",
		TechnicalExplanation: "Validate the repository before querying.",
	}
}

func aBlockingFinding() taskstate.ReviewFinding {
	return taskstate.ReviewFinding{
		Type:            taskstate.FindingTypeBlocking,
		Step:            "correctness",
		Title:           "Handle unknown repositories",
		Description:     "An unknown repository must return an empty result.",
		SuggestedAction: "Return an empty result for unknown repositories.",
	}
}

func (f *taskWorkflowFixture) withCompletingAgent(completions ...agent.CompleteOptions) {
	f.t.Helper()
	f.configureImplementer("recorder", agent.Profile{Command: "unused-agent", Interactive: true})
	f.agent.outcomes = make([]semanticAgentOutcome, len(completions))
	for i := range completions {
		f.agent.outcomes[i].completion = &completions[i]
	}
	f.git.hasCandidateChanges = true
}

func (f *taskWorkflowFixture) withFailingAgent(err error) {
	f.t.Helper()
	f.configureImplementer("recorder", agent.Profile{Command: "unused-agent", Interactive: true})
	f.agent.outcomes = []semanticAgentOutcome{{err: err}}
}

func (f *taskWorkflowFixture) withAgentExitingWithoutCompletion(launches int) {
	f.t.Helper()
	f.configureImplementer("recorder", agent.Profile{Command: "unused-agent", Interactive: true})
	f.agent.outcomes = make([]semanticAgentOutcome, launches)
	for i := range f.agent.outcomes {
		f.agent.outcomes[i].exitWithoutCompletion = true
	}
}

func (f *taskWorkflowFixture) withAbsentProcesses(pids ...int) {
	f.t.Helper()
	f.options.Dependencies.ProcessProbe = func(pid int) (agentexec.ProcessLiveness, error) {
		f.probedPIDs[pid]++
		if !slices.Contains(pids, pid) {
			return agentexec.ProcessUnknown, fmt.Errorf("unexpected process probe for PID %d", pid)
		}
		return agentexec.ProcessAbsent, nil
	}
}

func (f *taskWorkflowFixture) withRealKeptBlockerThenManualReviewPipeline(finding taskstate.ReviewFinding) {
	f.t.Helper()
	f.configureReviewPipeline("inspect", []review.Step{
		{Kind: review.KindAgentReview, Name: finding.Step, Agent: "recorder"},
		{Kind: review.KindManual, Name: "accept"},
	})
	f.withRealReviewPipeline()
	reviewWithBlocker := semanticAgentOutcome{review: true, findings: []taskstate.ReviewFinding{finding}}
	reviewWithoutFindings := semanticAgentOutcome{review: true}
	require.GreaterOrEqual(f.t, len(f.agent.outcomes), 2, "blocking review scenario requires implementation and repair outcomes")
	outcomes := []semanticAgentOutcome{f.agent.outcomes[0], reviewWithBlocker, f.agent.outcomes[1], reviewWithoutFindings}
	outcomes = append(outcomes, f.agent.outcomes[2:]...)
	f.agent.outcomes = outcomes
}

func (f *taskWorkflowFixture) onlyAgentLaunch() semanticAgentLaunch {
	f.t.Helper()
	require.Len(f.t, f.agent.launches, 1, "agent launches")
	return f.agent.launches[0]
}

func assertCompletionRecorded(t *testing.T, want agent.CompleteOptions, got *taskstate.Completion) {
	t.Helper()
	require.NotNil(t, got, "recorded completion")
	assert.Equal(t, want.Summary, got.Summary)
	assert.Equal(t, want.Description, got.Description)
	assert.Equal(t, want.DetailedDescription, got.DetailedDescription)
	assert.Equal(t, want.TechnicalExplanation, got.TechnicalExplanation)
	assert.False(t, got.CompletedAt.IsZero(), "completion timestamp")
}

func assertWaitingForManualReview(t *testing.T, localState taskstate.TaskState) {
	t.Helper()
	latest, ok := taskstate.LatestReview(localState)
	require.True(t, ok, "review for %s", localState.TaskID)
	assert.Equal(t, taskstate.ReviewStatusWaitingForManual, latest.Status)
	assert.Equal(t, "local-review", latest.Step)
}

func assertTaskNotPublished(t *testing.T, localState taskstate.TaskState, item taskmodel.Task) {
	t.Helper()
	assert.Nil(t, localState.Finalization, "finalization for %s", item.ID)
	assert.False(t, item.OrpheusMetadata().HasPRURL, "PR metadata for %s", item.ID)
}

func assertEventTypes(t *testing.T, events []taskstate.Event, want ...taskstate.EventType) {
	t.Helper()
	got := make([]taskstate.EventType, len(events))
	for i, event := range events {
		got[i] = event.Type
	}
	assert.Equal(t, want, got, "event order")
}

func (f *taskWorkflowFixture) assertAgentRanInTaskWorktree(taskID string) {
	f.t.Helper()
	require.Len(f.t, f.agent.launches, 1, "agent launches")
	assert.Equal(f.t, taskWorkflowDataRoot+"/repos/alpha/worktrees/"+taskID, f.agent.launches[0].dir)
}

func (f *taskWorkflowFixture) assertIsTaskInAgentContext(taskID string) {
	f.t.Helper()
	seeded, ok := f.initialTasks[taskID]
	require.True(f.t, ok, "task %s must have been seeded", taskID)
	require.NotEmpty(f.t, seeded.Title, "the context check needs a nonempty title")
	require.Len(f.t, f.agent.contexts, 1, "agent contexts")
	assert.Contains(f.t, f.agent.contexts[0], "- ID: "+taskID+"\n")
	assert.Contains(f.t, f.agent.contexts[0], "- Title: "+seeded.Title+"\n")
}

func (f *taskWorkflowFixture) assertAgentContextTarget(branch, worktree string) {
	f.t.Helper()
	require.Len(f.t, f.agent.contexts, 1, "agent contexts")
	context := f.agent.contexts[0]
	assert.Contains(f.t, context, "# Orpheus Agent Context")
	assert.Contains(f.t, context, "- Current branch: "+branch+"\n")
	assert.Contains(f.t, context, "- Work Directory: "+worktree+"\n")
	assert.Contains(f.t, context, "- Current directory: "+worktree+"\n")
	assert.Contains(f.t, context, "deterministic task worktree and task branch")
}

func assertPersistedTaskTarget(t *testing.T, localState taskstate.TaskState, item taskmodel.Task, branch, worktree string) {
	t.Helper()
	assert.Equal(t, branch, localState.GitFacts.Branch)
	assert.Equal(t, worktree, localState.GitFacts.Worktree)
	assert.Equal(t, worktree, localState.WorkDirectory.Path)
	metadata := item.OrpheusMetadata()
	assert.Equal(t, branch, metadata.Branch)
	assert.Equal(t, worktree, metadata.Worktree)
}

func (f *taskWorkflowFixture) assertRepairContextContainsFinding(finding taskstate.ReviewFinding) {
	f.t.Helper()
	require.Len(f.t, f.agent.contexts, 2, "implementation and repair contexts")
	assert.Contains(f.t, f.agent.contexts[1], finding.Title)
}

func (f *taskWorkflowFixture) assertRepairSession(taskID string, execution taskstate.AgentExecution) {
	f.t.Helper()
	seeded, ok := f.initialTasks[taskID]
	require.True(f.t, ok, "task %s must have been seeded", taskID)
	assert.Equal(f.t, "Resolving issues in "+taskID+" "+seeded.Title, execution.SessionName)
}

func assertBootstrapPromptOmitsTaskDetails(t *testing.T, prompt, taskTitle string) {
	t.Helper()
	assert.Contains(t, prompt, "You are an agent dispatched by Orpheus.")
	assert.Contains(t, prompt, "Run `orpheus agent context` now")
	assert.Contains(t, prompt, "task instructions and execution contract")
	assert.NotContains(t, prompt, taskTitle)
}

func assertStatusShowsManualReview(t *testing.T, stdout, taskID string) {
	t.Helper()
	assert.Contains(t, stdout, taskID)
	assert.Contains(t, stdout, "Reviewing")
	assert.Contains(t, stdout, "local review; run task run")
}

func aClosedTask(id string) taskmodel.Task {
	item := anOpenTask(id)
	item.Status = taskmodel.StatusClosed
	return item
}

func anOpenEpic(id string) taskmodel.Task {
	item := anOpenTask(id)
	item.IssueType = taskmodel.IssueTypeEpic
	return item
}

func (f *taskWorkflowFixture) withCompletedRunAndPassedReview(taskID string) {
	f.t.Helper()
	f.seedRunningAttempt(taskID, 100, 101)
	completion := aCompletion()
	_, err := f.taskStore.CompleteRun("alpha", taskID, 1, taskstate.CompleteRunOptions{
		Summary: completion.Summary, Description: completion.Description,
		DetailedDescription: completion.DetailedDescription, TechnicalExplanation: completion.TechnicalExplanation,
	})
	require.NoError(f.t, err)
	_, err = f.taskStore.FinishRun("alpha", taskID, 1, taskstate.RunStatusSucceeded)
	require.NoError(f.t, err)
	review, err := f.taskStore.StartReview("alpha", taskID)
	require.NoError(f.t, err)
	_, err = f.taskStore.FinishReview("alpha", taskID, review.Attempt, taskstate.ReviewStatusPassed)
	require.NoError(f.t, err)
}
