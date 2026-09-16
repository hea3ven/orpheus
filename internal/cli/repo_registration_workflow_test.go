//go:build integration

package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRepoAddStoresDiscoveredRootForNestedPath(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	fixture := newRepoWorkflowFixture(t, repo)
	fixture.withoutLocalBeads()
	nested := filepath.Join(repo.path, "nested", "dir")
	fixture.gitResults[nested] = fixture.gitResults[repo.path]

	added, stderr, err := fixture.execute("repo", "add", nested)
	require.NoError(t, err, "stderr: %s", stderr)
	listed, stderr, err := fixture.execute("repo", "list")
	require.NoError(t, err, "stderr: %s", stderr)

	final := fixture.loadFinalRegistry()
	assert.Equal(t, []registry.Repo{repo.registeredWithManagedBeads()}, final.Repos)
	assertRepoAdded(t, added, repo.registeredWithManagedBeads())
	assertRepoListed(t, listed, repo.registeredWithManagedBeads())
	assert.NotContains(t, added, nested)
	assert.NotContains(t, listed, nested)
	assert.Equal(t, []string{nested}, fixture.gitInspections)
}

func TestIntegrationRepoAddWarnsWhenRemoteIsMissing(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	repo.remote = ""
	fixture := newRepoWorkflowFixture(t, repo)
	fixture.withoutLocalBeads()
	fixture.gitResults[repo.path] = gitmeta.Inspection{Root: repo.path, RemoteErr: gitmeta.ErrNoRemote, DefaultBranchCandidate: "main", DefaultBranchSource: gitmeta.DefaultBranchSourceCurrentBranch}

	stdout, stderr, err := fixture.execute("repo", "add", repo.path)
	require.NoError(t, err)
	listed, listStderr, err := fixture.execute("repo", "list")
	require.NoError(t, err)

	assert.Contains(t, stderr, "No Git remote detected")
	assert.Contains(t, stderr, "using current branch \"main\"")
	assertRepoAdded(t, stdout, repo.registeredWithManagedBeads())
	assertRepoListed(t, listed, repo.registeredWithManagedBeads())
	assert.Empty(t, listStderr)
	final := fixture.loadFinalRegistry()
	assert.Equal(t, []registry.Repo{repo.registeredWithManagedBeads()}, final.Repos)
}

func TestIntegrationRepoAddRejectsGitInspectionFailureWithoutRegistration(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	fixture := newRepoWorkflowFixture(t, repo)
	inspectionErr := errors.New("not a git worktree")
	fixture.gitErrors[repo.path] = inspectionErr

	stdout, _, err := fixture.execute("repo", "add", repo.path)

	assert.ErrorIs(t, err, inspectionErr)
	assert.Empty(t, stdout)
	assert.Empty(t, fixture.loadFinalRegistry().Repos)
	assert.Empty(t, fixture.beadsInspections)
	assert.Empty(t, fixture.beadsInitializations)
}

func TestIntegrationRepoAddRejectsRegistrationConflictsBeforeInitialization(t *testing.T) {
	for _, conflict := range []string{"path", "identity", "prefix"} {
		t.Run(conflict, func(t *testing.T) {
			repo := aGitRepository(t, "alpha")
			fixture := newRepoWorkflowFixture(t, repo)
			fixture.withoutLocalBeads()
			existing := aRegisteredRepo("beta")
			wantError := "duplicate repo path"
			switch conflict {
			case "path":
				existing.Path = repo.path
			case "identity":
				existing.ID, existing.Name = "alpha", "alpha"
				wantError = "duplicate repo id \"alpha\""
			case "prefix":
				existing.BeadsPrefix = "alpha"
				wantError = "duplicate beads prefix \"alpha\""
			}
			fixture.withRegisteredRepos(existing)

			stdout, _, err := fixture.execute("repo", "add", repo.path)

			assert.ErrorContains(t, err, wantError)
			assert.Empty(t, stdout)
			assert.Empty(t, fixture.beadsInitializations)
			assert.Equal(t, []registry.Repo{existing}, fixture.loadFinalRegistry().Repos)
		})
	}
}

func TestIntegrationRepoAddInitializationFailureLeavesRegistryUnchanged(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	fixture := newRepoWorkflowFixture(t, repo)
	fixture.withoutLocalBeads()
	existing := aRegisteredRepo("beta")
	fixture.withRegisteredRepos(existing)
	initErr := errors.New("directory already exists and is not empty")
	fixture.initialize = func(string, string) error { return initErr }

	stdout, _, err := fixture.execute("repo", "add", repo.path)

	assert.ErrorIs(t, err, initErr)
	assert.Empty(t, stdout)
	assert.Equal(t, []registry.Repo{existing}, fixture.loadFinalRegistry().Repos)
	require.Len(t, fixture.beadsInitializations, 1)
	assert.Equal(t, beadsInitialization{dir: repoWorkflowDataRoot + "/repos/alpha/beads", prefix: "alpha"}, fixture.beadsInitializations[0])
}

