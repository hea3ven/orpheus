package review

import (
	"context"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This owns the pipeline's ordinary command receipts. Existing CLI journeys own
// check findings, decisions, manual drafts, output, and cross-phase progression.
func TestPipelineCommandReceiptsPrecedeEffectsAndControlContinuation(t *testing.T) {
	for _, failAt := range []int{0, 1, 2} {
		name := map[int]string{0: "committed results continue", 1: "reservation failure", 2: "result failure"}[failAt]
		t.Run(name, func(t *testing.T) {
			paths, initial, pipeline, attempt := commandReceiptFixture(t)
			injected := errors.New("command receipt unavailable")
			store := &commandReceiptStore{Store: workstate.NewStore(paths), failAt: failAt, failure: injected}
			calls := 0
			opts := PipelineRunOptions{
				Runner:  phaseexec.Runner{Paths: paths, Store: store, RepositoryID: "repo", TaskID: "task"},
				Context: context.Background(), RepoID: "repo", TaskID: "task", Attempt: attempt,
				Pipeline: pipeline, Execution: FreshExecution(),
				Effects: Effects{
					CaptureCandidate: func(context.Context, string, CommittedCandidate, *slog.Logger, ...slog.Attr) (CandidateCheck, error) {
						return func() error { return nil }, nil
					},
					RunCommand: func(command CommandOptions) (*int, error) {
						calls++
						observed, err := store.Store.Load("repo", "task")
						require.NoError(t, err)
						p := observed.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
						require.Len(t, p.Steps, calls, "reservation must precede the command")
						assert.Nil(t, p.Steps[calls-1].(*work.CheckStep).Result)
						assert.Equal(t, pipeline.Steps[calls-1].Name, command.Step.Name)
						assert.Equal(t, initial.Version+work.StateVersion(2*calls-1), observed.Version)
						if calls == 2 {
							assert.Equal(t, 0, *p.Steps[0].(*work.CheckStep).Result.ExitCode)
						}
						zero := 0
						return &zero, nil
					},
				},
			}

			phase := newPipelineExecution(opts)
			current := initial
			var outcome PipelineOutcome
			var err error
			for {
				beforeCalls, beforeSaves := calls, store.saves
				selected, selectErr := phase.Next(current)
				require.NoError(t, selectErr)
				assert.Equal(t, beforeCalls, calls, "selection must not execute commands")
				assert.Equal(t, beforeSaves, store.saves, "selection must not save state")
				if op, ok := selected.Operation(); ok {
					current, err = opts.Runner.Execute(opts.Context, current, op)
					reloaded, loadErr := store.Store.Load("repo", "task")
					require.NoError(t, loadErr)
					assert.Equal(t, reloaded, current, "only committed state may feed the next selection")
					if err != nil {
						break
					}
					continue
				}
				var completed bool
				outcome, completed = selected.Outcome()
				require.True(t, completed, "successful checks do not require operator input")
				break
			}

			saved, loadErr := store.Store.Load("repo", "task")
			require.NoError(t, loadErr)
			assert.Equal(t, initial.ActiveWorkflow().Phases[:2], saved.ActiveWorkflow().Phases[:2])
			assert.Equal(t, initial.ActiveWorkflow().ReviewContracts, saved.ActiveWorkflow().ReviewContracts)
			assert.Equal(t, initial.ActiveWorkflow().LastPhase().(*work.ReviewPhase).Scope, saved.ActiveWorkflow().LastPhase().(*work.ReviewPhase).Scope)
			p := saved.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
			if failAt != 0 {
				require.ErrorIs(t, err, injected)
				assert.Empty(t, outcome.Status)
				assert.Equal(t, failAt-1, calls, "no external retry or next command after a failed save")
				assert.Equal(t, initial.Version+work.StateVersion(failAt-1), saved.Version)
				if failAt == 1 {
					assert.Equal(t, initial, saved)
				} else {
					require.Len(t, p.Steps, 1)
					assert.Nil(t, p.Steps[0].(*work.CheckStep).Result)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, taskstate.ReviewStatusPassed, outcome.Status)
			assert.Equal(t, 2, calls)
			assert.Equal(t, initial.Version+4, saved.Version)
			assert.Equal(t, 2, store.loads, "one independent-write adoption per released effect; no post-save reload")
			require.Len(t, p.Steps, 2)
			assert.Equal(t, 0, *p.Steps[1].(*work.CheckStep).Result.ExitCode)
		})
	}
}

type commandReceiptStore struct {
	phaseexec.Store
	failAt  int
	failure error
	saves   int
	loads   int
}

func (s *commandReceiptStore) Load(repoID, taskID string) (work.State, error) {
	s.loads++
	return s.Store.Load(repoID, taskID)
}
func (s *commandReceiptStore) Save(current work.State, version work.StateVersion) (work.StateVersion, error) {
	s.saves++
	if s.saves == s.failAt {
		return 0, s.failure
	}
	return s.Store.Save(current, version)
}

func commandReceiptFixture(t *testing.T) (state.Paths, work.State, Pipeline, taskstate.ReviewAttempt) {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	require.NoError(t, err)
	initial := testutil.ReviewableWork("repo", "task", "/fixture/work", "task-branch")
	w := initial.ActiveWorkflow()
	w.ReviewContracts = []work.ReviewContract{{PipelineKey: "quality", Slots: []work.ReviewSlot{{Kind: work.ReviewCheck, ConfigKey: "first"}, {Kind: work.ReviewCheck, ConfigKey: "later"}}}}
	w.Phases = append(w.Phases, &work.ReviewPhase{Contract: 0, StartedAt: time.Now().UTC(), Scope: work.ReviewScope{Requirements: "Task requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "destination"}}})
	initial.Version, err = workstate.NewStore(paths).Save(initial, 0)
	require.NoError(t, err)
	view, err := taskstate.Project(initial)
	require.NoError(t, err)
	attempt, ok := taskstate.LatestReview(view)
	require.True(t, ok)
	return paths, initial, Pipeline{Name: "quality", Steps: []Step{{Kind: KindCheck, Name: "first"}, {Kind: KindCheck, Name: "later"}}}, attempt
}
