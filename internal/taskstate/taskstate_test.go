package taskstate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestStoreRecordsWorktreeAndRunAttempts(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 2, 0, 0, time.UTC),
	)

	if _, err := store.RecordWorktreeEvent("alpha", "op-1", taskstate.EventWorktreeCreated, taskstate.WorktreeEventOptions{
		Branch:   "orpheus/op-1",
		Worktree: "/tmp/op-1",
	}); err != nil {
		t.Fatalf("record worktree event: %v", err)
	}

	attempt, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "orpheus/op-1",
		Worktree: "/tmp/op-1",
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if attempt.Attempt != 1 || attempt.Status != taskstate.RunStatusRunning {
		t.Fatalf("attempt = %#v, want running attempt 1", attempt)
	}

	finished, err := store.FinishRun("alpha", "op-1", attempt.Attempt, taskstate.RunStatusSucceeded)
	if err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if finished.Status != taskstate.RunStatusSucceeded || finished.FinishedAt == nil {
		t.Fatalf("finished attempt = %#v, want succeeded with finished_at", finished)
	}

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.RepoID != "alpha" || loaded.TaskID != "op-1" || len(loaded.Runs) != 1 || len(loaded.Events) != 3 {
		t.Fatalf("loaded state = %#v", loaded)
	}
	if loaded.Events[0].Type != taskstate.EventWorktreeCreated || loaded.Events[1].Type != taskstate.EventRunStarted || loaded.Events[2].Type != taskstate.EventRunFinished {
		t.Fatalf("event types = %#v", loaded.Events)
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"repo_id: alpha",
		"task_id: op-1",
		"attempt: 1",
		"status: succeeded",
		"agent: recorder",
		"worktree_created",
		"run_started",
		"run_finished",
	)
}

func TestStoreRecordsFeatureBranchPREventsIdempotently(t *testing.T) {
	tests := []struct {
		name         string
		wasRecovered bool
		wantType     taskstate.EventType
	}{
		{
			name:     "created PR",
			wantType: taskstate.EventPRCreated,
		},
		{
			name:         "recovered PR",
			wasRecovered: true,
			wantType:     taskstate.EventPRRecovered,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
			store := newTestStore(t, now, now.Add(time.Minute))
			opts := taskstate.FeatureBranchPROptions{
				PRURL:        " https://github.test/org/repo/pull/42 ",
				Branch:       "orpheus/op-1",
				WasRecovered: tt.wasRecovered,
			}

			event, err := store.RecordFeatureBranchPR("alpha", "op-1", opts)
			if err != nil {
				t.Fatalf("record PR event: %v", err)
			}
			if event.Type != tt.wantType || event.PRURL != "https://github.test/org/repo/pull/42" || event.Branch != "orpheus/op-1" {
				t.Fatalf("event = %#v, want %q with structured PR facts", event, tt.wantType)
			}

			again, err := store.RecordFeatureBranchPR("alpha", "op-1", opts)
			if err != nil {
				t.Fatalf("record same PR event: %v", err)
			}
			if !again.At.Equal(event.At) {
				t.Fatalf("idempotent event time = %s, want %s", again.At, event.At)
			}

			loaded, err := store.Load("alpha", "op-1")
			if err != nil {
				t.Fatalf("load state: %v", err)
			}
			if len(loaded.Events) != 1 {
				t.Fatalf("events = %#v, want one idempotent PR event", loaded.Events)
			}
		})
	}
}

