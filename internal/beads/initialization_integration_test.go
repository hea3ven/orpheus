//go:build integration

package beads_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/beads"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationInitializeManagedCreatesUsableIsolatedDatabase(t *testing.T) {
	binary, err := exec.LookPath("bd")
	require.NoError(t, err, "bd is required for the integration lane")
	root := testutil.CanonicalTempDir(t)
	dir := filepath.Join(root, "managed")
	runner := isolatedBeadsRunner(t, binary)
	runner.Environment = append(runner.Environment, "BEADS_DIR="+filepath.Join(root, "wrong-workspace"))

	err = beads.InitializeManagedWithRunner(dir, "managed", runner)

	require.NoError(t, err)
	item := createBeadsTask(t, dir, runner)
	assert.Contains(t, item.ID, "managed-")
	assertBeadsTaskStored(t, dir, runner, item)
	assert.NoDirExists(t, filepath.Join(root, "wrong-workspace"))
	assert.NoFileExists(t, filepath.Join(dir, "AGENTS.md"))

	require.ErrorContains(t, beads.InitializeManagedWithRunner(dir, "managed", runner), "not empty")
	assertBeadsTaskStored(t, dir, runner, item)
}

func TestIntegrationInspectLocalFindsExistingUsableDatabaseWithoutReinitialization(t *testing.T) {
	binary, err := exec.LookPath("bd")
	require.NoError(t, err, "bd is required for the integration lane")
	root := testutil.CanonicalTempDir(t)
	runner := isolatedBeadsRunner(t, binary)
	_, err = runner.Run(root, "init", "--prefix", "local", "--non-interactive", "--skip-agents", "--skip-hooks", "--quiet")
	require.NoError(t, err)
	item := createBeadsTask(t, root, runner)
	require.Contains(t, item.ID, "local-")

	inspection, err := beads.InspectLocalWithRunner(root, runner)

	require.NoError(t, err)
	assert.Equal(t, beads.LocalInspection{Root: root, BeadsDir: filepath.Join(root, ".beads"), Prefix: "local"}, inspection)
	assertBeadsTaskStored(t, root, runner, item)
}

func createBeadsTask(t *testing.T, dir string, runner beads.Runner) task.Task {
	t.Helper()
	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	require.NoError(t, err)
	created, err := backend.Create(context.Background(), task.CreateOptions{
		Title: "Stored task", Description: "Verify the initialized database.", AcceptanceCriteria: "Task can be read back.", IssueType: task.IssueTypeTask,
	})
	require.NoError(t, err)
	return created
}

func assertBeadsTaskStored(t *testing.T, dir string, runner beads.Runner, want task.Task) {
	t.Helper()
	backend, err := beads.NewTaskBackendWithRunner(dir, runner)
	require.NoError(t, err)
	loaded, err := backend.Get(context.Background(), want.ID)
	require.NoError(t, err)
	assert.Equal(t, want.ID, loaded.ID)
	assert.Equal(t, want.Title, loaded.Title)
	assert.Equal(t, want.Description, loaded.Description)
	assert.Equal(t, want.AcceptanceCriteria, loaded.AcceptanceCriteria)
	assert.Equal(t, task.StatusOpen, loaded.Status)
	assert.Equal(t, task.IssueTypeTask, loaded.IssueType)
}

func isolatedBeadsRunner(t *testing.T, binary string) beads.CommandRunner {
	t.Helper()
	home := testutil.CanonicalTempDir(t)
	return beads.CommandRunner{Binary: binary, Environment: []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=Integration Test", "GIT_AUTHOR_EMAIL=test@example.test",
		"GIT_COMMITTER_NAME=Integration Test", "GIT_COMMITTER_EMAIL=test@example.test",
	}}
}
