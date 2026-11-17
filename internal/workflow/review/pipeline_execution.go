package review

import (
	"context"
	"errors"
	"fmt"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// PipelineExecution selects one operation or attached conversation at a time.
// Scratch belongs to this invocation; a new invocation reconstructs continuation
// from the retained assessment, contract and actual command/process observations.
type PipelineExecution struct {
	opts        PipelineRunOptions
	active      *pipelineStep
	initialized bool
	start       int
	outcome     *PipelineOutcome
	pending     PipelineRequest
}

type pipelineStep struct {
	step            Step
	index           int
	check           CandidateCheck
	resumedDecision bool
	notified        bool
	opened          bool
	rendered        bool
	confirmed       *bool
	manual          manualCommand
	commandDone     bool
	commandChecked  bool
	manualPrompt    *ManualStepPrompt
	inputID         phaseexec.InputID
	checkCommand    *checkCommand
	reviewer        *reviewerCommand
	ran             bool
	decisionDone    bool
	terminal        *stepOutcome
	safetyVersion   *work.StateVersion
	verified        bool
	outputFinished  bool
}

// PipelineRequest keeps the existing specialized conversations distinct.
type PipelineRequest interface{ pipelineRequest() }
type ManualCommandRequest struct {
	ID   phaseexec.InputID
	Step Step
}
type ManualAssessmentRequest struct {
	ID     phaseexec.InputID
	Prompt ManualStepPrompt
}
type AutomatedAssessmentRequest struct {
	ID     phaseexec.InputID
	Review AutomatedBlockerReview
}

func (ManualCommandRequest) pipelineRequest()       {}
func (ManualAssessmentRequest) pipelineRequest()    {}
func (AutomatedAssessmentRequest) pipelineRequest() {}

// PipelineAnswer carries an attached conversation's result, including the
// existing distinction between missing input, cancellation and presentation errors.
type PipelineAnswer interface{ pipelineAnswer() }
type ManualCommandAnswer struct {
	Confirmed bool
	Err       error
}
type ManualAssessmentAnswer struct {
	Result ManualResult
	Err    error
}
type AutomatedAssessmentAnswer struct {
	Decisions   []AutomatedBlockerDecision
	Err         error
	Unavailable bool
}

func (ManualCommandAnswer) pipelineAnswer()       {}
func (ManualAssessmentAnswer) pipelineAnswer()    {}
func (AutomatedAssessmentAnswer) pipelineAnswer() {}

func NewPipelineExecution(opts PipelineRunOptions) *PipelineExecution {
	return &PipelineExecution{opts: normalizePipelineRunOptions(opts)}
}

func pipelineOperation(op phaseexec.Operation) phaseexec.Selection[PipelineRequest, PipelineOutcome] {
	return phaseexec.Run[PipelineRequest, PipelineOutcome](op)
}

// Next never executes effects or saves. Ordinary results are supplied directly
// by the runner; independent accepted responses retain their existing boundary.
func (s *PipelineExecution) Next(current work.State) (phaseexec.Selection[PipelineRequest, PipelineOutcome], error) {
	s.opts.current = &current
	if !s.initialized {
		if err := s.initialize(current); err != nil {
			return phaseexec.Selection[PipelineRequest, PipelineOutcome]{}, err
		}
	}

	if s.outcome != nil {
		return phaseexec.Return[PipelineRequest, PipelineOutcome](*s.outcome), nil
	}
	if s.active == nil {
		if s.start >= len(s.opts.Pipeline.Steps) {
			return phaseexec.Return[PipelineRequest, PipelineOutcome](PipelineOutcome{Status: taskstate.ReviewStatusPassed}), nil
		}
		s.active = &pipelineStep{step: s.opts.Pipeline.Steps[s.start], index: s.start, resumedDecision: s.opts.Execution.ResumesAutomatedDecision()}
	}
	a := s.active
	if a.terminal != nil {
		return s.finishStep(current)
	}
	if a.resumedDecision {
		return s.nextAutomatedDecision(current)
	}
	if a.check == nil {
		return pipelineOperation(s.captureOperation()), nil
	}
	switch a.step.Kind {
	case KindCheck:
		return s.nextCheck(current)
	case KindAgentReview:
		return s.nextReviewer(current)
	case KindManual:
		return s.nextManual(current)
	default:
		return phaseexec.Selection[PipelineRequest, PipelineOutcome]{}, fmt.Errorf("task run %s: review step %q has unsupported kind %q", s.opts.TaskID, a.step.Name, a.step.Kind)
	}
}

func (s *PipelineExecution) await(request PipelineRequest) phaseexec.Selection[PipelineRequest, PipelineOutcome] {
	s.pending = request
	return phaseexec.Await[PipelineRequest, PipelineOutcome](request)
}

func pipelineInputID(request PipelineRequest) phaseexec.InputID {
	switch request := request.(type) {
	case ManualCommandRequest:
		return request.ID
	case ManualAssessmentRequest:
		return request.ID
	case AutomatedAssessmentRequest:
		return request.ID
	default:
		return phaseexec.InputID{}
	}
}

func (s *PipelineExecution) initialize(current work.State) error {
	if err := validatePipelineExecution(s.opts, current); err != nil {
		return err
	}
	if s.opts.Execution.Resumed() {
		index, err := pipelineStartIndex(s.opts.Pipeline, s.opts.Execution.step)
		if err != nil {
			return err
		}
		s.start = index
		if s.opts.Execution.kind == executionContinue {
			s.start++
		}
	}
	s.initialized = true
	return nil
}

func (s *PipelineExecution) captureOperation() phaseexec.Operation {
	a, opts := s.active, s.opts
	return phaseexec.Operation{Name: "admit review step checkout", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		candidate, err := CommittedInput(taskstate.TaskState{Work: current}, opts.Attempt.Address)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			capture := opts.Effects.CaptureCandidate
			if capture == nil {
				capture = captureLocalCandidate
			}
			check, err := capture(ctx, opts.Workdir, candidate, opts.Logger, reviewStepAttrs(opts, a.step)...)
			if err != nil {
				cause := fmt.Errorf("admit committed review revision: %w", err)
				return phaseexec.Result{Update: AdmissionFailureChange(opts.Attempt.Address, cause), Err: cause}
			}
			a.check = check
			return phaseexec.Result{}
		}}, nil
	}}
}

