package review_test

import (
	"context"
	"errors"
	"github.com/hea3ven/orpheus/internal/workfixture"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewerChildPIDPreservesIndependentFinding(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	require.NoError(t, err)
	store := workfixture.NewStore(paths)
	snapshot := testutil.ReviewableWork("review-eval", "eval-1", "/fixture/work", "task-branch")
	_, err = workstate.NewStore(paths).Save(snapshot, 0)
	require.NoError(t, err)
	attempt, err := store.SeedReview("review-eval", "eval-1", review.Reservation{Pipeline: "evaluation", Contract: work.ReviewContract{PipelineKey: "evaluation", Slots: []work.ReviewSlot{{Kind: work.ReviewAgent, ConfigKey: "ai-review"}}}, Scope: work.ReviewScope{Requirements: "Fixture task requirements", Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}, Output: work.OutputRef{Phase: 1}}})
	require.NoError(t, err)
	execution := taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeReview, Status: taskstate.RunStatusRunning, Agent: "reviewer", StartedAt: attempt.StartedAt, SupervisorPID: 10}
	startedReview, err := store.ObserveStep("review-eval", "eval-1", attempt.Attempt, review.StepObservation{Address: attempt.Address, Kind: taskstate.ReviewStepKindAgentReview, Name: "ai-review", Execution: &execution})
	require.NoError(t, err)
	actor := startedReview.Steps[len(startedReview.Steps)-1].Address
	recorder := phaseexec.Runner{Paths: paths, Store: workstate.NewStore(paths), RepositoryID: "review-eval", TaskID: "eval-1"}

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		errs <- review.RecordChildPID(recorder, actor, 4242)
	}()
	go func() {
		defer wait.Done()
		<-start
		errs <- retryFindingMutationLock(paths, func() error {
			_, err := recorder.CommitHeld(context.Background(), review.FindingChange(actor, review.FindingInput{Classification: work.FindingSeparateTask, Title: " immediate ", Description: " finding ", Proposal: &work.FollowUpProposal{Title: " Follow-up ", Description: " Later work ", AcceptanceCriteria: " Required outcome "}}))
			return err
		})
	}()
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	loaded, err := store.Load("review-eval", "eval-1")
	require.NoError(t, err)
	latest, ok := taskstate.LatestReview(loaded)
	require.True(t, ok)
	assert.Equal(t, 4242, latest.Steps[0].Execution.ChildPID)
	require.Len(t, latest.Findings, 1)
	assert.Equal(t, "immediate", latest.Findings[0].Title)
	assert.Equal(t, "finding", latest.Findings[0].Description)
	assert.Equal(t, taskstate.FindingTypeSeparateTask, latest.Findings[0].Type)
	assert.Equal(t, taskstate.ReviewTaskProposal{Title: "Follow-up", Description: "Later work", AcceptanceCriteria: "Required outcome"}, latest.Findings[0].TaskProposal)
	require.NotNil(t, latest.Findings[0].Address)
	assert.Equal(t, work.FindingRef{Phase: 2, Step: 0, Finding: 0}, *latest.Findings[0].Address)
}

func retryFindingMutationLock(paths state.Paths, mutate func() error) error {
	var err error
	for retry := 0; retry < 100; retry++ {
		err = state.WithGlobalMutationLock(paths, "test independent reviewer finding", mutate)
		if err == nil || !errors.Is(err, os.ErrExist) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
	return err
}
