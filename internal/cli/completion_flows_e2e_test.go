//go:build integration

//nolint:testpackage // Invocation-scoped fixture requires internal composition wiring.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationDoctorRepairsCleanClosedTaskWorktreeAndPreservesDirtyAndLockedWorktrees(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	paths, repoPath := setupCompletionFlowRepo(t)
	const taskID = "op-doctor-worktree"
	worktree, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", taskID))
	must.NoError(err)
	runGit(t, repoPath, "branch", "orpheus/"+taskID, "main")
	runGit(t, repoPath, "worktree", "add", worktree, "orpheus/"+taskID)

	store := taskstate.NewStore(paths)
	_, err = store.StartRun("alpha", taskID, taskstate.StartRunOptions{
		Agent: "implementer", WorkDirectory: worktree, Branch: "orpheus/" + taskID, Worktree: worktree,
	})
	must.NoError(err)
	const lockedTaskID = "op-doctor-locked"
	lockedWorktree, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", lockedTaskID))
	must.NoError(err)
	runGit(t, repoPath, "branch", "orpheus/"+lockedTaskID, "main")
	runGit(t, repoPath, "worktree", "add", lockedWorktree, "orpheus/"+lockedTaskID)
	runGit(t, repoPath, "worktree", "lock", "--reason", "operator repair", lockedWorktree)
	_, err = store.StartRun("alpha", lockedTaskID, taskstate.StartRunOptions{
		Agent: "implementer", WorkDirectory: lockedWorktree, Branch: "orpheus/" + lockedTaskID, Worktree: lockedWorktree,
	})
	must.NoError(err)
	withFakeBDTaskResponses(t, map[string]fakeBDTaskResponse{
		repoPath: {stdout: `[{"id":"op-doctor-worktree","title":"Doctor cleanup","status":"closed","priority":1,"issue_type":"task","metadata":{"orpheus.branch":"orpheus/op-doctor-worktree","orpheus.worktree":"` + worktree + `"}},{"id":"op-doctor-locked","title":"Doctor locked cleanup","status":"closed","priority":1,"issue_type":"task","metadata":{"orpheus.branch":"orpheus/op-doctor-locked","orpheus.worktree":"` + lockedWorktree + `"}}]`},
	})

	marker := filepath.Join(worktree, "preserve-me.txt")
	must.NoError(os.WriteFile(marker, []byte("operator change"), 0o644))
	plainOut, plainErr := executeCommand(t, []string{"doctor"})
	is.Empty(plainErr)
	is.Contains(plainOut, "Closed-task worktree cleanup")
	is.Contains(plainOut, "dirty")
	is.Contains(plainOut, worktree)
	is.Contains(plainOut, "unsafe")
	is.Contains(plainOut, lockedWorktree)
	is.Contains(plainOut, "locked")
	_, statErr := os.Stat(marker)
	is.NoError(statErr)
	_, statErr = os.Stat(lockedWorktree)
	is.NoError(statErr)

	fixOut, fixErr := executeCommand(t, []string{"doctor", "--fix"})
	is.Empty(fixErr)
	is.Contains(fixOut, "dirty")
	is.Contains(fixOut, "unsafe")
	is.Contains(fixOut, "locked")
	_, statErr = os.Stat(marker)
	is.NoError(statErr)
	_, statErr = os.Stat(lockedWorktree)
	is.NoError(statErr)

	must.NoError(os.Remove(marker))
	fixedOut, fixedErr := executeCommand(t, []string{"doctor", "--fix"})
	is.Empty(fixedErr)
	is.Contains(fixedOut, "removed")
	_, statErr = os.Stat(worktree)
	is.ErrorIs(statErr, os.ErrNotExist)
	_, statErr = os.Stat(lockedWorktree)
	is.NoError(statErr)

	runGit(t, repoPath, "worktree", "unlock", lockedWorktree)
	unlockedOut, unlockedErr := executeCommand(t, []string{"doctor", "--fix"})
	is.Empty(unlockedErr)
	is.Contains(unlockedOut, "removed")
	_, statErr = os.Stat(lockedWorktree)
	is.ErrorIs(statErr, os.ErrNotExist)

	emptyOut, emptyErr := executeCommand(t, []string{"doctor"})
	is.Empty(emptyErr)
	is.Contains(emptyOut, "No lingering closed-task worktrees found.")
	is.NotContains(emptyOut, "already_absent")
	loaded, err := store.Load("alpha", taskID)
	must.NoError(err)
	is.Equal(taskstate.EventWorktreeRemoved, loaded.Events[len(loaded.Events)-1].Type)
}

func TestIntegrationOrpheusCLIHelperProcess(t *testing.T) {
	t.Parallel()
	if os.Getenv("GO_WANT_ORPHEUS_CLI_HELPER") != "1" {
		return
	}

	marker := -1
	for i, arg := range os.Args {
		if arg == "--" {
			marker = i
			break
		}
	}
	if marker < 0 {
		_, _ = fmt.Fprintln(os.Stderr, "missing -- before orpheus helper args")
		os.Exit(2)
	}

	command := NewRootCommand()
	command.SetIn(os.Stdin)
	command.SetOut(os.Stdout)
	command.SetErr(os.Stderr)
	command.SetArgs(os.Args[marker+1:])
	if err := command.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func setupCompletionFlowRepo(t *testing.T) (state.Paths, string) {
	t.Helper()

	root := newTestState(t)
	paths := currentTestPaths(t)
	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	require.NoError(t, registry.NewStore(paths).Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))
	return paths, repoPath
}

func withOrpheusCLIHelper(t *testing.T) string {
	t.Helper()
	requireCLIHelperFixture(t)
	prependTestPath(t, filepath.Dir(orpheusCLIHelperPath))
	return orpheusCLIHelperPath
}

func readFileString(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