func (s *PipelineExecution) nextCheck(current work.State) (phaseexec.Selection[PipelineRequest, PipelineOutcome], error) {
	a := s.active
	if !a.ran {
		a.checkCommand = &checkCommand{opts: s.opts, step: a.step, env: stepEnvironment(s.opts, a.step.Name, agent.RenderBootstrapPrompt())}
		op := a.checkCommand.operation()
		return pipelineOperation(afterEffect(op, func() { a.ran = true })), nil
	}
	c := a.checkCommand
	if !a.commandChecked {
		return pipelineOperation(s.verifyOperation(func() { a.commandChecked = true }, false)), nil
	}
	if !a.outputFinished {
		mode := OutputExpanded
		if c.err == nil {
			mode = OutputClear
		}
		return pipelineOperation(s.noticeOperation(func() error { c.output.finish(mode); return nil }, func() { a.outputFinished = true })), nil
	}
	if c.err == nil {
		a.terminal = &stepOutcome{}
		return s.finishStep(current)
	}
	if c.exitErr == nil {
		return phaseexec.Selection[PipelineRequest, PipelineOutcome]{}, fmt.Errorf("task run %s: start check step %q: %w", s.opts.TaskID, a.step.Name, c.err)
	}
	return s.nextAutomatedDecision(current)
}

