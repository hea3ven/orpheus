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

// PublicationPlan binds actual retained input separately from historical review.
// Input, destination context, content and target are immutable consent material.
type PublicationPlan struct {
	Review ReviewRef
	// FeatureCompletion identifies the original intent used to generate Content.
	// Nil denotes operator-authored content rather than an inferred agent handoff.
	FeatureCompletion *OutputRef
	Input             PublicationInput
	Destination       Destination
	Comparison        PublicationComparison
	Target            PublicationTarget
	Content           string
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
type PullRequestTarget struct{}

// DirectMergeTarget authorizes merge and verified destination push.
type DirectMergeTarget struct{}

func (*PullRequestTarget) publicationTarget() {}
func (*DirectMergeTarget) publicationTarget() {}

// PublicationConsent is the direct authorization of its containing plan.
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
	Parent              string
	Public              *CodeIdentity
	InputChanges        string
	PRCreationStartedAt *time.Time
}

// Delivery is an observed result, not a generic side-effect attempt or boolean flag.
type Delivery interface{ delivery() }

// PullRequestDelivery retains independent observed output and native PR identity.
// PublicCommit can squash local history while Tree must preserve authorized input.
type PullRequestDelivery struct {
	PullRequest  PullRequestRef
	PublicCommit string
	Head         string
	Tree         string
	ObservedAt   time.Time
	Evidence     string
}

// DirectDelivery requires observed destination code, not just a local merge commit.
type DirectDelivery struct {
	PublicCommit string
	Destination  CodeIdentity
	ObservedAt   time.Time
	Evidence     string
}

func (*PullRequestDelivery) delivery() {}
func (*DirectDelivery) delivery()      {}

// PullRequestRef retains the native URL exposed by the implemented PR provider.
type PullRequestRef struct{ URL string }

// Acceptance has one source of required delivery/acceptance evidence.
type Acceptance interface{ acceptance() }

// PRAcceptance records the implemented prerequisite: the PR was confirmed merged.
// PR identity follows ExternalReview; provider head/merge-commit data is not invented.
type PRAcceptance struct {
	ExternalReview int
	ObservedAt     time.Time
	Evidence       string
}

// DirectAcceptance references actual destination delivery; no PR observation applies.
type DirectAcceptance struct{ Publication PublicationRef }

func (*PRAcceptance) acceptance()     {}
func (*DirectAcceptance) acceptance() {}

// TaskClosure records observed source closure, not a pending command or operation.
type TaskClosure struct {
	At       time.Time
	Reason   string
	Evidence string
}

// CleanupObservation preserves both removed and safely retained work directories.
// Cleanup can append after closure without pretending that publication ran again.
type CleanupObservation interface{ cleanupObservation() }

// WorktreeRemoved is the actual removed-path observation, not a copy of intent.
type WorktreeRemoved struct {
	Path     string
	At       time.Time
	Evidence string
}

// WorktreeRetained records refusal or failure without changing source closure.
type WorktreeRetained struct {
	Path     string
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
	Source        CodeIdentity
	Destination   Destination
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

// NeedsPublicationRecovery identifies unrecorded local effects, not remote retries.
func (p *PublicationPhase) NeedsPublicationRecovery() bool {
	return (p.Operation != nil && p.Operation.Public == nil) ||
		(p.DirectSync != nil && p.DirectSync.Result == nil) ||
		(p.Integration != nil && (p.Integration.Result == nil || p.Integration.InvalidatedAt != nil))
}

// PublicSync resolves a completed local sync reused by a fresh exact-plan grant.
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
