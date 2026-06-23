package cli_test

import (
	"errors"
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

func TestRepoConfigInspectsEffectivePublicationPolicy(t *testing.T) {
	is := assert.New(t)
	withFakeBDInit(t)
	repoPath := newTestRepoPath(t)

	_, addErr := executeCommand(t, []string{"repo", "add", repoPath})
	is.Empty(addErr)

	stdout, stderr := executeCommand(t, []string{"repo", "config", "get", "alpha"})
	is.Empty(stderr)
	is.Contains(stdout, "POLICY")
	is.Contains(stdout, "summary guidance")
	is.Contains(stdout, "(not set)")
	is.Contains(stdout, "typed")
	is.Contains(stdout, "publication title template")
	is.Contains(stdout, "completion summary")
}

func TestRepoConfigUpdatesPublicationPolicyForExistingRepo(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	withFakeBDInit(t)
	repoPath := newTestRepoPath(t)

	_, addErr := executeCommand(t, []string{"repo", "add", repoPath})
	is.Empty(addErr)

	guidance := "Use sentence-case summaries without a type prefix."
	template := "[{{external_ref}}] {{summary}}"
	stdout, stderr := executeCommand(t, []string{
		"repo", "config", "set", "alpha", "summary-guidance", guidance,
	})
	is.Empty(stderr)
	is.Contains(stdout, guidance)
	stdout, stderr = executeCommand(t, []string{
		"repo", "config", "set", "alpha", "summary-style", registry.SummaryGuidanceStyleCapitalized,
	})
	is.Empty(stderr)
	is.Contains(stdout, registry.SummaryGuidanceStyleCapitalized)
	stdout, stderr = executeCommand(t, []string{
		"repo", "config", "set", "alpha", "title-template", template,
	})
	is.Empty(stderr)
	is.Contains(stdout, template)

	store := registry.NewStore(currentTestPaths(t))
	reg, err := store.Load()
	must.NoError(err)
	must.Len(reg.Repos, 1)
	is.Equal(guidance, reg.Repos[0].SummaryGuidance)
	is.Equal(registry.SummaryGuidanceStyleCapitalized, reg.Repos[0].SummaryGuidanceStyle)
	is.Equal(template, reg.Repos[0].TitleTemplate)
}

func TestRepoConfigClearsPublicationPolicy(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	withFakeBDInit(t)
	repoPath := newTestRepoPath(t)

	_, addErr := executeCommand(t, []string{"repo", "add", repoPath})
	is.Empty(addErr)
	for _, args := range [][]string{
		{"repo", "config", "set", "alpha", "summary-guidance", "Use sentence-case summaries."},
		{"repo", "config", "set", "alpha", "summary-style", registry.SummaryGuidanceStyleCapitalized},
		{"repo", "config", "set", "alpha", "title-template", "[OPS] {{summary}}"},
	} {
		_, configErr := executeCommand(t, args)
		is.Empty(configErr)
	}

	_, stderr := executeCommand(t, []string{
		"repo", "config", "set", "alpha", "summary-guidance", "",
	})
	is.Empty(stderr)
	_, stderr = executeCommand(t, []string{
		"repo", "config", "set", "alpha", "summary-style", "",
	})
	is.Empty(stderr)
	_, stderr = executeCommand(t, []string{
		"repo", "config", "set", "alpha", "title-template", "",
	})
	is.Empty(stderr)
	stdout, stderr := executeCommand(t, []string{"repo", "config", "get", "alpha"})
	is.Empty(stderr)
	is.Contains(stdout, "(not set)")
	is.Contains(stdout, "typed")
	is.Contains(stdout, "completion summary")

	store := registry.NewStore(currentTestPaths(t))
	reg, err := store.Load()
	must.NoError(err)
	must.Len(reg.Repos, 1)
	is.Empty(reg.Repos[0].SummaryGuidance)
	is.Empty(reg.Repos[0].SummaryGuidanceStyle)
	is.Empty(reg.Repos[0].TitleTemplate)
}

func TestRepoConfigRejectsInvalidPolicyWithoutMutatingRegistry(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	withFakeBDInit(t)
	repoPath := newTestRepoPath(t)

	_, addErr := executeCommand(t, []string{"repo", "add", repoPath})
	is.Empty(addErr)
	_, configErr := executeCommand(t, []string{"repo", "config", "set", "alpha", "title-template", "[OPS] {{summary}}"})
	is.Empty(configErr)

	for _, args := range [][]string{
		{"repo", "config", "set", "alpha", "summary-style", "informal"},
		{"repo", "config", "set", "alpha", "title-template", "{{task_id}}: {{summary}}"},
	} {
		stdout, _, err := executeCommandWithError(t, args)
		must.Error(err)
		is.Empty(stdout)
	}

	store := registry.NewStore(currentTestPaths(t))
	reg, err := store.Load()
	must.NoError(err)
	must.Len(reg.Repos, 1)
	is.Equal(registry.SummaryGuidanceStyleTyped, reg.Repos[0].SummaryGuidanceStyle)
	is.Equal("[OPS] {{summary}}", reg.Repos[0].TitleTemplate)
}

func TestRepoConfigRejectsUnknownConfigName(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	withFakeBDInit(t)
	repoPath := newTestRepoPath(t)

	_, addErr := executeCommand(t, []string{"repo", "add", repoPath})
	is.Empty(addErr)

	stdout, _, err := executeCommandWithError(t, []string{
		"repo", "config", "set", "alpha", "unknown", "value",
	})
	must.Error(err)
	is.Empty(stdout)
}

func TestRepoConfigGetOnePolicyValue(t *testing.T) {
	is := assert.New(t)
	withFakeBDInit(t)
	repoPath := newTestRepoPath(t)

	_, addErr := executeCommand(t, []string{"repo", "add", repoPath})
	is.Empty(addErr)
	_, setErr := executeCommand(t, []string{
		"repo", "config", "set", "alpha", "title-template", "[OPS] {{summary}}",
	})
	is.Empty(setErr)

	stdout, stderr := executeCommand(t, []string{"repo", "config", "get", "alpha", "title-template"})
	is.Empty(stderr)
	is.Contains(stdout, "publication title template")
	is.Contains(stdout, "[OPS] {{summary}}")
	is.NotContains(stdout, "summary guidance style")
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
		is.Equal(registry.SummaryGuidanceStyleTyped, repo.SummaryGuidanceStyle)
	}
}

