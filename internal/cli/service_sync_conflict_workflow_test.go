//go:build integration

package cli_test

import (
	"context"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type disappearingConflictGit struct {
	*memorySyncGit
	beginCalls int
}

func (g *disappearingConflictGit) BeginTaskBranchConflictResolution(
	_ context.Context,
	opts gitmeta.TaskBranchSyncOptions,
) (gitmeta.TaskBranchSyncResult, error) {
	if err := g.validate(opts); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	g.beginCalls++
	g.conflicts = nil
	g.behind = false
	return g.result(opts, gitmeta.TaskBranchSyncAlreadyCurrent), nil
}

func TestIntegrationWorkflowTaskSyncAllClearsConflictThatDisappearsAfterPreflight(t *testing.T) {
	f := newSyncFixture(t)
	f.syncGit.behind = true
	f.syncGit.conflicts = []string{"conflict.txt"}
	git := &disappearingConflictGit{memorySyncGit: f.syncGit}
	f.options.Dependencies.SyncGit = git
	head := f.candidate.head

	stdout, stderr := f.run("", "task", "sync", "--all")

	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Open/in-review PRs (1):")
	assert.Contains(t, stdout, "op-sync (alpha): PR "+f.pr.url+" is still open for review")
	assert.Equal(t, 1, git.beginCalls)
	assert.Equal(t, head, f.candidate.head)
	assert.Equal(t, head, f.publication.remote["orpheus/op-sync"])
	assert.Empty(t, f.candidate.pushes)
	assert.Empty(t, f.agent.launches)
	assert.Zero(t, f.syncGit.commits)
	assert.Zero(t, f.syncGit.rollbacks)
	state, _ := f.loadFinalTask("op-sync")
	assert.Nil(t, state.ActiveSyncConflict)
	for _, event := range state.Events {
		require.NotEqual(t, taskstate.EventSyncConflictFinished, event.Type)
		require.NotEqual(t, taskstate.EventSyncConflictFailed, event.Type)
	}
}