func TestStoreCompleteRunRecordsCompletionFacts(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
	)
	attempt := startAlphaRun(t, store, taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: "/tmp/alpha",
	})

	completed := completeAlphaRun(t, store, attempt.Attempt, "complete run", taskstate.CompleteRunOptions{
		Summary:             "Implemented completion",
		Description:         "Recorded local review data.",
		DetailedDescription: "Detailed PR body.",
	})
	assertInitialCompletionRecorded(t, completed)

	withCommit := completeAlphaRun(t, store, attempt.Attempt, "record completion commit", taskstate.CompleteRunOptions{
		Summary:             "Implemented completion",
		Description:         "Recorded local review data.",
		DetailedDescription: "Detailed PR body.",
		Commit:              "abc123",
	})
	if withCommit.Completion.Commit != "abc123" {
		t.Fatalf("completion commit = %q, want abc123", withCommit.Completion.Commit)
	}

	finishAlphaRunSucceeded(t, store, attempt.Attempt)

	again := completeAlphaRun(t, store, attempt.Attempt, "complete same run again", taskstate.CompleteRunOptions{
		Summary:             "Implemented completion",
		Description:         "Recorded local review data.",
		DetailedDescription: "Detailed PR body.",
		Commit:              "abc123",
	})
	if !again.Completion.CompletedAt.Equal(completed.Completion.CompletedAt) {
		t.Fatalf("idempotent completed_at = %s, want %s", again.Completion.CompletedAt, completed.Completion.CompletedAt)
	}

	assertConflictingCompletionRejected(t, store, attempt.Attempt)
	assertCompletionStateLoaded(t, store)
	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"status: succeeded",
		"completion:",
		"summary: Implemented completion",
		"description: Recorded local review data.",
		"detailed_description: Detailed PR body.",
		"completed_at: 2026-06-03T10:01:00Z",
		"commit: abc123",
	)
}

func TestStoreRecordsRepeatedCompletionDiagnostic(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 2, 0, 0, time.UTC),
	)
	attempt := startAlphaRun(t, store, taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: "/tmp/alpha",
	})
	completeAlphaRun(t, store, attempt.Attempt, "complete run", taskstate.CompleteRunOptions{
		Summary:             "First summary",
		Description:         "First details.",
		DetailedDescription: "Detailed PR body.",
	})

	event, err := store.RecordRepeatedCompletion("alpha", "op-1", attempt.Attempt, taskstate.RepeatedCompletionOptions{
		Summary:             "Second summary",
		Description:         "Second details.",
		DetailedDescription: "Detailed PR body.",
	})
	if err != nil {
		t.Fatalf("record repeated completion: %v", err)
	}
	if event.Type != taskstate.EventCompletionRepeated || event.Attempt != attempt.Attempt || event.Status != taskstate.RunStatusRunning {
		t.Fatalf("event = %#v, want completion_repeated for running attempt", event)
	}
	if event.RequestedSummary != "Second summary" ||
		event.RequestedDescription != "Second details." ||
		event.RequestedDetailedDescription != "Detailed PR body." {
		t.Fatalf("event requested payload = %#v", event)
	}
	if !strings.Contains(event.Message, "preserved first completion") {
		t.Fatalf("event message = %q, want preservation diagnostic", event.Message)
	}

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if len(loaded.Events) != 3 || loaded.Events[2].Type != taskstate.EventCompletionRepeated {
		t.Fatalf("events = %#v, want repeated completion diagnostic", loaded.Events)
	}
	if loaded.Runs[0].Completion.Summary != "First summary" ||
		loaded.Runs[0].Completion.Description != "First details." ||
		loaded.Runs[0].Completion.DetailedDescription != "Detailed PR body." {
		t.Fatalf("completion = %#v, want first payload preserved", loaded.Runs[0].Completion)
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"completion_repeated",
		"requested_summary: Second summary",
		"requested_description: Second details.",
		"requested_detailed_description: Detailed PR body.",
		"preserved first completion",
	)
}

