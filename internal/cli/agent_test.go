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
		"local-review-ready completion data",
		"The human operator will later run `orpheus task done op-1`",
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

func setupAgentContextWorktree(t *testing.T) (string, string, string, string) {
	t.Helper()

	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
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
	})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	is.Contains(err.Error(), "unknown flag: --details")
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
		Summary:             "First summary",
		Description:         "First details.",
		DetailedDescription: "Detailed PR body.",
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
	events, err := runStore.Events("alpha", "op-main")
	must.NoError(err)
	must.NotEmpty(events)
	last := events[len(events)-1]
	is.Equal(taskstate.EventCompletionRepeated, last.Type)
	is.Equal(attempt.Attempt, last.Attempt)
	is.Equal("Second summary", last.RequestedSummary)
	is.Equal("Second details.", last.RequestedDescription)
	is.Equal("Second detailed PR body.", last.RequestedDetailedDescription)
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
	})

	is.Empty(stderr)
	is.Contains(stdout, "Recorded completion for op-1; ready for local review")
	is.Contains(strings.TrimSpace(runGit(t, worktreePath, "status", "--porcelain=v1")), "ORPHEUS_WORKTREE_TEST.txt")

	runStore := taskstate.NewStore(currentTestPaths(t))
	latest, ok, err := runStore.LatestRun("alpha", "op-1")
	must.NoError(err)
	must.True(ok)
	is.Equal(taskstate.RunStatusRunning, latest.Status)
	must.NotNil(latest.Completion)
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
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)
	repoPath := newTestRepoAt(t, root, filepath.Join("repos", "alpha"), testRepoConfig{withRemote: true})
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	t.Chdir(repoPath)
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{
				"id":"op-main",
				"title":"Complete main run",
				"status":"in_progress",
				"priority":2,
				"issue_type":"task",
				"metadata":{"orpheus.branch":"main","orpheus.worktree":"` + repoPath + `"}
			}
		]`},
	})
	runStore := taskstate.NewStore(paths)
	_, err := runStore.StartRun("alpha", "op-main", taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: repoPath,
	})
	must.NoError(err)
	t.Setenv("ORPHEUS_REPO_ID", "alpha")
	t.Setenv("ORPHEUS_TASK_ID", "op-main")
	t.Setenv("ORPHEUS_WORKTREE", repoPath)
	t.Setenv("ORPHEUS_BRANCH", "main")

	stdout, stderr, err := executeCommandWithError(t, []string{
		"agent",
		"done",
		"--summary",
		"No changes",
		"--description",
		"Should fail before writing.",
		"--detailed-description",
		"Should fail before writing a PR body.",
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