func (s *PipelineExecution) nextReviewer(current work.State) (phaseexec.Selection[PipelineRequest, PipelineOutcome], error) {
	a := s.active
	if !a.ran {
		if s.opts.AgentLauncher == nil {
			return phaseexec.Selection[PipelineRequest, PipelineOutcome]{}, fmt.Errorf("task run %s: agent_review step %q requires an agent launcher", s.opts.TaskID, a.step.Name)
		}
		prepared, err := agent.PrepareReview(s.opts.AgentConfig, agent.ReviewPreparation{Profile: a.step.Agent, SessionName: s.opts.SessionName, Attempt: s.opts.Attempt.Attempt, Step: a.step.Name})
		if err != nil {
			return phaseexec.Selection[PipelineRequest, PipelineOutcome]{}, fmt.Errorf("task run %s: resolve agent_review step %q: %w", s.opts.TaskID, a.step.Name, err)
		}
		if s.opts.AgentResults.Commit == nil {
			return phaseexec.Selection[PipelineRequest, PipelineOutcome]{}, errors.New("review agent result recorder is required")
		}
		a.reviewer = &reviewerCommand{}
		return pipelineOperation(afterEffect(a.reviewer.operation(s.opts, a.step, prepared, a.check), func() { a.ran = true })), nil
	}
	latest, err := pipelineReview(s.opts, s.opts.Attempt.Address)
	if err != nil {
		return phaseexec.Selection[PipelineRequest, PipelineOutcome]{}, err
	}
	if len(automatedBlockersForStep(latest, a.step.Name)) > 0 {
		if !a.outputFinished {
			return pipelineOperation(s.noticeOperation(func() error { a.reviewer.output.finish(OutputExpanded); return nil }, func() { a.outputFinished = true })), nil
		}
		return s.nextAutomatedDecision(current)
	}
	mode := OutputClear
	for _, finding := range latest.Findings {
		if finding.Step == a.step.Name {
			mode = OutputTail
			break
		}
	}
	if !a.outputFinished {
		return pipelineOperation(s.noticeOperation(func() error { a.reviewer.output.finish(mode); return nil }, func() { a.outputFinished = true })), nil
	}
	a.terminal = &stepOutcome{}
	return s.finishStep(current)
}

func (s *PipelineExecution) nextAutomatedDecision(current work.State) (phaseexec.Selection[PipelineRequest, PipelineOutcome], error) {
	a := s.active
	if a.resumedDecision && !a.notified {
		return pipelineOperation(s.noticeOperation(func() error { return notifyStepStarted(s.opts, a.step) }, func() { a.notified = true })), nil
	}
	if a.decisionDone {
		latest, err := pipelineReview(s.opts, s.opts.Attempt.Address)
		if err != nil {
			return phaseexec.Selection[PipelineRequest, PipelineOutcome]{}, err
		}
		blocked := taskstate.ReviewHasOpenBlockers(latest)
		a.terminal = &stepOutcome{status: taskstate.ReviewStatusBlocked, stop: blocked}
		if !blocked {
			a.terminal.status = ""
		}
		if blocked && !a.resumedDecision {
			return pipelineOperation(s.noticeOperation(func() error { return reportNotice(s.opts, NoticeBlocked, a.step) }, nil)), nil
		}
		return s.finishStep(current)
	}
	latest, err := pipelineReview(s.opts, s.opts.Attempt.Address)
	if err != nil {
		return phaseexec.Selection[PipelineRequest, PipelineOutcome]{}, err
	}
	blockers := automatedBlockersForStep(latest, a.step.Name)
	if len(blockers) == 0 {
		a.decisionDone = true
		return s.nextAutomatedDecision(current)
	}
	owner := latest.Steps[len(latest.Steps)-1].Address
	id := phaseexec.InputID{Owner: owner, Version: current.Version, Kind: FindingsInputKind}
	return s.await(AutomatedAssessmentRequest{ID: id, Review: AutomatedBlockerReview{Step: a.step, Blockers: blockers}}), nil
}

