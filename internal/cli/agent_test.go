package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentContextRendersValidatedWorktreeContext(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	repoPath, worktreePath, cwd, bdLogPath := setupAgentContextWorktree(t)

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
		"- Workflow: worktree/team",
		"- Branch: orpheus/op-1",
		"- Path: " + worktreePath,
		"- Current directory: " + cwd,
		"- Run attempt: 1",
		"- Agent: recorder",
		"orpheus agent done",
		"one-time completion handoff",
		"run it at most once",
		"do not run it again after it succeeds",
		"PR-ready completion data for feature-branch publication",
		"The human operator will later run `orpheus task review op-1` to review and publish the feature branch as a pull request",
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

func TestAgentContextRendersRepoRootFeatureBranchContext(t *testing.T) {
	is := assert.New(t)
	root := newTestState(t)
	repoPath := filepath.Join(root, "repos", "alpha")
	writeAgentContextProfileConfig(t, "recorder", true)
	registerAgentTestRepo(t, repoPath)
	t.Chdir(repoPath)
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: agentContextRepoRootTaskJSON(repoPath)},
	})
	startAgentTestRun(t, "op-root", "orpheus/op-root", repoPath)
	setAgentRunEnv(t, "op-root", "orpheus/op-root", repoPath)

	stdout, stderr := executeCommand(t, []string{"agent", "context"})

	is.Empty(stderr)
	for _, want := range []string{
		"- Workflow: repo-root/team",
		"- Branch: orpheus/op-root",
		"- Path: " + repoPath,
		"registered repository root on the task branch",
		"orpheus agent done",
		"PR-ready completion data for feature-branch publication",
		"The human operator will later run `orpheus task review op-root` to review and publish the feature branch as a pull request",
	} {
		is.Contains(stdout, want)
	}
	is.NotEmpty(stdout)
}

