//go:build integration

package cli_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/cli"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowRegistrationSelectsManagedSourceAndPrefix(t *testing.T) {
	for _, source := range []string{"gig", "beads"} {
		sourcePrefix := map[string]string{"gig": "9.team-", "beads": "Team_9"}[source]
		for _, selection := range []struct {
			name, flag, input, prefix string
			interactive               bool
		}{
			{name: "noninteractive default", prefix: "alpha"},
			{name: "noninteractive flag", flag: "team-platform", prefix: "team-platform"},
			{name: "source-compatible characters", flag: sourcePrefix, prefix: sourcePrefix},
			{name: "interactive selection", interactive: true, input: "team-platform\n", prefix: "team-platform"},
			{name: "interactive default", interactive: true, input: "\n", prefix: "alpha"},
			{name: "interactive flag skips prefix prompt", interactive: true, flag: "team-platform", prefix: "team-platform"},
		} {
			t.Run(source+"/"+selection.name, func(t *testing.T) {
				repo := aGitRepository(t, "alpha")
				fixture := newRepoWorkflowFixture(t, repo)
				fixture.withoutLocalBeads()
				if source == "gig" {
					// Default Gig registration must ignore even an existing local Beads store.
					fixture.withLocalBeads(repo, "local")
				}
				fixture.options.Dependencies.Terminal = cli.TerminalCapabilities{InputIsTerminal: func(io.Reader) bool { return selection.interactive }}
				args := []string{"repo", "add", repo.path}
				if source == "beads" || selection.flag != "" {
					args = append(args, "--task-source", source)
				}
				if selection.flag != "" {
					args = append(args, "--prefix", selection.flag)
				}
				// One queued stream also protects the wizard against losing buffered answers.
				input := "\n\n" + selection.input + "capitalized\n[OPS] {{summary}}\n"
				added, stderr, err := fixture.executeWithInput(input, args...)
				require.NoError(t, err, "stderr: %s", stderr)
				want := repo.registeredWithManagedBeads()
				want.BeadsPrefix = selection.prefix
				if source == "gig" {
					want.TaskSource, want.TaskMode, want.TaskPrefix = "gig", "managed", selection.prefix
					want.BeadsMode, want.BeadsPrefix = "", ""
					assert.Empty(t, fixture.beadsInspections)
					assert.Empty(t, fixture.beadsInitializations)
					assert.Equal(t, []taskSourceInitialization{{dir: repoWorkflowDataRoot + "/repos/alpha/gig", prefix: selection.prefix}}, fixture.gigInitializations)
				} else {
					assert.Equal(t, []taskSourceInitialization{{dir: repoWorkflowDataRoot + "/repos/alpha/beads", prefix: selection.prefix}}, fixture.beadsInitializations)
					assert.Empty(t, fixture.gigInitializations)
				}
				if selection.interactive {
					want.SummaryGuidanceStyle, want.TitleTemplate = "capitalized", "[OPS] {{summary}}"
					assert.Equal(t, selection.flag == "", strings.Contains(stderr, "Task prefix [alpha]:"))
				} else {
					assert.Empty(t, stderr)
				}
				assert.Equal(t, []registry.Repo{want}, fixture.loadFinalRegistry().Repos)
				assertRepoAdded(t, added, want)
				listed, stderr, err := fixture.execute("repo", "list")
				require.NoError(t, err, "stderr: %s", stderr)
				assertRepoListed(t, listed, want)
				if source == "gig" {
					_, _, err = fixture.execute("repo", "beads-dir", selection.prefix)
					assert.ErrorContains(t, err, "applies only to Beads")
				}

				backend := newOwnershipAuthoringBackend()
				backend.createdID = selection.prefix + "-9"
				fixture.options.Dependencies.TaskBackendFactory = func(sourceConfig taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
					assert.Equal(t, source, sourceConfig.Kind)
					assert.Equal(t, selection.prefix, sourceConfig.Repository.TaskIDPrefix)
					return backend, nil
				}
				created, stderr, err := fixture.execute("task", "create", "--repo", selection.prefix, "--title", "Selected prefix", "--description", "Description", "--acceptance", "Acceptance")
				require.NoError(t, err, "stderr: %s", stderr)
				assert.Contains(t, created, "Created task "+selection.prefix+"-9")
				shown, stderr, err := fixture.execute("task", "show", selection.prefix+"-9")
				require.NoError(t, err, "stderr: %s", stderr)
				assert.Contains(t, shown, "Selected prefix")
				assert.Equal(t, []string{selection.prefix + "-9"}, backend.getCalls)
			})
		}
	}
}

