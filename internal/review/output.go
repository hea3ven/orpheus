package review

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// OutputDisposition reports the semantic reason to retain or clear captured
// process output. Terminal panels and rolling tails belong to the frontend.
type OutputDisposition string

const (
	OutputClear    OutputDisposition = "clear"
	OutputTail     OutputDisposition = "tail"
	OutputExpanded OutputDisposition = "expanded"
)

// StepOutput keeps raw child streams separate from decisions and progress.
// Finish is called before operator interaction, including error paths.
type StepOutput struct {
	Stdout io.Writer
	Stderr io.Writer
	Finish func(OutputDisposition)
}

func (o StepOutput) finish(disposition OutputDisposition) {
	if o.Finish != nil {
		o.Finish(disposition)
	}
}

func openStepOutput(opts PipelineRunOptions, rolling bool) StepOutput {
	if opts.OpenOutput != nil {
		return opts.OpenOutput(rolling)
	}
	return StepOutput{Stdout: opts.Stdout, Stderr: opts.Stderr}
}

func notifyStepStarted(opts PipelineRunOptions, step Step) error {
	if opts.StepStarted != nil {
		if err := opts.StepStarted(step); err != nil {
			return fmt.Errorf("%w: %w", ErrPresentationUnavailable, err)
		}
	}
	return nil
}

// ErrPresentationUnavailable leaves unanswered decisions available for another frontend.
var ErrPresentationUnavailable = errors.New("workflow presentation unavailable")

// A failed header must settle a reserved execution which never started. Manual
// assessments, in contrast, remain unanswered at their original address.
func notifyReservedStep(opts PipelineRunOptions, step Step, owner work.AttemptRef) error {
	err := notifyStepStarted(opts, step)
	if err == nil {
		return nil
	}
	var recordErr error
	switch step.Kind {
	case KindCheck:
		_, recordErr = opts.Store.RecordCheckResult(owner, nil, err, nil)
	case KindAgentReview:
		_, recordErr = opts.Store.FinishReviewStepExecution(opts.RepoID, opts.TaskID, opts.Attempt.Attempt, step.Name, taskstate.FinishReviewStepExecutionOptions{Address: owner, Process: &work.StartFailedProcess{At: time.Now().UTC(), Reason: err.Error()}})
	}
	return errors.Join(err, recordErr)
}
