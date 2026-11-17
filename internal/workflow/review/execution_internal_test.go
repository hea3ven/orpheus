package review

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
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
			assert.NoError(t, request.ValidateRunning(pipeline, paused), "waiting remains a derived boundary, not a flag to rewrite")
			running := paused
			running.Status = taskstate.ReviewStatusRunning
			assert.ErrorIs(t, request.ValidateRunning(pipeline, running), ErrInvalidExecution)
			assert.ErrorIs(t, request.ValidateResume(pipeline, running), ErrInvalidExecution)
			_, err = ResumeExecution(pipeline, running)
			assert.ErrorIs(t, err, ErrInvalidExecution, "a non-waiting view cannot claim a pending interaction")
		})
	}
}

func TestResumeExecutionRejectsInvalidWaitingState(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Pipeline, *taskstate.ReviewAttempt)
	}{
		{"missing attempt", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Attempt = 0 }},
		{"missing owner", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Address.RepositoryID = "" }},
		{"no actual pending step", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Steps = nil }},
		{"missing pipeline", func(p *Pipeline, _ *taskstate.ReviewAttempt) { p.Name = "" }},
		{"different pipeline", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Pipeline = "other" }},
		{"empty pipeline", func(p *Pipeline, _ *taskstate.ReviewAttempt) { p.Steps = nil }},
		{"missing step", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Step = "" }},
		{"blank step", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Step = "  " }},
		{"unknown step", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Step = "removed" }},
		{"nonmatching whitespace", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Step = " pending " }},
		{"manual wait at check", func(_ *Pipeline, a *taskstate.ReviewAttempt) { a.Status = taskstate.ReviewStatusWaitingForManual }},
		{"check changed to reviewer", func(p *Pipeline, _ *taskstate.ReviewAttempt) { p.Steps[1].Kind = KindAgentReview }},
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
		{"different waiting interaction", func(o *PipelineRunOptions, _ *executionStore) {
			o.Attempt.Status = taskstate.ReviewStatusWaitingForManual
		}},
		{"no latest attempt", func(_ *PipelineRunOptions, s *executionStore) {
			s.state.ActiveWorkflow().Phases = s.state.ActiveWorkflow().Phases[:2]
		}},
		{"stale attempt", func(_ *PipelineRunOptions, s *executionStore) {
			w := s.state.ActiveWorkflow()
			w.Phases = append(w.Phases, &work.ReviewPhase{Contract: 0})
		}},
		{"stale step", func(_ *PipelineRunOptions, s *executionStore) { s.pending().Slot = 0 }},
		{"stale status", func(_ *PipelineRunOptions, s *executionStore) {
			zero := 0
			s.pending().Result.ExitCode = &zero
			s.pending().Findings = nil
		}},
		{"stale findings", func(_ *PipelineRunOptions, s *executionStore) { s.pending().Findings = nil }},
		{"load failure", func(_ *PipelineRunOptions, s *executionStore) { s.err = errors.New("unavailable") }},
		{"missing runner", func(o *PipelineRunOptions, _ *executionStore) { o.Runner.Store = nil }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pipeline, _ := pausedExecution(KindCheck)
			store := &executionStore{state: testutil.ReviewableWork("alpha", "task", "/fixture/work", "task-branch")}
			w := store.state.ActiveWorkflow()
			zero, one := 0, 1
			w.ReviewContracts = []work.ReviewContract{{PipelineKey: "standard", Slots: []work.ReviewSlot{{Kind: work.ReviewCheck, ConfigKey: "first"}, {Kind: work.ReviewCheck, ConfigKey: "pending"}}}}
			at := time.Date(2026, 1, 1, 0, 1, 0, 0, time.UTC)
			w.Phases = append(w.Phases, &work.ReviewPhase{Contract: 0, StartedAt: at, Scope: work.ReviewScope{Requirements: "Task requirements", Output: work.OutputRef{Phase: 1}, Destination: &work.Destination{Remote: "origin", Branch: "main", Commit: "destination"}}, Steps: []work.ReviewStep{
				&work.CheckStep{Slot: 0, Result: &work.CheckResult{ExitCode: &zero, FinishedAt: at}},
				&work.CheckStep{Slot: 1, Result: &work.CheckResult{ExitCode: &one, FinishedAt: at}, Findings: []work.Finding{{Classification: work.FindingBlocking, Title: "existing finding", Description: "Required correction", Dispositions: []work.FindingDisposition{&work.RepairRequired{}}}}},
			}})
			projected, err := taskstate.Project(store.state)
			require.NoError(t, err)
			running, ok := taskstate.LatestReview(projected)
			require.True(t, ok)
			request, err := ResumeExecution(pipeline, running)
			require.NoError(t, err)
			opts := PipelineRunOptions{
				Execution: request, Pipeline: pipeline, Attempt: running, Runner: phaseexec.Runner{Store: store},
				Effects: Effects{CaptureCandidate: func(context.Context, string, CommittedCandidate, *slog.Logger, ...slog.Attr) (CandidateCheck, error) {
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
	attempt := taskstate.ReviewAttempt{Address: work.AttemptRef{RepositoryID: "alpha", TaskID: "task", Phase: 2}, Attempt: 2, Pipeline: pipeline.Name, Step: "first", Status: taskstate.ReviewStatusRunning}
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
	independent := paused
	independent.Steps = append([]taskstate.ReviewStep(nil), paused.Steps...)
	copyOf := *paused.Steps[len(paused.Steps)-1].Address.Step
	independent.Steps[len(independent.Steps)-1].Address.Step = &copyOf
	require.NoError(t, request.ValidateResume(pipeline, independent), "independently decoded equal addresses have different pointer allocations")
	for _, tt := range []struct {
		name   string
		change func(*taskstate.ReviewAttempt)
	}{
		{"attempt", func(a *taskstate.ReviewAttempt) { a.Attempt++ }},
		{"workflow", func(a *taskstate.ReviewAttempt) { a.Address.Workflow++ }},
		{"same name new owned step", func(a *taskstate.ReviewAttempt) {
			next := a.Steps[len(a.Steps)-1]
			index := len(a.Steps)
			next.Address.Step = &index
			a.Steps = append(append([]taskstate.ReviewStep(nil), a.Steps...), next)
		}},
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
	attempt := taskstate.ReviewAttempt{Address: work.AttemptRef{RepositoryID: "alpha", TaskID: "task", Phase: 2}, Attempt: 2, Pipeline: pipeline.Name, Step: "pending", Status: taskstate.ReviewStatusWaitingForManual}
	if kind != KindManual {
		attempt.Status = taskstate.ReviewStatusWaitingForAutomatedDecision
		attempt.Findings = []taskstate.ReviewFinding{{Type: taskstate.FindingTypeBlocking, Step: "pending", Title: "existing finding"}}
	}
	first, pending := 0, 1
	firstRef, pendingRef := attempt.Address, attempt.Address
	firstRef.Step, pendingRef.Step = &first, &pending
	attempt.Steps = []taskstate.ReviewStep{{Address: firstRef, Name: "first", Kind: KindCheck}, {Address: pendingRef, Name: "pending", Kind: kind}}
	return pipeline, attempt
}

// Any attempted mutation panics through the embedded nil store.
type executionStore struct {
	phaseexec.Store
	state work.State
	err   error
}

func (s *executionStore) Load(string, string) (work.State, error) { return s.state, s.err }
func (s *executionStore) pending() *work.CheckStep {
	return s.state.ActiveWorkflow().LastPhase().(*work.ReviewPhase).Steps[1].(*work.CheckStep)
}
