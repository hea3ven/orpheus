//go:build integration

package cli_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/stretchr/testify/require"
)

type syncWorkflowFixture struct {
	*finalizationWorkflowFixture
	syncGit *memorySyncGit
}

func newSyncFixture(t *testing.T) *syncWorkflowFixture {
	t.Helper()
	base := newFinalizationFixture(t, "op-sync")
	branch, dir := base.expectedTarget("op-sync")
	base.backend.tasks["op-sync"] = syncTask("op-sync", base.pr.url)
	item := base.backend.tasks["op-sync"]
	item.Metadata[taskmodel.MetadataBranch] = branch
	item.Metadata[taskmodel.MetadataWorktree] = dir
	base.backend.tasks[item.ID] = item
	base.git.targets[dir] = memoryGitTarget{branch: branch}
	base.git.hasCandidateChanges = false
	base.publication.remote[branch] = base.candidate.head
	base.pr.state = pullrequest.StateOpen
	base.pr.existing = &pullrequest.CreateRequest{RepositoryPath: taskWorkflowRepoRoot, HeadBranch: branch, BaseBranch: "main"}
	git := &memorySyncGit{memoryPublicationGit: base.publication}
	base.options.Dependencies.SyncGit = git
	return &syncWorkflowFixture{finalizationWorkflowFixture: base, syncGit: git}
}

func syncTask(id, url string) taskmodel.Task {
	item := anOpenTask(id)
	item.Status = taskmodel.StatusInProgress
	item.Metadata = taskmodel.Metadata{taskmodel.MetadataPRURL: url}
	return item
}

func (f *syncWorkflowFixture) resolvingAgent(cause error) {
	f.t.Helper()
	f.configureAgentProfiles(agent.AgentDefaults{Implementer: "impl-codex", SyncConflictResolver: "sync-codex"}, map[string]agent.Profile{
		"impl-codex": {Harness: "codex", Model: "gpt-5"},
		"sync-codex": {Harness: "codex", Model: "gpt-5"},
	})
	f.syncGit.behind = true
	f.syncGit.conflicts = []string{"conflict.txt"}
	f.agent.outcomes = append(f.agent.outcomes, semanticAgentOutcome{
		exitWithoutCompletion: true, err: cause,
		mutateCandidate: func() {
			state, _ := f.loadFinalTask("op-sync")
			require.NotNil(f.t, state.ActiveSyncConflict)
			require.Equal(f.t, taskstate.SyncConflictPhaseResolving, state.ActiveSyncConflict.Phase)
			require.NotNil(f.t, state.ActiveSyncConflict.Execution)
			require.NotZero(f.t, state.ActiveSyncConflict.Execution.ChildPID)
			if cause == nil {
				f.syncGit.resolved = true
			}
		},
	})
}

// This fake models branch heads and conflict effects. It does not parse commands
// or reproduce Git's merge algorithm; those belong to internal/git contracts.
type memorySyncGit struct {
	*memoryPublicationGit
	behind                bool
	conflicts             []string
	mergeActive, resolved bool
	calls                 []gitmeta.TaskBranchSyncOptions
	commits, rollbacks    int
	remoteError           error
}

var _ workflow.SyncConflictRecoveryGit = (*memorySyncGit)(nil)
var _ workflow.SyncConflictCompletionGit = (*memorySyncGit)(nil)

