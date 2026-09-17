//go:build integration

//nolint:testpackage // Invocation-scoped fixture requires internal composition wiring.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationAgentContextRendersValidatedWorktreeContext(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	repoPath, worktreePath, cwd, bdLogPath := setupAgentContextWorktree(t)
	configPath := filepath.Join(testInvocationFor(t).root, "xdg-config", state.AppName, agent.ConfigFile)
	must.NoError(os.Remove(configPath))

	stdout, stderr := executeCommand(t, []string{"agent", "context"})

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

	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-1")
	is.NotContains(string(bdLog), "--json --sandbox update")
	is.NotContains(string(bdLog), "--json --readonly --sandbox list")
}

func TestIntegrationAgentContextRendersRepoRootFeatureBranchContext(t *testing.T) {
	is := assert.New(t)
	root := newTestState(t)
	repoPath := filepath.Join(root, "repos", "alpha")
	writeAgentContextProfileConfig(t, "recorder", true)
	registerAgentTestRepo(t, repoPath)
	t.Chdir(repoPath)
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: agentContextRepoRootTaskJSON(repoPath)},
	})
	startAgentTestRun(t, "op-root", "orpheus/op-root", repoPath, true)
	setAgentRunEnv(t, "op-root", "orpheus/op-root", repoPath)

	stdout, stderr := executeCommand(t, []string{"agent", "context"})

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

func TestIntegrationAgentContextRendersNonInteractiveRunGuidanceAfterProfileChanges(t *testing.T) {
	is := assert.New(t)
	setupAgentContextWorktreeWithInteractivity(t, false)
	writeAgentContextProfileConfig(t, "recorder", true)

	stdout, stderr := executeCommand(t, []string{"agent", "context"})

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

func TestIntegrationAgentContextTreatsMissingRunInteractivityAsNonInteractive(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	setupAgentContextWorktreeWithInteractivity(t, false)
	paths := currentTestPaths(t)
	statePath, err := paths.DataPath(filepath.Join("repos", "alpha", "tasks", "op-1.yaml"))
	must.NoError(err)
	stateYAML := readFileString(t, statePath)
	must.Contains(stateYAML, "        interactive: false\n")
	legacyStateYAML := strings.Replace(stateYAML, "        interactive: false\n", "", 1)
	must.NoError(os.WriteFile(statePath, []byte(legacyStateYAML), 0o644))
	configPath := filepath.Join(testInvocationFor(t).root, "xdg-config", state.AppName, agent.ConfigFile)
	must.NoError(os.WriteFile(configPath, []byte("agents:\n  defaults: {}\n  profiles: {}\n"), 0o644))

	stdout, stderr := executeCommand(t, []string{"agent", "context"})

	is.Empty(stderr)
	is.Contains(stdout, "non-interactive implementation session")
	is.NotContains(stdout, "attached interactive implementation session")
}

func TestIntegrationAgentContextRendersReviewContext(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	repoPath, review := setupActiveAgentReview(t, "op-review")
	writeAgentContextProfileConfig(t, "recorder", false)

	stdout, stderr := executeCommand(t, []string{"agent", "context"})

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
	must.NotEmpty(stdout)
}

func TestIntegrationAgentContextRendersReviewFollowUpCompletionHistory(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	const taskID = "op-review-followup"
	repoPath, review := setupActiveAgentReview(t, taskID)
	paths := currentTestPaths(t)
	store := taskstate.NewStore(paths)
	writeAgentContextProfileConfig(t, "recorder", false)

	_, err := store.FinishReview("alpha", taskID, review.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)
	followUp, err := store.StartRun("alpha", taskID, taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: repoPath,
		ReviewFollowUp: &taskstate.ReviewFollowUp{
			ReviewAttempt:  review.Attempt,
			FindingIndexes: []int{0},
		},
	})
	must.NoError(err)
	_, err = store.CompleteRun("alpha", taskID, followUp.Attempt, taskstate.CompleteRunOptions{
		Summary:              "Follow-up summary",
		Description:          "Follow-up description.",
		DetailedDescription:  "Follow-up detailed PR body.",
		TechnicalExplanation: "Follow-up technical explanation.",
	})
	must.NoError(err)
	_, err = store.FinishRun("alpha", taskID, followUp.Attempt, taskstate.RunStatusSucceeded)
	must.NoError(err)
	nextReview, err := store.StartReviewWithOptions("alpha", taskID, taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "ai-review",
	})
	must.NoError(err)
	_, err = store.RecordReviewStep("alpha", taskID, nextReview.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "agent_review",
		Name: "ai-review",
	})
	must.NoError(err)
	setTestEnvironment(t, "ORPHEUS_REVIEW_ATTEMPT", "2")

	stdout, stderr := executeCommand(t, []string{"agent", "context"})

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

