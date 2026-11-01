package workflow

import (
	"context"
	"errors"
	"fmt"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// PhaseKind identifies a built-in purpose, independently of a terminal label.
type PhaseKind string

const (
	PhasePreparation    PhaseKind = "preparation"
	PhaseImplementation PhaseKind = "implementation"
	PhaseReview         PhaseKind = "review"
	PhasePublication    PhaseKind = "publication"
	PhaseExternalReview PhaseKind = "external_review"
	PhaseFinalization   PhaseKind = "finalization"
)

// View is a read-only projection of facts and legal continuation. It is not a
// second persistence aggregate. Callers may retain it across terminal rendering.
type View struct {
	TaskID        string
	Title         string
	Workflow      int
	WorkflowCount int
	Phase         PhaseKind
	PhaseAttempt  int
	Owner         *work.AttemptRef
	Step          *StepView
	Context       *work.WorkContext
	Execution     *ExecutionView
	Revision      *RevisionView
	Start         *RevisionView
	Scope         *ScopeView
	Findings      []work.FindingRef
	Publication   *PublicationView
	Published     *PublishedView
	Closure       *work.TaskClosure
	Next          TaskRunRoute
	Pending       *Interaction
}

// StepView describes an actual effort; kinds remain recognizable configuration keys.
type StepView struct {
	Name, Kind string
	Attempt    int
}

// ExecutionView contains execution context, not process streams.
type ExecutionView struct {
	Profile string
	Purpose work.ExecutionPurpose
}

// RevisionView identifies retained output without implying approval.
type RevisionView struct {
	Number int
	Code   work.CodeIdentity
}

// ScopeView retains full-task comparison endpoints separately from output identity.
type ScopeView struct{ Baseline, Head string }

// PublishedView keeps previously delivered output separate from current local work.
type PublishedView struct {
	Revision     *RevisionView
	PublicCommit string
	PRURL        string
}

// PublicationView describes selected intent and independent observed delivery.
type PublicationView struct {
	ReviewAttempt      int
	Content            string
	Flow               string
	Destination        work.Destination
	Consented          bool
	PRURL              string
	PRState            string
	PublicCommit       string
	DeliveryEvidence   string
	AcceptanceEvidence string
}

// Progress is a synchronous notification, separate from attached process stdio.
// Resumed means this invocation is continuing an existing effort, not a new attempt.
type Progress struct {
	View    View
	Resumed bool
}

// Inspect reads without recovery, reservation, prompting, or external effects.
func (s Controller) Inspect(ctx context.Context, taskID string) (View, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.RunStore == nil || s.BackendFactory == nil {
		return View{}, fmt.Errorf("workflow inspection requires state and task source")
	}
	base, err := s.resolveInspectionTask(ctx, taskID)
	if err != nil {
		return View{}, err
	}
	snapshot, err := s.RunStore.Load(base.RepoID(), base.TaskID())
	if err != nil {
		return View{}, err
	}
	return InspectTask(base.Task, snapshot)
}

// InspectTask projects the same next action used by advancement. All exported
// context is copied, so rendering cannot mutate the supplied storage snapshot.
func InspectTask(item task.Task, snapshot taskstate.TaskState) (View, error) {
	if snapshot.Work != nil {
		canonical, err := taskstate.Project(*snapshot.Work)
		if err != nil {
			return View{}, err
		}
		snapshot = canonical
	}

	route, err := SelectTaskRunRoute(item, snapshot)
	if item.Status == task.StatusClosed {
		open := item
		open.Status = task.StatusInProgress
		route, err = SelectTaskRunRoute(open, snapshot)
		if err == nil {
			route.Action = TaskRunActionCompleted
		}
	}
	if err != nil {
		return View{}, err
	}
	v := View{TaskID: item.ID, Title: item.Title, Next: route, Owner: route.Owner, Phase: PhasePreparation, PhaseAttempt: 1, Step: &StepView{Name: "prepare-workdir", Kind: "system", Attempt: 1}}
	if snapshot.Work == nil || len(snapshot.Work.Workflows) == 0 {
		return v, nil
	}
	w := &snapshot.Work.Workflows[len(snapshot.Work.Workflows)-1]
	v.WorkflowCount = len(snapshot.Work.Workflows)
	v.Workflow = v.WorkflowCount
	if prepared := w.Context(); prepared != nil {
		preparedCopy := *prepared
		v.Context = &preparedCopy
	}
	if len(w.Phases) == 0 {
		return v, nil
	}
	for _, phase := range w.Phases {
		if phaseKind(phase) == phaseKind(w.LastPhase()) {
			v.PhaseAttempt++
		}
	}
	v.PhaseAttempt--
	v.Phase = phaseKind(w.LastPhase())
	if err := fillPhaseView(&v, w); err != nil {
		return View{}, err
	}
	v.Pending = pendingInteraction(v, snapshot)
	return v, nil
}

func fillPhaseView(v *View, w *work.Workflow) error {
	switch p := w.LastPhase().(type) {
	case *work.ImplementationPhase:
		fillImplementationView(v, w, p)
	case *work.ReviewPhase:
		fillReviewView(v, w, p)
	case *work.PublicationPhase:
		fillPublicationView(v, w, p)
		v.Step = &StepView{Name: "deliver-plan", Kind: "system", Attempt: 1}
		if p.Consent == nil {
			v.Step = &StepView{Name: "publication-decision", Kind: "manual", Attempt: 1}
		}
	case *work.ExternalReviewPhase:
		fillPublicationView(v, w, w.Phases[p.Publication.Phase].(*work.PublicationPhase))
		v.Step = &StepView{Name: "observe-pr", Kind: "system", Attempt: 1}
	case *work.FinalizationPhase:
		publicationIndex, err := publicationOwner(w)
		if err != nil {
			return err
		}
		fillPublicationView(v, w, w.Phases[publicationIndex].(*work.PublicationPhase))
		if accepted, ok := p.Acceptance.(*work.PRAcceptance); ok {
			v.Publication.AcceptanceEvidence = accepted.Evidence
		}
		if p.Closure != nil {
			closureCopy := *p.Closure
			v.Closure = &closureCopy
		}
		v.Step = &StepView{Name: "close-task", Kind: "system", Attempt: 1}
	}
	return nil
}

func fillImplementationView(v *View, w *work.Workflow, p *work.ImplementationPhase) {
	v.Execution = &ExecutionView{Profile: p.Execution.Profile, Purpose: work.PurposeImplementation}
	v.Findings = append([]work.FindingRef(nil), p.TargetFindings...)
	v.Step = &StepView{Name: "implement", Kind: "agent", Attempt: 1}
	if len(p.TargetFindings) > 0 {
		v.Step.Name = "repair-findings"
	}
	if p.Input != nil {
		v.Start = revisionView(w, *p.Input)
	}
	if p.Output != nil {
		v.Revision = revisionView(w, work.OutputRef{Phase: len(w.Phases) - 1})
		v.Step = &StepView{Name: "record-revision", Kind: "system", Attempt: 1}
	}
}

func fillReviewView(v *View, w *work.Workflow, p *work.ReviewPhase) {
	v.Revision = revisionView(w, p.Scope.Output)
	v.Scope = &ScopeView{}
	if v.Revision != nil {
		v.Scope.Head = v.Revision.Code.Commit
	}
	if v.Context != nil {
		v.Scope.Baseline = v.Context.Baseline.Commit
	}
	v.Step = nil
	if len(p.Steps) > 0 {
		v.Step = reviewStepView(w, p, len(p.Steps)-1)
		if agent, ok := p.Steps[len(p.Steps)-1].(*work.AgentReviewStep); ok {
			v.Execution = &ExecutionView{Profile: agent.Execution.Profile, Purpose: work.PurposeReview}
		}
	}
	for _, phase := range w.Phases {
		publication, ok := phase.(*work.PublicationPhase)
		if !ok {
			continue
		}
		delivered, ok := publication.Delivery.(*work.PullRequestDelivery)
		if !ok {
			continue
		}
		reviewed := w.Phases[publication.Plan.Review.Phase].(*work.ReviewPhase)
		v.Published = &PublishedView{Revision: revisionView(w, reviewed.Scope.Output), PublicCommit: delivered.PublicCommit, PRURL: delivered.PullRequest.URL}
	}
}

func phaseKind(p work.Phase) PhaseKind {
	switch p.(type) {
	case *work.PreparationPhase:
		return PhasePreparation
	case *work.ImplementationPhase:
		return PhaseImplementation
	case *work.ReviewPhase:
		return PhaseReview
	case *work.PublicationPhase:
		return PhasePublication
	case *work.ExternalReviewPhase:
		return PhaseExternalReview
	case *work.FinalizationPhase:
		return PhaseFinalization
	default:
		return PhasePreparation
	}
}

func revisionView(w *work.Workflow, ref work.OutputRef) *RevisionView {
	output, err := w.Output(ref)
	if err != nil {
		return nil
	}
	number := 0
	for i, p := range w.Phases {
		if i > ref.Phase {
			break
		}
		if effort, ok := p.(*work.ImplementationPhase); ok {
			if _, captured := effort.Output.(*work.CodeOutput); captured {
				number++
			}
		}
	}
	return &RevisionView{Number: number, Code: output.CodeIdentity}
}

func reviewStepView(w *work.Workflow, p *work.ReviewPhase, index int) *StepView {
	slot := reviewSlotIndex(p.Steps[index])
	required := w.ReviewContracts[p.Contract].Slots[slot]
	attempt := 0
	for _, step := range p.Steps[:index+1] {
		if reviewSlotIndex(step) == slot {
			attempt++
		}
	}
	return &StepView{Name: required.ConfigKey, Kind: string(required.Kind), Attempt: attempt}
}

func reviewSlotIndex(step work.ReviewStep) int {
	switch p := step.(type) {
	case *work.ManualReviewStep:
		return p.Slot
	case *work.CheckStep:
		return p.Slot
	case *work.AgentReviewStep:
		return p.Slot
	}
	return 0
}

func fillPublicationView(v *View, w *work.Workflow, p *work.PublicationPhase) {
	review := w.Phases[p.Plan.Review.Phase].(*work.ReviewPhase)
	v.Revision = revisionView(w, review.Scope.Output)
	info := &PublicationView{Content: p.Plan.Content, Consented: p.Consent != nil, Flow: "pull-request", PRState: "awaiting observation"}
	if _, direct := p.Plan.Target.(*work.DirectMergeTarget); direct {
		info.Flow = "direct-merge"
	}
	for i, phase := range w.Phases {
		if i > p.Plan.Review.Phase {
			break
		}
		if _, ok := phase.(*work.ReviewPhase); ok {
			info.ReviewAttempt++
		}
	}
	if destination, err := w.ReviewDestination(p.Plan.Review); err == nil && destination != nil {
		info.Destination = *destination
	}
	if target, ok := p.Plan.Target.(*work.PullRequestTarget); ok && target.Existing != nil {
		info.PRURL = target.Existing.URL
	}
	switch delivered := p.Delivery.(type) {
	case *work.PullRequestDelivery:
		info.PRURL = delivered.PullRequest.URL
		info.PublicCommit = delivered.PublicCommit
		info.DeliveryEvidence = delivered.Evidence
	case *work.DirectDelivery:
		info.PublicCommit = delivered.PublicCommit
		info.DeliveryEvidence = delivered.Evidence
	}
	v.Publication = info
}

func (s ReviewLifecycleService) notify(repoID string, item task.Task, resumed bool, step *StepView) error {
	if s.Progress == nil {
		return nil
	}
	snapshot, err := s.RunStore.Load(repoID, item.ID)
	if err != nil {
		return err
	}
	view, err := InspectTask(item, snapshot)
	if err != nil {
		return fmt.Errorf("inspect workflow progress: %w", err)
	}
	if step != nil {
		view.Step = step
	}
	if err := s.Progress(Progress{View: view, Resumed: resumed}); err != nil {
		return fmt.Errorf("%w: %w", ErrProgressUnavailable, err)
	}
	return nil
}

func (s Controller) reportSnapshot(item task.Task, snapshot work.State, resumed bool) error {
	if s.Progress == nil {
		return nil
	}
	view, err := interactionView(item, snapshot)
	if err != nil {
		return err
	}
	if err := s.Progress(Progress{View: view, Resumed: resumed}); err != nil {
		return fmt.Errorf("%w: %w", ErrProgressUnavailable, err)
	}
	return nil
}

func (s Controller) resolveInspectionTask(ctx context.Context, taskID string) (ReviewContext, error) {
	source, err := task.ResolveTaskSource(s.Sources, taskID)
	if err != nil {
		return ReviewContext{}, err
	}
	backend, err := s.BackendFactory(source.Source)
	if err != nil {
		return ReviewContext{}, err
	}
	item, err := backend.Get(ctx, source.TaskID)
	if err != nil {
		return ReviewContext{}, err
	}
	if item.ID != source.TaskID {
		return ReviewContext{}, fmt.Errorf("task source returned a different task identity")
	}
	return ReviewContext{Source: source.Source, Task: item}, nil
}

// ErrProgressUnavailable means frontend output failed without answering a decision.
var ErrProgressUnavailable = errors.New("render workflow context")
