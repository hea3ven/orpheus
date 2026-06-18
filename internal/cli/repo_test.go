package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepoAddAndListFlow(t *testing.T) {
	is := assert.New(t)
	withFakeBDInit(t)

	repoPath := newTestRepoPath(t)

	addOut, addErr := executeCommand(t, []string{"repo", "add", repoPath})
	is.Empty(addErr)
	for _, want := range []string{"Added repo alpha", repoPath, "git@example.com:org/alpha.git", "main"} {
		is.Contains(addOut, want)
	}

	listOut, listErr := executeCommand(t, []string{"repo", "list"})
	is.Empty(listErr)
	for _, want := range []string{"ID", "NAME", "PATH", "REMOTE", "DEFAULT_BRANCH", "BEADS_MODE", "BEADS_PREFIX", "alpha", repoPath, "git@example.com:org/alpha.git", "main"} {
		is.Contains(listOut, want)
	}
}

func TestRepoAddStoresGitRootWhenPathIsNested(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	withFakeBDInit(t)

	repoPath := newTestRepoPath(t)
	nestedPath := filepath.Join(repoPath, "nested", "dir")
	must.NoError(os.MkdirAll(nestedPath, 0o755))

	addOut, addErr := executeCommand(t, []string{"repo", "add", nestedPath})
	is.Empty(addErr)
	is.Contains(addOut, repoPath)
	is.NotContains(addOut, nestedPath)

	listOut, listErr := executeCommand(t, []string{"repo", "list"})
	is.Empty(listErr)
	is.Contains(listOut, repoPath)
	is.NotContains(listOut, nestedPath)
}

func TestRepoAddWarnsWhenRemoteIsMissing(t *testing.T) {
	is := assert.New(t)
	withFakeBDInit(t)

	repoPath := newTestRepoPath(t, withoutRemote())

	addOut, addErr := executeCommand(t, []string{"repo", "add", repoPath})
	is.Contains(addErr, "No Git remote detected")
	is.Contains(addErr, "using current branch \"main\"")
	is.Contains(addOut, "Added repo alpha")
	is.Contains(addOut, "main")

	listOut, listErr := executeCommand(t, []string{"repo", "list"})
	is.Empty(listErr)
	is.Contains(listOut, "alpha")
	is.Contains(listOut, "main")
}

func TestRepoAddRejectsNonGitPath(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)

	root := newTestState(t)
	nonGitPath := filepath.Join(root, "repos", "alpha")
	must.NoError(os.MkdirAll(nonGitPath, 0o755))

	stdout, _, err := executeCommandWithError(t, []string{"repo", "add", nonGitPath})
	must.Error(err)
	is.ErrorContains(err, "not a git worktree")
	is.Empty(stdout)
}

func TestRepoAddRejectsDuplicatePath(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	withFakeBDInit(t)

	repoPath := newTestRepoPath(t)

	_, errOutput := executeCommand(t, []string{"repo", "add", repoPath})
	is.Empty(errOutput)

	stdout, _, err := executeCommandWithError(t, []string{"repo", "add", filepath.Join(repoPath, ".")})
	must.Error(err)
	is.ErrorContains(err, "duplicate repo path")
	is.Empty(stdout)
}

func TestRepoAddRejectsDuplicateDerivedIDAndName(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	withFakeBDInit(t)

	root := newTestState(t)
	config := testRepoConfig{withRemote: true}
	firstRepo := newTestRepoAt(t, root, filepath.Join("one", "alpha"), config)
	secondRepo := newTestRepoAt(t, root, filepath.Join("two", "alpha"), config)

	_, errOutput := executeCommand(t, []string{"repo", "add", firstRepo})
	is.Empty(errOutput)

	stdout, _, err := executeCommandWithError(t, []string{"repo", "add", secondRepo})
	must.Error(err)
	is.ErrorContains(err, "duplicate repo id \"alpha\"")
	is.Empty(stdout)
}

func TestRepoAddVerboseEmitsDiagnosticsToStderr(t *testing.T) {
	is := assert.New(t)
	withFakeBDInit(t)

	repoPath := newTestRepoPath(t)

	stdout, stderr := executeCommand(t, []string{"--verbose", "repo", "add", repoPath})

	is.Contains(stdout, "Added repo alpha")
	is.NotContains(stdout, "level=DEBUG")
	is.Contains(stderr, "level=DEBUG")
	is.Contains(stderr, "operation=repo_add")
	is.Contains(stderr, "msg=\"starting repo registration\"")
	is.Contains(stderr, "msg=\"saved repo registration\"")
}

func TestRepoAddInitializesManagedBeadsAfterValidation(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	logPath := withFakeBDInit(t)
	repoPath := newTestRepoPath(t)
	paths := currentTestPaths(t)

	stdout, stderr, err := executeCommandWithError(t, []string{"repo", "add", repoPath})
	must.NoError(err)
	is.Empty(stderr)
	for _, want := range []string{"Added repo alpha", repoPath, "managed", "alpha"} {
		is.Contains(stdout, want)
	}

	managedDir, err := registry.ManagedBeadsDir(paths, "alpha")
	must.NoError(err)
	logData, err := os.ReadFile(logPath)
	must.NoError(err)
	log := string(logData)
	is.Contains(log, managedDir)
	is.Contains(log, "init\n--non-interactive\n--prefix\nalpha\n--skip-agents\n--skip-hooks\n--quiet")
	is.Contains(log, "BD_NON_INTERACTIVE=1")
	is.Contains(log, "BEADS_DIR=unset")

	store := registry.NewStore(paths)
	reg, err := store.Load()
	must.NoError(err)
	if is.Len(reg.Repos, 1) {
		repo := reg.Repos[0]
		is.Equal(registry.BeadsModeManaged, repo.BeadsMode)
		is.Equal("alpha", repo.BeadsPrefix)
	}
}