func (s *PipelineExecution) nextManual(current work.State) (phaseexec.Selection[PipelineRequest, PipelineOutcome], error) {
	a := s.active
	if !a.opened {
		entry := s.opts.Attempt.Address
		if s.opts.Execution.kind == executionResumeStep {
			entry = s.opts.Execution.owner
		}
		return pipelineOperation(s.localOperation("open manual assessment", manualEntryChange(entry, a.step.Name), func() { a.opened = true })), nil
	}
	pending, err := pipelineReview(s.opts, s.opts.Attempt.Address)
	if err != nil {
		return phaseexec.Selection[PipelineRequest, PipelineOutcome]{}, err
	}
	owner := pending.Steps[len(pending.Steps)-1].Address
	s.opts.Attempt = pending
	s.opts.Attempt.Address = owner
	if !a.notified {
		return pipelineOperation(s.noticeOperation(func() error { return notifyStepStarted(s.opts, a.step) }, func() { a.notified = true })), nil
	}
	if s.opts.PauseBeforeManual {
		a.terminal = &stepOutcome{status: taskstate.ReviewStatusWaitingForManual, stop: true}
		return pipelineOperation(s.noticeOperation(func() error { return reportNotice(s.opts, NoticeManualPending, a.step) }, nil)), nil
	}
	if s.opts.RenderManualStep == nil || s.opts.PromptManualStep == nil {
		return phaseexec.Selection[PipelineRequest, PipelineOutcome]{}, fmt.Errorf("task run %s: manual step %q requires manual review hooks", s.opts.TaskID, a.step.Name)
	}
	if !a.rendered {
		return pipelineOperation(s.renderManualOperation(current)), nil
	}
	if a.step.Command != "" && a.confirmed == nil {
		return s.await(ManualCommandRequest{ID: phaseexec.InputID{Owner: owner, Version: current.Version, Kind: ManualInputKind}, Step: a.step}), nil
	}
	if a.step.Command != "" && *a.confirmed && !a.commandDone {
		env := stepEnvironment(s.opts, a.step.Name, agent.RenderBootstrapPrompt())
		return pipelineOperation(afterEffect(a.manual.operation(s.opts, a.step, env), func() { a.commandDone = true })), nil
	}
	if !a.commandChecked {
		return pipelineOperation(s.verifyOperation(func() { a.commandChecked = true }, false)), nil
	}
	if a.confirmed != nil && !*a.confirmed {
		op, err := s.responseOperation(phaseexec.InputID{Owner: owner, Version: current.Version, Kind: ManualInputKind}, ManualSubmission{Decision: ManualAbort}, stepOutcome{status: taskstate.ReviewStatusAborted, stop: true})
		return pipelineOperation(op), err
	}
	prompt, id, err := manualAssessmentPrompt(s.opts, owner, a.step, a.manual.notes)
	if err != nil {
		return phaseexec.Selection[PipelineRequest, PipelineOutcome]{}, err
	}
	a.manualPrompt, a.inputID = &prompt, id
	return s.await(ManualAssessmentRequest{ID: id, Prompt: prompt}), nil
}

func (s *PipelineExecution) renderManualOperation(current work.State) phaseexec.Operation {
	a, opts := s.active, s.opts
	return s.noticeOperation(func() error {
		manual, err := manualStepContext(opts.Context, current, opts, a.step)
		if err == nil {
			err = opts.RenderManualStep(manual)
		}
		if err != nil {
			return fmt.Errorf("%w: task run %s: %w", ErrPresentationUnavailable, opts.TaskID, err)
		}
		return nil
	}, func() { a.rendered = true })
}

func afterEffect(op phaseexec.Operation, done func()) phaseexec.Operation {
	prepare := op.Prepare
	op.Prepare = func(current *work.State) (phaseexec.Prepared, error) {
		prepared, err := prepare(current)
		if err != nil {
			return prepared, err
		}
		run := prepared.Run
		prepared.Run = func(ctx context.Context) phaseexec.Result {
			result := run(ctx)
			if result.Err == nil {
				done()
			}
			return result
		}
		return prepared, nil
	}
	return op
}