//nolint:funlen // The durable boundary sequence is the behavior under test.
func TestStoreRecordsFinalizationFactsIdempotently(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 4, 10, 1, 0, 0, time.UTC),
		time.Date(2026, 6, 4, 10, 2, 0, 0, time.UTC),
	)

	commitFacts, err := store.RecordFinalizationCommit("alpha", "op-1", " abc123 ")
	if err != nil {
		t.Fatalf("record commit: %v", err)
	}
	if commitFacts.Commit != "abc123" || commitFacts.CommittedAt == nil ||
		!commitFacts.CommittedAt.Equal(time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("commit facts = %#v, want commit and committed_at", commitFacts)
	}

	pushFacts, err := store.RecordFinalizationPush("alpha", "op-1", taskstate.FinalizationPushOptions{
		Branch:     "main",
		PushTarget: taskstate.PushTargetMain,
	})
	if err != nil {
		t.Fatalf("record push: %v", err)
	}
	if pushFacts.PushedAt == nil || !pushFacts.PushedAt.Equal(time.Date(2026, 6, 4, 10, 1, 0, 0, time.UTC)) {
		t.Fatalf("push facts = %#v, want pushed_at", pushFacts)
	}

	closeFacts, err := store.RecordFinalizationClose("alpha", "op-1", taskstate.FinalizationCloseOptions{
		Reason: taskstate.CloseReasonDefaultBranchPublished,
	})
	if err != nil {
		t.Fatalf("record close: %v", err)
	}
	if closeFacts.ClosedAt == nil || !closeFacts.ClosedAt.Equal(time.Date(2026, 6, 4, 10, 2, 0, 0, time.UTC)) {
		t.Fatalf("close facts = %#v, want closed_at", closeFacts)
	}

	again, err := store.RecordFinalizationCommit("alpha", "op-1", "abc123")
	if err != nil {
		t.Fatalf("record same commit again: %v", err)
	}
	if !again.CommittedAt.Equal(*commitFacts.CommittedAt) || !again.PushedAt.Equal(*pushFacts.PushedAt) || !again.ClosedAt.Equal(*closeFacts.ClosedAt) {
		t.Fatalf("idempotent facts = %#v, want original timestamps preserved", again)
	}
	repeatedPush, err := store.RecordFinalizationPush("alpha", "op-1", taskstate.FinalizationPushOptions{
		Branch:     "main",
		PushTarget: taskstate.PushTargetMain,
	})
	if err != nil {
		t.Fatalf("record repeated push: %v", err)
	}
	if !repeatedPush.PushedAt.Equal(*pushFacts.PushedAt) {
		t.Fatalf("repeated push facts = %#v, want original push timestamp", repeatedPush)
	}
	repeatedClose, err := store.RecordFinalizationClose("alpha", "op-1", taskstate.FinalizationCloseOptions{
		Reason: taskstate.CloseReasonDefaultBranchPublished,
	})
	if err != nil {
		t.Fatalf("record repeated close: %v", err)
	}
	if !repeatedClose.ClosedAt.Equal(*closeFacts.ClosedAt) {
		t.Fatalf("repeated close facts = %#v, want original close timestamp", repeatedClose)
	}

	_, err = store.RecordFinalizationCommit("alpha", "op-1", "def456")
	if !errors.Is(err, taskstate.ErrFinalizationConflict) {
		t.Fatalf("conflicting commit error = %v, want ErrFinalizationConflict", err)
	}

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	facts := taskstate.FinalizationFacts(loaded)
	if facts.Commit != "abc123" || facts.CommittedAt == nil || facts.PushedAt == nil || facts.ClosedAt == nil {
		t.Fatalf("loaded finalization = %#v, want all facts", facts)
	}
	if len(loaded.Events) != 2 || loaded.Events[0].Type != taskstate.EventChangesPushed || loaded.Events[1].Type != taskstate.EventTaskClosed {
		t.Fatalf("events = %#v, want one push and one close event", loaded.Events)
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"finalization:",
		"committed_at: 2026-06-04T10:00:00Z",
		"commit: abc123",
		"pushed_at: 2026-06-04T10:01:00Z",
		"closed_at: 2026-06-04T10:02:00Z",
		"changes_pushed",
		"push_target: main",
		"task_closed",
		"close_reason: default_branch_published",
	)
}

