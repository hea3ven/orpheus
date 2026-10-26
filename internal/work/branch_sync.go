package work

import (
	"fmt"
	"time"
)

// BranchSync owns one effort to merge the destination into an existing PR branch.
// It does not create feature completion, task-review approval, or publication consent.
// Local commit and remote push are independent facts; neither is an editable status.
type BranchSync struct {
	StartedAt          time.Time
	Checkpoint         BranchSyncCheckpoint
	ConflictResolution *ConflictResolution
	LocalCommit        *BranchSyncCommit
	Push               *BranchSyncPush
	Rollback           *BranchSyncRollback
}

// BranchSyncCheckpoint retains the actual native Git inputs for safe recovery.
// Branch, destination, PR, and work directory follow the containing phase/workflow.
type BranchSyncCheckpoint struct {
	LocalHead   string
	RemoteHead  string
	MergeSource string
}

// ConflictResolution exists only when synchronization needed an agent.
// A failed attempt is retained; another synchronization effort gets another entry.
type ConflictResolution struct {
	Files     []string
	Execution AgentExecution
}

// BranchSyncCommit records the local result, including an already-current head.
type BranchSyncCommit struct {
	Head       string
	ObservedAt time.Time
}

// BranchSyncPush retains independently verified remote delivery of the local result.
type BranchSyncPush struct {
	Head       string
	ObservedAt time.Time
	Evidence   string
}

// BranchSyncRollback records verified restoration to the checkpoint after failure.
// A failed lookup is not rollback evidence and cannot authorize another effort.
type BranchSyncRollback struct {
	LocalHead  string
	RemoteHead string
	ObservedAt time.Time
	Reason     string
	Evidence   string
}

func (s *BranchSync) settled() bool { return s.Push != nil || s.Rollback != nil }

func (s *BranchSync) settledAt() time.Time {
	if s.Push != nil {
		return s.Push.ObservedAt
	}
	if s.Rollback != nil {
		return s.Rollback.ObservedAt
	}
	return time.Time{}
}

func (w *Workflow) externalDelivery(phase int) (*PullRequestDelivery, error) {
	external, err := phaseAs[*ExternalReviewPhase](w, phase)
	if err != nil || external == nil {
		return nil, fmt.Errorf("reference requires an external-review phase")
	}
	publication, err := phaseAs[*PublicationPhase](w, external.Publication.Phase)
	if err != nil || publication == nil {
		return nil, fmt.Errorf("external review requires publication")
	}
	delivery, ok := publication.Delivery.(*PullRequestDelivery)
	if !ok || delivery == nil {
		return nil, fmt.Errorf("external review requires delivered PR")
	}
	return delivery, nil
}