func (s *PipelineExecution) localOperation(name string, change phaseexec.Change, done func()) phaseexec.Operation {
	return phaseexec.Operation{Name: name, Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		changed, err := change(current)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		return phaseexec.Prepared{Changed: changed, Run: func(context.Context) phaseexec.Result {
			if done != nil {
				done()
			}
			return phaseexec.Result{}
		}}, nil
	}}
}

func (s *PipelineExecution) noticeOperation(effect func() error, done func()) phaseexec.Operation {
	return phaseexec.Operation{Name: "present review assessment", Prepare: func(*work.State) (phaseexec.Prepared, error) {
		return phaseexec.Prepared{Run: func(context.Context) phaseexec.Result {
			err := effect()
			if err == nil && done != nil {
				done()
			}
			return phaseexec.Result{Err: err}
		}}, nil
	}}
}

func (s *PipelineExecution) verifyOperation(done func(), record bool) phaseexec.Operation {
	a, opts := s.active, s.opts
	return phaseexec.Operation{Name: "verify read-only review checkout", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		var owner work.AttemptRef
		latest, err := pipelineReview(opts, opts.Attempt.Address)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		if len(latest.Steps) > 0 && latest.Steps[len(latest.Steps)-1].Name == a.step.Name {
			owner = latest.Steps[len(latest.Steps)-1].Address
		}
		version := current.Version
		return phaseexec.Prepared{Run: func(context.Context) phaseexec.Result {
			err := a.check()
			if err == nil {
				if done != nil {
					done()
				}
				return phaseexec.Result{}
			}
			if !record {
				return phaseexec.Result{Err: err}
			}
			a.safetyVersion = &version
			change := AdmissionFailureChange(opts.Attempt.Address, err)
			if owner.RepositoryID != "" {
				change = ReadOnlyFailureChange(owner, err)
			}
			return phaseexec.Result{Update: change, Err: err}
		}}, nil
	}}
}

func (s *PipelineExecution) finishStep(current work.State) (phaseexec.Selection[PipelineRequest, PipelineOutcome], error) {
	a := s.active
	if a.check != nil && !a.verified {
		return pipelineOperation(s.verifyOperation(func() { a.verified = true }, true)), nil
	}
	result := *a.terminal
	if result.stop {
		s.outcome = &PipelineOutcome{Status: result.status}
		return phaseexec.Return[PipelineRequest, PipelineOutcome](*s.outcome), nil
	}
	s.start = a.index + 1
	if result.restart {
		s.start = a.index
	}
	s.opts.Attempt.Address.Step = nil
	s.opts.Execution = FreshExecution()
	s.active = nil
	return s.Next(current)
}

// Respond translates an attached answer into one operation. It does not save,
// prompt, run commands or accept partially edited manual data.
func (s *PipelineExecution) Respond(current work.State, id phaseexec.InputID, answer PipelineAnswer) (phaseexec.Operation, error) {
	s.opts.current = &current

	expected := pipelineInputID(s.pending)
	if s.active == nil || s.pending == nil || current.Version != id.Version || expected.Version != id.Version || expected.Kind != id.Kind || !expected.Owner.Equal(id.Owner) {
		return phaseexec.Operation{}, phaseexec.ErrStaleInput
	}

	_, phase, err := currentReview(&current, id.Owner)
	if err != nil {
		return phaseexec.Operation{}, err
	}
	if id.Owner.Step == nil || *id.Owner.Step != len(phase.Steps)-1 {
		return phaseexec.Operation{}, phaseexec.ErrStaleInput
	}
	switch answer := answer.(type) {
	case ManualCommandAnswer:
		if _, ok := s.pending.(ManualCommandRequest); !ok {
			return phaseexec.Operation{}, phaseexec.ErrStaleInput
		}
		if answer.Err != nil {
			return s.manualInputFailure(answer.Err)
		}
		confirmed := answer.Confirmed
		return s.noticeOperation(func() error { return nil }, func() { s.active.confirmed = &confirmed }), nil
	case ManualAssessmentAnswer:
		if _, ok := s.pending.(ManualAssessmentRequest); !ok {
			return phaseexec.Operation{}, phaseexec.ErrStaleInput
		}
		return s.manualAnswer(id, answer)
	case AutomatedAssessmentAnswer:
		if _, ok := s.pending.(AutomatedAssessmentRequest); !ok {
			return phaseexec.Operation{}, phaseexec.ErrStaleInput
		}
		return s.automatedAnswer(current, id, answer)
	default:
		return phaseexec.Operation{}, errors.New("answer does not belong to a pipeline interaction")
	}
}

