//go:build integration

package cli_test

import (
	"testing"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowAgentContextRendersValidatedWorktreeContext(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	backend, repoPath, worktreePath, cwd := setupOwnershipAgentImplementation(t, fixture, true)

	stdout, stderr := fixture.mustExecute("agent", "context")

	is.Empty(stderr)
	for _, want := range []string{
		"# Orpheus Agent Context",
		"- ID: op-1",
		"- Title: Render context",
		"Move detailed task instructions to agent context.",
		"Only the latest running attempt can render context.",
		"- ID: alpha",
		"- Name: Alpha Repo",
		"- Registered root: " + repoPath,
		"- Registered default branch: main",
		"- Current branch: orpheus/op-1",
		"- Work Directory: " + worktreePath,
		"- Current directory: " + cwd,
		"- Run attempt: 1",
		"- Agent: recorder",
		"orpheus agent done",
		"Do not create Git commits yourself. Leave completed changes uncommitted; Orpheus owns commit creation at the appropriate workflow stage.",
		"one-time completion handoff for this Orpheus run attempt",
		"not once per reusable harness session",
		"call it exactly once after finishing the current attempt's work",
		"whether this harness session is fresh or resumed",
		"successful `orpheus agent done` visible in resumed session history belongs to an earlier",
		"does not satisfy the current attempt",
		"do not run `orpheus agent done` again",
		"repeated same-attempt calls are no-ops",
		"PR-ready completion data for feature-branch publication",
		"The human operator will later run `orpheus task run op-1` to review and publish the feature branch as a pull request",
		"Interaction guidance:",
		"attached interactive implementation session",
		"may ask the human operator for clarification or decisions",
		"Minimize interruptions",
		"ask only for critical ambiguity or major product/architecture decisions",
		"Make low-risk, low-level implementation decisions independently",
	} {
		is.Contains(stdout, want)
	}
	is.NotContains(stdout, "Beads")
	is.NotContains(stdout, "bd")
	is.Equal([]string{"op-1"}, backend.getCalls)
	is.Zero(backend.listCalls)
	is.Zero(backend.mutationCount())
}

func TestIntegrationWorkflowAgentContextRendersRepoRootFeatureBranchContext(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	_, repoPath := setupOwnershipAgentRepoRoot(t, fixture)

	stdout, stderr := fixture.mustExecute("agent", "context")

	is.Empty(stderr)
	for _, want := range []string{
		"- Current branch: orpheus/op-root",
		"- Work Directory: " + repoPath,
		"registered repository root on the task branch",
		"orpheus agent done",
		"Do not create Git commits yourself. Leave completed changes uncommitted; Orpheus owns commit creation at the appropriate workflow stage.",
		"PR-ready completion data for feature-branch publication",
		"The human operator will later run `orpheus task run op-root` to review and publish the feature branch as a pull request",
	} {
		is.Contains(stdout, want)
	}
	is.NotEmpty(stdout)
}

func TestIntegrationWorkflowAgentContextRendersNonInteractiveRunGuidanceAfterProfileChanges(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	setupOwnershipAgentImplementation(t, fixture, false)
	writeOwnershipAgentProfileConfig(t, fixture, "recorder", true)

	stdout, stderr := fixture.mustExecute("agent", "context")

	is.Empty(stderr)
	for _, want := range []string{
		"Interaction guidance:",
		"non-interactive implementation session",
		"do not ask the human operator for clarification or decisions",
		"Decide independently when a reasonable, low-risk path exists",
		"fail clearly",
		"missing information",
		"summarize significant decisions in the visible terminal/session output",
	} {
		is.Contains(stdout, want)
	}
	is.NotContains(stdout, "attached interactive implementation session")
}

func TestIntegrationWorkflowAgentContextTreatsMissingRunInteractivityAsNonInteractive(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	_, _, worktree, _ := setupOwnershipAgentImplementation(t, fixture, false)
	seedOwnershipLegacyRunningAttempt(t, fixture, "op-1", "orpheus/op-1", worktree)

	stdout, stderr := fixture.mustExecute("agent", "context")

	is.Empty(stderr)
	is.Contains(stdout, "non-interactive implementation session")
	is.NotContains(stdout, "attached interactive implementation session")
}

func TestIntegrationWorkflowAgentContextRendersReviewContext(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	_, repoPath, review := setupOwnershipActiveReview(t, fixture, "op-review")
	writeOwnershipAgentProfileConfig(t, fixture, "recorder", false)

	stdout, stderr := fixture.mustExecute("agent", "context")

	is.Empty(stderr)
	for _, want := range []string{
		"# Orpheus Review Agent Context",
		"- ID: op-review",
		"- Title: Ready for task done",
		"- Registered root: " + repoPath,
		"- Review attempt: 1",
		"- Review step: ai-review",
		"Latest completion:",
		"- Summary: Review summary",
		"- Description: Review description.",
		"strict read-only review step",
		"git status --short",
		"orpheus agent review add",
		"--type blocking",
		"--type separate-task",
		"Blocking findings require `--suggested-action`",
		"Do not call `orpheus agent done`",
	} {
		is.Contains(stdout, want)
	}
	is.NotContains(stdout, "Interaction guidance:")
	is.NotContains(stdout, "attached interactive implementation session")
	is.Equal(1, review.Attempt)
	require.NotEmpty(t, stdout)
}

func TestIntegrationWorkflowAgentContextRendersReviewFollowUpCompletionHistory(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	const taskID = "op-review-followup"
	_, _, review := setupOwnershipActiveReview(t, fixture, taskID)
	store := taskstate.NewStore(fixture.paths)
	writeOwnershipAgentProfileConfig(t, fixture, "recorder", false)

	_, err := store.FinishReview("alpha", taskID, review.Attempt, taskstate.ReviewStatusBlocked)
	require.NoError(t, err)
	followUp, err := store.StartRun("alpha", taskID, taskstate.StartRunOptions{
		Agent:  "recorder",
		Branch: "main", Worktree: ownershipAuthoringRepo().Path,
		ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: review.Attempt, FindingIndexes: []int{0}},
	})
	require.NoError(t, err)
	_, err = store.CompleteRun("alpha", taskID, followUp.Attempt, taskstate.CompleteRunOptions{
		Summary:              "Follow-up summary",
		Description:          "Follow-up description.",
		DetailedDescription:  "Follow-up detailed PR body.",
		TechnicalExplanation: "Follow-up technical explanation.",
	})
	require.NoError(t, err)
	_, err = store.FinishRun("alpha", taskID, followUp.Attempt, taskstate.RunStatusSucceeded)
	require.NoError(t, err)
	nextReview, err := store.StartReviewWithOptions("alpha", taskID, taskstate.StartReviewOptions{Pipeline: "standard", Step: "ai-review"})
	require.NoError(t, err)
	_, err = store.RecordReviewStep("alpha", taskID, nextReview.Attempt, taskstate.RecordReviewStepOptions{Kind: "agent_review", Name: "ai-review"})
	require.NoError(t, err)
	setWorkflowEnvironment(t, fixture, "ORPHEUS_REVIEW_ATTEMPT", "2")

	stdout, stderr := fixture.mustExecute("agent", "context")

	is.Empty(stderr)
	for _, want := range []string{
		"Original completion:",
		"- Summary: Review summary",
		"- Technical explanation: Technical explanation.",
		"Latest fix completion:",
		"- Summary: Follow-up summary",
		"- Description: Follow-up description.",
		"- Detailed description: Follow-up detailed PR body.",
		"- Technical explanation: Follow-up technical explanation.",
	} {
		is.Contains(stdout, want)
	}
	is.NotContains(stdout, "Latest completion:")
}

