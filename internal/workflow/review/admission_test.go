package review_test

import (
	"context"
	"errors"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewAdmissionRetainsItsSubjectOnceAndReturnsCommittedState(t *testing.T) {
	for _, failure := range []string{"none", "destination", "save"} {
		t.Run(failure, func(t *testing.T) {
			paths := newTestPaths(t)
			raw := workstate.NewStore(paths)
			initial := testutil.ReviewableWork("repo", "task", "/fixture/work", "task-branch")
			var err error
			initial.Version, err = raw.Save(initial, 0)
			require.NoError(t, err)
			store := &manualSaveStore{Store: raw}
			runner := phaseexec.Runner{Paths: paths, Store: store, RepositoryID: "repo", TaskID: "task"}
			injected := errors.New("admission boundary unavailable")
			git := &admissionDestination{}
			if failure == "destination" {
				git.err = injected
			}
			if failure == "save" {
				store.failure = injected
			}
			phase, intent := newReviewAdmission(t, paths, git)
			current := initial
			var ready review.AdmittedReview
			for {
				selection, selectErr := phase.Next(current, intent)
				require.NoError(t, selectErr)
				if op, ok := selection.Operation(); ok {
					current, err = runner.Execute(context.Background(), current, op)
					if err != nil {
						break
					}
					continue
				}
				outcome, ok := selection.Outcome()
				require.True(t, ok)
				ready, ok = outcome.(review.AdmittedReview)
				require.True(t, ok)
				break
			}
			after, loadErr := raw.Load("repo", "task")
			require.NoError(t, loadErr)
			assert.Equal(t, 1, git.calls, "an error does not retry destination inspection")
			assert.Equal(t, "chosen-base", git.branch)
			assert.Equal(t, "/fixture/work", git.dir)
			assert.Equal(t, gitmeta.CheckoutIdentity{Commit: "fixture-output", Ref: "refs/heads/task-branch"}, git.identity)
			assert.Equal(t, gitmeta.Revision{Commit: "fixture-output", Tree: "fixture-tree", RetainedRef: "refs/orpheus/fixture-output"}, git.revision)
			if failure != "none" {
				require.ErrorIs(t, err, injected)
				assert.Equal(t, initial, current, "selection receives no unsaved reservation")
				assert.Equal(t, initial, after)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 1, store.saves)
			assert.Equal(t, initial.Version+1, current.Version)
			assert.Equal(t, current, after)
			assert.False(t, ready.Execution.Resumed())
			assert.Equal(t, tasktarget.Target{Kind: tasktarget.TargetRepoRootTeam, Branch: "task-branch", Worktree: "/fixture/work"}, ready.Target)
			assert.Equal(t, work.AttemptRef{RepositoryID: "repo", TaskID: "task", Phase: 2}, ready.Review.Address)
			w := current.ActiveWorkflow()
			assert.Equal(t, initial.Workflows[0].Phases, w.Phases[:2])
			require.Len(t, w.ReviewContracts, 1)
			assert.Equal(t, work.ReviewContract{PipelineKey: "quality", Slots: []work.ReviewSlot{{Kind: work.ReviewManual, ConfigKey: "inspect"}, {Kind: work.ReviewCheck, ConfigKey: "check"}}}, w.ReviewContracts[0])
			p := w.LastPhase().(*work.ReviewPhase)
			assert.Equal(t, work.ReviewScope{Requirements: "Task title\n\nRequired outcome\n\nAccepted evidence", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "chosen-base", Commit: "destination"}}, p.Scope)
			assert.Empty(t, p.Steps, "admission does not invent a started slot")
			selection, err := phase.Next(current, intent)
			require.NoError(t, err)
			_, ok := selection.Outcome()
			assert.True(t, ok, "the committed reservation is not reserved again")
			assert.Equal(t, taskstate.ReviewStatusRunning, ready.Review.Status)
		})
	}
}

