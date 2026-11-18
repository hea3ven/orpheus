package review_test

import (
	"context"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewContinuationReceiptFailurePreservesTheStartedContractAndAssessment(t *testing.T) {
	paths, initial, owner := manualSubmissionFixture(t)
	require.NoError(t, testutil.WriteConfigYAML(paths, review.ConfigFile, map[string]any{"reviews": map[string]any{"pipelines": map[string]any{
		"quality": map[string]any{"steps": []map[string]any{
			{"kind": "agent_review", "name": "changed-earlier", "agent": "missing-earlier-profile"},
			{"kind": "manual", "name": "assessment"},
			{"kind": "manual", "name": "new-later"},
		}},
	}}}))
	injected := errors.New("contract receipt unavailable")
	store := &manualSaveStore{Store: workstate.NewStore(paths), failure: injected}
	runner := phaseexec.Runner{Paths: paths, Store: store, RepositoryID: "repo", TaskID: "task"}
	phase := review.Continuation{Paths: paths, Repository: task.Repository{ID: "repo", ReviewPipeline: "changed-default"}}
	current := initial
	for {
		selected, err := phase.Next(current, owner)
		require.NoError(t, err)
		op, ok := selected.Operation()
		require.True(t, ok)
		current, err = runner.Execute(context.Background(), current, op)
		if err != nil {
			require.ErrorIs(t, err, injected)
			break
		}
	}
	assert.Equal(t, 1, store.saves)
	assert.Equal(t, initial, current)
	afterFailure, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, initial, afterFailure, "no partial forward contract or assessment edits survive a failed save")

	store.failure = nil
	phase = review.Continuation{Paths: paths, Repository: task.Repository{ID: "repo"}}
	for {
		selected, err := phase.Next(current, owner)
		require.NoError(t, err)
		if op, ok := selected.Operation(); ok {
			current, err = runner.Execute(context.Background(), current, op)
			require.NoError(t, err)
			continue
		}
		_, ok := selected.Outcome()
		require.True(t, ok)
		break
	}
	assert.Equal(t, 2, store.saves, "retry records only the new forward contract")
	assert.Equal(t, initial.Version+1, current.Version)
	w := current.ActiveWorkflow()
	require.Len(t, w.ReviewContracts, 2)
	assert.Equal(t, initial.ActiveWorkflow().ReviewContracts[0], w.ReviewContracts[0])
	assert.Equal(t, work.ReviewContract{PipelineKey: "quality", Slots: []work.ReviewSlot{{Kind: work.ReviewManual, ConfigKey: "prior"}, {Kind: work.ReviewManual, ConfigKey: "assessment"}, {Kind: work.ReviewManual, ConfigKey: "new-later"}}}, w.ReviewContracts[1])
	p := w.LastPhase().(*work.ReviewPhase)
	prior := initial.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
	assert.Equal(t, 1, p.Contract)
	assert.Equal(t, prior.Scope, p.Scope)
	assert.Equal(t, prior.StartedAt, p.StartedAt)
	assert.Equal(t, prior.Steps, p.Steps)
	after, err := store.Load("repo", "task")
	require.NoError(t, err)
	assert.Equal(t, current, after)
}
