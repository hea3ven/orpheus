// Package delivery owns the behavior of the domain Publication phase.
package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// ErrConsentRequired distinguishes publication consent from task review approval.
var ErrConsentRequired = errors.New("publication awaits explicit exact-plan authorization; review approval and retained commits do not grant consent")

type Backend interface {
	task.Getter
	task.Lister
	task.PRURLMutator
}

// Service is scoped to one task and invocation. Its transient observations never
// authorize recovery after restart; unresolved local effects still require input.
type Service struct {
	Paths            state.Paths
	Source           task.RepositorySource
	Task             task.Task
	Backend          Backend
	Git              Git
	DirectGit        DirectGit
	PRProvider       pullrequest.Provider
	ConflictResolver phaseexec.ConflictResolver
	ProcessProbe     phaseexec.ProcessProbe
	Commit           func(context.Context, phaseexec.Change) (work.State, error)
	Progress         func(work.State) error
	reported         bool

	refreshed        bool
	syncInput        *work.CodeIdentity
	freshConflict    bool
	packageDescribed bool
	preflight        *publicPreflight
	prObservation    *prObservation
	metadataObserved bool
	resolverLaunch   *phaseexec.PreparedConflictResolution
	resolverObserved bool
}

type Intent struct {
	Review *work.AttemptRef
	// AuthorizedOnly retains delivery-only command boundaries before inspection.
	AuthorizedOnly bool
}

type Request interface{ deliveryRequest() }
type ConsentRequest struct {
	ID          phaseexec.InputID
	Content     work.PublicationContent
	Input       work.PublicationInput
	Destination work.Destination
	Comparison  work.PublicationComparison
	Flow        publication.IntegrationFlow
	PRDraft     bool
}
type RecoveryRequest struct{ ID phaseexec.InputID }

func (ConsentRequest) deliveryRequest()  {}
func (RecoveryRequest) deliveryRequest() {}

type Outcome interface{ deliveryOutcome() }
type PRDelivered struct {
	Publication work.AttemptRef
	PRURL       string
	Recovered   bool
	Task        task.Task
}
type DirectDelivered struct {
	Publication work.AttemptRef
	Task        task.Task
}

func (PRDelivered) deliveryOutcome()     {}
func (DirectDelivered) deliveryOutcome() {}

const (
	ConsentInput  = "publication_consent"
	RecoveryInput = "publication_recovery"
)

// Next selects initialization, one local operation, a typed decision, or delivery.
func (s *Service) Next(current work.State, intent Intent) (phaseexec.Selection[Request, Outcome], error) {
	w := lastWorkflow(&current)
	if w == nil {
		return phaseexec.Selection[Request, Outcome]{}, ErrConsentRequired
	}
	if _, reviewing := w.LastPhase().(*work.ReviewPhase); reviewing {
		if intent.Review == nil {
			return phaseexec.Selection[Request, Outcome]{}, errors.New("active local work prevents publication")
		}
		return phaseexec.Run[Request, Outcome](s.initialize(current, *intent.Review)), nil
	}
	if intent.AuthorizedOnly {
		if pending := Pending(current); pending != nil {
			return phaseexec.Await[Request, Outcome](pending), nil
		}
	}
	selected, err := selectPublication(&current, s.Task)
	if err != nil {
		return phaseexec.Selection[Request, Outcome]{}, err
	}
	owner := selected.owner(current)
	p := selected.publication
	if _, publishing := w.LastPhase().(*work.PublicationPhase); publishing {
		if !s.refreshed {
			return phaseexec.Run[Request, Outcome](s.refresh(owner)), nil
		}
		if p.NeedsPublicationRecovery() && !s.canContinueLocal(p) {
			return phaseexec.Await[Request, Outcome](RecoveryRequest{ID: phaseexec.InputID{Owner: owner, Version: current.Version, Kind: RecoveryInput}}), nil
		}
		if p.Consent == nil {
			return phaseexec.Await[Request, Outcome](consentRequest(current, owner, w, p)), nil
		}
	}
	if p.Consent == nil {
		return phaseexec.Selection[Request, Outcome]{}, ErrConsentRequired
	}
	if !s.reported && s.Progress != nil {
		return phaseexec.Run[Request, Outcome](operation("report publication", owner, func(snapshot *work.State, _ *work.Workflow, _ *work.PublicationPhase) (phaseexec.Prepared, error) {
			return phaseexec.Prepared{Run: func(context.Context) phaseexec.Result {
				err := s.Progress(*snapshot)
				s.reported = err == nil
				return phaseexec.Result{Err: err}
			}}, nil
		})), nil
	}
	if p.Delivery == nil {
		if s.Task.Status == task.StatusClosed {
			return phaseexec.Selection[Request, Outcome]{}, errors.New("task source is closed without recorded delivery; reconcile before publishing")
		}
		switch p.Target.(type) {
		case *work.PullRequestTarget:
			return s.nextPR(current, selected)
		case *work.DirectMergeTarget:
			return s.nextDirect(current, selected)
		default:
			return phaseexec.Selection[Request, Outcome]{}, errors.New("unsupported publication target")
		}
	}
	return s.delivered(current, selected)
}

