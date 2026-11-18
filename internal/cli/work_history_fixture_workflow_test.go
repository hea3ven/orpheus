//go:build integration

package cli_test

import (
	"errors"
	"fmt"
	"github.com/hea3ven/orpheus/internal/workfixture"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"
	"github.com/stretchr/testify/require"
	"testing"
)

// Reader fixtures describe synthetic, already observed history. These helpers
// only arrange that data; dispatched children and workflow operations under test
// must use their real stable addresses, launch observations and Git collaborators.
func fixtureImplementationAddress(store workfixture.Store, repoID, taskID string, number int) (work.AttemptRef, error) {
	view, err := store.Load(repoID, taskID)
	if err != nil {
		return work.AttemptRef{}, err
	}
	for _, run := range view.Runs {
		if run.Address.Workflow == view.CurrentWorkflow && run.Attempt == number {
			return run.Address, nil
		}
	}
	return work.AttemptRef{}, errors.New("fixture implementation owner not found")
}
func seedFixtureHandoff(store workfixture.Store, repoID, taskID string, number int, opts taskstate.CompleteRunOptions) (taskstate.RunAttempt, error) {
	ref, err := fixtureImplementationAddress(store, repoID, taskID, number)
	if err != nil {
		return taskstate.RunAttempt{}, err
	}
	return store.CompleteImplementation(ref, opts)
}
func seedFixtureUsage(store workfixture.Store, repoID, taskID string, number int, opts taskstate.RecordRunUsageOptions) (taskstate.RunAttempt, error) {
	ref, err := fixtureImplementationAddress(store, repoID, taskID, number)
	if err != nil {
		return taskstate.RunAttempt{}, err
	}
	return store.SupplementUsage(ref, opts)
}
func seedFixtureChildPID(store workfixture.Store, repoID, taskID string, number, pid int) (taskstate.RunAttempt, error) {
	ref, err := fixtureImplementationAddress(store, repoID, taskID, number)
	if err != nil {
		return taskstate.RunAttempt{}, err
	}
	return store.CheckpointPID(ref, pid)
}
func seedFixtureExit(store workfixture.Store, repoID, taskID string, number, code int) (taskstate.RunAttempt, error) {
	ref, err := fixtureImplementationAddress(store, repoID, taskID, number)
	if err != nil {
		return taskstate.RunAttempt{}, err
	}
	view, err := store.Load(repoID, taskID)
	if err != nil {
		return taskstate.RunAttempt{}, err
	}
	execution, err := view.Work.Workflows[ref.Workflow].Execution(work.ExecutionRef{Phase: ref.Phase})
	if err != nil {
		return taskstate.RunAttempt{}, err
	}
	// The one-minute fixture boundary is explicit synthetic input, not an inferred
	// production exit. Timing-specific fixtures should call FinishObservedRun with
	// their own independently specified observation time instead.
	at := execution.StartedAt.Add(time.Minute)
	effort := view.Work.Workflows[ref.Workflow].Phases[ref.Phase].(*work.ImplementationPhase)
	if effort.Completion != nil && effort.Completion.CompletedAt.After(at) {
		at = effort.Completion.CompletedAt
	}
	now := time.Now().UTC()
	if !execution.StartedAt.After(now) && at.After(now) {
		at = now
	}
	return store.ObserveProcess(ref, &work.ExitedProcess{Code: &code, At: at}, execution.ChildPID)
}
func fixtureCurrentReview(store workfixture.Store, repoID, taskID string, number int) (taskstate.ReviewAttempt, error) {
	view, err := store.Load(repoID, taskID)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	current, ok := taskstate.LatestReview(view)
	if !ok || current.Attempt != number {
		return taskstate.ReviewAttempt{}, errors.New("fixture review owner not found")
	}
	return store.CurrentReviewAt(current.Address)
}
func fixtureReviewOutcome(store workfixture.Store, repoID, taskID string, number int, want taskstate.ReviewStatus) (taskstate.ReviewAttempt, error) {
	current, err := fixtureCurrentReview(store, repoID, taskID, number)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	if current.Status != want {
		return taskstate.ReviewAttempt{}, fmt.Errorf("fixture needs explicit owned observations for %s; actual outcome is %s", want, current.Status)
	}
	return current, nil
}
func seedFixtureManualEntry(store workfixture.Store, repoID, taskID string, number int, name string) (taskstate.ReviewAttempt, error) {
	current, err := fixtureCurrentReview(store, repoID, taskID, number)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	return store.SeedManual(current.Address, name)
}
func beginFixtureManualReview(store workfixture.Store, repoID, taskID string) (taskstate.ReviewAttempt, error) {
	snapshot, err := store.Load(repoID, taskID)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	w := snapshot.Work.ActiveWorkflow()
	if w == nil {
		return taskstate.ReviewAttempt{}, errors.New("fixture has no open workflow")
	}
	output, ok := w.LatestOutput()
	if !ok {
		return taskstate.ReviewAttempt{}, errors.New("fixture must supply retained output before review")
	}
	pipeline := review.BuiltinManualPipeline()
	attempt, err := store.SeedReview(repoID, taskID, review.Reservation{Pipeline: pipeline.Name, Contract: work.ReviewContract{PipelineKey: pipeline.Name, Slots: []work.ReviewSlot{{Kind: work.ReviewManual, ConfigKey: pipeline.Steps[0].Name}}}, Scope: work.ReviewScope{Requirements: "Synthetic reader fixture requirements", Output: output, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}}})
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	return store.SeedManual(attempt.Address, pipeline.Steps[0].Name)
}

