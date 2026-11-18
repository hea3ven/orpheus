package review

import (
	"context"
	"errors"
	"fmt"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// Native error and output handles are invocation-local presentation data. The
// pending check and its actual result are committed by one ordinary operation.
type checkCommand struct {
	opts     PipelineRunOptions
	step     Step
	env      []string
	output   StepOutput
	err      error
	exitErr  interface{ ExitCode() int }
	findings []taskstate.ReviewFinding
}

func (c *checkCommand) operation() phaseexec.Operation {
	return phaseexec.Operation{Name: "run review check", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		changed, err := StepChange(c.opts.Attempt.Attempt, StepObservation{Address: c.opts.Attempt.Address, Kind: c.step.Kind, Name: c.step.Name})(current)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		owner := c.opts.Attempt.Address
		index := len(current.ActiveWorkflow().LastPhase().(*work.ReviewPhase).Steps) - 1
		owner.Step = &index
		return phaseexec.Prepared{Changed: changed, Scope: phaseexec.ReleaseForEffect, Run: func(ctx context.Context) phaseexec.Result {
			return c.run(ctx, owner)
		}}, nil
	}}
}

func (c *checkCommand) run(ctx context.Context, owner work.AttemptRef) phaseexec.Result {
	if err := notifyStepStarted(c.opts, c.step); err != nil {
		return phaseexec.Result{Update: checkResultChange(owner, nil, err, nil), Err: err}
	}
	c.output = openStepOutput(c.opts, true)
	opts := c.opts
	opts.Context = ctx
	exitCode, err := runStepCommandWithOutput(opts, c.step, c.env, c.output.Stdout, c.output.Stderr)
	c.err = err
	if errors.As(c.err, &c.exitErr) {
		c.findings = []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Title: fmt.Sprintf("Check %q failed", c.step.Name), Description: fmt.Sprintf("%s exited with status %d.", commandLine(c.step), c.exitErr.ExitCode()), Step: c.step.Name, SuggestedAction: "Inspect the check output, fix the issue, then rerun task run."}}
	}
	return phaseexec.Result{Update: checkResultChange(owner, exitCode, c.err, c.findings)}
}
