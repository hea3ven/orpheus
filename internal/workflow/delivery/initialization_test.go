package delivery_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/delivery"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Admission is separate from the complete publication journey: it must refuse
// a blocked or replaced review before observing inputs or saving a new phase.
func TestPublicationInitializationRequiresItsPassedReviewAndRetainsUnconsentedSelection(t *testing.T) {
	current, err := testutil.DeliveredPRWork("repo", "op-1", "/fixture/task", "orpheus/op-1", "https://example.test/pr/1")
	require.NoError(t, err)
	current.Workflows[0].Phases = current.Workflows[0].Phases[:3]
	current.Version = 8
	reviewOwner := work.AttemptRef{RepositoryID: "repo", TaskID: "op-1", Phase: 2}
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	require.NoError(t, err)
	original, err := workstate.Encode(current)
	require.NoError(t, err)
	store := &initializationStore{data: original}
	runner := phaseexec.Runner{Paths: paths, Store: store, RepositoryID: "repo", TaskID: "op-1"}
	boundary := &initializationGit{}
	service := &delivery.Service{Paths: paths, Git: boundary, Source: task.RepositorySource{Repository: task.Repository{ID: "repo", DefaultBranch: "main"}}}

	foreign := reviewOwner
	foreign.Workflow = 1
	selected, err := service.Next(current, delivery.Intent{Review: &foreign})
	require.NoError(t, err)
	op, ok := selected.Operation()
	require.True(t, ok)
	_, err = runner.Execute(context.Background(), current, op)
	require.ErrorIs(t, err, phaseexec.ErrStaleInput)
	assert.Zero(t, boundary.observed)
	assert.Equal(t, original, store.data)

	blocked, err := workstate.Decode(original)
	require.NoError(t, err)
	agentStep := blocked.Workflows[0].Phases[2].(*work.ReviewPhase).Steps[0].(*work.AgentReviewStep)
	agentStep.Report = &work.ReviewReport{Findings: []work.Finding{{Classification: work.FindingBlocking, Title: "Unresolved issue", Description: "Requires repair", Dispositions: []work.FindingDisposition{&work.RepairRequired{At: agentStep.Execution.StartedAt}}}}}
	selected, err = service.Next(blocked, delivery.Intent{Review: &reviewOwner})
	require.NoError(t, err)
	op, ok = selected.Operation()
	require.True(t, ok)
	_, err = runner.Execute(context.Background(), blocked, op)
	require.ErrorContains(t, err, "complete passed review")
	assert.Zero(t, boundary.observed)
	assert.Equal(t, original, store.data)

	selected, err = service.Next(current, delivery.Intent{Review: &reviewOwner})
	require.NoError(t, err)
	op, ok = selected.Operation()
	require.True(t, ok)
	failedSave := errors.New("initial publication receipt unavailable")
	store.failure = failedSave
	returned, err := runner.Execute(context.Background(), current, op)
	require.ErrorIs(t, err, failedSave)
	assert.Equal(t, current, returned, "uncommitted phase cannot become the next selection")
	assert.Equal(t, original, store.data)

	store.failure = nil
	committed, err := runner.Execute(context.Background(), current, op)
	require.NoError(t, err)
	require.Equal(t, work.StateVersion(9), committed.Version)
	assert.Equal(t, current.Workflows[0].Phases, committed.Workflows[0].Phases[:3], "initialization leaves review and implementation history unchanged")
	p := committed.Workflows[0].Phases[3].(*work.PublicationPhase)
	assert.Equal(t, work.ReviewRef{Phase: 2}, p.Review)
	assert.IsType(t, &work.PullRequestTarget{}, p.Target)
	assert.Nil(t, p.Consent)
	assert.Nil(t, p.Delivery)
	assert.Equal(t, "fixture-output", p.Input.Code.Commit)
	assert.Equal(t, "main", p.Destination.Branch)
	assert.Equal(t, "destination", p.Destination.Commit)
	selected, err = service.Next(committed, delivery.Intent{})
	require.NoError(t, err)
	request, ok := selected.Input()
	require.True(t, ok)
	consent := request.(delivery.ConsentRequest)
	assert.Equal(t, work.StateVersion(9), consent.ID.Version)
	assert.Equal(t, 3, consent.ID.Owner.Phase)
	assert.Zero(t, store.reads, "the runner's saved state is used directly for the next selection")
}

type initializationGit struct {
	delivery.Git
	observed int
}

func (g *initializationGit) ObservePublicationInput(_ context.Context, request gitmeta.PublicationInputRequest) (gitmeta.PublicationInputObservation, error) {
	g.observed++
	return gitmeta.PublicationInputObservation{Revision: request.Reviewed, Relationship: "reviewed"}, nil
}
func (*initializationGit) ObserveDestination(context.Context, string, string) (work.Destination, error) {
	return work.Destination{Remote: "origin", Branch: "main", Commit: "destination"}, nil
}

type initializationStore struct {
	data    []byte
	failure error
	reads   int
}

func (s *initializationStore) Load(string, string) (work.State, error) {
	s.reads++
	return workstate.Decode(s.data)
}
func (s *initializationStore) Save(proposed work.State, expected work.StateVersion) (work.StateVersion, error) {
	if s.failure != nil {
		return 0, s.failure
	}
	previous, err := workstate.Decode(s.data)
	if err != nil {
		return 0, err
	}
	if previous.Version != expected || proposed.Version != expected {
		return 0, errors.New("wrong saved-state version")
	}
	if err := proposed.ValidateSuccessor(&previous); err != nil {
		return 0, err
	}
	proposed.Version++
	s.data, err = workstate.Encode(proposed)
	return proposed.Version, err
}