func (s *PipelineExecution) manualInputFailure(err error) (phaseexec.Operation, error) {
	if errors.Is(err, ErrInteractionRequired) {
		return phaseexec.Operation{}, err
	}
	if errors.Is(err, ErrManualInputUnavailable) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || s.opts.Context.Err() != nil {
		latest, readErr := pipelineReview(s.opts, s.opts.Attempt.Address)
		if readErr != nil {
			return phaseexec.Operation{}, readErr
		}
		if latest.Status != taskstate.ReviewStatusFailed {
			return s.terminalOperation(stepOutcome{status: taskstate.ReviewStatusWaitingForManual, stop: true}), nil
		}
	}
	return phaseexec.Operation{}, err
}

func (s *PipelineExecution) terminalOperation(outcome stepOutcome) phaseexec.Operation {
	return s.noticeOperation(func() error { return nil }, func() { s.active.terminal = &outcome })
}

func (s *PipelineExecution) manualAnswer(id phaseexec.InputID, answer ManualAssessmentAnswer) (phaseexec.Operation, error) {
	if answer.Err != nil {
		return s.manualInputFailure(fmt.Errorf("%w: %w", ErrPresentationUnavailable, answer.Err))
	}
	result := answer.Result
	if result.Status == taskstate.ReviewStatusWaitingForManual {
		return s.terminalOperation(stepOutcome{status: result.Status, stop: true}), nil
	}
	if s.active.manualPrompt == nil || (s.active.inputID.Version != id.Version || s.active.inputID.Kind != id.Kind || !s.active.inputID.Owner.Equal(id.Owner)) {
		return phaseexec.Operation{}, phaseexec.ErrStaleInput
	}
	decision, err := result.Decision()
	if err != nil {
		return phaseexec.Operation{}, err
	}
	submission, err := s.active.manualPrompt.Draft.Submit(decision)
	if err != nil {
		return phaseexec.Operation{}, err
	}
	return s.responseOperation(id, submission, stepOutcome{status: result.Status, stop: result.Stop})
}

func (s *PipelineExecution) responseOperation(id phaseexec.InputID, answer Answer, outcome stepOutcome) (phaseexec.Operation, error) {
	a, opts := s.active, s.opts
	if opts.ValidateAnswer == nil {
		return phaseexec.Operation{}, errors.New("review response boundary is required")
	}
	responses := Assessments{Paths: opts.Runner.Paths, Repository: opts.Source.Repository, Git: opts.CandidateGit}
	op, err := responses.Respond(*opts.current, id, answer)
	if err != nil {
		return phaseexec.Operation{}, errors.Join(ErrInvalidExecution, err)
	}
	prepare := op.Prepare
	op.Prepare = func(current *work.State) (phaseexec.Prepared, error) {
		prepared, err := prepare(current)
		if err != nil {
			return prepared, errors.Join(ErrInvalidExecution, err)
		}
		run := prepared.Run
		prepared.Run = func(ctx context.Context) phaseexec.Result {
			if a.check != nil {
				if err := a.check(); err != nil {
					return phaseexec.Result{Err: err}
				}
			}
			// After a long conversation, refresh the task source and validate the
			// original token before any revision read or accepted submission save.
			if err := opts.ValidateAnswer(ctx, id); err != nil {
				return phaseexec.Result{Err: errors.Join(ErrInvalidExecution, err)}
			}
			result := run(ctx)
			if result.Err != nil {
				result.Err = errors.Join(ErrInvalidExecution, result.Err)
				return result
			}
			if _, automated := answer.(FindingsAnswer); automated {
				a.decisionDone = true
			} else {
				a.terminal = &outcome
			}
			return result
		}
		return prepared, nil
	}
	return op, nil
}

