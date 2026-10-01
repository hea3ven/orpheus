package review

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResumeExecutionDerivesInteractionBeforeRunning(t *testing.T) {
	for _, kind := range []string{KindManual, KindCheck, KindAgentReview} {
		t.Run(kind, func(t *testing.T) {
			pipeline, paused := pausedExecution(kind)

			request, err := ResumeExecution(pipeline, paused)

			require.NoError(t, err)
			assert.True(t, request.Resumed())
			assert.Equal(t, kind != KindManual, request.ResumesAutomatedDecision())
			assert.Equal(t, paused.Attempt, request.attempt)
			assert.Equal(t, paused.Step, request.step)
			assert.NoError(t, request.ValidateResume(pipeline, paused))
			assert.ErrorIs(t, request.ValidateRunning(pipeline, paused), ErrInvalidExecution)
			running := paused
			running.Status = taskstate.ReviewStatusRunning
			assert.NoError(t, request.ValidateRunning(pipeline, running))
			assert.ErrorIs(t, request.ValidateResume(pipeline, running), ErrInvalidExecution)
			_, err = ResumeExecution(pipeline, running)
			assert.ErrorIs(t, err, ErrInvalidExecution, "intent cannot be recovered after the transition")
		})
	}
}

func TestResumeExecutionRejectsInvalidWaitingState(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Pipeline, *taskstate.ReviewAttempt)
	}{
		{"missing attempt", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Attempt = 0 }},
		{"missing pipeline", func(p *Pipeline, _ *taskstate.ReviewAttempt) { p.Name = "" }},
		{"different pipeline", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Pipeline = "other" }},
		{"empty pipeline", func(p *Pipeline, _ *taskstate.ReviewAttempt) { p.Steps = nil }},
		{"missing step", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Step = "" }},
		{"blank step", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Step = "  " }},
		{"unknown step", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Step = "removed" }},
		{"nonmatching whitespace", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Step = " pending " }},
		{"manual wait at check", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Status = taskstate.ReviewStatusWaitingForManual }},
		{"decision at manual step", func(p *Pipeline, _ *taskstate.ReviewAttempt) { p.Steps[1].Kind = KindManual }},
		{"unsupported step kind", func(p *Pipeline, _ *taskstate.ReviewAttempt) { p.Steps[1].Kind = "unknown" }},
		{"no findings", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Findings = nil }},
		{"other step finding", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Findings[0].Step = "first" }},
		{"advisory finding", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Findings[0].Type = taskstate.FindingTypeAdvisory }},
		{"waived finding", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Findings[0].Waiver = "accepted" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pipeline, paused := pausedExecution(KindCheck)
			tt.change(&pipeline, &paused)

			request, err := ResumeExecution(pipeline, paused)

			assert.ErrorIs(t, err, ErrInvalidExecution)
			assert.Zero(t, request)
		})
	}
}

func TestRunPipelineRejectsInvalidExecutionBeforeEffectsOrMutations(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*PipelineRunOptions, *executionStore)
	}{
		{"unspecified execution", func(o *PipelineRunOptions, _ *executionStore) { o.Execution = ExecutionRequest{} }},
		{"unknown alternative", func(o *PipelineRunOptions, _ *executionStore) { o.Execution.kind = 99 }},
		{"fresh with resume target", func(o *PipelineRunOptions, _ *executionStore) { o.Execution.kind = executionFresh }},
		{"fresh on prior execution", func(o *PipelineRunOptions, _ *executionStore) { o.Execution = FreshExecution() }},
		{"wrong attempt", func(o *PipelineRunOptions, _ *executionStore) { o.Attempt.Attempt++ }},
		{"wrong step", func(o *PipelineRunOptions, _ *executionStore) { o.Attempt.Step = "first" }},
		{"wrong pipeline", func(o *PipelineRunOptions, _ *executionStore) { o.Pipeline.Name = "other" }},
		{"waiting snapshot", func(o *PipelineRunOptions, _ *executionStore) {
			o.Attempt.Status = taskstate.ReviewStatusWaitingForAutomatedDecision
		}},
		{"no latest attempt", func(_ *PipelineRunOptions, s *executionStore) { s.state.Reviews = nil }},
		{"stale attempt", func(_ *PipelineRunOptions, s *executionStore) { s.state.Reviews[0].Attempt++ }},
		{"stale step", func(_ *PipelineRunOptions, s *executionStore) { s.state.Reviews[0].Step = "first" }},
		{"stale status", func(_ *PipelineRunOptions, s *executionStore) {
			s.state.Reviews[0].Status = taskstate.ReviewStatusPassed
		}},
		{"stale findings", func(_ *PipelineRunOptions, s *executionStore) { s.state.Reviews[0].Findings = nil }},
		{"load failure", func(_ *PipelineRunOptions, s *executionStore) { s.err = errors.New("unavailable") }},
		{"missing store", func(o *PipelineRunOptions, _ *executionStore) { o.Store = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pipeline, paused := pausedExecution(KindCheck)
			request, err := ResumeExecution(pipeline, paused)
			require.NoError(t, err)
			running := paused
			running.Status = taskstate.ReviewStatusRunning
			store := &executionStore{state: taskstate.TaskState{Reviews: []taskstate.ReviewAttempt{running}}}
			opts := PipelineRunOptions{
				Execution: request, Pipeline: pipeline, Attempt: running, Store: store,
				Effects: Effects{CaptureCandidate: func(context.Context, string, *slog.Logger, ...slog.Attr) (CandidateCheck, error) {
					t.Fatal("invalid execution captured the candidate")
					return nil, nil
				}},
				PromptAutomatedBlockers: func(AutomatedBlockerReview) ([]AutomatedBlockerDecision, error) {
					t.Fatal("invalid execution presented findings")
					return nil, nil
				},
			}
			tt.change(&opts, store)

			outcome, err := RunPipeline(opts)

			assert.ErrorIs(t, err, ErrInvalidExecution)
			assert.Empty(t, outcome.Status)
		})
	}
}

