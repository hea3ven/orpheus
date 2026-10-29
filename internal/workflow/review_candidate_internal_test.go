package workflow

import (
	"context"
	"errors"
	"testing"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewCandidateReadinessRequiresExactCapturedRevision(t *testing.T) {
	boundaryError := errors.New("clean checkout is on a different retained commit")
	loadError := errors.New("state unavailable")
	for _, scenario := range []string{"retained committed input", "wrong clean checkout", "missing implementation", "state read failure"} {
		t.Run(scenario, func(t *testing.T) {
			at := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
			zero := 0
			snapshot := work.State{RepositoryID: "alpha", TaskID: "op-review", Workflows: []work.Workflow{{Phases: []work.Phase{
				&work.PreparationPhase{EstablishedAt: &at, Context: &work.WorkContext{Directory: "/fixture/candidate", TaskBranch: "task", CheckpointRef: "refs/orpheus/baseline", Baseline: work.CodeIdentity{Commit: "B", Tree: "BT"}}},
				&work.ImplementationPhase{Execution: work.AgentExecution{Profile: "implementer", Harness: "pi", Command: "pi", StartedAt: at.Add(time.Second), Process: &work.ExitedProcess{Code: &zero, At: at.Add(3 * time.Second)}}, Completion: &work.Completion{Summary: "feat: output", Description: "Completed work", DetailedDescription: "PR body", CompletedAt: at.Add(2 * time.Second)}, Output: &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: "R", Tree: "T"}, RetainedRef: "refs/orpheus/output"}},
			}}}}
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

type candidateReadinessGit struct {
	err      error
	dir      string
	identity gitmeta.CheckoutIdentity
	revision gitmeta.Revision
}

func (g *candidateReadinessGit) VerifyRevision(_ context.Context, dir string, identity gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	g.dir, g.identity, g.revision = dir, identity, revision
	return g.err
}
func (*candidateReadinessGit) ObserveDestination(context.Context, string, string) (work.Destination, error) {
	return work.Destination{}, errors.New("unexpected destination observation")
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