// seedFixtureManualFinding represents a finding saved by an earlier editing
// session. Current manual edits must instead be submitted with their assessment.
func seedFixtureManualFinding(paths state.Paths, owner work.AttemptRef, finding work.Finding) (work.State, error) {
	store := workstate.NewStore(paths)
	current, err := store.Load(owner.RepositoryID, owner.TaskID)
	if err != nil {
		return work.State{}, err
	}
	step := current.Workflows[owner.Workflow].Phases[owner.Phase].(*work.ReviewPhase).Steps[*owner.Step].(*work.ManualReviewStep)
	step.Findings = append(step.Findings, finding)
	version, err := store.Save(current, current.Version)
	current.Version = version
	return current, err
}

func fixtureRepairTargets(store workfixture.Store, repoID, taskID string, reviewNumber int, indexes []int, runNumber int) (taskstate.ReviewAttempt, error) {
	view, err := store.Load(repoID, taskID)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	current, ok := taskstate.LatestReview(view)
	if !ok || current.Attempt != reviewNumber {
		return taskstate.ReviewAttempt{}, errors.New("fixture review owner not found")
	}
	ref, err := fixtureImplementationAddress(store, repoID, taskID, runNumber)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	effort := view.Work.Workflows[ref.Workflow].Phases[ref.Phase].(*work.ImplementationPhase)
	if len(effort.TargetFindings) != len(indexes) {
		return taskstate.ReviewAttempt{}, errors.New("fixture must select repair targets at implementation admission")
	}
	for i, index := range indexes {
		if current.Findings[index].Address == nil || effort.TargetFindings[i] != *current.Findings[index].Address {
			return taskstate.ReviewAttempt{}, errors.New("fixture repair target differs from its original finding owner")
		}
	}
	return current, nil
}
func fixturePublicationReceipt(store workfixture.Store, repoID, taskID, commit string) (taskstate.Finalization, error) {
	view, err := store.Load(repoID, taskID)
	if err != nil {
		return taskstate.Finalization{}, err
	}
	facts := taskstate.FinalizationFacts(view)
	if facts.Commit != commit {
		return taskstate.Finalization{}, errors.New("a fixture SHA alone cannot create retained output, review approval or publication consent")
	}
	return facts, nil
}

// readerDeliveredHistory supplies complete, explicitly fixture-authorized native
// history to reader/recovery owners. It never substitutes for a workflow under test.
func readerDeliveredHistory(t *testing.T, repoID, taskID, directory, branch string, start time.Time) work.State {
	t.Helper()
	snapshot := testutil.ReviewableWork(repoID, taskID, directory, branch)
	w := &snapshot.Workflows[0]
	effort := w.Phases[1].(*work.ImplementationPhase)
	zero := 0
	effort.Execution = work.AgentExecution{Profile: "codex-profile", Harness: "codex", Model: "gpt-5", Command: "codex", Args: []string{"exec"}, StartedAt: start, Process: &work.ExitedProcess{Code: &zero, At: start.Add(2 * time.Minute)}}
	effort.Completion.CompletedAt = start.Add(time.Minute)
	w.ReviewContracts = []work.ReviewContract{{PipelineKey: "standard", Slots: []work.ReviewSlot{{Kind: work.ReviewAgent, ConfigKey: "ai-review"}}}}
	w.Phases = append(w.Phases, &work.ReviewPhase{Contract: 0, StartedAt: start.Add(10 * time.Minute), Scope: work.ReviewScope{Requirements: "Explicit synthetic reader fixture task", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "initial-commit"}}, Steps: []work.ReviewStep{&work.AgentReviewStep{Slot: 0, Execution: work.AgentExecution{Profile: "reviewer", Harness: "codex", Model: "gpt-5", Command: "codex", Args: []string{"exec", "review"}, StartedAt: start.Add(10 * time.Minute), Process: &work.ExitedProcess{Code: &zero, At: start.Add(12 * time.Minute)}}}}}, &work.PublicationPhase{Review: work.ReviewRef{Phase: 2}, Target: &work.PullRequestTarget{}})
	testutil.SeedPublicationInputs(&snapshot)
	require.NoError(t, snapshot.AuthorizePublication(0, work.PublicationRef{Phase: 3}, start.Add(14*time.Minute), "Explicit synthetic reader-fixture grant"))
	w.Phases[3].(*work.PublicationPhase).Delivery = &work.PullRequestDelivery{PullRequest: work.PullRequestRef{URL: "https://github.test/org/repo/pull/42"}, ObservedAt: start.Add(15 * time.Minute)}
	w.Phases = append(w.Phases, &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: 3}})
	return snapshot
}

func resolverEvents(events []taskstate.Event) []taskstate.Event {
	var selected []taskstate.Event
	for _, event := range events {
		switch event.Type {
		case taskstate.EventSyncConflictStarted, taskstate.EventSyncConflictFinished, taskstate.EventSyncConflictFailed:
			selected = append(selected, event)
		}
	}
	return selected
}