func TestStoreRecordsTaskClosedPRMergedEventIdempotently(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 9, 10, 1, 0, 0, time.UTC),
	)

	event, err := store.RecordTaskClosedPRMerged("alpha", "op-1", taskstate.TaskClosedPRMergedOptions{
		PRURL:           " https://github.test/org/repo/pull/42 ",
		ObservedPRState: "merged",
	})
	if err != nil {
		t.Fatalf("record merged PR close event: %v", err)
	}
	if event.Type != taskstate.EventTaskClosedPRMerged ||
		event.PRURL != "https://github.test/org/repo/pull/42" ||
		event.ObservedPRState != "merged" ||
		!event.At.Equal(time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("event = %#v, want merged PR close facts", event)
	}
	if !strings.Contains(event.Message, "recorded PR was merged") {
		t.Fatalf("event message = %q, want merged PR audit message", event.Message)
	}

	again, err := store.RecordTaskClosedPRMerged("alpha", "op-1", taskstate.TaskClosedPRMergedOptions{
		PRURL:           "https://github.test/org/repo/pull/42",
		ObservedPRState: "merged",
	})
	if err != nil {
		t.Fatalf("record same merged PR close event again: %v", err)
	}
	if !again.At.Equal(event.At) {
		t.Fatalf("idempotent event time = %s, want %s", again.At, event.At)
	}

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Events) != 1 {
		t.Fatalf("events = %#v, want one idempotent merged PR close event", loaded.Events)
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"task_closed_due_to_pr_merged",
		"at: 2026-06-09T10:00:00Z",
		"pr_url: https://github.test/org/repo/pull/42",
		"observed_pr_state: merged",
	)
}

func TestStartRunRefusesLatestRunningAttempt(t *testing.T) {
	store := newTestStore(t, time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC))
	if _, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "recorder"}); err != nil {
		t.Fatalf("start run: %v", err)
	}

	_, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "recorder"})
	if !errors.Is(err, taskstate.ErrActiveRun) {
		t.Fatalf("second start error = %v, want ErrActiveRun", err)
	}

	active, ok, err := store.ActiveRun("alpha", "op-1")
	if err != nil || !ok || active.Attempt != 1 {
		t.Fatalf("active = %#v ok=%v err=%v, want attempt 1", active, ok, err)
	}
}

func TestRetriesCreateNewAttemptsWithoutOverwritingOldAttempts(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 2, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 3, 0, 0, time.UTC),
	)

	first, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "recorder"})
	if err != nil {
		t.Fatalf("start first: %v", err)
	}
	if _, err := store.FinishRun("alpha", "op-1", first.Attempt, taskstate.RunStatusFailed); err != nil {
		t.Fatalf("fail first: %v", err)
	}
	second, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "recorder"})
	if err != nil {
		t.Fatalf("start second: %v", err)
	}
	if _, err := store.FinishRun("alpha", "op-1", second.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish second: %v", err)
	}

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Runs) != 2 {
		t.Fatalf("runs = %#v, want two attempts", loaded.Runs)
	}
	if loaded.Runs[0].Attempt != 1 || loaded.Runs[0].Status != taskstate.RunStatusFailed {
		t.Fatalf("first run = %#v, want failed attempt 1", loaded.Runs[0])
	}
	if loaded.Runs[1].Attempt != 2 || loaded.Runs[1].Status != taskstate.RunStatusSucceeded {
		t.Fatalf("second run = %#v, want succeeded attempt 2", loaded.Runs[1])
	}
	latest, ok := taskstate.LatestRun(loaded)
	if !ok || latest.Attempt != 2 || latest.Status != taskstate.RunStatusSucceeded {
		t.Fatalf("latest = %#v ok=%v, want succeeded attempt 2", latest, ok)
	}
}

func TestFailRunStartRecordsFailedAttemptAndStartFailureEvent(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
	)

	attempt, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "missing"})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	failed, err := store.FailRunStart("alpha", "op-1", attempt.Attempt, errors.New("command not found"))
	if err != nil {
		t.Fatalf("fail run start: %v", err)
	}
	if failed.Status != taskstate.RunStatusFailed {
		t.Fatalf("failed status = %q, want failed", failed.Status)
	}

	events, err := store.Events("alpha", "op-1")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	last := events[len(events)-1]
	if last.Type != taskstate.EventRunStartFailed || last.Status != taskstate.RunStatusFailed || !strings.Contains(last.Error, "command not found") {
		t.Fatalf("last event = %#v, want run_start_failed with error", last)
	}
}