func (s *Service) delivered(current work.State, selected committedPublication) (phaseexec.Selection[Request, Outcome], error) {
	owner, p := selected.owner(current), selected.publication
	if needsRefRelease(p) {
		return phaseexec.Run[Request, Outcome](s.releaseHistory(owner)), nil
	}
	switch delivered := p.Delivery.(type) {
	case *work.PullRequestDelivery:
		if !s.metadataObserved {
			return phaseexec.Run[Request, Outcome](s.recordPRMetadata(owner, delivered.PullRequest.URL)), nil
		}
		recovered := s.prObservation != nil && s.prObservation.recovered
		return phaseexec.Return[Request, Outcome](PRDelivered{Publication: owner, PRURL: delivered.PullRequest.URL, Recovered: recovered, Task: s.Task}), nil
	case *work.DirectDelivery:
		return phaseexec.Return[Request, Outcome](DirectDelivered{Publication: owner, Task: s.Task}), nil
	default:
		return phaseexec.Selection[Request, Outcome]{}, errors.New("publication did not establish observed delivery")
	}
}

// Owner follows containment references, never task-wide latest PR metadata.
func Owner(w *work.Workflow) (int, error) {
	switch p := w.LastPhase().(type) {
	case *work.PublicationPhase:
		return len(w.Phases) - 1, nil
	case *work.ExternalReviewPhase:
		return p.Publication.Phase, nil
	case *work.FinalizationPhase:
		prerequisite, err := w.FinalizationContext(len(w.Phases) - 1)
		return prerequisite.Publication.Phase, err
	}
	return 0, errors.New("active local work prevents publication")
}

func (s committedPublication) owner(current work.State) work.AttemptRef {
	return work.AttemptRef{RepositoryID: current.RepositoryID, TaskID: current.TaskID, Workflow: s.workflowIndex, Phase: s.phaseIndex}
}

func publicationAt(current *work.State, owner work.AttemptRef) (*work.Workflow, *work.PublicationPhase, error) {
	if current.RepositoryID != owner.RepositoryID || current.TaskID != owner.TaskID || owner.Workflow != len(current.Workflows)-1 || owner.Step != nil || owner.BranchSync != nil {
		return nil, nil, phaseexec.ErrStaleInput
	}
	w := lastWorkflow(current)
	if w == nil || owner.Phase < 0 || owner.Phase >= len(w.Phases) {
		return nil, nil, phaseexec.ErrStaleInput
	}
	index, err := Owner(w)
	if err != nil || index != owner.Phase {
		return nil, nil, phaseexec.ErrStaleInput
	}
	p, ok := w.Phases[owner.Phase].(*work.PublicationPhase)
	if !ok {
		return nil, nil, phaseexec.ErrStaleInput
	}
	return w, p, nil
}

// operation freezes phase inputs under mutation protection. Each effect returns
// just its own observation update, never a replacement for the old snapshot.
func operation(name string, owner work.AttemptRef, prepare func(*work.State, *work.Workflow, *work.PublicationPhase) (phaseexec.Prepared, error)) phaseexec.Operation {
	return phaseexec.Operation{Name: name, Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		w, p, err := publicationAt(current, owner)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		return prepare(current, w, p)
	}}
}