func TestIntegrationWorkflowAgentReviewAddRecordsFindingTypesAndRejectsStaleAttempt(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	_, _, review := setupOwnershipActiveReview(t, fixture, "op-review")

	stdout, stderr := fixture.mustExecute(
		"agent", "review", "add",
		"--type", "blocking",
		"--title", "Missing validation",
		"--description", "Empty IDs are accepted.",
		"--suggested-action", "Reject empty IDs and add tests.",
	)
	is.Empty(stderr)
	is.Contains(stdout, "Recorded blocking review finding 1 for op-review.")

	stdout, stderr = fixture.mustExecute(
		"agent", "review", "add",
		"--type", "advisory",
		"--title", "Small cleanup",
		"--description", "A helper could be renamed later.",
	)
	is.Empty(stderr)
	is.Contains(stdout, "Recorded advisory review finding 2 for op-review.")

	stdout, stderr = fixture.mustExecute(
		"agent", "review", "add",
		"--type", "separate-task",
		"--title", "Duplicate validation helper",
		"--description", "Extracting validation would reduce duplication.",
		"--task-title", "Extract shared validation helper",
		"--task-description", "Create a shared helper for validation.",
		"--task-acceptance-criteria", "Callers use the shared helper.",
	)
	is.Empty(stderr)
	is.Contains(stdout, "Recorded separate-task review finding 3 for op-review.")

	store := taskstate.NewStore(fixture.paths)
	state, err := store.Load("alpha", "op-review")
	require.NoError(t, err)
	latest, ok := taskstate.LatestReview(state)
	require.True(t, ok)
	require.Len(t, latest.Findings, 3)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Equal("ai-review", latest.Findings[0].Step)
	is.Equal("Reject empty IDs and add tests.", latest.Findings[0].SuggestedAction)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[1].Type)
	is.Equal(taskstate.FindingTypeSeparateTask, latest.Findings[2].Type)
	is.Equal("Extract shared validation helper", latest.Findings[2].TaskProposal.Title)
	is.Equal("Create a shared helper for validation.", latest.Findings[2].TaskProposal.Description)
	is.Equal("Callers use the shared helper.", latest.Findings[2].TaskProposal.AcceptanceCriteria)

	_, err = store.FinishReview("alpha", "op-review", review.Attempt, taskstate.ReviewStatusBlocked)
	require.NoError(t, err)
	stdout, stderr, err = fixture.execute(
		"agent", "review", "add",
		"--type", "blocking",
		"--title", "Too late",
		"--description", "This should not write.",
		"--suggested-action", "Do not record.",
	)

	require.Error(t, err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "expected \"running\"")
	state, err = store.Load("alpha", "op-review")
	require.NoError(t, err)
	latest, ok = taskstate.LatestReview(state)
	require.True(t, ok)
	is.Len(latest.Findings, 3)
}

