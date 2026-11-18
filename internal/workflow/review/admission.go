package review

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// Admission owns checkout admission, prior-finding input, reservation and
// executable configuration of the selected Review.
type Admission struct {
	Paths            state.Paths
	Repository       task.Repository
	Task             task.Task
	Git              AdmissionGit
	HasAgentLauncher bool
	ReportSelection  func(RevisionSelectionNotice) error

	checkout   *admittedCheckout
	prior      PriorFindings
	configured *AdmittedReview
}

// AdmissionIntent authorizes either initialization after captured implementation
// output, or continuation of the selected Review. It is never persisted.
type AdmissionIntent struct {
	Owner        work.AttemptRef
	Resume       bool
	PipelineName string
}

type AdmissionOutcome interface{ admissionOutcome() }

// AdmissionBlocked returns the preserved finding stop boundary to workflow.
type AdmissionBlocked struct {
	Target      tasktarget.Target
	Pipeline    Pipeline
	Interrupted bool
}

func (AdmissionBlocked) admissionOutcome() {}

// AdmittedReview identifies the exact executable Review and its target.
type AdmittedReview struct {
	Target      tasktarget.Target
	Review      taskstate.ReviewAttempt
	Pipeline    Pipeline
	AgentConfig agent.Config
	Execution   ExecutionRequest
}

// RevisionSelectionNotice describes a fresh review of a different commit than
// the implementation handoff. The report is invocation-local.
type RevisionSelectionNotice struct {
	Implementation string
	Selection      gitmeta.ReviewRevisionSelection
}

func (AdmittedReview) admissionOutcome() {}

// Next configures the selected execution, initializes Review when necessary,
// and returns its admitted state without entering another phase.
func (s *Admission) Next(current work.State, intent AdmissionIntent) (phaseexec.Selection[PriorFindingsRequest, AdmissionOutcome], error) {
	if s.checkout == nil {
		return phaseexec.Run[PriorFindingsRequest, AdmissionOutcome](s.inspect(intent)), nil
	}
	if !intent.Resume && isAdmissionInput(current, intent.Owner) {
		selected, err := s.prior.Next(current, intent.Owner)
		if err != nil {
			return phaseexec.Selection[PriorFindingsRequest, AdmissionOutcome]{}, err
		}
		if op, ok := selected.Operation(); ok {
			return phaseexec.Run[PriorFindingsRequest, AdmissionOutcome](op), nil
		}
		if request, ok := selected.Input(); ok {
			return phaseexec.Await[PriorFindingsRequest, AdmissionOutcome](request), nil
		}
		if outcome, _ := selected.Outcome(); outcome.Blocked || outcome.Interrupted {
			return phaseexec.Return[PriorFindingsRequest, AdmissionOutcome](AdmissionBlocked{Target: s.checkout.target, Pipeline: s.checkout.pipeline, Interrupted: outcome.Interrupted}), nil
		}
	}
	if s.configured == nil {
		return phaseexec.Run[PriorFindingsRequest, AdmissionOutcome](s.configure(intent)), nil
	}
	if !intent.Resume && isAdmissionInput(current, intent.Owner) {
		return phaseexec.Run[PriorFindingsRequest, AdmissionOutcome](s.initialize(intent.Owner)), nil
	}
	owner := intent.Owner
	if !intent.Resume {
		owner.Phase++
	}
	owner.Step = nil
	latest, err := reviewAt(current, owner)
	if err != nil {
		return phaseexec.Selection[PriorFindingsRequest, AdmissionOutcome]{}, err
	}
	ready := *s.configured
	if err := ready.Execution.ValidateRunning(ready.Pipeline, latest); err != nil {
		return phaseexec.Selection[PriorFindingsRequest, AdmissionOutcome]{}, err
	}
	ready.Review = latest
	return phaseexec.Return[PriorFindingsRequest, AdmissionOutcome](ready), nil
}

func (s *Admission) Respond(current work.State, id phaseexec.InputID, answer PriorFindingsAnswer) (phaseexec.Operation, error) {
	return s.prior.Respond(current, id, answer)
}

func isAdmissionInput(current work.State, owner work.AttemptRef) bool {
	w := current.ActiveWorkflow()
	return w != nil && owner.RepositoryID == current.RepositoryID && owner.TaskID == current.TaskID && owner.Workflow == len(current.Workflows)-1 && owner.Phase == len(w.Phases)-1 && owner.BranchSync == nil
}

func validateAdmissionInput(current work.State, owner work.AttemptRef) error {
	if !isAdmissionInput(current, owner) {
		return errors.New("review advancement is stale; current work changed")
	}
	switch phase := current.ActiveWorkflow().LastPhase().(type) {
	case *work.ImplementationPhase:
		if owner.Step == nil && phase.Output != nil {
			return nil
		}
	case *work.ReviewPhase:
		_, _, err := currentReview(&current, owner)
		return err
	}
	return errors.New("review requires captured implementation output")
}

func implementationOutputForAdmission(w *work.Workflow, owner work.AttemptRef) work.OutputRef {
	if prior, ok := w.LastPhase().(*work.ReviewPhase); ok {
		return prior.Scope.Output
	}
	return work.OutputRef{Phase: owner.Phase}
}

func (s *Admission) configure(intent AdmissionIntent) phaseexec.Operation {
	return phaseexec.Operation{Name: "configure selected review", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		var latest taskstate.ReviewAttempt
		if intent.Resume {
			var err error
			latest, err = reviewAt(*current, intent.Owner)
			if err != nil {
				return phaseexec.Prepared{}, err
			}
		} else if err := validateAdmissionInput(*current, intent.Owner); err != nil {
			return phaseexec.Prepared{}, err
		}
		return phaseexec.Prepared{Run: func(context.Context) phaseexec.Result {
			ready, err := s.configuration(intent, latest)
			if err == nil {
				s.configured = &ready
			}
			return phaseexec.Result{Err: err}
		}}, nil
	}}
}