func TestAgentContextRendersNonInteractiveProfileGuidance(t *testing.T) {
	is := assert.New(t)
	setupAgentContextWorktree(t)
	writeAgentContextProfileConfig(t, "recorder", false)

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

func TestAgentContextRendersReviewContext(t *testing.T) {
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
		"- Workflow: main/solo",
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

func TestAgentContextRendersReviewFollowUpCompletionHistory(t *testing.T) {
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
	t.Setenv("ORPHEUS_REVIEW_ATTEMPT", "2")

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

//nolint:funlen // The three finding types and stale-write assertion share one active review setup.
func TestAgentReviewAddRecordsFindingTypesAndRejectsStaleAttempt(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	_, review := setupActiveAgentReview(t, "op-review")
	paths := currentTestPaths(t)
	descriptionFile := filepath.Join(t.TempDir(), "finding.md")
	taskDescriptionFile := filepath.Join(t.TempDir(), "task.md")
	taskAcceptanceFile := filepath.Join(t.TempDir(), "acceptance.md")
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

func TestAgentReviewAddRejectsInvalidFindingWithoutWriting(t *testing.T) {
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
	startAgentTestRun(t, "op-1", "orpheus/op-1", worktreePath)
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
	require.NoError(t, currentTestPaths(t).WriteConfigYAML("config.yaml", map[string]any{
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

func startAgentTestRun(t *testing.T, taskID string, branch string, worktreePath string) taskstate.RunAttempt {
	t.Helper()

	attempt, err := taskstate.NewStore(currentTestPaths(t)).StartRun("alpha", taskID, taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   branch,
		Worktree: worktreePath,
	})
	require.NoError(t, err)
	return attempt
}

func setAgentRunEnv(t *testing.T, taskID string, branch string, worktreePath string) {
	t.Helper()

	t.Setenv("ORPHEUS_REPO_ID", "alpha")
	t.Setenv("ORPHEUS_TASK_ID", taskID)
	t.Setenv("ORPHEUS_WORKTREE", worktreePath)
	t.Setenv("ORPHEUS_BRANCH", branch)
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
	t.Setenv("ORPHEUS_AGENT_PURPOSE", "review")
	t.Setenv("ORPHEUS_REVIEW_ATTEMPT", "1")
	t.Setenv("ORPHEUS_REVIEW_STEP", "ai-review")
	return repoPath, review
}

func TestAgentContextFailsBeforeRenderingWhenRunIsStale(t *testing.T) {
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
	t.Setenv("ORPHEUS_REPO_ID", "alpha")
	t.Setenv("ORPHEUS_TASK_ID", "op-1")
	t.Setenv("ORPHEUS_WORKTREE", worktreePath)
	t.Setenv("ORPHEUS_BRANCH", "orpheus/op-1")

	stdout, stderr, err := executeCommandWithError(t, []string{"agent", "context"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "agent context:")
	is.Contains(err.Error(), "latest Orpheus run attempt 1")
	is.NotContains(err.Error(), "# Orpheus Agent Context")
}

func TestAgentDoneRecordsMainCompletionForLocalReview(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	repoPath, bdLogPath := setupAgentDoneMainRun(t, "op-main")
	runStore := taskstate.NewStore(currentTestPaths(t))
	must.NoError(os.WriteFile(filepath.Join(repoPath, "ORPHEUS_TEST.txt"), []byte("local review\n"), 0o644))

	stdout, stderr := executeCommand(t, []string{
		"agent",
		"done",
		"--summary",
		"Add local review file",
		"--description",
		"Created ORPHEUS_TEST.txt for local review.",
		"--detailed-description",
		"## PR body\n\nCreated ORPHEUS_TEST.txt for local review.",
		"--technical-explanation",
		"Updated the main-target completion path and left changes uncommitted for local review.",
	})

	is.Empty(stderr)
	is.Contains(stdout, "Recorded completion for op-main")

	latest, ok, err := runStore.LatestRun("alpha", "op-main")
	must.NoError(err)
	must.True(ok)
	is.Equal(taskstate.RunStatusRunning, latest.Status)
	must.NotNil(latest.Completion)
	is.Equal("Add local review file", latest.Completion.Summary)
	is.Equal("Created ORPHEUS_TEST.txt for local review.", latest.Completion.Description)
	is.Equal("## PR body\n\nCreated ORPHEUS_TEST.txt for local review.", latest.Completion.DetailedDescription)
	is.Equal("Updated the main-target completion path and left changes uncommitted for local review.", latest.Completion.TechnicalExplanation)
	is.Contains(runGit(t, repoPath, "status", "--porcelain=v1"), "ORPHEUS_TEST.txt")
	is.NotContains(runGit(t, repoPath, "log", "--oneline", "--max-count=1"), "Add local review file")

	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-main")
	is.NotContains(string(bdLog), "--json --sandbox update")
}

func setupAgentDoneMainRun(t *testing.T, taskID string) (string, string) {
	t.Helper()

	root := newTestState(t)
	repoPath := newTestRepoAt(t, root, filepath.Join("repos", "alpha"), testRepoConfig{withRemote: true})
	registerAgentTestRepo(t, repoPath)
	t.Chdir(repoPath)
	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: agentDoneMainTaskJSON(taskID, repoPath)},
	})
	startAgentTestRun(t, taskID, "main", repoPath)
	setAgentRunEnv(t, taskID, "main", repoPath)
	return repoPath, bdLogPath
}

func agentDoneMainTaskJSON(taskID string, repoPath string) string {
	return `[
		{
			"id":"` + taskID + `",
			"title":"Complete main run",
			"status":"in_progress",
			"priority":2,
			"issue_type":"task",
			"metadata":{"orpheus.branch":"main","orpheus.worktree":"` + repoPath + `"}
		}
	]`
}

func TestAgentDoneRejectsMissingDescription(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	stdout, stderr, err := executeCommandWithError(t, []string{
		"agent",
		"done",
		"--summary",
		"Missing description",
		"--detailed-description",
		"Detailed PR body.",
		"--technical-explanation",
		"Technical explanation.",
	})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), `required flag(s) "description" not set`)
}

func TestAgentDoneRejectsMissingDetailedDescription(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	stdout, stderr, err := executeCommandWithError(t, []string{
		"agent",
		"done",
		"--summary",
		"Missing detailed description",
		"--description",
		"Commit body.",
		"--technical-explanation",
		"Technical explanation.",
	})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "detailed description is required")
}

func TestAgentDoneRejectsMultipleDetailedDescriptionSources(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := t.TempDir()
	detailedPath := filepath.Join(root, "body.md")
	must.NoError(os.WriteFile(detailedPath, []byte("File PR body."), 0o644))

	stdout, stderr, err := executeCommandWithError(t, []string{
		"agent",
		"done",
		"--summary",
		"Multiple detailed sources",
		"--description",
		"Commit body.",
		"--detailed-description",
		"Inline PR body.",
		"--detailed-description-file",
		detailedPath,
		"--technical-explanation",
		"Technical explanation.",
	})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "use exactly one of --detailed-description or --detailed-description-file")
}

func TestAgentDoneRejectsRemovedDetailsFlag(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	stdout, stderr, err := executeCommandWithError(t, []string{
		"agent",
		"done",
		"--summary",
		"Removed flag",
		"--description",
		"Commit body.",
		"--details",
		"Old details.",
		"--detailed-description",
		"Detailed PR body.",
		"--technical-explanation",
		"Technical explanation.",
	})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "unknown flag: --details")
}