func (s *PipelineExecution) automatedAnswer(current work.State, id phaseexec.InputID, answer AutomatedAssessmentAnswer) (phaseexec.Operation, error) {
	if errors.Is(answer.Err, ErrInteractionRequired) {
		return phaseexec.Operation{}, answer.Err
	}
	if s.opts.Context.Err() != nil || errors.Is(answer.Err, context.Canceled) || errors.Is(answer.Err, context.DeadlineExceeded) {
		return s.waitForAutomatedDecision()
	}
	if answer.Unavailable || errors.Is(answer.Err, ErrAutomatedBlockerInputUnavailable) {
		return s.pauseAutomatedOperation(id.Owner, work.DecisionInputUnavailable, NoticeDecisionInterrupted, Step{}), nil
	}
	if answer.Err != nil {
		return phaseexec.Operation{}, fmt.Errorf("%w: task run %s: review automated blockers: %w", ErrPresentationUnavailable, s.opts.TaskID, answer.Err)
	}
	action, control, err := automatedBlockerControlAction(answer.Decisions)
	if err != nil {
		return phaseexec.Operation{}, fmt.Errorf("task run %s: review automated blockers: %w", s.opts.TaskID, err)
	}
	if control {
		if action == AutomatedBlockerActionRestart {
			return s.terminalOperation(stepOutcome{restart: true}), nil
		}
		return s.pauseAutomatedOperation(id.Owner, work.DecisionPaused, NoticeDecisionPaused, s.active.step), nil
	}
	if s.opts.ValidateAnswer == nil && len(answer.Decisions) == 0 {
		return s.noticeOperation(func() error { return nil }, func() { s.active.decisionDone = true }), nil
	}
	latest, err := pipelineReview(s.opts, s.opts.Attempt.Address)
	if err != nil {
		return phaseexec.Operation{}, err
	}
	decisions, err := FindingAnswers(latest, answer.Decisions)
	if err != nil {
		return phaseexec.Operation{}, err
	}
	return s.responseOperation(id, decisions, stepOutcome{})
}

func (s *PipelineExecution) waitForAutomatedDecision() (phaseexec.Operation, error) {
	return s.terminalOperation(stepOutcome{status: taskstate.ReviewStatusWaitingForAutomatedDecision, stop: true}), nil
}

func (s *PipelineExecution) pauseAutomatedOperation(owner work.AttemptRef, kind work.AutomatedDecisionPauseKind, notice Notice, step Step) phaseexec.Operation {
	op := s.localOperation("retain finding decision pause", automatedPauseChange(owner, kind), nil)
	prepare := op.Prepare
	op.Prepare = func(current *work.State) (phaseexec.Prepared, error) {
		prepared, err := prepare(current)
		if err != nil {
			return prepared, err
		}
		prepared.Run = func(context.Context) phaseexec.Result {
			err := reportNotice(s.opts, notice, step)
			if err == nil {
				s.active.terminal = &stepOutcome{status: taskstate.ReviewStatusWaitingForAutomatedDecision, stop: true}
			}
			return phaseexec.Result{Err: err}
		}
		return prepared, nil
	}
	return op
}
