package finalization_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/finalization"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFinalizationReconcilesSourceClosureAndRetriesCleanupIndependently(t *testing.T) {
	failure := errors.New("receipt unavailable")
	for _, boundary := range []string{"intent save", "lost close response", "closure receipt", "cleanup receipt"} {
		t.Run(boundary, func(t *testing.T) {
			root := testutil.CanonicalTempDir(t)
			paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
			require.NoError(t, err)
			repo := task.Repository{ID: "alpha", Path: "/fixture/repo", DefaultBranch: "main", TaskIDPrefix: "op"}
			item := task.Task{ID: "op-1", Status: task.StatusInProgress}
			targets, err := tasktarget.ExpectedTargetsForTask(repo, item.ID, paths)
			require.NoError(t, err)
			directory, branch := targets.WorktreeTeam.Worktree, targets.WorktreeTeam.Branch
			item.Metadata = task.Metadata{task.MetadataBranch: branch, task.MetadataWorktree: directory}
			current, err := testutil.DeliveredPRWork("alpha", "op-1", directory, branch, "https://example.test/pr/1")
			require.NoError(t, err)
			w := &current.Workflows[0]
			at := time.Date(2026, 1, 1, 0, 0, 10, 0, time.UTC)
			w.LastPhase().(*work.ExternalReviewPhase).Outcome = &work.PROutcome{State: work.PRMerged, ObservedAt: at}
			w.Phases = append(w.Phases, &work.FinalizationPhase{})
			store := workstate.NewStore(paths)
			current.Version, err = store.Save(current, 0)
			require.NoError(t, err)
			faults := &finalizationStore{Store: store, failure: failure, boundary: boundary}
			backend := &closingBackend{item: item, store: store, t: t}
			if boundary == "lost close response" {
				backend.closeErr = failure
			}
			git := &fakeClosedTaskWorktreeGit{inspection: gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeAbsent, Worktree: directory}}
			runner := phaseexec.Runner{Store: faults, Paths: paths, RepositoryID: "alpha", TaskID: "op-1"}
			phase := finalization.Service{Paths: paths, Repository: repo, Task: item, Backend: backend, Git: git}

			saved, outcome, runErr := phase.Run(context.Background(), current, runner.Execute)
			if boundary == "cleanup receipt" {
				require.NoError(t, runErr)
				require.NotNil(t, outcome.Cleanup)
				assert.Equal(t, finalization.WorktreeCleanupAlreadyAbsent, outcome.Cleanup.Outcome)
				assert.Equal(t, directory, outcome.Cleanup.Worktree)
				assert.Contains(t, outcome.Cleanup.Reason, failure.Error())
				assert.Equal(t, "https://example.test/pr/1", outcome.PRURL)
				assert.Equal(t, task.StatusClosed, outcome.Task.Status)
			} else {
				require.ErrorIs(t, runErr, failure)
				assert.Zero(t, git.inspects, "cleanup cannot run before closure is durable")
			}
			persisted, err := store.Load("alpha", "op-1")
			require.NoError(t, err)
			assert.Equal(t, persisted, saved, "the next selection must see only committed state")
			final := persisted.Workflows[0].LastPhase().(*work.FinalizationPhase)
			assert.Empty(t, final.Cleanup)
			if boundary == "intent save" {
				assert.Nil(t, final.ClosureIntentAt)
				assert.Zero(t, backend.closes)
			} else {
				require.NotNil(t, final.ClosureIntentAt)
				assert.Equal(t, 1, backend.closes)
			}
			if boundary != "cleanup receipt" {
				assert.Nil(t, final.Closure)
			} else {
				require.NotNil(t, final.Closure)
			}
			faults.boundary, backend.closeErr = "", nil
			phase = finalization.Service{Paths: paths, Repository: repo, Task: backend.item, Backend: backend, Git: git}
			saved, outcome, err = phase.Run(context.Background(), saved, runner.Execute)
			require.NoError(t, err)
			assert.Equal(t, 1, backend.closes, "reconcile an already closed source instead of repeating Close")
			assert.Equal(t, task.StatusClosed, outcome.Task.Status)
			assert.Equal(t, "https://example.test/pr/1", outcome.PRURL)
			final = saved.Workflows[0].LastPhase().(*work.FinalizationPhase)
			require.NotNil(t, final.Closure)
			require.Len(t, final.Cleanup, 1)
			assert.IsType(t, &work.WorktreeRemoved{}, final.Cleanup[0])
			assert.False(t, finalization.Pending(saved, repo.Path))
		})
	}
}

type closingBackend struct {
	item     task.Task
	store    phaseexec.Store
	t        *testing.T
	closeErr error
	closes   int
}

func (b *closingBackend) Get(context.Context, string) (task.Task, error) { return b.item, nil }
func (b *closingBackend) Close(context.Context, string) error {
	b.t.Helper()
	current, err := b.store.Load("alpha", "op-1")
	require.NoError(b.t, err)
	require.NotNil(b.t, current.Workflows[0].LastPhase().(*work.FinalizationPhase).ClosureIntentAt, "source effects require durable intent")
	b.closes++
	b.item.Status = task.StatusClosed
	return b.closeErr
}

type finalizationStore struct {
	phaseexec.Store
	failure  error
	boundary string
}

func (s *finalizationStore) Save(current work.State, expected work.StateVersion) (work.StateVersion, error) {
	final := current.Workflows[0].LastPhase().(*work.FinalizationPhase)
	if (s.boundary == "intent save" && final.ClosureIntentAt != nil) ||
		(s.boundary == "closure receipt" && final.Closure != nil) ||
		(s.boundary == "cleanup receipt" && len(final.Cleanup) > 0) {
		return 0, s.failure
	}
	return s.Store.Save(current, expected)
}