func TestIntegrationWorkflowRepoAddStoresDiscoveredRootForNestedPath(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	fixture := newRepoWorkflowFixture(t, repo)
	fixture.withoutLocalBeads()
	nested := filepath.Join(repo.path, "nested", "dir")
	fixture.gitResults[nested] = fixture.gitResults[repo.path]

	added, stderr, err := fixture.execute("repo", "add", "--task-source", "beads", nested)
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

func TestIntegrationWorkflowRepoAddWarnsWhenRemoteIsMissing(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	repo.remote = ""
	fixture := newRepoWorkflowFixture(t, repo)
	fixture.withoutLocalBeads()
	fixture.gitResults[repo.path] = gitmeta.Inspection{Root: repo.path, RemoteErr: gitmeta.ErrNoRemote, DefaultBranchCandidate: "main", DefaultBranchSource: gitmeta.DefaultBranchSourceCurrentBranch}

	stdout, stderr, err := fixture.execute("repo", "add", "--task-source", "beads", repo.path)
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

func TestIntegrationWorkflowRepoAddRejectsGitInspectionFailureWithoutRegistration(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	fixture := newRepoWorkflowFixture(t, repo)
	inspectionErr := errors.New("not a git worktree")
	fixture.gitErrors[repo.path] = inspectionErr

	stdout, _, err := fixture.execute("repo", "add", "--task-source", "beads", repo.path)

	assert.ErrorIs(t, err, inspectionErr)
	assert.Empty(t, stdout)
	assert.Empty(t, fixture.loadFinalRegistry().Repos)
	assert.Empty(t, fixture.beadsInspections)
	assert.Empty(t, fixture.beadsInitializations)
}

func TestIntegrationWorkflowRepoAddRejectsInvalidRegistrationBeforeInitialization(t *testing.T) {
	for _, source := range []string{"gig", "beads"} {
		type registrationFailure struct {
			name, prefix, conflict, wantError string
			interactive                       bool
		}
		cases := []registrationFailure{
			{name: "path", prefix: "chosen", conflict: "path", wantError: "duplicate repo path"},
			{name: "identity", prefix: "chosen", conflict: "identity", wantError: `duplicate repo id "alpha"`},
			{name: "default prefix collision", conflict: "prefix", wantError: `duplicate task prefix "alpha"`},
			{name: "selected prefix collision", prefix: "occupied", conflict: "prefix", wantError: `duplicate task prefix "occupied"`},
			{name: "prefix collides with id", prefix: "beta", wantError: `repo task prefix "beta" collides with repo[0] id`},
			{name: "prefix collides with name", prefix: "Beta", wantError: `repo task prefix "Beta" collides with repo[0] name`},
			{name: "empty flag", wantError: "--prefix must not be empty"},
			{name: "blank flag", prefix: " \t", wantError: "--prefix must not be empty"},
			{name: "whitespace", prefix: "two words", wantError: "task prefix"},
			{name: "unicode whitespace", prefix: "two\u00a0words", wantError: "task prefix"},
			{name: "control character", prefix: "op\x00", wantError: "task prefix"},
			{name: "forward slash", prefix: "bad/prefix", wantError: "task prefix"},
			{name: "backslash", prefix: `bad\prefix`, wantError: "task prefix"},
			{name: "leading hyphen", prefix: "-bad", wantError: "task prefix"},
			{name: "invalid prompt answer", interactive: true, prefix: "bad/prefix", wantError: "task prefix"},
			{name: "prompt prefix collision", interactive: true, prefix: "occupied", conflict: "prefix", wantError: `duplicate task prefix "occupied"`},
		}
		if source == "beads" {
			for _, prefix := range []string{"9team", "team.name", "team-", "team!", strings.Repeat("a", 65)} {
				cases = append(cases, registrationFailure{name: "invalid Beads prefix " + prefix, prefix: prefix, wantError: "invalid Beads task prefix"})
			}
			cases = append(cases, registrationFailure{name: "local prefix mismatch", prefix: "other", conflict: "local", wantError: `--prefix "other" conflicts with existing repo-local Beads prefix "op"`})
		}
		for _, test := range cases {
			t.Run(source+"/"+test.name, func(t *testing.T) {
				repo := aGitRepository(t, "alpha")
				fixture := newRepoWorkflowFixture(t, repo)
				fixture.withoutLocalBeads()
				existing := aRegisteredRepo("beta")
				existing.Name, existing.BeadsPrefix = "Beta", "busy"
				switch test.conflict {
				case "path":
					existing.Path = repo.path
				case "identity":
					existing.ID, existing.Name = "alpha", "alpha"
				case "prefix":
					existing.BeadsPrefix = "alpha"
					if test.prefix != "" {
						existing.BeadsPrefix = test.prefix
					}
				case "local":
					fixture.withLocalBeads(repo, "op")
				}
				fixture.withRegisteredRepos(existing)
				fixture.options.Dependencies.Terminal = cli.TerminalCapabilities{InputIsTerminal: func(io.Reader) bool { return test.interactive }}
				args := []string{"repo", "add", "--task-source", source, repo.path}
				if !test.interactive && test.name != "default prefix collision" {
					args = append(args, "--prefix", test.prefix)
				}

				stdout, _, err := fixture.executeWithInput("\n\n"+test.prefix+"\n\n\n", args...)

				assert.ErrorContains(t, err, test.wantError)
				assert.Empty(t, stdout)
				assert.Empty(t, fixture.beadsInitializations)
				assert.Empty(t, fixture.gigInitializations)
				assert.Equal(t, []registry.Repo{existing}, fixture.loadFinalRegistry().Repos)
			})
		}
	}
}

func TestIntegrationWorkflowRepoAddInitializationFailureLeavesRegistryUnchanged(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	fixture := newRepoWorkflowFixture(t, repo)
	fixture.withoutLocalBeads()
	existing := aRegisteredRepo("beta")
	fixture.withRegisteredRepos(existing)
	initErr := errors.New("directory already exists and is not empty")
	fixture.initialize = func(string, string) error { return initErr }

	stdout, _, err := fixture.execute("repo", "add", "--task-source", "beads", repo.path)

	assert.ErrorIs(t, err, initErr)
	assert.Empty(t, stdout)
	assert.Equal(t, []registry.Repo{existing}, fixture.loadFinalRegistry().Repos)
	require.Len(t, fixture.beadsInitializations, 1)
	assert.Equal(t, taskSourceInitialization{dir: repoWorkflowDataRoot + "/repos/alpha/beads", prefix: "alpha"}, fixture.beadsInitializations[0])
}

func TestIntegrationWorkflowRepoRegistersAndListsLocalAndManagedRepositories(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "stored prefix", true: "matching flag"}[explicit], func(t *testing.T) {
			local := aGitRepository(t, "localrepo")
			managed := aGitRepository(t, "managedrepo")
			fixture := newRepoWorkflowFixture(t, local, managed)
			fixture.withoutLocalBeads()
			fixture.withLocalBeads(local, "op")
			wantLocal := local.registeredWithManagedBeads()
			wantLocal.BeadsMode, wantLocal.BeadsPrefix = registry.BeadsModeLocal, "op"
			wantManaged := managed.registeredWithManagedBeads()

			fixture.options.Dependencies.Terminal = cli.TerminalCapabilities{InputIsTerminal: func(io.Reader) bool { return true }}
			args := []string{"repo", "add", "--task-source", "beads", local.path}
			if explicit {
				args = append(args, "--prefix", "op")
			}
			localOutput, stderr, err := fixture.executeWithInput("\n\n\n\n", args...)
			require.NoError(t, err, "stderr: %s", stderr)
			assert.NotContains(t, stderr, "Task prefix")
			fixture.options.Dependencies.Terminal = cli.TerminalCapabilities{}
			managedOutput, stderr, err := fixture.execute("repo", "add", "--task-source", "beads", managed.path)
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
			assert.Equal(t, []taskSourceInitialization{{dir: repoWorkflowDataRoot + "/repos/managedrepo/beads", prefix: "managedrepo"}}, fixture.beadsInitializations)
			for _, lookup := range []struct{ token, dir string }{
				{"localrepo", local.path}, {"op", local.path}, {"managedrepo", repoWorkflowDataRoot + "/repos/managedrepo/beads"},
			} {
				stdout, stderr, err := fixture.execute("repo", "beads-dir", lookup.token)
				require.NoError(t, err)
				assert.Equal(t, lookup.dir+"\n", stdout)
				assert.Empty(t, stderr)
			}
		})
	}
}

