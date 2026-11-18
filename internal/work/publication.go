package work

import "time"

func deliveryTime(delivery Delivery) time.Time {
	switch delivery := delivery.(type) {
	case *PullRequestDelivery:
		if delivery != nil {
			return delivery.ObservedAt
		}
	case *DirectDelivery:
		if delivery != nil {
			return delivery.ObservedAt
		}
	}
	return time.Time{}
}

// PublicationContent is the authored cumulative outcome of one workflow.
// Commit text is independent of the PR description and generated PR sections.
type PublicationContent struct {
	CommitSubject string
	CommitBody    string
	PRDescription string
}

// CommitMessage contains only authored publication commit text.
func (c PublicationContent) CommitMessage() string { return c.CommitSubject + "\n\n" + c.CommitBody }

// PublicationLocked remains true after the first explicit operator proceed,
// including when later attempts need renewed consent for different input.
func (w *Workflow) PublicationLocked() bool {
	for _, phase := range w.Phases {
		if p, ok := phase.(*PublicationPhase); ok && p.Consent != nil {
			return true
		}
	}
	return false
}

// PublicationInput retains the selected commit without inventing an agent effort.
type PublicationInput struct {
	Code       CodeOutput
	Provenance PublicationProvenance
}

// PublicationProvenance distinguishes reviewed output from operator-selected code.
type PublicationProvenance string

const (
	InputReviewed     PublicationProvenance = "reviewed_output"
	InputOperator     PublicationProvenance = "operator_selected"
	InputSynchronized PublicationProvenance = "direct_sync_not_reviewed"
)

// PublicationComparison discloses the selected input's relationship and diff to
// the reviewed commit. It never establishes review approval of operator changes.
type PublicationComparison struct {
	Relationship string
	Changes      string
}

// PublicationTarget distinguishes initial PR creation from destination delivery.
type PublicationTarget interface{ publicationTarget() }

// PullRequestTarget selects one initial PR delivery. Recovery identity belongs
// to its operation rather than another PR-update publication target.
type PullRequestTarget struct {
	Draft bool `json:",omitempty"`
}

// DirectMergeTarget authorizes merge and verified destination push.
type DirectMergeTarget struct{}

func (*PullRequestTarget) publicationTarget() {}
func (*DirectMergeTarget) publicationTarget() {}

// PublicationConsent authorizes its phase selection and workflow content.
// Nil is not consent. Safe successor validation prevents changed intent reusing it.
type PublicationConsent struct {
	GrantedAt    time.Time
	Evidence     string
	IntentDigest [32]byte
}

// PublicationOperation records intent before packaging, then its actual retained
// public commit before any push or provider write. Its containing phase is its ID.
type PublicationOperation struct {
	StartedAt           time.Time
	Ref                 string
	Public              *CodeIdentity
	InputChanges        string
	PRCreationStartedAt *time.Time
}

// Delivery is an observed result, not a generic side-effect attempt or boolean flag.
type Delivery interface{ delivery() }

// PullRequestDelivery records independent confirmation of the PR and its published code.
type PullRequestDelivery struct {
	PullRequest PullRequestRef
	ObservedAt  time.Time
}

// DirectDelivery requires observed destination code, not just a local merge commit.
type DirectDelivery struct {
	ObservedAt time.Time
}

func (*PullRequestDelivery) delivery() {}
func (*DirectDelivery) delivery()      {}

// PullRequestRef retains the native URL exposed by the implemented PR provider.
type PullRequestRef struct{ URL string }

// TaskClosure records observed source closure, not a pending command or operation.
type TaskClosure struct {
	At     time.Time
	Reason string
}

// CleanupObservation preserves both removed and safely retained work directories.
// Cleanup can append after closure without pretending that publication ran again.
type CleanupObservation interface{ cleanupObservation() }

// WorktreeRemoved confirms removal or absence of the prepared worktree.
type WorktreeRemoved struct {
	At       time.Time
	Evidence string
}

// WorktreeRetained records refusal or failure without changing source closure.
type WorktreeRetained struct {
	At       time.Time
	Reason   string
	Evidence string
}

func (*WorktreeRemoved) cleanupObservation()  {}
func (*WorktreeRetained) cleanupObservation() {}

// DirectPublicationSync owns local-only reconciliation, never task-review approval.
// A missing Result after intent requires explicit operator recovery.
type DirectPublicationSync struct {
	StartedAt     time.Time
	Source        CodeIdentity
	Destination   Destination
	Result        *CodeIdentity
	ConflictFiles []string
	Execution     *AgentExecution
}

// DirectIntegration records intent and the actual ordinary merge before pushing.
type DirectIntegration struct {
	InvalidatedAt *time.Time
	StartedAt     time.Time
	Result        *CodeIdentity
}

// PublicPreparation resolves initial packaging across renewed direct consent.
func (w *Workflow) PublicPreparation(p *PublicationPhase) *PublicationPhase {
	if p.PreparedFrom == nil {
		return p
	}
	if p.PreparedFrom.Phase < 0 || p.PreparedFrom.Phase >= len(w.Phases) {
		return nil
	}
	prepared, _ := w.Phases[p.PreparedFrom.Phase].(*PublicationPhase)
	return prepared
}

// PublicCode gives the available public identity for inspection. Older PR
// deliveries without recorded packaging identify the selected publication input.
// The fallback does not imply that packaging was performed.
func (w *Workflow) PublicCode(p *PublicationPhase) (CodeIdentity, bool) {
	prepared := w.PublicPreparation(p)
	if prepared != nil && prepared.Operation != nil && prepared.Operation.Public != nil {
		return *prepared.Operation.Public, true
	}
	if delivered, ok := p.Delivery.(*PullRequestDelivery); ok && delivered != nil {
		return p.Input.Code.CodeIdentity, true
	}
	return CodeIdentity{}, false
}

// NeedsPublicationRecovery identifies unrecorded local effects, not remote retries.
func (p *PublicationPhase) NeedsPublicationRecovery() bool {
	return (p.Operation != nil && p.Operation.Public == nil) ||
		(p.DirectSync != nil && p.DirectSync.Result == nil) ||
		(p.Integration != nil && (p.Integration.Result == nil || p.Integration.InvalidatedAt != nil))
}

// PublicSync resolves a completed local sync reused by a fresh exact-selection grant.
func (w *Workflow) PublicSync(p *PublicationPhase) *DirectPublicationSync {
	if p.SyncFrom == nil {
		return p.DirectSync
	}
	if p.SyncFrom.Phase < 0 || p.SyncFrom.Phase >= len(w.Phases) {
		return nil
	}
	owner, _ := w.Phases[p.SyncFrom.Phase].(*PublicationPhase)
	if owner == nil {
		return nil
	}
	return owner.DirectSync
}
