package review

import (
	"errors"
	"fmt"
	"io"
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
