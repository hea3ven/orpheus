package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewCandidateReadinessRequiresCleanIndexAndChangesOrFinalization(t *testing.T) {
	for _, tt := range []struct {
		name       string
		indexErr   error
		hasChanges bool
		changeErr  error
		commit     string
		loadErr    error
		wantErr    string
		wantReads  int
	}{
		{name: "staged index rejects before candidate and state reads", indexErr: errors.New("review requires a clean Git index"), wantErr: "review requires a clean Git index"},
		{name: "uncommitted candidate needs no finalization", hasChanges: true},
		{name: "missing changes and finalization rejects", wantErr: "has no candidate changes to review and task has no recorded finalization commit", wantReads: 1},
		{name: "recorded finalization permits clean candidate", commit: "reviewed-commit", wantReads: 1},
		{name: "candidate observation failure", changeErr: errors.New("candidate unavailable"), wantErr: "candidate unavailable"},
		{name: "state load failure", loadErr: errors.New("state unavailable"), wantErr: "load task state: state unavailable", wantReads: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &candidateReadinessStore{state: taskstate.TaskState{Finalization: &taskstate.Finalization{Commit: tt.commit}}, err: tt.loadErr}
			reviewCtx := ReviewAttemptContext{Source: task.RepositorySource{Repository: task.Repository{ID: "alpha"}}, Task: task.Task{ID: "op-review"}}
			var observations []string
			err := validateReviewCandidateReady(context.Background(), store, reviewCtx, "/fixture/candidate",
				func(_ context.Context, dir string) error {
					assert.Equal(t, "/fixture/candidate", dir)
					observations = append(observations, "index")
					return tt.indexErr
				},
				func(_ context.Context, dir string) (bool, error) {
					assert.Equal(t, "/fixture/candidate", dir)
					observations = append(observations, "candidate")
					return tt.hasChanges, tt.changeErr
				},
			)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
			wantObservations := []string{"index"}
			if tt.indexErr == nil {
				wantObservations = append(wantObservations, "candidate")
			}
			assert.Equal(t, wantObservations, observations)
			assert.Equal(t, tt.wantReads, store.reads)
			if tt.wantReads > 0 {
				assert.Equal(t, []string{"alpha", "op-review"}, store.target)
			}
		})
	}
}

type candidateReadinessStore struct {
	ReviewLifecycleStore
	state  taskstate.TaskState
	err    error
	reads  int
	target []string
}

func (s *candidateReadinessStore) Load(repoID, taskID string) (taskstate.TaskState, error) {
	s.reads++
	s.target = []string{repoID, taskID}
	return s.state, s.err
}
