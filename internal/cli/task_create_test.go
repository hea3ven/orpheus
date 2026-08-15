//go:build integration

//nolint:testpackage // Invocation-scoped fixture requires internal composition wiring.
package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationTaskCreateReadsPlanningFilesAndRendersCreatedTypeAndID(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	repoPath := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(repoPath, 0o755))
	store := registry.NewStore(paths)
	require.NoError(t, store.Save(registry.Registry{Repos: []registry.Repo{{
		ID: "alpha", Name: "Alpha", Path: repoPath, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op",
	}}}))
	descriptionFile := filepath.Join(root, "description.md")
	designFile := filepath.Join(root, "design.md")
	acceptanceFile := filepath.Join(root, "acceptance.md")
	require.NoError(t, os.WriteFile(descriptionFile, []byte("# Description\n\nLong form."), 0o644))
	require.NoError(t, os.WriteFile(designFile, []byte("# Design"), 0o644))
	require.NoError(t, os.WriteFile(acceptanceFile, []byte("- works"), 0o644))
	logPath := fakeTaskCreateBD(t)

	stdout, stderr := executeCommand(t, []string{
		"task", "create", "--repo", "alpha", "--type", "epic", "--title", "Plan work",
		"--description-file", descriptionFile, "--design-file", designFile, "--acceptance-file", acceptanceFile,
		"--external-ref", "PLAN-3",
	})
	is.Empty(stderr)
	is.Equal("Created epic op-9.\n", stdout)
	log := readFileString(t, logPath)
	for _, want := range []string{
		"--json --sandbox create Plan work", "--description # Description", "--acceptance - works", "--type epic",
		"--design # Design", "--external-ref PLAN-3",
	} {
		is.Contains(log, want)
	}
}

func TestIntegrationTaskCreateRequiresExternalReferenceForGatedRepository(t *testing.T) {
	is := assert.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	repoPath := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(repoPath, 0o755))
	store := registry.NewStore(paths)
	require.NoError(t, store.Save(registry.Registry{Repos: []registry.Repo{{
		ID: "alpha", Name: "Alpha", Path: repoPath, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op",
		TitleTemplate: "[{{external_ref}}] {{summary}}",
	}}}))
	logPath := fakeTaskCreateBD(t)

	_, _, err := executeCommandWithError(t, []string{
		"task", "create", "--repo", "alpha", "--title", "Implement work", "--description", "Description", "--acceptance", "Acceptance",
	})
	require.Error(t, err)
	is.Contains(err.Error(), "--external-ref <reference>")

	stdout, stderr := executeCommand(t, []string{
		"task", "create", "--repo", "alpha", "--title", "Implement work", "--description", "Description", "--acceptance", "Acceptance", "--external-ref", "PLAN-7",
	})
	is.Empty(stderr)
	is.Equal("Created task op-9.\n", stdout)
	is.Contains(readFileString(t, logPath), "--json --sandbox create Implement work")
}

func TestIntegrationTaskEditRequiresExternalReferenceForGatedRepository(t *testing.T) {
	is := assert.New(t)
	root := newTestState(t)
	paths := currentTestPaths(t)
	repoPath := filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(repoPath, 0o755))
	store := registry.NewStore(paths)
	require.NoError(t, store.Save(registry.Registry{Repos: []registry.Repo{{
		ID: "alpha", Name: "Alpha", Path: repoPath, BeadsMode: registry.BeadsModeLocal, BeadsPrefix: "op",
		TitleTemplate: "[{{external_ref}}] {{summary}}",
	}}}))
	taskJSON := `[{"id":"op-1","title":"Existing task","description":"Description","acceptance_criteria":"Acceptance","status":"open","issue_type":"task"}]`
	logPath := withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-1", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox show --id op-1", stdout: taskJSON},
	})

	_, _, err := executeCommandWithError(t, []string{"task", "edit", "op-1", "--title", "Updated task"})
	require.Error(t, err)
	is.Contains(err.Error(), "--external-ref <reference>")
	is.NotContains(readFileString(t, logPath), "update")
}

func TestIntegrationTaskCreateFailsWithoutRepositoryGuidance(t *testing.T) {
	t.Parallel()
	newTestState(t)
	_, _, err := executeCommandWithError(t, []string{
		"task", "create", "--title", "Plan", "--description", "Description", "--acceptance", "Acceptance",
	})
	if err == nil || !strings.Contains(err.Error(), "pass --repo") || strings.Contains(strings.ToLower(err.Error()), "beads") {
		t.Fatalf("error = %v, want source-neutral repository guidance", err)
	}
}

func fakeTaskCreateBD(t *testing.T) string {
	t.Helper()
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "bd.log")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_BD_LOG"
case "$*" in
  "--json --sandbox create "*)
    case "$*" in
      *"--type epic"*)
        printf '%s\n' '{"id":"op-9","title":"Plan work","status":"open","issue_type":"epic"}'
        ;;
      *)
        printf '%s\n' '{"id":"op-9","title":"Implement work","status":"open","issue_type":"task"}'
        ;;
    esac
    ;;
  *)
    echo "unexpected args: $*" >&2
    exit 64
    ;;
esac
`
	path := filepath.Join(binDir, "bd")
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
	setTestEnvironment(t, "FAKE_BD_LOG", logPath)
	prependTestPath(t, binDir)
	return logPath
}
