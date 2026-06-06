package cli_test

import (
	"os"
	"path/filepath"
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