func TestAgentDoneRejectsMissingTechnicalExplanation(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	stdout, stderr, err := executeCommandWithError(t, []string{
		"agent",
		"done",
		"--summary",
		"Missing technical explanation",
		"--description",
		"Commit body.",
		"--detailed-description",
		"Detailed PR body.",
	})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "technical explanation is required")
}

func TestAgentDoneRejectsMultipleTechnicalExplanationSources(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := t.TempDir()
	technicalPath := filepath.Join(root, "technical.md")
	must.NoError(os.WriteFile(technicalPath, []byte("File technical explanation."), 0o644))

	stdout, stderr, err := executeCommandWithError(t, []string{
		"agent",
		"done",
		"--summary",
		"Multiple technical sources",
		"--description",
		"Commit body.",
		"--detailed-description",
		"Detailed PR body.",
		"--technical-explanation",
		"Inline technical explanation.",
		"--technical-explanation-file",
		technicalPath,
	})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "use exactly one of --technical-explanation or --technical-explanation-file")
}

func TestAgentDoneRepeatedMainCompletionIsNoopWithGuidance(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	setupAgentDoneMainRun(t, "op-main")
	runStore := taskstate.NewStore(currentTestPaths(t))
	attempt := completeAgentTestRun(t, runStore)
	must.NotZero(attempt.Attempt)

	stdout, stderr := executeCommand(t, []string{
		"agent",
		"done",
		"--summary",
		"Second summary",
		"--description",
		"Second details.",
		"--detailed-description",
		"Second detailed PR body.",
		"--technical-explanation",
		"Second technical explanation.",
	})

	is.Empty(stderr)
	assertRepeatedAgentDoneOutput(t, stdout)
	assertRepeatedAgentCompletion(t, runStore, attempt)
}

func completeAgentTestRun(t *testing.T, runStore taskstate.Store) taskstate.RunAttempt {
	t.Helper()

	latest, ok, err := runStore.LatestRun("alpha", "op-main")
	require.NoError(t, err)
	require.True(t, ok)
	completed, err := runStore.CompleteRun("alpha", "op-main", latest.Attempt, taskstate.CompleteRunOptions{
		Summary:              "First summary",
		Description:          "First details.",
		DetailedDescription:  "Detailed PR body.",
		TechnicalExplanation: "Technical explanation.",
	})
	require.NoError(t, err)
	return completed
}

func assertRepeatedAgentDoneOutput(t *testing.T, stdout string) {
	t.Helper()

	is := assert.New(t)
	is.Contains(stdout, "already recorded")
	is.Contains(stdout, "Do not run `orpheus agent done` again")
	is.Contains(stdout, "first completion remains authoritative")
	is.Contains(stdout, "local diagnostic")
}

