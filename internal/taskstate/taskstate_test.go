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

	statePath, err := store.Path("alpha", "op-1")
	if err != nil {
		t.Fatalf("state path: %v", err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read YAML: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"repo_id: alpha",
		"task_id: op-1",
		"attempt: 1",
		"status: succeeded",
		"agent: recorder",
		"worktree_created",
		"run_started",
		"run_finished",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("YAML missing %q:\n%s", want, text)
		}
	}
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
