package review

import (
	"context"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// RecordChildPID is the mandatory synchronous reviewer launch checkpoint.
func RecordChildPID(runner phaseexec.Runner, owner work.AttemptRef, pid int) error {
	return phaseexec.PIDCheckpoint(runner.Paths, runner.Logger, "review child PID persistence", owner, func() error {
		_, err := runner.CommitHeld(context.Background(), ChildPIDChange(owner, pid))
		return err
	})
}
