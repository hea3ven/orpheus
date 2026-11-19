package review

import (
	"context"
	"fmt"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

type manualCommand struct {
	exitCode *int
	notes    []HunkNote
}

func (c *manualCommand) operation(opts PipelineRunOptions, step Step, env []string) phaseexec.Operation {
	return phaseexec.Operation{Name: "inspect manual review", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		owner := opts.Attempt.Address
		if err := setManualInspectionPending(current, owner, true); err != nil {
			return phaseexec.Prepared{}, err
		}
		return phaseexec.Prepared{Changed: true, Scope: phaseexec.ReleaseForEffect, Run: func(ctx context.Context) phaseexec.Result {
			runOpts := opts
			runOpts.Context = ctx
			var runErr error
			c.exitCode, c.notes, runErr = runManualStepCommand(runOpts, step, env)
			if cancellation := manualCommandCancellation(ctx, c.exitCode, runErr); cancellation != nil {
				var update phaseexec.Change
				if c.exitCode != nil {
					update = manualInterruptionChange(opts.Attempt.Address, *c.exitCode)
				}
				return phaseexec.Result{Update: finishManualInspection(owner, update), Err: cancellation}
			}
			// Reopening inspection retains its original command observation.
			var update phaseexec.Change
			if opts.Attempt.Steps[len(opts.Attempt.Steps)-1].ExitCode == nil {
				update = StepChange(opts.Attempt.Attempt, StepObservation{Address: opts.Attempt.Address, Kind: step.Kind, Name: step.Name, ExitCode: c.exitCode})
			}
			if runErr != nil {
				runErr = fmt.Errorf("task run %s: run manual step %q: %w", opts.TaskID, step.Name, runErr)
			} else {
				runErr = ctx.Err()
			}
			return phaseexec.Result{Update: finishManualInspection(owner, update), Err: runErr}
		}}, nil
	}}
}

func setManualInspectionPending(current *work.State, owner work.AttemptRef, pending bool) error {
	if err := current.CheckActiveAttempt(owner); err != nil {
		return err
	}
	_, review, err := currentReview(current, owner)
	if err != nil {
		return err
	}
	step, ok := review.Steps[*owner.Step].(*work.ManualReviewStep)
	if !ok {
		return fmt.Errorf("inspection command requires a manual assessment")
	}
	step.InspectionPending = &pending
	return nil
}

func finishManualInspection(owner work.AttemptRef, update phaseexec.Change) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		if err := setManualInspectionPending(current, owner, false); err != nil {
			return false, err
		}
		if update != nil {
			_, err := update(current)
			if err != nil {
				return false, err
			}
		}
		return true, nil
	}
}
