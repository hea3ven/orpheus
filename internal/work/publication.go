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

// PublicationPlan inherits output and destination from its selected review.
// Content and delivery target are immutable intent covered by its owned consent.
type PublicationPlan struct {
	Review  ReviewRef
	Target  PublicationTarget
	Content string
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

// Delivery is an observed result, not a generic side-effect attempt or boolean flag.
type Delivery interface{ delivery() }

// PullRequestDelivery retains independent observed output and native PR identity.
// PublicCommit can squash local history while Tree must preserve reviewed output.
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
