package review

import (
	"context"
	"errors"
	"fmt"
	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"os"
	"time"
)

type reviewerCommand struct{ output StepOutput }

func (c *reviewerCommand) operation(opts PipelineRunOptions, step Step, prepared agent.PreparedExecution, checkCandidate CandidateCheck) phaseexec.Operation {
	return phaseexec.Operation{Name: "run review agent", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		execution := prepared.Execution()
		execution.StartedAt, execution.SupervisorPID = time.Now().UTC(), os.Getpid()
		changed, err := StepChange(opts.Attempt.Attempt, StepObservation{Address: opts.Attempt.Address, Kind: step.Kind, Name: step.Name, Execution: &execution})(current)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		index := len(current.ActiveWorkflow().LastPhase().(*work.ReviewPhase).Steps) - 1
		execution.Address = opts.Attempt.Address
		execution.Address.Step = &index
		return phaseexec.Prepared{Changed: changed, Scope: phaseexec.ReleaseForEffect, Run: func(ctx context.Context) phaseexec.Result {
			if err := notifyStepStarted(opts, step); err != nil {
				_, recordErr := opts.AgentResults.Commit(context.WithoutCancel(ctx), ProcessChange(opts.Attempt.Attempt, step.Name, ProcessObservation{Address: execution.Address, Process: &work.StartFailedProcess{At: time.Now().UTC(), Reason: err.Error()}}))
				return phaseexec.Result{Err: fmt.Errorf("task run %s: run agent_review step %q: %w", opts.TaskID, step.Name, errors.Join(err, recordErr))}
			}
			c.output = openStepOutput(opts, !prepared.Interactive())
			runOpts := opts
			runOpts.Context = ctx
			result := launchAgentReview(runOpts, step, prepared, execution, c.output)
			// Safety and process facts precede any finding disposition prompt.
			mutationErr := checkCandidate()
			recordErr := finishAgentReviewExecution(runOpts, step, execution.Address, result, mutationErr)
			err := errors.Join(result.Err, mutationErr, recordErr)
			if err != nil {
				err = fmt.Errorf("task run %s: run agent_review step %q: %w", opts.TaskID, step.Name, err)
			}
			return phaseexec.Result{Err: err}
		}}, nil
	}}
}