func TestStoreValidatesTaskStatePathComponents(t *testing.T) {
	store := newTestStore(t, time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC))

	_, err := store.Path("alpha", "../op-1")
	if err == nil || !strings.Contains(err.Error(), "task id") {
		t.Fatalf("path error = %v, want task id validation", err)
	}
	_, err = store.Load("alpha/other", "op-1")
	if err == nil || !strings.Contains(err.Error(), "repo id") {
		t.Fatalf("load error = %v, want repo id validation", err)
	}
}

func newTestStore(t *testing.T, times ...time.Time) taskstate.Store {
	t.Helper()

	root := t.TempDir()
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	index := 0
	clock := func() time.Time {
		if len(times) == 0 {
			return time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
		}
		if index >= len(times) {
			return times[len(times)-1]
		}
		value := times[index]
		index++
		return value
	}
	return taskstate.NewStoreWithClock(paths, clock)
}

func startAlphaRun(t *testing.T, store taskstate.Store, opts taskstate.StartRunOptions) taskstate.RunAttempt {
	t.Helper()

	attempt, err := store.StartRun("alpha", "op-1", opts)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	return attempt
}

func completeAlphaRun(
	t *testing.T,
	store taskstate.Store,
	attempt int,
	action string,
	opts taskstate.CompleteRunOptions,
) taskstate.RunAttempt {
	t.Helper()

	completed, err := store.CompleteRun("alpha", "op-1", attempt, opts)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return completed
}

func assertInitialCompletionRecorded(t *testing.T, completed taskstate.RunAttempt) {
	t.Helper()

	if completed.Status != taskstate.RunStatusRunning || completed.FinishedAt != nil {
		t.Fatalf("completed run = %#v, want still-running completion without finished_at", completed)
	}
	if completed.Completion == nil {
		t.Fatalf("completed run missing completion: %#v", completed)
	}
	if completed.Completion.Summary != "Implemented completion" ||
		completed.Completion.Description != "Recorded local review data." ||
		completed.Completion.DetailedDescription != "Detailed PR body." ||
		!completed.Completion.CompletedAt.Equal(time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC)) {
		t.Fatalf("completion = %#v, want recorded summary/description/detailed_description/completed_at", completed.Completion)
	}
}

func finishAlphaRunSucceeded(t *testing.T, store taskstate.Store, attempt int) {
	t.Helper()

	finished, err := store.FinishRun("alpha", "op-1", attempt, taskstate.RunStatusSucceeded)
	if err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if finished.Status != taskstate.RunStatusSucceeded || finished.FinishedAt == nil {
		t.Fatalf("finished run = %#v, want succeeded with finished_at", finished)
	}
}

func assertConflictingCompletionRejected(t *testing.T, store taskstate.Store, attempt int) {
	t.Helper()

	_, err := store.CompleteRun("alpha", "op-1", attempt, taskstate.CompleteRunOptions{
		Summary:             "Different",
		Description:         "Recorded local review data.",
		DetailedDescription: "Detailed PR body.",
	})
	if !errors.Is(err, taskstate.ErrCompletionConflict) {
		t.Fatalf("conflicting completion error = %v, want ErrCompletionConflict", err)
	}
}

func assertCompletionStateLoaded(t *testing.T, store taskstate.Store) {
	t.Helper()

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if len(loaded.Runs) != 1 || loaded.Runs[0].Completion == nil || len(loaded.Events) != 3 {
		t.Fatalf("loaded state = %#v, want one completed run and three events", loaded)
	}
}

func assertStoreYAMLContains(t *testing.T, store taskstate.Store, repoID, taskID string, wants ...string) {
	t.Helper()

	statePath, err := store.Path(repoID, taskID)
	if err != nil {
		t.Fatalf("state path: %v", err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read YAML: %v", err)
	}
	text := string(data)
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("YAML missing %q:\n%s", want, text)
		}
	}
}
