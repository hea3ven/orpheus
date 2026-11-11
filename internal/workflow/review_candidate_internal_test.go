package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewCandidateReadinessRequiresExactCapturedRevision(t *testing.T) {
	boundaryError := errors.New("clean checkout is on a different retained commit")
	loadError := errors.New("state unavailable")
	for _, scenario := range []string{"retained committed input", "wrong clean checkout", "missing implementation", "missing output", "state read failure"} {
		t.Run(scenario, func(t *testing.T) {
			at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
			zero := 0
			snapshot := work.State{RepositoryID: "alpha", TaskID: "op-review", Workflows: []work.Workflow{{Phases: []work.Phase{
				&work.PreparationPhase{EstablishedAt: &at, Context: &work.WorkContext{Directory: "/fixture/candidate", TaskBranch: "task", CheckpointRef: "refs/orpheus/baseline", Baseline: work.CodeIdentity{Commit: "B", Tree: "BT"}}},
				&work.ImplementationPhase{Execution: work.AgentExecution{Profile: "implementer", Harness: "pi", Command: "pi", StartedAt: at.Add(time.Second), Process: &work.ExitedProcess{Code: &zero, At: at.Add(3 * time.Second)}}, Completion: &work.Completion{CommitSubject: "feat: output", CommitBody: "Completed work", CompletedAt: at.Add(2 * time.Second)}, Output: &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: "R", Tree: "T"}, RetainedRef: "refs/orpheus/output"}},
			}}}}
			if scenario == "missing output" {
				snapshot.Workflows[0].Phases[1].(*work.ImplementationPhase).Output = nil
			}
			if scenario == "missing implementation" {
				snapshot.Workflows[0].Phases = snapshot.Workflows[0].Phases[:1]
			}
			view, err := taskstate.Project(snapshot)
			require.NoError(t, err)
			store := &candidateReadinessStore{state: view}
			boundary := &candidateReadinessGit{}
			if scenario == "wrong clean checkout" {
				boundary.err = boundaryError
			}
			if scenario == "state read failure" {
				store.err = loadError
			}
			service := ReviewLifecycleService{RunStore: store, CandidateGit: boundary}
			base := ReviewContext{Source: task.RepositorySource{Repository: task.Repository{ID: "alpha"}}, Task: task.Task{ID: "op-review"}}

			err = service.inspectReviewCandidate(context.Background(), base, "/fixture/candidate")

			switch scenario {
			case "retained committed input":
				require.NoError(t, err)
			case "wrong clean checkout":
				require.ErrorIs(t, err, boundaryError)
			case "state read failure":
				require.ErrorIs(t, err, loadError)
			case "missing output":
				require.ErrorContains(t, err, "review requires recorded implementation output")
				assert.Nil(t, snapshot.Workflows[0].Phases[1].(*work.ImplementationPhase).Output)
			default:
				require.ErrorContains(t, err, "workflow-owned implementation")
			}
			if scenario == "retained committed input" || scenario == "wrong clean checkout" {
				assert.Equal(t, "/fixture/candidate", boundary.dir)
				assert.Equal(t, gitmeta.CheckoutIdentity{Commit: "R", Ref: "refs/heads/task"}, boundary.identity)
				assert.Equal(t, gitmeta.Revision{Commit: "R", Tree: "T", RetainedRef: "refs/orpheus/output"}, boundary.revision)
			} else {
				assert.Empty(t, boundary.dir)
			}
			assert.Equal(t, 1, store.reads)
		})
	}
}

func TestManualReviewContextComparesRecordedBaselineWithScopedOutput(t *testing.T) {
	statError := errors.New("retained commit unavailable")
	for _, fail := range []bool{false, true} {
		name := "retained scope despite later output"
		if fail {
			name = "Git failure is not an empty diff"
		}
		t.Run(name, func(t *testing.T) {
			snapshot := testutil.ReviewableWork("alpha", "op-review", "/fixture/candidate", "task")
			w := &snapshot.Workflows[0]
			w.ReviewContracts = []work.ReviewContract{{PipelineKey: "manual", Slots: []work.ReviewSlot{{Kind: work.ReviewManual, ConfigKey: "inspect"}}}}
			w.Phases = append(w.Phases, &work.ReviewPhase{
				Scope:     work.ReviewScope{Requirements: "Task requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "different-destination"}},
				StartedAt: w.Phases[1].(*work.ImplementationPhase).Execution.StartedAt.Add(time.Minute),
			})
			later := *w.Phases[1].(*work.ImplementationPhase)
			later.Output = &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: "later-output", Tree: "later-tree"}, RetainedRef: "refs/orpheus/later"}
			w.Phases = append(w.Phases, &later)
			view, err := taskstate.Project(snapshot)
			require.NoError(t, err)
			store := &candidateReadinessStore{state: view}
			boundary := &candidateReadinessGit{stat: " reviewed.txt | 1 +\n 1 file changed, 1 insertion(+)\n"}
			if fail {
				boundary.err = statError
			}
			service := ReviewLifecycleService{CandidateGit: boundary, ReviewStatus: func(context.Context, string) (string, error) { return "", nil }}
			ctx := ReviewAttemptContext{ReviewContext: ReviewContext{store: store, Source: task.RepositorySource{Repository: task.Repository{ID: "alpha"}}, Task: task.Task{ID: "op-review"}, Workdir: "/fixture/candidate"}, Review: taskstate.ReviewAttempt{Address: work.AttemptRef{Workflow: 0, Phase: 2}}}

			got, err := service.manualStepContext(context.Background(), ctx, review.Step{Name: "inspect"})

			assert.Equal(t, "/fixture/candidate", boundary.dir)
			assert.Equal(t, "initial-commit", boundary.baseline)
			assert.Equal(t, "fixture-output", boundary.comparedRevision)
			if fail {
				require.ErrorIs(t, err, statError)
				assert.Empty(t, got.GitDiffStat)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "initial-commit", got.BaselineCommit)
			assert.Equal(t, "fixture-output", got.RevisionCommit)
			assert.Equal(t, boundary.stat, got.GitDiffStat)
			assert.Empty(t, got.GitStatus)
		})
	}
}

type candidateReadinessGit struct {
	stat, baseline, comparedRevision string
	err                              error
	dir                              string
	identity                         gitmeta.CheckoutIdentity
	revision                         gitmeta.Revision
}

func (g *candidateReadinessGit) VerifyRevision(_ context.Context, dir string, identity gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	g.dir, g.identity, g.revision = dir, identity, revision
	return g.err
}
func (g *candidateReadinessGit) DiffStat(_ context.Context, dir, baseline, revision string) (string, error) {
	g.dir, g.baseline, g.comparedRevision = dir, baseline, revision
	return g.stat, g.err
}

func (*candidateReadinessGit) ObserveDestination(_ context.Context, _, branch string) (work.Destination, error) {
	return work.Destination{Remote: "origin", Branch: branch, Commit: "initial-commit"}, nil
}

type candidateReadinessStore struct {
	ReviewLifecycleStore
	state taskstate.TaskState
	err   error
	reads int
}

func (s *candidateReadinessStore) Load(string, string) (taskstate.TaskState, error) {
	s.reads++
	return s.state, s.err
}
