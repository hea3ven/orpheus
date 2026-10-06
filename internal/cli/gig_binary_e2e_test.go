//go:build integration

package cli_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This contract owns production linking and fresh-process startup. The binary
// must use embedded gig without a task-source executable or inherited config.
func TestIntegrationBinaryE2EEmbeddedGigWorksAcrossInvocationsWithoutTaskCLIs(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	source, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	binary := filepath.Join(root, "orpheus")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/orpheus")
	build.Dir = source
	output, err := build.CombinedOutput()
	require.NoError(t, err, "%s", output)
	git, err := exec.LookPath("git")
	require.NoError(t, err)
	bin := filepath.Join(root, "bin")
	require.NoError(t, os.Mkdir(bin, 0o700))
	require.NoError(t, os.Symlink(git, filepath.Join(bin, "git")))
	repo := filepath.Join(root, "sample")
	require.NoError(t, os.Mkdir(repo, 0o700))
	environment := []string{"HOME=" + root, "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "PATH=" + bin, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull}
	for _, args := range [][]string{{"init", "--initial-branch=main"}, {"-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "Initial"}} {
		command := exec.CommandContext(t.Context(), git, args...)
		command.Dir, command.Env = repo, environment
		output, err := command.CombinedOutput()
		require.NoError(t, err, "%s", output)
	}
	// An unusable local Beads directory must not cause discovery or CLI calls.
	require.NoError(t, os.Mkdir(filepath.Join(repo, ".beads"), 0o700))
	run := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), binary, args...)
		command.Dir, command.Env = repo, environment
		output, err := command.CombinedOutput()
		require.NoError(t, err, "%v: %s", args, output)
		return string(output)
	}
	assert.Contains(t, run("repo", "add", repo), "gig")
	// No executables are available to subsequent task-source invocations.
	environment[3] = "PATH=" + filepath.Join(root, "no-executables")
	assert.Contains(t, run("task", "create", "--repo", "sample", "--title", "Embedded task", "--description", "Description", "--acceptance", "Acceptance"), "Created task sample-")
	var rows []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	require.NoError(t, json.Unmarshal([]byte(run("task", "list", "--json")), &rows))
	require.Len(t, rows, 1)
	assert.Equal(t, "Embedded task", rows[0].Title)
	assert.Contains(t, run("task", "show", rows[0].ID), "Acceptance")
	assert.Contains(t, run("repo", "list"), "gig")
}