func TestIntegrationRepoRegistersAndListsLocalAndManagedRepositories(t *testing.T) {
	local := aGitRepository(t, "localrepo")
	managed := aGitRepository(t, "managedrepo")
	fixture := newRepoWorkflowFixture(t, local, managed)
	fixture.withoutLocalBeads()
	fixture.withLocalBeads(local, "op")
	wantLocal := local.registeredWithManagedBeads()
	wantLocal.BeadsMode, wantLocal.BeadsPrefix = registry.BeadsModeLocal, "op"
	wantManaged := managed.registeredWithManagedBeads()

	localOutput, stderr, err := fixture.execute("repo", "add", local.path)
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Empty(t, stderr)
	managedOutput, stderr, err := fixture.execute("repo", "add", managed.path)
	require.NoError(t, err, "stderr: %s", stderr)
	assert.Empty(t, stderr)
	listed, stderr, err := fixture.execute("repo", "list")
	require.NoError(t, err, "stderr: %s", stderr)

	final := fixture.loadFinalRegistry()
	assert.ElementsMatch(t, []registry.Repo{wantLocal, wantManaged}, final.Repos)
	assertRepoAdded(t, localOutput, wantLocal)
	assertRepoAdded(t, managedOutput, wantManaged)
	assertRepoListed(t, listed, wantLocal)
	assertRepoListed(t, listed, wantManaged)
	assert.Empty(t, stderr)
	assert.Equal(t, []beadsInitialization{{dir: repoWorkflowDataRoot + "/repos/managedrepo/beads", prefix: "managedrepo"}}, fixture.beadsInitializations)
	for _, lookup := range []struct{ token, dir string }{
		{"localrepo", local.path}, {"op", local.path}, {"managedrepo", repoWorkflowDataRoot + "/repos/managedrepo/beads"},
	} {
		stdout, stderr, err := fixture.execute("repo", "beads-dir", lookup.token)
		require.NoError(t, err)
		assert.Equal(t, lookup.dir+"\n", stdout)
		assert.Empty(t, stderr)
	}
}

func TestIntegrationRepoAddRejectsDuplicateLocalBeadsPrefix(t *testing.T) {
	first := aGitRepository(t, "alpha")
	second := aGitRepository(t, "beta")
	fixture := newRepoWorkflowFixture(t, first, second)
	fixture.withLocalBeads(first, "op")
	fixture.withLocalBeads(second, "op")
	_, stderr, err := fixture.execute("repo", "add", first.path)
	require.NoError(t, err, "stderr: %s", stderr)
	before := fixture.loadFinalRegistry()

	stdout, _, err := fixture.execute("repo", "add", second.path)

	assert.ErrorContains(t, err, "duplicate beads prefix \"op\"")
	assert.Empty(t, stdout)
	assert.Equal(t, before, fixture.loadFinalRegistry())
	assert.Empty(t, fixture.beadsInitializations)
}

func TestIntegrationRepoAddHoldsMutationLockDuringInitializationAndReleasesIt(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	fixture := newRepoWorkflowFixture(t, repo)
	fixture.withoutLocalBeads()
	var contention error
	fixture.initialize = func(string, string) error {
		contention = state.WithGlobalMutationLock(fixture.paths, "competing registration", func() error { return nil })
		return nil
	}

	_, stderr, err := fixture.execute("repo", "add", repo.path)

	require.NoError(t, err, "stderr: %s", stderr)
	assert.ErrorIs(t, contention, os.ErrExist)
	assert.NoError(t, state.WithGlobalMutationLock(fixture.paths, "next registration", func() error { return nil }))
}

func TestIntegrationRepoAddHeldMutationLockPreventsInitializationAndRegistration(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	fixture := newRepoWorkflowFixture(t, repo)
	fixture.withoutLocalBeads()
	holdMutationLock(t, fixture.paths)

	stdout, _, err := fixture.execute("repo", "add", repo.path)

	var lockErr *state.LockAcquisitionError
	require.ErrorAs(t, err, &lockErr)
	assert.Equal(t, "repo add", lockErr.Operation)
	assert.Equal(t, repoWorkflowDataRoot+"/locks/mutation.lock", lockErr.Path)
	assert.Empty(t, stdout)
	assert.Empty(t, fixture.beadsInitializations)
	assert.Empty(t, fixture.loadFinalRegistry().Repos)
}
