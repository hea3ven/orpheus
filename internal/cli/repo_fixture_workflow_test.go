//go:build integration

package cli_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/beads"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	repoWorkflowConfigRoot = "/fixture/orpheus-memory/xdg-config/orpheus"
	repoWorkflowDataRoot   = "/fixture/orpheus-memory/xdg-data/orpheus"
)

type gitRepositoryFixture struct {
	name          string
	path          string
	remote        string
	defaultBranch string
}

func aGitRepository(t *testing.T, name string) gitRepositoryFixture {
	t.Helper()
	path := filepath.Join(testutil.CanonicalTempDir(t), name)
	// Repo registration validates the real directory; Git inspection is supplied.
	require.NoError(t, os.MkdirAll(path, 0o755))
	return gitRepositoryFixture{
		name: name, path: path,
		remote: "git@example.com:org/" + name + ".git", defaultBranch: "main",
	}
}

func (r gitRepositoryFixture) registeredWithManagedBeads() registry.Repo {
	return registry.Repo{
		ID: r.name, Name: r.name, Path: r.path,
		Remote: r.remote, DefaultBranch: r.defaultBranch,
		BeadsMode: registry.BeadsModeManaged, BeadsPrefix: r.name,
	}
}

type beadsInitialization struct {
	dir    string
	prefix string
}

type localBeadsResult struct {
	inspection beads.LocalInspection
	err        error
}

type repoWorkflowFixture struct {
	*workflowFixture
	gitInspections       []string
	beadsInspections     []string
	beadsInitializations []beadsInitialization
	gitResults           map[string]gitmeta.Inspection
	gitErrors            map[string]error
	localBeads           map[string]localBeadsResult
	initialize           func(string, string) error
}

func newRepoWorkflowFixture(t *testing.T, repos ...gitRepositoryFixture) *repoWorkflowFixture {
	t.Helper()
	return withRepoDiscovery(newWorkflowFixture(t, repoWorkflowConfigRoot, repoWorkflowDataRoot), repos...)
}

func withRepoDiscovery(base *workflowFixture, repos ...gitRepositoryFixture) *repoWorkflowFixture {
	base.t.Helper()
	f := &repoWorkflowFixture{
		workflowFixture: base,
		gitResults:      make(map[string]gitmeta.Inspection), gitErrors: make(map[string]error), localBeads: make(map[string]localBeadsResult),
	}
	for _, repo := range repos {
		f.gitResults[repo.path] = gitmeta.Inspection{
			Root: repo.path, RemoteCandidate: repo.remote, RemoteCandidateName: "origin",
			DefaultBranchCandidate: repo.defaultBranch, DefaultBranchSource: gitmeta.DefaultBranchSourceOriginHEAD,
		}
	}
	f.options.Dependencies.InspectGit = func(_ context.Context, path string) (gitmeta.Inspection, error) {
		f.gitInspections = append(f.gitInspections, path)
		if err := f.gitErrors[path]; err != nil {
			return gitmeta.Inspection{}, err
		}
		result, ok := f.gitResults[filepath.Clean(path)]
		if !ok {
			return gitmeta.Inspection{}, fmt.Errorf("unexpected Git inspection: %s", path)
		}
		return result, nil
	}
	f.options.Dependencies.InspectLocalBeads = func(path string, _ ...slog.Attr) (beads.LocalInspection, error) {
		f.beadsInspections = append(f.beadsInspections, path)
		result, ok := f.localBeads[path]
		if !ok {
			return beads.LocalInspection{}, fmt.Errorf("local Beads inspection needs an explicit outcome for %s", path)
		}
		return result.inspection, result.err
	}
	f.options.Dependencies.InitializeBeads = func(path, prefix string, _ ...slog.Attr) error {
		f.beadsInitializations = append(f.beadsInitializations, beadsInitialization{dir: path, prefix: prefix})
		if f.initialize != nil {
			return f.initialize(path, prefix)
		}
		return nil
	}
	return f
}

func (f *repoWorkflowFixture) withoutLocalBeads() {
	f.t.Helper()
	for _, repo := range f.gitResults {
		f.localBeads[repo.Root] = localBeadsResult{err: beads.ErrNoLocal}
	}
}

func (f *repoWorkflowFixture) withLocalBeads(repo gitRepositoryFixture, prefix string) {
	f.t.Helper()
	f.localBeads[repo.path] = localBeadsResult{inspection: beads.LocalInspection{Prefix: prefix}}
}

func assertRepoAdded(t *testing.T, output string, want registry.Repo) {
	t.Helper()
	is := assert.New(t)
	is.Contains(output, "Added repo "+want.ID)
	is.Contains(output, want.Path)
	is.Contains(output, want.Remote)
	is.Contains(output, want.DefaultBranch)
	is.Contains(output, want.BeadsMode)
	is.Contains(output, want.BeadsPrefix)
}

func assertRepoListed(t *testing.T, output string, want registry.Repo) {
	t.Helper()
	is := assert.New(t)
	for _, header := range []string{"ID", "NAME", "PATH", "REMOTE", "DEFAULT_BRANCH", "BEADS_MODE", "BEADS_PREFIX"} {
		is.Contains(output, header)
	}
	is.Contains(output, want.ID)
	is.Contains(output, want.Name)
	is.Contains(output, want.Path)
	is.Contains(output, want.Remote)
	is.Contains(output, want.DefaultBranch)
	is.Contains(output, want.BeadsMode)
	is.Contains(output, want.BeadsPrefix)
}