func TestRepoAddHoldsGlobalMutationLockDuringManagedInitialization(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	withFakeBDInit(t)
	repoPath := newTestRepoPath(t)
	paths := currentTestPaths(t)
	lockPath, err := paths.GlobalMutationLockPath()
	must.NoError(err)
	t.Setenv("FAKE_BD_LOCK_PATH", lockPath)

	stdout, stderr, err := executeCommandWithError(t, []string{"repo", "add", repoPath})

	must.NoError(err)
	is.Empty(stderr)
	is.Contains(stdout, "Added repo alpha")
	if _, err := os.Stat(lockPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock after repo add: %v, want removed", err)
	}
}

func TestRepoAddFailsFastWhenGlobalMutationLockIsHeld(t *testing.T) {
	is := assert.New(t)
	must := require.New(t)
	logPath := withFakeBDInit(t)
	repoPath := newTestRepoPath(t)
	paths := currentTestPaths(t)
	lockPath, err := paths.GlobalMutationLockPath()
	must.NoError(err)
	must.NoError(os.MkdirAll(filepath.Dir(lockPath), 0o755))
	must.NoError(os.WriteFile(lockPath, []byte("held"), 0o644))

	stdout, _, err := executeCommandWithError(t, []string{"repo", "add", repoPath})

	must.Error(err)
	var acquisitionErr *state.LockAcquisitionError
	must.ErrorAs(err, &acquisitionErr)
	is.Equal(lockPath, acquisitionErr.Path)
	is.ErrorContains(err, "failed to acquire lock for repo add: "+lockPath)
	is.Empty(stdout)
	_, logErr := os.Stat(logPath)
	is.ErrorIs(logErr, os.ErrNotExist)

	store := registry.NewStore(paths)
	reg, err := store.Load()
	must.NoError(err)
	is.Empty(reg.Repos)
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
