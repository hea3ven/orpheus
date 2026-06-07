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
	cwd := filepath.Join(worktreePath, "internal")
	must.NoError(os.MkdirAll(cwd, 0o755))
	t.Chdir(cwd)
	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
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
		]`},
	})
	_, err = taskstate.NewStore(paths).StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "orpheus/op-1",
		Worktree: worktreePath,
	})
	must.NoError(err)
	t.Setenv("ORPHEUS_REPO_ID", "alpha")
	t.Setenv("ORPHEUS_TASK_ID", "op-1")
	t.Setenv("ORPHEUS_WORKTREE", worktreePath)
	t.Setenv("ORPHEUS_BRANCH", "orpheus/op-1")

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
		"Orpheus will create the pull request",
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
	must.NoError(os.WriteFile(filepath.Join(repoPath, "ORPHEUS_TEST.txt"), []byte("local review\n"), 0o644))
	bdLogPath := withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
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

	stdout, stderr := executeCommand(t, []string{
		"agent",
		"done",
		"--summary",
		"Add local review file",
		"--details",
		"Created ORPHEUS_TEST.txt for local review.",
	})

	is.Empty(stderr)
	is.Contains(stdout, "Recorded completion for op-main")

	latest, ok, err := runStore.LatestRun("alpha", "op-main")
	must.NoError(err)
	must.True(ok)
	is.Equal(taskstate.RunStatusRunning, latest.Status)
	must.NotNil(latest.Completion)
	is.Equal("Add local review file", latest.Completion.Summary)
	is.Equal("Created ORPHEUS_TEST.txt for local review.", latest.Completion.Details)
	is.Contains(runGit(t, repoPath, "status", "--porcelain=v1"), "ORPHEUS_TEST.txt")
	is.NotContains(runGit(t, repoPath, "log", "--oneline", "--max-count=1"), "Add local review file")

	bdLog, err := os.ReadFile(bdLogPath)
	must.NoError(err)
	is.Contains(string(bdLog), "--json --readonly --sandbox show --id op-main")
	is.NotContains(string(bdLog), "--json --sandbox update")
}

func TestAgentDoneCommitsWorktreeCompletion(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)
	repoPath := newTestRepoAt(t, root, filepath.Join("repos", "alpha"), testRepoConfig{withRemote: true})
	configureTestGitUser(t, repoPath)
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
	runGit(t, repoPath, "branch", "orpheus/op-1", "main")
	runGit(t, repoPath, "worktree", "add", worktreePath, "orpheus/op-1")
	t.Chdir(worktreePath)
	must.NoError(os.WriteFile(filepath.Join(worktreePath, "ORPHEUS_WORKTREE_TEST.txt"), []byte("pr review\n"), 0o644))

	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[
			{
				"id":"op-1",
				"title":"Complete worktree run",
				"status":"in_progress",
				"priority":2,
				"issue_type":"task",
				"metadata":{"orpheus.branch":"orpheus/op-1","orpheus.worktree":"` + worktreePath + `"}
			}
		]`},
	})
	runStore := taskstate.NewStore(paths)
	_, err = runStore.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "orpheus/op-1",
		Worktree: worktreePath,
	})
	must.NoError(err)
	t.Setenv("ORPHEUS_REPO_ID", "alpha")
	t.Setenv("ORPHEUS_TASK_ID", "op-1")
	t.Setenv("ORPHEUS_WORKTREE", worktreePath)
	t.Setenv("ORPHEUS_BRANCH", "orpheus/op-1")

	stdout, stderr := executeCommand(t, []string{
		"agent",
		"done",
		"--summary",
		"Add worktree review file",
		"--details",
		"Created ORPHEUS_WORKTREE_TEST.txt for pull request review.",
	})

	is.Empty(stderr)
	is.Contains(stdout, "Recorded completion for op-1 and committed")
	is.Empty(strings.TrimSpace(runGit(t, worktreePath, "status", "--porcelain=v1")))
	message := strings.TrimSpace(runGit(t, worktreePath, "log", "-1", "--format=%B"))
	is.Equal("Add worktree review file\n\nCreated ORPHEUS_WORKTREE_TEST.txt for pull request review.", message)

	latest, ok, err := runStore.LatestRun("alpha", "op-1")
	must.NoError(err)
	must.True(ok)
	is.Equal(taskstate.RunStatusRunning, latest.Status)
	must.NotNil(latest.Completion)
	is.NotEmpty(latest.Completion.Commit)
	is.Empty(latest.Completion.CommitError)
	is.Equal(strings.TrimSpace(runGit(t, worktreePath, "rev-parse", "HEAD")), latest.Completion.Commit)
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
		"--details",
		"Should fail before writing.",
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
