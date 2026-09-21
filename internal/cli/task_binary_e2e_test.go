//go:build integration

//nolint:testpackage // Compiled CLI scenario requires internal invocation fixtures.
package cli

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationBinaryE2ETaskRunUsesSeparateTaskProposalSelection(t *testing.T) {
	t.Parallel()
	is := assert.New(t)
	must := require.New(t)
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	must.NoError(err)
	orpheusBin := buildOrpheusTestBinary(t, sourceRoot)
	root := newTestState(t)
	paths := currentTestPaths(t)
	store := registry.NewStore(paths)

	repoPath := newTestRepoWithLocalOriginAt(t, root, filepath.Join("repos", "alpha"))
	must.NoError(store.Save(registry.Registry{Repos: []registry.Repo{{
		ID:            "alpha",
		Name:          "Alpha Repo",
		Path:          repoPath,
		DefaultBranch: "main",
		BeadsMode:     registry.BeadsModeLocal,
		BeadsPrefix:   "op",
	}}}))

	implementer := writeReviewScript(t, fmt.Sprintf(`#!/bin/sh
printf 'reviewed\n' > reviewed.txt
%s agent done --summary "Implementation" --description "Implemented." --detailed-description "Implemented details." --technical-explanation "Implemented the review fixture."
`, shellQuote(orpheusBin)))
	reviewer := writeReviewScript(t, fmt.Sprintf(`#!/bin/sh
%s agent review add \
  --type separate-task \
  --title "Extract helper" \
  --description "The helper can be extracted later." \
  --task-title "Extract shared helper" \
  --task-description "Create a shared helper." \
  --task-acceptance-criteria "Helper is covered by tests."
`, shellQuote(orpheusBin)))
	must.NoError(testutil.WriteConfigYAML(paths, agent.ConfigFile, map[string]any{
		"agents": map[string]any{
			"defaults": map[string]any{
				"implementer": "implementer",
				"reviewer":    "reviewer",
			},
			"profiles": map[string]any{
				"implementer": map[string]any{"command": implementer, "interactive": false},
				"reviewer": map[string]any{
					"command":     reviewer,
					"interactive": false,
				},
			},
		},
		"reviews": map[string]any{
			"default_pipeline": "standard",
			"pipelines": map[string]any{
				"standard": map[string]any{
					"steps": []map[string]any{{"kind": "agent_review", "name": "ai-review"}},
				},
			},
		},
	}))

	taskJSON := mainReadyTaskJSON("op-main", repoPath)
	withFakeBDCommandResponses(t, []fakeBDCommandResponse{
		{dir: repoPath, args: "--json --readonly --sandbox show --id op-main", stdout: taskJSON},
		{dir: repoPath, args: "--json --readonly --sandbox list --all --limit 0", stdout: taskJSON},
		{dir: repoPath, args: "--json --sandbox close op-main", stdout: "{}"},
	})

	withFakeGHPRResponses(t, fakeGHPRResponses{listStdout: "[]", createStdout: "https://github.test/org/alpha/pull/1\n"})
	stdout, stderr := executeCommandWithScriptedInput(t, []string{"task", "run", "--repo-root", "op-main"}, "", "n\n")

	is.Contains(stdout, "Published op-main")
	is.Contains(stderr, "Separate-task review findings can be created as standalone Beads")
	is.Contains(stderr, "Create follow-up Beads [numbers, a=all, n=none]")
	is.NotContains(stderr, "Created follow-up Bead")
	var state taskstate.TaskState
	must.NoError(paths.ReadDataYAML(filepath.Join("repos", "alpha", "tasks", "op-main.yaml"), &state))
	latest, ok := taskstate.LatestReview(state)
	must.True(ok)
	is.Equal(taskstate.ReviewStatusPassed, latest.Status)
	must.Len(latest.Findings, 1)
	is.Equal(taskstate.FindingTypeSeparateTask, latest.Findings[0].Type)
	is.Empty(latest.Findings[0].CreatedTaskID)
}
func buildOrpheusTestBinary(t *testing.T, sourceRoot string) string {
	t.Helper()

	binPath := filepath.Join(testutil.CanonicalTempDir(t), "orpheus")
	command := exec.Command("go", "build", "-o", binPath, "./cmd/orpheus")
	command.Dir = sourceRoot
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build orpheus test binary: %v\n%s", err, output)
	}
	return binPath
}