func assertRepeatedAgentCompletion(t *testing.T, runStore taskstate.Store, attempt taskstate.RunAttempt) {
	t.Helper()

	is := assert.New(t)
	must := require.New(t)
	latest, ok, err := runStore.LatestRun("alpha", "op-main")
	must.NoError(err)
	must.True(ok)
	must.NotNil(latest.Completion)
	is.Equal("First summary", latest.Completion.Summary)
	is.Equal("First details.", latest.Completion.Description)
	is.Equal("Detailed PR body.", latest.Completion.DetailedDescription)
	is.Equal("Technical explanation.", latest.Completion.TechnicalExplanation)
	events, err := runStore.Events("alpha", "op-main")
	must.NoError(err)
	must.NotEmpty(events)
	last := events[len(events)-1]
	is.Equal(taskstate.EventCompletionRepeated, last.Type)
	is.Equal(attempt.Attempt, last.Attempt)
	is.Equal("Second summary", last.RequestedSummary)
	is.Equal("Second details.", last.RequestedDescription)
	is.Equal("Second detailed PR body.", last.RequestedDetailedDescription)
	is.Equal("Second technical explanation.", last.RequestedTechnicalExplanation)
}

func TestAgentDoneCommitsWorktreeCompletion(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	worktreePath := setupAgentDoneWorktreeRun(t)
	must.NoError(os.WriteFile(filepath.Join(worktreePath, "ORPHEUS_WORKTREE_TEST.txt"), []byte("pr review\n"), 0o644))

	stdout, stderr := executeCommand(t, []string{
		"agent",
		"done",
		"--summary",
		"Add worktree review file",
		"--description",
		"Created ORPHEUS_WORKTREE_TEST.txt for pull request review.",
		"--detailed-description",
		"## Pull request\n\nCreated ORPHEUS_WORKTREE_TEST.txt for pull request review.",
		"--technical-explanation",
		"Added the worktree validation fixture so review can inspect an uncommitted candidate change.",
	})

	is.Empty(stderr)
	is.Contains(stdout, "Recorded completion for op-1; ready for feature-branch review with `orpheus task review op-1`")
	is.Contains(strings.TrimSpace(runGit(t, worktreePath, "status", "--porcelain=v1")), "ORPHEUS_WORKTREE_TEST.txt")

	runStore := taskstate.NewStore(currentTestPaths(t))
	latest, ok, err := runStore.LatestRun("alpha", "op-1")
	must.NoError(err)
	must.True(ok)
	is.Equal(taskstate.RunStatusRunning, latest.Status)
	must.NotNil(latest.Completion)
	is.Equal("Added the worktree validation fixture so review can inspect an uncommitted candidate change.", latest.Completion.TechnicalExplanation)
	is.Empty(latest.Completion.Commit)
	is.Empty(latest.Completion.CommitError)
}

func setupAgentDoneWorktreeRun(t *testing.T) string {
	t.Helper()

	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	repoPath := newTestRepoAt(t, root, filepath.Join("repos", "alpha"), testRepoConfig{withRemote: true})
	configureTestGitUser(t, repoPath)
	registerAgentTestRepo(t, repoPath)
	worktreePath, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-1"))
	must.NoError(err)
	runGit(t, repoPath, "branch", "orpheus/op-1", "main")
	runGit(t, repoPath, "worktree", "add", worktreePath, "orpheus/op-1")
	t.Chdir(worktreePath)
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: agentDoneWorktreeTaskJSON(worktreePath)},
	})
	startAgentTestRun(t, "op-1", "orpheus/op-1", worktreePath)
	setAgentRunEnv(t, "op-1", "orpheus/op-1", worktreePath)
	return worktreePath
}

func agentDoneWorktreeTaskJSON(worktreePath string) string {
	return `[
		{
			"id":"op-1",
			"title":"Complete worktree run",
			"status":"in_progress",
			"priority":2,
			"issue_type":"task",
			"metadata":{"orpheus.branch":"orpheus/op-1","orpheus.worktree":"` + worktreePath + `"}
		}
	]`
}

func TestAgentDoneRequiresMainWorkingTreeChangesBeforeWriting(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	setupAgentDoneMainRun(t, "op-main")
	runStore := taskstate.NewStore(currentTestPaths(t))

	stdout, stderr, err := executeCommandWithError(t, []string{
		"agent",
		"done",
		"--summary",
		"No changes",
		"--description",
		"Should fail before writing.",
		"--detailed-description",
		"Should fail before writing a PR body.",
		"--technical-explanation",
		"Should fail before writing a technical explanation.",
	})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "working tree has no changes")

	latest, ok, err := runStore.LatestRun("alpha", "op-main")
	must.NoError(err)
	must.True(ok)
	is.Equal(taskstate.RunStatusRunning, latest.Status)
	is.Nil(latest.Completion)
}
