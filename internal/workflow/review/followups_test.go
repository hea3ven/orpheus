package review_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectSeparateTaskCandidatesParsesSelection(t *testing.T) {
	t.Parallel()

	candidates := []review.SeparateTaskCandidate{{Index: 2}, {Index: 4}, {Index: 8}}
	selected, err := review.SelectSeparateTaskCandidates(candidates, "3, 1, 3")
	if err != nil {
		t.Fatalf("select candidates: %v", err)
	}
	if len(selected) != 2 || selected[0].Index != 8 || selected[1].Index != 2 {
		t.Fatalf("selected = %#v, want candidate indexes 8 and 2", selected)
	}
}

func TestFollowUpReceiptsPrecedePresentationAndStopOnFailure(t *testing.T) {
	for _, failure := range []string{"none", "receipt", "presentation"} {
		t.Run(failure, func(t *testing.T) {
			paths := newTestPaths(t)
			raw := workstate.NewStore(paths)
			initial := testutil.ReviewableWork("repo", "task", "/fixture/work", "task-branch")
			w := initial.ActiveWorkflow()
			at := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
			w.ReviewContracts = []work.ReviewContract{{PipelineKey: "manual", Slots: []work.ReviewSlot{{Kind: work.ReviewManual, ConfigKey: "assessment"}}}}
			w.Phases = append(w.Phases, &work.ReviewPhase{StartedAt: at, Scope: work.ReviewScope{Requirements: "Retained requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "destination"}}, Steps: []work.ReviewStep{&work.ManualReviewStep{Slot: 0, Decision: &work.ApprovedReview{At: at}, Findings: []work.Finding{
				{Classification: work.FindingSeparateTask, Title: "Earlier proposal", Description: "Previously submitted", Proposal: &work.FollowUpProposal{Title: "Earlier native task", Description: "Earlier description", AcceptanceCriteria: "Earlier criteria"}, CreatedTask: &work.CreatedFollowUp{TaskID: "existing", ObservedAt: at}},
				{Classification: work.FindingAdvisory, Title: "Not a task", Description: "Retained advisory"},
				{Classification: work.FindingSeparateTask, Title: "First proposal", Description: "First evidence", Proposal: &work.FollowUpProposal{Title: "First native task", Description: " First authored description ", AcceptanceCriteria: "First criteria"}},
				{Classification: work.FindingSeparateTask, Title: "Second proposal", Description: "Second evidence", Proposal: &work.FollowUpProposal{Title: "Second native task", Description: "Second authored description", AcceptanceCriteria: "Second criteria"}},
			}}}})
			var err error
			initial.Version, err = raw.Save(initial, 0)
			require.NoError(t, err)
			owner := work.AttemptRef{RepositoryID: "repo", TaskID: "task", Phase: 2}
			store := &manualSaveStore{Store: raw}
			runner := phaseexec.Runner{Paths: paths, Store: store, RepositoryID: "repo", TaskID: "task"}
			injected := errors.New("follow-up boundary failed")
			if failure == "receipt" {
				store.failure = injected
			}
			backend := &followUpBackend{}
			var reported []string
			phase := review.FollowUps{
				Backend: func() (task.CreateMutator, error) { return backend, nil },
				Created: func(candidate review.SeparateTaskCandidate, created task.Task) error {
					saved, err := raw.Load("repo", "task")
					require.NoError(t, err)
					finding, err := saved.ActiveWorkflow().Finding(*candidate.Finding.Address)
					require.NoError(t, err)
					require.NotNil(t, finding.CreatedTask, "presentation follows a durable native receipt")
					assert.Equal(t, created.ID, finding.CreatedTask.TaskID)
					if failure == "presentation" {
						return injected
					}
					reported = append(reported, created.ID)
					return nil
				},
			}
			current := initial
			for turns := 0; ; turns++ {
				require.Less(t, turns, 12, "phase failed to stop")
				selection, selectErr := phase.Next(current, owner)
				require.NoError(t, selectErr)
				if _, ok := selection.Outcome(); ok {
					break
				}
				op, _ := selection.Operation()
				if request, ok := selection.Input(); ok {
					input, ok := request.(review.FollowUpSelection)
					require.True(t, ok)
					require.Len(t, input.Candidates, 2, "existing receipt and advisory are not offered")
					assert.Equal(t, []int{2, 3}, []int{input.Candidates[0].Index, input.Candidates[1].Index})
					op, err = phase.Respond(current, input.ID, review.SelectFollowUps{Candidates: []review.SeparateTaskCandidate{input.Candidates[1], input.Candidates[0]}})
					require.NoError(t, err)
				}
				current, err = runner.Execute(context.Background(), current, op)
				if err != nil {
					break
				}
			}
			after, loadErr := raw.Load("repo", "task")
			require.NoError(t, loadErr)
			assert.Equal(t, current, after)
			if failure != "none" {
				require.ErrorIs(t, err, injected)
				require.Len(t, backend.requests, 1, "failure does not retry creation or select the next proposal")
				assert.Empty(t, reported)
			} else {
				require.NoError(t, err)
				require.Len(t, backend.requests, 2)
				assert.Equal(t, []string{"created-1", "created-2"}, reported)
				assert.Equal(t, initial.Version+2, current.Version)
				assert.Equal(t, "First native task", backend.requests[1].Title)
			}
			switch failure {
			case "receipt":
				assert.Equal(t, initial, current, "failed receipt does not leak an unsaved native identity")
				_, err := phase.Next(current, owner)
				assert.ErrorContains(t, err, "receipt was not committed")
			case "presentation":
				assert.ErrorIs(t, err, review.ErrPresentationUnavailable)
				assert.Equal(t, initial.Version+1, current.Version)
			}
			assert.Equal(t, task.CreateOptions{Title: "Second native task", Description: "Second authored description\n\nProvenance:\nDiscovered during review of task in repository repo (review attempt 1, finding 4). Review step: assessment.", AcceptanceCriteria: "Second criteria", IssueType: task.IssueTypeTask}, backend.requests[0])
			old, err := after.ActiveWorkflow().Finding(work.FindingRef{Phase: 2, Step: 0, Finding: 0})
			require.NoError(t, err)
			assert.Equal(t, &work.CreatedFollowUp{TaskID: "existing", ObservedAt: at}, old.CreatedTask)
			assert.Equal(t, initial.Workflows[0].Phases[:2], after.Workflows[0].Phases[:2])
			assert.Len(t, after.ActiveWorkflow().Phases, 3, "task creation cannot enter Publication")
		})
	}
}

type followUpBackend struct{ requests []task.CreateOptions }

func (b *followUpBackend) Create(_ context.Context, options task.CreateOptions) (task.Task, error) {
	b.requests = append(b.requests, options)
	return task.Task{ID: fmt.Sprintf("created-%d", len(b.requests)), Title: options.Title}, nil
}