func TestReviewAdmissionRefusesAnUnownedOrDifferentCheckoutBeforeReservation(t *testing.T) {
	for _, failure := range []string{"checkout", "metadata", "missing output", "missing implementation"} {
		t.Run(failure, func(t *testing.T) {
			paths := newTestPaths(t)
			initial := testutil.ReviewableWork("repo", "task", "/fixture/work", "task-branch")
			git := &admissionDestination{}
			phase, intent := newReviewAdmission(t, paths, git)
			injected := errors.New("clean checkout is on a different retained commit")
			var expected string
			switch failure {
			case "checkout":
				git.verifyErr = injected
			case "metadata":
				phase.Task.Metadata[task.MetadataBranch] = "chosen-base"
				expected = "does not mirror taskstate target"
			case "missing output":
				initial.ActiveWorkflow().LastPhase().(*work.ImplementationPhase).Output = nil
				expected = "captured implementation output"
			case "missing implementation":
				initial.ActiveWorkflow().Phases = initial.ActiveWorkflow().Phases[:1]
				expected = "review advancement is stale"
			}
			raw := workstate.NewStore(paths)
			var err error
			initial.Version, err = raw.Save(initial, 0)
			require.NoError(t, err)
			store := &manualSaveStore{Store: raw}
			runner := phaseexec.Runner{Paths: paths, Store: store, RepositoryID: "repo", TaskID: "task"}
			selected, err := phase.Next(initial, intent)
			require.NoError(t, err)
			op, ok := selected.Operation()
			require.True(t, ok)

			returned, err := runner.Execute(context.Background(), initial, op)

			if failure == "checkout" {
				require.ErrorIs(t, err, injected)
				assert.Equal(t, "/fixture/work", git.dir)
				assert.Equal(t, gitmeta.CheckoutIdentity{Commit: "fixture-output", Ref: "refs/heads/task-branch"}, git.identity)
				assert.Equal(t, gitmeta.Revision{Commit: "fixture-output", Tree: "fixture-tree", RetainedRef: "refs/orpheus/fixture-output"}, git.revision)
			} else {
				require.ErrorContains(t, err, expected)
				assert.Empty(t, git.dir)
			}
			assert.Zero(t, git.calls, "checkout refusal precedes destination observation and Review reservation")
			assert.Zero(t, store.saves)
			assert.Equal(t, initial, returned)
			persisted, err := raw.Load("repo", "task")
			require.NoError(t, err)
			assert.Equal(t, initial, persisted)
		})
	}
}

func newReviewAdmission(t *testing.T, paths state.Paths, git *admissionDestination) (review.Admission, review.AdmissionIntent) {
	t.Helper()
	require.NoError(t, testutil.WriteConfigYAML(paths, review.ConfigFile, map[string]any{"reviews": map[string]any{"pipelines": map[string]any{"quality": map[string]any{"steps": []map[string]any{{"kind": "manual", "name": "inspect"}, {"kind": "check", "name": "check", "command": "fixture-check"}}}}}}))
	item := task.Task{ID: "task", Title: "Task title", Description: "Required outcome", AcceptanceCriteria: "Accepted evidence", Metadata: task.Metadata{task.MetadataBranch: "task-branch", task.MetadataWorktree: "/fixture/work"}}
	return review.Admission{Paths: paths, Repository: task.Repository{ID: "repo", Name: "repo", Path: "/fixture/work", DefaultBranch: "chosen-base"}, Task: item, Git: git}, review.AdmissionIntent{Owner: work.AttemptRef{RepositoryID: "repo", TaskID: "task", Phase: 1}, PipelineName: "quality"}
}

type admissionDestination struct {
	err       error
	calls     int
	branch    string
	verifyErr error
	dir       string
	identity  gitmeta.CheckoutIdentity
	revision  gitmeta.Revision
}

func (g *admissionDestination) SelectReviewRevision(ctx context.Context, req gitmeta.ReviewRevisionRequest) (gitmeta.ReviewRevisionSelection, error) {
	err := g.VerifyRevision(ctx, req.Directory, gitmeta.CheckoutIdentity{Commit: req.Implementation.Commit, Ref: "refs/heads/" + req.Branch}, req.Implementation)
	return gitmeta.ReviewRevisionSelection{Revision: req.Implementation}, err
}

func (g *admissionDestination) VerifyRevision(_ context.Context, dir string, identity gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	g.dir, g.identity, g.revision = dir, identity, revision
	return g.verifyErr
}

func (g *admissionDestination) ObserveDestination(_ context.Context, _ string, branch string) (work.Destination, error) {
	g.calls++
	g.branch = branch
	return work.Destination{Remote: "origin", Branch: branch, Commit: "destination"}, g.err
}
