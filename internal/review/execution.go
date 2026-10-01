package review

import (
	"errors"
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

// ErrInvalidExecution means a pipeline request was rejected before execution.
// Callers must not mark the review failed or otherwise mutate it for this error.
var ErrInvalidExecution = errors.New("invalid review execution")

type executionKind uint8

const (
	executionFresh executionKind = iota + 1
	executionResumeStep
	executionResumeDecision
)

// ExecutionRequest selects exactly one execution alternative. Its zero value is
// invalid; use FreshExecution or ResumeExecution to construct a request.
// Resume requests bind the waiting interaction to its attempt, pipeline and step.
type ExecutionRequest struct {
	kind     executionKind
	attempt  int
	pipeline string
	step     string
}

// FreshExecution starts a new attempt at the first pipeline step.
func FreshExecution() ExecutionRequest {
	return ExecutionRequest{kind: executionFresh}
}

// ResumeExecution derives the continuation from a persisted waiting attempt.
// Workflow must construct and validate it before changing that attempt to running.
func ResumeExecution(pipeline Pipeline, paused taskstate.ReviewAttempt) (ExecutionRequest, error) {
	request := ExecutionRequest{attempt: paused.Attempt, pipeline: paused.Pipeline, step: paused.Step}
	switch paused.Status {
	case taskstate.ReviewStatusWaitingForManual:
		request.kind = executionResumeStep
	case taskstate.ReviewStatusWaitingForAutomatedDecision:
		request.kind = executionResumeDecision
	default:
		return ExecutionRequest{}, fmt.Errorf("%w: attempt %d is not waiting", ErrInvalidExecution, paused.Attempt)
	}
	if err := request.ValidateResume(pipeline, paused); err != nil {
		return ExecutionRequest{}, err
	}
	return request, nil
}

// Resumed reports whether the request continues a paused review.
func (r ExecutionRequest) Resumed() bool {
	return r.kind == executionResumeStep || r.kind == executionResumeDecision
}

// ResumesAutomatedDecision reports whether to present existing automated findings.
func (r ExecutionRequest) ResumesAutomatedDecision() bool {
	return r.kind == executionResumeDecision
}

// ValidateResume checks the current waiting state before workflow resumes it.
func (r ExecutionRequest) ValidateResume(pipeline Pipeline, paused taskstate.ReviewAttempt) error {
	if !r.Resumed() {
		return fmt.Errorf("%w: expected a resume request", ErrInvalidExecution)
	}
	want := taskstate.ReviewStatusWaitingForManual
	if r.ResumesAutomatedDecision() {
		want = taskstate.ReviewStatusWaitingForAutomatedDecision
	}
	if paused.Status != want {
		return fmt.Errorf("%w: attempt %d is %q, expected %q", ErrInvalidExecution, paused.Attempt, paused.Status, want)
	}
	return r.validateTarget(pipeline, paused)
}

// ValidateRunning checks the request against an attempt after startup or resume.
func (r ExecutionRequest) ValidateRunning(pipeline Pipeline, attempt taskstate.ReviewAttempt) error {
	if attempt.Status != taskstate.ReviewStatusRunning {
		return fmt.Errorf("%w: attempt %d is not running", ErrInvalidExecution, attempt.Attempt)
	}
	return r.validateTarget(pipeline, attempt)
}

func (r ExecutionRequest) validateTarget(pipeline Pipeline, attempt taskstate.ReviewAttempt) error {
	if attempt.Attempt <= 0 || strings.TrimSpace(pipeline.Name) == "" || attempt.Pipeline != pipeline.Name || len(pipeline.Steps) == 0 {
		return fmt.Errorf("%w: attempt %d does not identify pipeline %q", ErrInvalidExecution, attempt.Attempt, pipeline.Name)
	}
	switch r.kind {
	case executionFresh:
		if r.attempt != 0 || r.pipeline != "" || r.step != "" {
			return fmt.Errorf("%w: fresh execution cannot carry a resume target", ErrInvalidExecution)
		}
		if attempt.Step != pipeline.Steps[0].Name || len(attempt.Steps) != 0 || len(attempt.Findings) != 0 {
			return fmt.Errorf("%w: fresh execution requires a new attempt at step one", ErrInvalidExecution)
		}
		return nil
	case executionResumeStep, executionResumeDecision:
		if r.attempt != attempt.Attempt || r.pipeline != attempt.Pipeline || r.step != attempt.Step || strings.TrimSpace(r.step) == "" {
			return fmt.Errorf("%w: resume target does not match attempt %d at step %q", ErrInvalidExecution, attempt.Attempt, attempt.Step)
		}
	default:
		return fmt.Errorf("%w: execution alternative is required", ErrInvalidExecution)
	}
	index, err := pipelineStartIndex(pipeline, r.step)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecution, err)
	}
	step := pipeline.Steps[index]
	if r.kind == executionResumeStep {
		if step.Kind != KindManual {
			return fmt.Errorf("%w: pending step %q is not manual", ErrInvalidExecution, step.Name)
		}
		return nil
	}
	if step.Kind != KindCheck && step.Kind != KindAgentReview {
		return fmt.Errorf("%w: pending step %q is not automated", ErrInvalidExecution, step.Name)
	}
	if len(automatedBlockersForStep(attempt, step.Name)) == 0 {
		return fmt.Errorf("%w: pending step %q has no active blockers", ErrInvalidExecution, step.Name)
	}
	return nil
}

func validatePipelineExecution(opts PipelineRunOptions) error {
	if err := opts.Execution.ValidateRunning(opts.Pipeline, opts.Attempt); err != nil {
		return err
	}
	if opts.Store == nil {
		return fmt.Errorf("%w: review store is required", ErrInvalidExecution)
	}
	state, err := opts.Store.Load(opts.RepoID, opts.TaskID)
	if err != nil {
		return fmt.Errorf("%w: load current review: %w", ErrInvalidExecution, err)
	}
	latest, ok := taskstate.LatestReview(state)
	if !ok || latest.Attempt != opts.Attempt.Attempt {
		return fmt.Errorf("%w: latest review no longer matches attempt %d", ErrInvalidExecution, opts.Attempt.Attempt)
	}
	return opts.Execution.ValidateRunning(opts.Pipeline, latest)
}