func TestIntegrationWorkflowRepoAddRejectsDuplicateLocalBeadsPrefix(t *testing.T) {
	first := aGitRepository(t, "alpha")
	second := aGitRepository(t, "beta")
	fixture := newRepoWorkflowFixture(t, first, second)
	fixture.withLocalBeads(first, "op")
	fixture.withLocalBeads(second, "op")
	_, stderr, err := fixture.execute("repo", "add", "--task-source", "beads", first.path)
	require.NoError(t, err, "stderr: %s", stderr)
	before := fixture.loadFinalRegistry()

	stdout, _, err := fixture.execute("repo", "add", "--task-source", "beads", second.path)

	assert.ErrorContains(t, err, "duplicate task prefix \"op\"")
	assert.Empty(t, stdout)
	assert.Equal(t, before, fixture.loadFinalRegistry())
	assert.Empty(t, fixture.beadsInitializations)
}

func TestIntegrationWorkflowRepoAddHoldsMutationLockDuringInitializationAndReleasesIt(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	fixture := newRepoWorkflowFixture(t, repo)
	fixture.withoutLocalBeads()
	var contention error
	fixture.initialize = func(string, string) error {
		contention = state.WithGlobalMutationLock(fixture.paths, "competing registration", func() error { return nil })
		return nil
	}

	_, stderr, err := fixture.execute("repo", "add", "--task-source", "beads", repo.path)

	require.NoError(t, err, "stderr: %s", stderr)
	assert.ErrorIs(t, contention, os.ErrExist)
	assert.NoError(t, state.WithGlobalMutationLock(fixture.paths, "next registration", func() error { return nil }))
}

func TestIntegrationWorkflowRepoAddHeldMutationLockPreventsInitializationAndRegistration(t *testing.T) {
	repo := aGitRepository(t, "alpha")
	fixture := newRepoWorkflowFixture(t, repo)
	fixture.withoutLocalBeads()
	holdMutationLock(t, fixture.paths)

	stdout, _, err := fixture.execute("repo", "add", "--task-source", "beads", repo.path)

	var lockErr *state.LockAcquisitionError
	require.ErrorAs(t, err, &lockErr)
	assert.Equal(t, "repo add", lockErr.Operation)
	assert.Equal(t, repoWorkflowDataRoot+"/locks/mutation.lock", lockErr.Path)
	assert.Empty(t, stdout)
	assert.Empty(t, fixture.beadsInitializations)
	assert.Empty(t, fixture.loadFinalRegistry().Repos)
}