func TestIntegrationAgentReviewAddRecordsFindingTypesAndRejectsStaleAttempt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	_, review := setupActiveAgentReview(t, "op-review")
	paths := currentTestPaths(t)
	descriptionFile := filepath.Join(testutil.CanonicalTempDir(t), "finding.md")
	taskDescriptionFile := filepath.Join(testutil.CanonicalTempDir(t), "task.md")
	taskAcceptanceFile := filepath.Join(testutil.CanonicalTempDir(t), "acceptance.md")
	must.NoError(os.WriteFile(descriptionFile, []byte("Extracting validation would reduce duplication.\n"), 0o644))
	must.NoError(os.WriteFile(taskDescriptionFile, []byte("Create a shared helper for validation.\n"), 0o644))
	must.NoError(os.WriteFile(taskAcceptanceFile, []byte("Callers use the shared helper.\n"), 0o644))

	stdout, stderr := executeCommand(t, []string{
		"agent", "review", "add",
		"--type", "blocking",
		"--title", "Missing validation",
		"--description", "Empty IDs are accepted.",
		"--suggested-action", "Reject empty IDs and add tests.",
	})
	is.Empty(stderr)
	is.Contains(stdout, "Recorded blocking review finding 1 for op-review.")

	stdout, stderr = executeCommand(t, []string{
		"agent", "review", "add",
		"--type", "advisory",
		"--title", "Small cleanup",
		"--description", "A helper could be renamed later.",
	})
	is.Empty(stderr)
	is.Contains(stdout, "Recorded advisory review finding 2 for op-review.")

	stdout, stderr = executeCommand(t, []string{
		"agent", "review", "add",
		"--type", "separate-task",
		"--title", "Duplicate validation helper",
		"--description-file", descriptionFile,
		"--task-title", "Extract shared validation helper",
		"--task-description-file", taskDescriptionFile,
		"--task-acceptance-criteria-file", taskAcceptanceFile,
	})
	is.Empty(stderr)
	is.Contains(stdout, "Recorded separate-task review finding 3 for op-review.")

	store := taskstate.NewStore(paths)
	state, err := store.Load("alpha", "op-review")
	must.NoError(err)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	must.Len(latest.Findings, 3)
	is.Equal(taskstate.FindingTypeBlocking, latest.Findings[0].Type)
	is.Equal("ai-review", latest.Findings[0].Step)
	is.Equal("Reject empty IDs and add tests.", latest.Findings[0].SuggestedAction)
	is.Equal(taskstate.FindingTypeAdvisory, latest.Findings[1].Type)
	is.Equal(taskstate.FindingTypeSeparateTask, latest.Findings[2].Type)
	is.Equal("Extract shared validation helper", latest.Findings[2].TaskProposal.Title)
	is.Equal("Create a shared helper for validation.", latest.Findings[2].TaskProposal.Description)
	is.Equal("Callers use the shared helper.", latest.Findings[2].TaskProposal.AcceptanceCriteria)

	_, err = store.FinishReview("alpha", "op-review", review.Attempt, taskstate.ReviewStatusBlocked)
	must.NoError(err)
	stdout, stderr, err = executeCommandWithError(t, []string{
		"agent", "review", "add",
		"--type", "blocking",
		"--title", "Too late",
		"--description", "This should not write.",
		"--suggested-action", "Do not record.",
	})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "expected \"running\"")
	state, err = store.Load("alpha", "op-review")
	must.NoError(err)
	latest, ok = taskstate.LatestReview(state)
	must.True(ok)
	is.Len(latest.Findings, 3)
}

func TestIntegrationAgentReviewAddRejectsInvalidFindingWithoutWriting(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	setupActiveAgentReview(t, "op-review")

	stdout, stderr, err := executeCommandWithError(t, []string{
		"agent", "review", "add",
		"--type", "blocking",
		"--title", "Missing suggested action",
		"--description", "Blocking findings need remediation guidance.",
	})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.ErrorContains(err, "blocking findings require --suggested-action")

	state, err := taskstate.NewStore(currentTestPaths(t)).Load("alpha", "op-review")
	must.NoError(err)
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Empty(latest.Findings)
}

func setupAgentContextWorktree(t *testing.T) (string, string, string, string) {
	t.Helper()
	return setupAgentContextWorktreeWithInteractivity(t, true)
}

func setupAgentContextWorktreeWithInteractivity(t *testing.T, interactive bool) (string, string, string, string) {
	t.Helper()

	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	writeAgentContextProfileConfig(t, "recorder", true)
	repoPath := filepath.Join(root, "repos", "alpha")
	registerAgentTestRepo(t, repoPath)
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-1"))
	must.NoError(err)
	cwd := filepath.Join(worktreePath, "internal")
	must.NoError(os.MkdirAll(cwd, 0o755))
	t.Chdir(cwd)
	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: agentContextTaskJSON(worktreePath)},
	})
	startAgentTestRun(t, "op-1", "orpheus/op-1", worktreePath, interactive)
	setAgentRunEnv(t, "op-1", "orpheus/op-1", worktreePath)
	return repoPath, worktreePath, cwd, bdLogPath
}