func TestIntegrationWorkflowAgentReviewAddRejectsInvalidFindingWithoutWriting(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)
	setupOwnershipActiveReview(t, fixture, "op-review")

	stdout, stderr, err := fixture.execute(
		"agent", "review", "add",
		"--type", "blocking",
		"--title", "Missing suggested action",
		"--description", "Blocking findings need remediation guidance.",
	)

	require.Error(t, err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "blocking findings require --suggested-action")

	state, err := taskstate.NewStore(fixture.paths).Load("alpha", "op-review")
	require.NoError(t, err)
	latest, ok := taskstate.LatestReview(state)
	require.True(t, ok)
	is.Empty(latest.Findings)
}

func TestIntegrationWorkflowAgentContextFailsBeforeRenderingWhenRunIsStale(t *testing.T) {
	fixture := newCommandWorkflow(t)
	is := assert.New(t)

	repo := ownershipAuthoringRepo()
	worktree, err := fixture.paths.DataPath("repos/alpha/worktrees/op-1")
	require.NoError(t, err)
	backend := newOwnershipAuthoringBackend(taskmodel.Task{
		ID:        "op-1",
		Title:     "Render context",
		Status:    taskmodel.StatusInProgress,
		Priority:  2,
		IssueType: taskmodel.IssueTypeTask,
		Metadata: taskmodel.Metadata{
			taskmodel.MetadataBranch:   "orpheus/op-1",
			taskmodel.MetadataWorktree: worktree,
		},
	})
	setupOwnershipAuthoringWorkflow(t, fixture, repo, backend)
	store := taskstate.NewStore(fixture.paths)
	_, err = store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Branch: "orpheus/op-1", Worktree: worktree})
	require.NoError(t, err)
	_, err = store.FinishRun("alpha", "op-1", 1, taskstate.RunStatusSucceeded)
	require.NoError(t, err)
	fixture.options.AgentWorkingDirectory = worktree
	setWorkflowEnvironment(t, fixture, "ORPHEUS_REPO_ID", "alpha")
	setWorkflowEnvironment(t, fixture, "ORPHEUS_TASK_ID", "op-1")
	setWorkflowEnvironment(t, fixture, "ORPHEUS_WORKTREE", worktree)
	setWorkflowEnvironment(t, fixture, "ORPHEUS_BRANCH", "orpheus/op-1")

	stdout, stderr, err := fixture.execute("agent", "context")

	require.Error(t, err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "agent context:")
	is.Contains(err.Error(), "latest Orpheus run attempt 1")
	is.NotContains(err.Error(), "# Orpheus Agent Context")
}