func (g *memorySyncGit) result(opts gitmeta.TaskBranchSyncOptions, status gitmeta.TaskBranchSyncStatus) gitmeta.TaskBranchSyncResult {
	return gitmeta.TaskBranchSyncResult{Status: status, Branch: opts.Branch, DefaultBranch: opts.DefaultBranch, Head: g.head, ConflictFiles: slices.Clone(g.conflicts)}
}
func (g *memorySyncGit) validate(opts gitmeta.TaskBranchSyncOptions) error {
	if opts.RepoPath != taskWorkflowRepoRoot || opts.DefaultBranch != "main" || g.targets[opts.Worktree].branch != opts.Branch || g.remote[opts.Branch] == "" {
		return fmt.Errorf("unknown sync target: %+v", opts)
	}
	return nil
}
func (g *memorySyncGit) SyncTaskBranchWithDefault(ctx context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	if err := g.validate(opts); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	g.calls = append(g.calls, opts)
	if g.behind && len(g.conflicts) > 0 {
		return gitmeta.TaskBranchSyncResult{}, gitmeta.ErrMergeConflict
	}
	if opts.UpdatePolicy == gitmeta.TaskBranchUpdateConflictsOnly {
		if g.behind {
			return g.result(opts, gitmeta.TaskBranchSyncConflictFree), nil
		}
		return g.result(opts, gitmeta.TaskBranchSyncAlreadyCurrent), nil
	}
	status := gitmeta.TaskBranchSyncAlreadyCurrent
	if g.behind {
		g.commits++
		g.head = fmt.Sprintf("sync-merge-%d", g.commits)
		g.behind = false
		status = gitmeta.TaskBranchSyncUpdated
	} else if g.remote[opts.Branch] != g.head {
		status = gitmeta.TaskBranchSyncPushed
	}
	if status != gitmeta.TaskBranchSyncAlreadyCurrent {
		if err := g.PushTaskBranch(ctx, opts.Worktree, opts.Branch); err != nil {
			return gitmeta.TaskBranchSyncResult{}, err
		}
	}
	return g.result(opts, status), nil
}
func (g *memorySyncGit) BeginTaskBranchConflictResolution(_ context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	if err := g.validate(opts); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	if !g.behind || len(g.conflicts) == 0 || g.mergeActive {
		return gitmeta.TaskBranchSyncResult{}, errors.New("no new conflict to resolve")
	}
	g.mergeActive = true
	return g.result(opts, gitmeta.TaskBranchSyncConflicted), nil
}
func (g *memorySyncGit) CompleteTaskBranchConflictResolution(_ context.Context, opts gitmeta.TaskBranchSyncOptions, _ []string) (gitmeta.TaskBranchSyncResult, error) {
	if err := g.validate(opts); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	return gitmeta.TaskBranchSyncResult{}, errors.New("durable sync must commit and push separately")
}
func (g *memorySyncGit) CommitTaskBranchConflictResolution(_ context.Context, opts gitmeta.TaskBranchSyncOptions, files []string) (gitmeta.TaskBranchSyncResult, error) {
	if err := g.validate(opts); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	if !g.mergeActive || !g.resolved || !slices.Equal(files, g.conflicts) {
		return gitmeta.TaskBranchSyncResult{}, errors.New("unresolved conflict files")
	}
	g.commits++
	g.head = fmt.Sprintf("conflict-merge-%d", g.commits)
	g.mergeActive, g.behind = false, false
	return g.result(opts, gitmeta.TaskBranchSyncUpdated), nil
}
func (g *memorySyncGit) PushCommittedTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	if err := g.validate(opts); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	if g.mergeActive || g.behind {
		return gitmeta.TaskBranchSyncResult{}, errors.New("conflict merge is not committed")
	}
	if err := g.PushTaskBranch(ctx, opts.Worktree, opts.Branch); err != nil {
		return gitmeta.TaskBranchSyncResult{}, err
	}
	return g.result(opts, gitmeta.TaskBranchSyncUpdated), nil
}
func (g *memorySyncGit) InspectTaskBranchConflictCheckpoint(_ context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchConflictCheckpoint, error) {
	if err := g.validate(opts); err != nil {
		return gitmeta.TaskBranchConflictCheckpoint{}, err
	}
	return gitmeta.TaskBranchConflictCheckpoint{LocalHead: g.head, RemoteHead: g.remote[opts.Branch], MergeSource: "default-head"}, nil
}
func (g *memorySyncGit) InspectRemoteTaskBranchHead(_ context.Context, opts gitmeta.TaskBranchSyncOptions) (string, error) {
	if err := g.validate(opts); err != nil {
		return "", err
	}
	if g.remoteError != nil {
		return "", g.remoteError
	}
	return g.remote[opts.Branch], nil
}
func (g *memorySyncGit) InspectTaskBranchConflictRollbackEligibility(_ context.Context, opts gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint, localHead string) error {
	if err := g.validate(opts); err != nil {
		return err
	}
	if g.remote[opts.Branch] != checkpoint.RemoteHead || (g.head != checkpoint.LocalHead && g.head != localHead) {
		return errors.New("branch moved since checkpoint")
	}
	return nil
}
func (g *memorySyncGit) RollbackTaskBranchConflictResolution(_ context.Context, opts gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint) error {
	if err := g.validate(opts); err != nil {
		return err
	}
	g.rollbacks++
	g.head = checkpoint.LocalHead
	g.mergeActive, g.resolved, g.behind = false, false, true
	return nil
}
func (g *memorySyncGit) VerifyTaskBranchConflictRollback(_ context.Context, opts gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint) error {
	if err := g.validate(opts); err != nil {
		return err
	}
	if g.mergeActive || g.head != checkpoint.LocalHead || g.remote[opts.Branch] != checkpoint.RemoteHead {
		return errors.New("rollback not complete")
	}
	return nil
}