func change(owner work.AttemptRef, update func(*work.State, *work.Workflow, *work.PublicationPhase) (bool, error)) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		w, p, err := publicationAt(current, owner)
		if err != nil {
			return false, err
		}
		return update(current, w, p)
	}
}

func (s *Service) recordPRMetadata(owner work.AttemptRef, url string) phaseexec.Operation {
	return operation("record publication PR", owner, func(_ *work.State, _ *work.Workflow, _ *work.PublicationPhase) (phaseexec.Prepared, error) {
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			observed, err := recordPublicationPR(ctx, s.Backend, s.Task, url)
			if err == nil {
				s.Task, s.metadataObserved = observed, true
			}
			return phaseexec.Result{Err: err}
		}}, nil
	})
}

func recordPublicationPR(ctx context.Context, backend Backend, item task.Task, url string) (task.Task, error) {
	metadata := item.OrpheusMetadata()
	if metadata.HasPRURL && metadata.PRURL != "" && metadata.PRURL != url {
		return task.Task{}, errors.New("task source names a different PR; refusing to overwrite it")
	}
	if metadata.HasPRURL && metadata.PRURL == url {
		return item, nil
	}
	if backend == nil {
		return task.Task{}, errors.New("publication task source is required")
	}
	if err := backend.SetPRURL(ctx, item.ID, url); err != nil {
		return task.Task{}, err
	}
	observed, err := backend.Get(ctx, item.ID)
	if err != nil {
		return task.Task{}, err
	}
	if observed.OrpheusMetadata().PRURL != url {
		return task.Task{}, errors.New("task-source PR update was not independently observed")
	}
	return observed, nil
}

func needsRefRelease(p *work.PublicationPhase) bool {
	if _, pr := p.Target.(*work.PullRequestTarget); pr && p.Delivery == nil {
		return false
	}
	return p.Operation != nil && p.Operation.Public != nil && p.PrivateRefsReleasedAt == nil
}

func (s *Service) releaseHistory(owner work.AttemptRef) phaseexec.Operation {
	return operation("release private publication history", owner, func(_ *work.State, w *work.Workflow, _ *work.PublicationPhase) (phaseexec.Prepared, error) {
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			root := owner
			root.Phase = 0
			revisions := phaseexec.RevisionNamespace(root)
			revisions = revisions[:len(revisions)-1]
			err := s.Git.ReleasePrivatePublicationRefs(ctx, w.Context().Directory, []string{revisions, phaseexec.PublicationInputNamespace(root) + "/"})
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			at := time.Now().UTC()
			return phaseexec.Result{Update: change(owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (bool, error) {
				p.PrivateRefsReleasedAt = &at
				return true, nil
			})}
		}}, nil
	})
}

func (s *Service) canContinueLocal(p *work.PublicationPhase) bool {
	return p.DirectSync != nil && p.DirectSync.Result == nil && (s.freshConflict || s.resolverLaunch != nil || s.resolverObserved)
}

func packageTreeError(expected, actual work.CodeIdentity) error {
	return fmt.Errorf("packaging changed authorized tree %s to actual committed tree %s at %s on the owned task branch; nothing was pushed. Continue task run for a new proceed-or-abort decision on the actual committed input", expected.Tree, actual.Tree, actual.Commit)
}

func selectPublication(snapshot *work.State, item task.Task) (committedPublication, error) {
	// Refresh checks an unconsented selection before it becomes an input request.
	selected, err := selectCommittedPublication(snapshot, item)
	if !errors.Is(err, ErrConsentRequired) {
		return selected, err
	}
	if len(snapshot.Workflows) == 0 {
		return selected, err
	}
	w := lastWorkflow(snapshot)
	pi, ownerErr := Owner(w)
	if ownerErr != nil {
		return selected, ownerErr
	}
	p := w.Phases[pi].(*work.PublicationPhase)
	return committedPublication{workflowIndex: len(snapshot.Workflows) - 1, phaseIndex: pi, workflow: w, publication: p, prepared: w.Context(), output: &p.Input.Code, destination: &p.Destination}, nil
}

func lastWorkflow(current *work.State) *work.Workflow {
	if len(current.Workflows) == 0 {
		return nil
	}
	return &current.Workflows[len(current.Workflows)-1]
}
