package preparation_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/preparation"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI's reset journey owns fetch/apply interruptions and authorization. This
// contract owns failed receipts: no destructive effect before the target save,
// and a lost establishment save must retry the exact retained target.
func TestResetReceiptFailuresRetainTheAuthorizedTargetAndHistoricalWork(t *testing.T) {
	for _, failedSave := range []int{1, 2} {
		t.Run(map[int]string{1: "target receipt", 2: "establishment receipt"}[failedSave], func(t *testing.T) {
			current, err := testutil.DeliveredPRWork("repo", "op-1", "/fixture/task", "orpheus/op-1", "https://example.test/pr/1")
			require.NoError(t, err)
			published := current.Workflows[0].Phases[3].(*work.PublicationPhase)
			published.Operation = &work.PublicationOperation{StartedAt: published.Consent.GrantedAt, Parent: "initial-commit", Ref: "refs/orpheus/fixture-public/input", Public: &work.CodeIdentity{Commit: "fixture-output", Tree: "fixture-tree"}}
			at := time.Now().UTC()
			current.Workflows = append(current.Workflows, work.Workflow{ResetRequestedAt: &at})
			current.Version = 7
			originalHistory := current.Workflows[0]
			original, err := workstate.Encode(current)
			require.NoError(t, err)
			failure := errors.New("receipt write unavailable")
			store := &preparationStore{data: original, failAt: failedSave, failure: failure}
			paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
			require.NoError(t, err)
			runner := phaseexec.Runner{Store: store, Paths: paths, RepositoryID: "repo", TaskID: "op-1"}
			git := &resetGit{target: "selected-default"}
			backend := &preparationBackend{item: task.Task{ID: "op-1", Status: task.StatusInProgress}, failure: errors.New("activation failed")}
			newPhase := func() *preparation.Service {
				return &preparation.Service{Repository: task.Repository{ID: "repo", Path: "/fixture/repo", DefaultBranch: "main"}, Task: backend.item, Backend: backend, Git: git}
			}
			git.beforeApply = func(req gitmeta.WorkflowResetRequest) {
				durable, err := store.Load("repo", "op-1")
				require.NoError(t, err)
				p := durable.ActiveWorkflow().Phases[0].(*work.PreparationPhase)
				require.NotNil(t, p.ResetTarget)
				assert.Equal(t, "selected-default", req.Target.Commit)
				assert.Equal(t, p.ResetTarget.Baseline.Commit, req.Target.Commit)
				assert.Contains(t, req.RetainCommits, "fixture-output", "reset retains the earlier public history")
				assert.Nil(t, p.EstablishedAt)
				assert.NotNil(t, durable.ActiveWorkflow().ResetRequestedAt)
			}

			returned, err := drive(t, runner, newPhase(), current)
			require.ErrorIs(t, err, failure)
			assert.Empty(t, backend.activations)
			if failedSave == 1 {
				assert.Empty(t, git.applications)
				assert.Equal(t, original, store.data)
			} else {
				assert.Len(t, git.applications, 1)
				assert.Nil(t, returned.ActiveWorkflow().Context())
				assert.Equal(t, "selected-default", returned.ActiveWorkflow().Phases[0].(*work.PreparationPhase).ResetTarget.Baseline.Commit)
			}
			durable, err := store.Load("repo", "op-1")
			require.NoError(t, err)
			assert.Equal(t, durable, returned, "no unsaved establishment may escape")

			store.failAt = 0
			// After a durable target exists, later default-branch changes cannot retarget it.
			if failedSave == 2 {
				git.target = "newer-default"
			}
			returned, err = drive(t, runner, newPhase(), durable)
			require.ErrorIs(t, err, backend.failure)
			assert.Nil(t, returned.ActiveWorkflow().ResetRequestedAt, "establishment consumes reset before activation")
			require.NotNil(t, returned.ActiveWorkflow().Context())
			assert.Equal(t, "selected-default", returned.ActiveWorkflow().Context().Baseline.Commit)
			assert.Equal(t, originalHistory, returned.Workflows[0])
			assert.Equal(t, "orpheus/op-1", backend.activations[0].TaskBranch)
			assert.Equal(t, "/fixture/task", backend.activations[0].Directory)
			if failedSave == 2 {
				assert.Equal(t, 1, git.resolutions)
			}
		})
	}
}

func drive(t *testing.T, runner phaseexec.Runner, phase *preparation.Service, current work.State) (work.State, error) {
	t.Helper()
	for range 8 {
		selected, err := phase.Next(current, preparation.Intent{})
		if err != nil {
			return current, err
		}
		if op, ok := selected.Operation(); ok {
			current, err = runner.Execute(context.Background(), current, op)
			if err != nil {
				return current, err
			}
			continue
		}
		_, ok := selected.Outcome()
		require.True(t, ok)
		return current, nil
	}
	t.Fatal("preparation did not reach its boundary")
	return current, nil
}

type resetGit struct {
	preparation.Git
	target       string
	resolutions  int
	applications []gitmeta.WorkflowResetRequest
	beforeApply  func(gitmeta.WorkflowResetRequest)
}

func (*resetGit) InspectWorkflowReset(context.Context, gitmeta.WorkflowResetRequest) (string, error) {
	return "", nil
}
func (g *resetGit) ResolveWorkflowReset(_ context.Context, req gitmeta.WorkflowResetRequest) (gitmeta.Revision, error) {
	g.resolutions++
	return gitmeta.Revision{Commit: g.target, Tree: "selected-tree", RetainedRef: req.Ref}, nil
}
func (g *resetGit) ApplyWorkflowReset(_ context.Context, req gitmeta.WorkflowResetRequest) error {
	g.beforeApply(req)
	g.applications = append(g.applications, req)
	return nil
}

type preparationBackend struct {
	item        task.Task
	failure     error
	activations []work.WorkContext
}

func (b *preparationBackend) Get(context.Context, string) (task.Task, error) { return b.item, nil }
func (b *preparationBackend) MarkInProgress(_ context.Context, _ string, branch, dir string) error {
	b.activations = append(b.activations, work.WorkContext{TaskBranch: branch, Directory: dir})
	return b.failure
}

type preparationStore struct {
	data          []byte
	failAt, saves int
	failure       error
}

func (s *preparationStore) Load(string, string) (work.State, error) { return workstate.Decode(s.data) }
func (s *preparationStore) Save(proposed work.State, expected work.StateVersion) (work.StateVersion, error) {
	s.saves++
	if s.saves == s.failAt {
		return 0, s.failure
	}
	previous, err := workstate.Decode(s.data)
	if err != nil {
		return 0, err
	}
	if previous.Version != expected || proposed.Version != expected {
		return 0, errors.New("unexpected state version")
	}
	if err := proposed.ValidateSuccessor(&previous); err != nil {
		return 0, err
	}
	proposed.Version++
	s.data, err = workstate.Encode(proposed)
	return proposed.Version, err
}
