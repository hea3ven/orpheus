package review_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManualSubmissionCommitsEditsPromotionAndAssessmentTogether(t *testing.T) {
	for _, failure := range []string{"none", "save", "invalid decision"} {
		t.Run(failure, func(t *testing.T) {
			paths, current, owner := manualSubmissionFixture(t)
			savedStore := &manualSaveStore{Store: workstate.NewStore(paths)}
			runner := phaseexec.Runner{Paths: paths, Store: savedStore, RepositoryID: "repo", TaskID: "task"}
			refused, err := runner.Commit(context.Background(), review.FindingChange(owner, review.FindingInput{Classification: work.FindingAdvisory, Title: "Unsubmitted edit", Description: "Cannot bypass the assessment"}))
			require.Error(t, err)
			assert.Equal(t, current, refused)
			assert.Zero(t, savedStore.saves, "the immediate agent endpoint cannot save manual draft edits")
			projected, err := taskstate.Project(current)
			require.NoError(t, err)
			latest, ok := taskstate.LatestReview(projected)
			require.True(t, ok)
			prior := *latest.Findings[0].Address
			retained := *latest.Findings[1].Address
			id := phaseexec.InputID{Owner: owner, Version: current.Version, Kind: review.ManualInputKind}
			draft := review.NewManualDraft(review.ManualRequest{ID: id, Review: latest, Step: "assessment"})
			require.NoError(t, draft.Add(review.FindingInput{Classification: work.FindingBlocking, Title: " New blocker ", Description: " Required correction "}))
			require.NoError(t, draft.Promote(prior))
			assert.True(t, draft.View().HasOpenBlockers)
			assert.Nil(t, draft.View().Findings[2].Address, "new edits have no durable address")
			assert.Equal(t, taskstate.FindingTypeAdvisory, latest.Findings[0].Type, "promotion is only a preview")
			view := draft.View()
			view.Findings[0].Address.Finding = 99
			view.Findings[2].Title = "caller mutation"
			assert.Equal(t, prior, *draft.View().Findings[0].Address)
			assert.Equal(t, "New blocker", draft.View().Findings[2].Title)
			_, err = draft.Submit(review.ManualApprove)
			require.Error(t, err, "a draft blocker prevents approval before submission")
			answer, err := draft.Submit(review.ManualRequestChanges)
			require.NoError(t, err)
			assert.Equal(t, []work.FindingRef{prior}, answer.Promote)
			assert.Zero(t, savedStore.saves, "editing and preview do not write")
			injected := errors.New("receipt failed")
			if failure == "save" {
				savedStore.failure = injected
			}
			if failure == "invalid decision" {
				answer.Decision = review.ManualApprove
			}
			service := review.Assessments{Paths: paths, Repository: task.Repository{ID: "repo"}, Git: manualRevisionVerifier{}}
			op, err := service.Respond(current, id, answer)
			require.NoError(t, err)
			saved, err := runner.Execute(context.Background(), current, op)
			after, loadErr := savedStore.Load("repo", "task")
			require.NoError(t, loadErr)
			if failure != "none" {
				require.Error(t, err)
				if failure == "save" {
					require.ErrorIs(t, err, injected)
					assert.Equal(t, 1, savedStore.saves)
				} else {
					assert.Zero(t, savedStore.saves)
				}
				assert.Equal(t, current, saved)
				assert.Equal(t, current, after, "no part of a refused submission may persist")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, 1, savedStore.saves, "edits and final decision share one save")
			assert.Equal(t, current.Version+1, saved.Version)
			assert.Equal(t, saved, after)
			require.IsType(t, &work.ReviewPhase{}, saved.ActiveWorkflow().LastPhase())
			phase := saved.ActiveWorkflow().LastPhase().(*work.ReviewPhase)
			manual := phase.Steps[1].(*work.ManualReviewStep)
			require.Len(t, manual.Findings, 2)
			assert.Equal(t, "Persisted manual note", manual.Findings[0].Title)
			assert.Equal(t, "New blocker", manual.Findings[1].Title)
			require.IsType(t, &work.RequestedChanges{}, manual.Decision)
			assert.Equal(t, []work.FindingRef{prior}, manual.Decision.(*work.RequestedChanges).Targets)
			viewAfter, err := taskstate.Project(saved)
			require.NoError(t, err)
			completed, _ := taskstate.LatestReview(viewAfter)
			assert.Equal(t, prior, *completed.Findings[0].Address)
			assert.Equal(t, retained, *completed.Findings[1].Address)
			assert.Equal(t, taskstate.FindingTypeBlocking, completed.Findings[0].Type)
			_, err = service.Respond(saved, id, answer)
			assert.ErrorIs(t, err, phaseexec.ErrStaleInput)
		})
	}
}

type manualSaveStore struct {
	phaseexec.Store
	failure error
	failAt  int
	saves   int
}

func (s *manualSaveStore) Save(current work.State, version work.StateVersion) (work.StateVersion, error) {
	s.saves++
	if s.failure != nil && (s.failAt == 0 || s.saves == s.failAt) {
		return 0, s.failure
	}
	return s.Store.Save(current, version)
}

type manualRevisionVerifier struct{}

func (manualRevisionVerifier) VerifyRevision(context.Context, string, gitmeta.CheckoutIdentity, gitmeta.Revision) error {
	return nil
}

func manualSubmissionFixture(t *testing.T) (state.Paths, work.State, work.AttemptRef) {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	config := filepath.Join(root, "config", state.AppName)
	paths, err := state.NewPaths(config, filepath.Join(root, "data"))
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(config, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(config, "config.yaml"), []byte("reviews:\n  pipelines:\n    quality:\n      steps:\n        - {kind: manual, name: prior}\n        - {kind: manual, name: assessment}\n"), 0o600))
	current := testutil.ReviewableWork("repo", "task", "/fixture/work", "task-branch")
	w := current.ActiveWorkflow()
	w.ReviewContracts = []work.ReviewContract{{PipelineKey: "quality", Slots: []work.ReviewSlot{{Kind: work.ReviewManual, ConfigKey: "prior"}, {Kind: work.ReviewManual, ConfigKey: "assessment"}}}}
	at := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
	w.Phases = append(w.Phases, &work.ReviewPhase{StartedAt: at, Contract: 0, Scope: work.ReviewScope{Requirements: "Task requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "destination"}}, Steps: []work.ReviewStep{
		&work.ManualReviewStep{Slot: 0, Decision: &work.ApprovedReview{At: at}, Findings: []work.Finding{{Classification: work.FindingAdvisory, Title: "Prior advisory", Description: "Earlier assessment finding"}}},
		&work.ManualReviewStep{Slot: 1, Findings: []work.Finding{{Classification: work.FindingAdvisory, Title: "Persisted manual note", Description: "Existing saved operator data"}}},
	}})
	current.Version, err = workstate.NewStore(paths).Save(current, 0)
	require.NoError(t, err)
	step := 1
	owner := work.AttemptRef{RepositoryID: "repo", TaskID: "task", Workflow: 0, Phase: 2, Step: &step}
	return paths, current, owner
}
