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
	InputReviewed PublicationProvenance = "reviewed_output"
	InputOperator PublicationProvenance = "operator_selected"
)

// PublicationComparison discloses the selected input's relationship and diff to
// the reviewed commit. It never establishes review approval of operator changes.
type PublicationComparison struct {
	Relationship string
	Changes      string
}

// PublicationTarget distinguishes PR creation/update from destination delivery.
type PublicationTarget interface{ publicationTarget() }

// PullRequestTarget identifies an existing PR only for an update. Importing a PR
// discovered after disconnected creation does not require inventing an operation.
type PullRequestTarget struct {
	Existing     *PullRequestRef
	ExpectedHead string
}

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
	StartedAt time.Time
	Ref       string
	Parent    string
	Public    *CodeOutput
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
	PreviousHead string
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