func (s *Admission) configuration(intent AdmissionIntent, paused taskstate.ReviewAttempt) (AdmittedReview, error) {
	if !intent.Resume {
		return s.prepareConfiguration(s.checkout.pipeline, s.checkout.pipeline, FreshExecution())
	}
	pipeline, err := ResolvePausedTaskReviewPipeline(s.Paths, s.Repository, paused, intent.PipelineName)
	if err != nil {
		return AdmittedReview{}, fmt.Errorf("task run %s: %w", s.Task.ID, err)
	}
	start, err := ContinuationIndex(pipeline, paused)
	if err != nil {
		return AdmittedReview{}, err
	}
	remaining := pipeline
	remaining.Steps = pipeline.Steps[start:]
	request := FreshExecution()
	switch {
	case len(paused.Steps) == 0:
		pipeline = remaining
	case paused.Status == taskstate.ReviewStatusRunning:
		remaining.Steps = pipeline.Steps[start+1:]
		request, err = ContinueExecution(pipeline, paused)
	default:
		request, err = ResumeExecution(pipeline, paused)
	}
	if err != nil {
		return AdmittedReview{}, err
	}
	return s.prepareConfiguration(pipeline, remaining, request)
}

func (s *Admission) prepareConfiguration(pipeline, remaining Pipeline, request ExecutionRequest) (AdmittedReview, error) {
	ready := AdmittedReview{Target: s.checkout.target, Pipeline: pipeline, Execution: request}
	config, err := ResolveReviewAgentConfig(s.Paths, remaining)
	if err != nil {
		return ready, fmt.Errorf("task run %s: %w", s.Task.ID, err)
	}
	if UsesAgentReview(remaining) && !s.HasAgentLauncher {
		return ready, fmt.Errorf("task run %s: review agent launcher is required", s.Task.ID)
	}
	ready.AgentConfig = config
	return ready, nil
}

func (s *Admission) initialize(owner work.AttemptRef) phaseexec.Operation {
	return phaseexec.Operation{Name: "reserve task review", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		if err := validateAdmissionInput(*current, owner); err != nil {
			return phaseexec.Prepared{}, err
		}
		w := current.ActiveWorkflow()
		output := implementationOutputForAdmission(w, owner)
		effort := w.Phases[output.Phase].(*work.ImplementationPhase)
		destinationBranch := s.Repository.DefaultBranch
		if fixed := w.FixedDestination(); fixed != nil {
			destinationBranch = fixed.Branch
		}
		version := current.Version
		options := Reservation{
			Pipeline: s.configured.Pipeline.Name, Contract: capturedContract(s.configured.Pipeline),
			Scope:           work.ReviewScope{Requirements: s.Task.Title + "\n\n" + s.Task.Description + "\n\n" + s.Task.AcceptanceCriteria, Output: output, DeltaFrom: effort.Input},
			ExpectedVersion: &version,
		}
		if notice := s.checkout.selection; notice != nil {
			revision := notice.Selection.Revision
			options.Scope.Selected = &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: revision.Commit, Tree: revision.Tree}, RetainedRef: revision.RetainedRef}
		}
		return phaseexec.Prepared{Scope: phaseexec.ReleaseForEffect, Run: func(ctx context.Context) phaseexec.Result {
			boundary := s.Git
			if boundary == nil {
				boundary = LocalReviewCandidateGit{}
			}
			destination, err := boundary.ObserveDestination(ctx, s.configured.Target.Worktree, destinationBranch)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			if err := s.reportSelection(); err != nil {
				return phaseexec.Result{Err: err}
			}
			options.Scope.Destination = &destination
			return phaseexec.Result{Update: ReserveChange(options)}
		}}, nil
	}}
}

// Report before saving: resuming a reserved review does not repeat the notice.
func (s *Admission) reportSelection() error {
	if s.checkout.selection == nil {
		return nil
	}
	if s.ReportSelection == nil {
		return errors.New("review revision selection requires a notice writer")
	}
	if err := s.ReportSelection(*s.checkout.selection); err != nil {
		return fmt.Errorf("report selected review revision: %w", err)
	}
	return nil
}

func capturedContract(pipeline Pipeline) work.ReviewContract {
	contract := work.ReviewContract{PipelineKey: pipeline.Name}
	for _, step := range pipeline.Steps {
		contract.Slots = append(contract.Slots, work.ReviewSlot{Kind: work.ReviewStepKind(step.Kind), ConfigKey: step.Name})
	}
	return contract
}

// The complete contract and exact scope retain their existing persisted shape.
// ReserveChange retains a selected review subject and its complete contract.
func ReserveChange(options Reservation) phaseexec.Change {
	if options.StartedAt.IsZero() {
		options.StartedAt = time.Now().UTC()
	}
	return func(current *work.State) (bool, error) {
		if options.ExpectedVersion != nil && current.Version != *options.ExpectedVersion {
			return false, errors.New("review reservation is stale; current work changed")
		}
		w := current.ActiveWorkflow()
		if w == nil || w.Context() == nil {
			return false, errors.New("review requires an established workflow context")
		}
		if len(options.Contract.Slots) == 0 || options.Contract.PipelineKey != options.Pipeline {
			return false, errors.New("review requires its complete captured pipeline contract")
		}
		contract := len(w.ReviewContracts)
		w.ReviewContracts = append(w.ReviewContracts, options.Contract)
		w.Phases = append(w.Phases, &work.ReviewPhase{Contract: contract, Scope: options.Scope, StartedAt: options.StartedAt})
		return true, nil
	}
}
