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
	indexErr := errors.New("index unavailable")
	changeErr := errors.New("candidate unavailable")
	loadErr := errors.New("state unavailable")
	for _, tt := range []struct {
		name             string
		staged           bool
		indexErr         error
		statusErr        error
		hasChanges       bool
		changeErr        error
		commit           string
		loadErr          error
		wantErr          string
		wantCause        error
		wantObservations []string
		wantReads        int
	}{
		{
			name:   "staged index rejects before candidate and state reads",
			staged: true, hasChanges: true, changeErr: changeErr, commit: "reviewed-commit", loadErr: loadErr,
			wantErr: "review requires a clean Git index", wantObservations: []string{"index", "status"},
		},
		{
			name:   "status failure cannot bypass staged rejection",
			staged: true, statusErr: errors.New("status unavailable"),
			wantErr: "review requires a clean Git index", wantObservations: []string{"index", "status"},
		},
		{
			name:   "index observation failure",
			staged: true, indexErr: indexErr, wantErr: "index unavailable", wantCause: indexErr,
			wantObservations: []string{"index"},
		},
		{
			name:       "uncommitted candidate needs no task-state read",
			hasChanges: true, loadErr: loadErr, wantObservations: []string{"index", "candidate"},
		},
		{
			name:             "missing changes and finalization rejects",
			wantErr:          "has no candidate changes to review and task has no recorded finalization commit",
			wantObservations: []string{"index", "candidate"}, wantReads: 1,
		},
		{
			name: "blank finalization commit does not admit unchanged candidate", commit: " \n\t ",
			wantErr:          "has no candidate changes to review and task has no recorded finalization commit",
			wantObservations: []string{"index", "candidate"}, wantReads: 1,
		},
		{
			name: "recorded finalization permits unchanged candidate", commit: "reviewed-commit",
			wantObservations: []string{"index", "candidate"}, wantReads: 1,
		},
		{
			name:       "candidate observation failure precedes recorded finalization",
			hasChanges: true, changeErr: changeErr, commit: "reviewed-commit", wantErr: "candidate unavailable", wantCause: changeErr,
			wantObservations: []string{"index", "candidate"},
		},
		{
			name: "required task-state read failure", loadErr: loadErr,
			wantErr: "load task state: state unavailable", wantCause: loadErr,
			wantObservations: []string{"index", "candidate"}, wantReads: 1,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &candidateReadinessStore{state: taskstate.TaskState{Finalization: &taskstate.Finalization{Commit: tt.commit}}, err: tt.loadErr}
			reviewCtx := ReviewContext{Source: task.RepositorySource{Repository: task.Repository{ID: "alpha"}}, Task: task.Task{ID: "op-review"}}
			git := &candidateReadinessGit{t: t, staged: tt.staged, indexErr: tt.indexErr, statusErr: tt.statusErr, hasChanges: tt.hasChanges, changeErr: tt.changeErr}
			service := ReviewLifecycleService{RunStore: store, CandidateGit: git}

			err := service.inspectReviewCandidate(context.Background(), reviewCtx, "/fixture/candidate")

			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tt.wantErr)
			}
			if tt.wantCause != nil {
				assert.ErrorIs(t, err, tt.wantCause)
			}
			if tt.staged && tt.indexErr == nil {
				assert.ErrorContains(t, err, "unstage them, then rerun `orpheus task run <task-id>`")
				if tt.statusErr != nil {
					assert.ErrorContains(t, err, "unable to read git status: status unavailable")
				} else {
					assert.ErrorContains(t, err, "A  reviewed.txt")
				}
			}
			assert.Equal(t, tt.wantObservations, git.observations)
			assert.Equal(t, tt.wantReads, store.reads)
			if tt.wantReads > 0 {
				assert.Equal(t, []string{"alpha", "op-review"}, store.target)
			}
		})
	}
}

type candidateReadinessGit struct {
	t            *testing.T
	staged       bool
	indexErr     error
	statusErr    error
	hasChanges   bool
	changeErr    error
	observations []string
}

func (g *candidateReadinessGit) HasStagedChanges(_ context.Context, dir string) (bool, error) {
	assert.Equal(g.t, "/fixture/candidate", dir)
	g.observations = append(g.observations, "index")
	return g.staged, g.indexErr
}

func (g *candidateReadinessGit) HasCandidateChanges(_ context.Context, dir string) (bool, error) {
	assert.Equal(g.t, "/fixture/candidate", dir)
	g.observations = append(g.observations, "candidate")
	return g.hasChanges, g.changeErr
}

func (g *candidateReadinessGit) ShortStatus(_ context.Context, dir string) (string, error) {
	assert.Equal(g.t, "/fixture/candidate", dir)
	g.observations = append(g.observations, "status")
	return "A  reviewed.txt\n", g.statusErr
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
