package implementation

import (
	"context"
	"errors"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"time"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// CompletionRecorder adapts the agent's independent handoff endpoint to the
// phase-owned change. Agent CompletionService already owns the mutation scope.
type CompletionRecorder struct {
	Paths state.Paths
	Store phaseexec.Store
}

func (r CompletionRecorder) CompleteImplementation(owner work.AttemptRef, opts taskstate.CompleteRunOptions) (taskstate.RunAttempt, error) {
	runner := phaseexec.Runner{Store: r.Store, Paths: r.Paths, RepositoryID: owner.RepositoryID, TaskID: owner.TaskID}
	current, err := runner.CommitHeld(context.Background(), CompletionChange(owner, opts, time.Now().UTC()))
	if err != nil {
		return taskstate.RunAttempt{}, err
	}
	view, err := taskstate.Project(current)
	if err != nil {
		return taskstate.RunAttempt{}, err
	}
	run, ok := taskstate.LatestRun(view)
	if !ok || !owner.Equal(run.Address) {
		return taskstate.RunAttempt{}, errors.New("implementation no longer owns its handoff")
	}
	return run, nil
}

// RecordChildPID is the mandatory synchronous attached-launch checkpoint.
func RecordChildPID(runner phaseexec.Runner, owner work.AttemptRef, pid int) error {
	return phaseexec.PIDCheckpoint(runner.Paths, runner.Logger, "task run child PID persistence", owner, func() error {
		_, err := runner.CommitHeld(context.Background(), ChildPIDChange(owner, pid))
		return err
	})
}