func agentContextTaskJSON(worktreePath string) string {
	return `[
		{
			"id":"op-1",
			"title":"Render context",
			"description":"Move detailed task instructions to agent context.",
			"acceptance_criteria":"Only the latest running attempt can render context.",
			"status":"in_progress",
			"priority":2,
			"issue_type":"task",
			"metadata":{"orpheus.branch":"orpheus/op-1","orpheus.worktree":"` + worktreePath + `"}
		}
	]`
}

func agentContextRepoRootTaskJSON(repoPath string) string {
	return `[
		{
			"id":"op-root",
			"title":"Render repo-root context",
			"status":"in_progress",
			"priority":2,
			"issue_type":"task",
			"metadata":{"orpheus.branch":"orpheus/op-root","orpheus.worktree":"` + repoPath + `"}
		}
	]`
}

func registerAgentTestRepo(t *testing.T, repoPath string) {
	t.Helper()

	must := require.New(t)
	must.NoError(os.MkdirAll(repoPath, 0o755))
	must.NoError(registry.NewStore(currentTestPaths(t)).Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
}

func writeAgentContextProfileConfig(t *testing.T, name string, interactive bool) {
	t.Helper()

	profile := map[string]any{"command": name}
	if !interactive {
		profile["interactive"] = false
	}
	require.NoError(t, testutil.WriteConfigYAML(currentTestPaths(t), "config.yaml", map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer": name,
			},
			"profiles": map[string]any{
				name: profile,
			},
		},
	}))
}

func startAgentTestRun(t *testing.T, taskID string, branch string, worktreePath string, interactive bool) taskstate.RunAttempt {
	t.Helper()

	attempt, err := taskstate.NewStore(currentTestPaths(t)).StartRun("alpha", taskID, taskstate.StartRunOptions{
		Agent:       "recorder",
		Interactive: interactive,
		Branch:      branch,
		Worktree:    worktreePath,
	})
	require.NoError(t, err)
	return attempt
}

func setAgentRunEnv(t *testing.T, taskID string, branch string, worktreePath string) {
	t.Helper()

	setTestEnvironment(t, "ORPHEUS_REPO_ID", "alpha")
	setTestEnvironment(t, "ORPHEUS_TASK_ID", taskID)
	setTestEnvironment(t, "ORPHEUS_WORKTREE", worktreePath)
	setTestEnvironment(t, "ORPHEUS_BRANCH", branch)
}

func setupActiveAgentReview(t *testing.T, taskID string) (string, taskstate.ReviewAttempt) {
	t.Helper()

	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	repoPath := filepath.Join(root, "repos", "alpha")
	registerAgentTestRepo(t, repoPath)
	t.Chdir(repoPath)
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: mainReadyTaskJSON(taskID, repoPath)},
	})
	recordMainCompletion(t, paths, "alpha", taskID, repoPath, "Review summary", "Review description.")
	store := taskstate.NewStore(paths)
	review, err := store.StartReviewWithOptions("alpha", taskID, taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "ai-review",
	})
	must.NoError(err)
	_, err = store.RecordReviewStep("alpha", taskID, review.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "agent_review",
		Name: "ai-review",
	})
	must.NoError(err)
	setAgentRunEnv(t, taskID, "main", repoPath)
	setTestEnvironment(t, "ORPHEUS_AGENT_PURPOSE", "review")
	setTestEnvironment(t, "ORPHEUS_REVIEW_ATTEMPT", "1")
	setTestEnvironment(t, "ORPHEUS_REVIEW_STEP", "ai-review")
	return repoPath, review
}

func TestIntegrationAgentContextFailsBeforeRenderingWhenRunIsStale(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)
	repoPath := filepath.Join(root, "repos", "alpha")
	must.NoError(os.MkdirAll(repoPath, 0o755))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-1"))
	must.NoError(err)
	must.NoError(os.MkdirAll(worktreePath, 0o755))
	t.Chdir(worktreePath)
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{
				"id":"op-1",
				"title":"Render context",
				"status":"in_progress",
				"priority":2,
				"issue_type":"task",
				"metadata":{"orpheus.branch":"orpheus/op-1","orpheus.worktree":"` + worktreePath + `"}
			}
		]`},
	})
	runStore := taskstate.NewStore(paths)
	_, err = runStore.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Branch:   "orpheus/op-1",
		Worktree: worktreePath,
	})
	must.NoError(err)
	_, err = runStore.FinishRun("alpha", "op-1", 1, taskstate.RunStatusSucceeded)
	must.NoError(err)
	setTestEnvironment(t, "ORPHEUS_REPO_ID", "alpha")
	setTestEnvironment(t, "ORPHEUS_TASK_ID", "op-1")
	setTestEnvironment(t, "ORPHEUS_WORKTREE", worktreePath)
	setTestEnvironment(t, "ORPHEUS_BRANCH", "orpheus/op-1")

	stdout, stderr, err := executeCommandWithError(t, []string{"agent", "context"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "agent context:")
	is.Contains(err.Error(), "latest Orpheus run attempt 1")
	is.NotContains(err.Error(), "# Orpheus Agent Context")
}