func TestFreshExecutionRequiresNewAttempt(t *testing.T) {
	pipeline := Pipeline{Name: "standard", Steps: []Step{{Name: "first", Kind: KindCheck}}}
	attempt := taskstate.ReviewAttempt{Attempt: 2, Pipeline: pipeline.Name, Step: "first", Status: taskstate.ReviewStatusRunning}
	request := FreshExecution()

	assert.NoError(t, request.ValidateRunning(pipeline, attempt))
	assert.False(t, request.Resumed())
	assert.False(t, request.ResumesAutomatedDecision())
	assert.ErrorIs(t, request.ValidateResume(pipeline, attempt), ErrInvalidExecution)
	attempt.Steps = []taskstate.ReviewStep{{Name: "first", Kind: KindCheck}}
	assert.ErrorIs(t, request.ValidateRunning(pipeline, attempt), ErrInvalidExecution)
	attempt.Steps = nil
	attempt.Findings = []taskstate.ReviewFinding{{Step: "first"}}
	assert.ErrorIs(t, request.ValidateRunning(pipeline, attempt), ErrInvalidExecution)
}

func TestResumeExecutionRejectsChangedWaitingTarget(t *testing.T) {
	pipeline, paused := pausedExecution(KindManual)
	request, err := ResumeExecution(pipeline, paused)
	require.NoError(t, err)

	for _, tt := range []struct {
		name   string
		change func(*taskstate.ReviewAttempt)
	}{
		{"attempt", func(a *taskstate.ReviewAttempt) { a.Attempt++ }},
		{"step", func(a *taskstate.ReviewAttempt) { a.Step = "first" }},
		{"pipeline", func(a *taskstate.ReviewAttempt) { a.Pipeline = "other" }},
		{"waiting interaction", func(a *taskstate.ReviewAttempt) { a.Status = taskstate.ReviewStatusWaitingForAutomatedDecision }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			latest := paused
			tt.change(&latest)

			err := request.ValidateResume(pipeline, latest)

			assert.ErrorIs(t, err, ErrInvalidExecution)
		})
	}
}

func pausedExecution(kind string) (Pipeline, taskstate.ReviewAttempt) {
	pipeline := Pipeline{Name: "standard", Steps: []Step{{Name: "first", Kind: KindCheck}, {Name: "pending", Kind: kind}}}
	attempt := taskstate.ReviewAttempt{Attempt: 2, Pipeline: pipeline.Name, Step: "pending", Status: taskstate.ReviewStatusWaitingForManual}
	if kind != KindManual {
		attempt.Status = taskstate.ReviewStatusWaitingForAutomatedDecision
		attempt.Findings = []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Step: "pending", Title: "existing finding"}}
	}
	return pipeline, attempt
}

// Any attempted mutation panics through the embedded nil store.
type executionStore struct {
	PipelineStore
	state taskstate.TaskState
	err   error
}

func (s *executionStore) Load(string, string) (taskstate.TaskState, error) {
	return s.state, s.err
}
