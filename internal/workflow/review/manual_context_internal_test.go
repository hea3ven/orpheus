package review

import (
	"context"
	"errors"
	"testing"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
			boundary := &candidateReadinessGit{stat: " reviewed.txt | 1 +\n 1 file changed, 1 insertion(+)\n"}
			if fail {
				boundary.err = statError
			}
			opts := PipelineRunOptions{Source: task.RepositorySource{Repository: task.Repository{ID: "alpha"}}, Task: task.Task{ID: "op-review"}, Workdir: "/fixture/candidate", Attempt: taskstate.ReviewAttempt{Address: work.AttemptRef{Workflow: 0, Phase: 2}}, CandidateGit: boundary, ReadStatus: func(context.Context, string) (string, error) { return "", nil }}

			got, err := manualStepContext(context.Background(), snapshot, opts, Step{Name: "inspect"})

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