func TestRepoAddValidatesManagedRegistryConflictsBeforeInitialization(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	logPath := withFakeBDInit(t)
	repoPath := newTestRepoPath(t)
	paths := currentTestPaths(t)

	store := registry.NewStore(paths)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "beta",
		Name:        "Beta",
		Path:        filepath.Join(filepath.Dir(repoPath), "beta"),
		BeadsMode:   registry.BeadsModeManaged,
		BeadsPrefix: "alpha",
	}}}))

	stdout, _, err := executeCommandWithError(t, []string{"repo", "add", repoPath})
	must.Error(err)
	must.ErrorContains(err, "duplicate beads prefix \"alpha\"")
	is.Empty(stdout)
	_, logErr := os.Stat(logPath)
	is.ErrorIs(logErr, os.ErrNotExist)
}

func TestRepoAddRejectsExistingManagedStateWithoutSavingRegistry(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	logPath := withFakeBDInit(t)
	repoPath := newTestRepoPath(t)
	paths := currentTestPaths(t)

	managedDir, err := registry.ManagedBeadsDir(paths, "alpha")
	must.NoError(err)
	must.NoError(os.MkdirAll(managedDir, 0o755))
	must.NoError(os.WriteFile(filepath.Join(managedDir, "leftover"), []byte("state"), 0o644))

	stdout, _, err := executeCommandWithError(t, []string{"repo", "add", repoPath})
	must.Error(err)
	is.ErrorContains(err, "directory already exists and is not empty")
	is.Empty(stdout)
	_, logErr := os.Stat(logPath)
	is.ErrorIs(logErr, os.ErrNotExist)

	store := registry.NewStore(paths)
	reg, err := store.Load()
	must.NoError(err)
	is.Empty(reg.Repos)
}

func TestRepoBeadsDirResolvesLocalRepoByID(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	localPath := filepath.Join(t.TempDir(), "local-alpha")
	store := registry.NewStore(paths)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha-id",
		Name:        "Alpha Repo",
		Path:        localPath,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "alpha-prefix",
	}}}))

	stdout, stderr := executeCommand(t, []string{"repo", "beads-dir", "alpha-id"})

	is.Equal(localPath+"\n", stdout)
	is.Empty(stderr)
}

func TestRepoBeadsDirResolvesManagedRepoByName(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "managed-id",
		Name:        "Managed Repo",
		Path:        filepath.Join(t.TempDir(), "managed"),
		BeadsMode:   registry.BeadsModeManaged,
		BeadsPrefix: "managed-prefix",
	}}}))
	managedDir, err := registry.ManagedBeadsDir(paths, "managed-id")
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"repo", "beads-dir", "Managed Repo"})

	is.Equal(managedDir+"\n", stdout)
	is.Empty(stderr)
}

func TestRepoBeadsDirResolvesLocalRepoByBeadsPrefix(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	localPath := filepath.Join(t.TempDir(), "prefix-alpha")
	store := registry.NewStore(paths)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha-id",
		Name:        "Alpha Repo",
		Path:        localPath,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "alpha-prefix",
	}}}))

	stdout, stderr := executeCommand(t, []string{"repo", "beads-dir", "alpha-prefix"})

	is.Equal(localPath+"\n", stdout)
	is.Empty(stderr)
}

func TestRepoBeadsDirResolvesManagedRepoByBeadsPrefix(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "managed-id",
		Name:        "Managed Repo",
		Path:        filepath.Join(t.TempDir(), "managed"),
		BeadsMode:   registry.BeadsModeManaged,
		BeadsPrefix: "managed-prefix",
	}}}))
	managedDir, err := registry.ManagedBeadsDir(paths, "managed-id")
	must.NoError(err)

	stdout, stderr := executeCommand(t, []string{"repo", "beads-dir", "managed-prefix"})

	is.Equal(managedDir+"\n", stdout)
	is.Empty(stderr)
}

func TestRepoBeadsDirVerboseEmitsDiagnosticsToStderr(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	localPath := filepath.Join(t.TempDir(), "verbose-alpha")
	store := registry.NewStore(paths)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha-id",
		Name:        "Alpha Repo",
		Path:        localPath,
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "alpha-prefix",
	}}}))

	stdout, stderr := executeCommand(t, []string{"--verbose", "repo", "beads-dir", "alpha-prefix"})

	is.Equal(localPath+"\n", stdout)
	is.NotContains(stdout, "level=DEBUG")
	is.Contains(stderr, "level=DEBUG")
	is.Contains(stderr, "operation=repo_beads_dir")
	is.Contains(stderr, "token=alpha-prefix")
	is.Contains(stderr, "repo_id=alpha-id")
	is.Contains(stderr, "beads_dir="+localPath)
}

func TestRepoBeadsDirRejectsUnknownRepo(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:          "alpha-id",
		Name:        "Alpha Repo",
		Path:        filepath.Join(t.TempDir(), "alpha"),
		BeadsMode:   registry.BeadsModeLocal,
		BeadsPrefix: "alpha-prefix",
	}}}))

	stdout, stderr, err := executeCommandWithError(t, []string{"repo", "beads-dir", "missing"})

	must.Error(err)
	is.Empty(stdout)
	is.Empty(stderr)
	must.ErrorContains(err, "repo \"missing\" is not registered")
	is.ErrorContains(err, "orpheus repo list")
}

func currentTestPaths(t *testing.T) state.Paths {
	t.Helper()

	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		t.Fatalf("resolve state: %v", err)
	}
	return paths
}
