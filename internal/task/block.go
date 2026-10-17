package task

import "context"

// MetadataPreBlockStatus records the lifecycle to restore on explicit unblocking.
const MetadataPreBlockStatus = "orpheus.pre_block_status"

// BlockMutator changes native blocking without editing content or relationships.
// Callers must verify execution and review safety before invoking it. Unblock
// restores only a currently blocked item; native reopening takes precedence.
type BlockMutator interface {
	Block(ctx context.Context, id string) error
	Unblock(ctx context.Context, id string) error
}

// PreBlockStatus returns the recorded lifecycle, defaulting to open for items
// blocked outside Orpheus. Invalid restoration data must not authorize dispatch.
func PreBlockStatus(item Task) (Status, error) {
	switch status := Status(item.Metadata[MetadataPreBlockStatus]); status {
	case StatusUnknown, StatusOpen:
		return StatusOpen, nil
	case StatusInProgress:
		return status, nil
	default:
		return StatusUnknown, MutationConflictError{TaskID: item.ID, Reason: "invalid pre-block lifecycle; inspect task-source metadata before retrying"}
	}
}

// ValidateBlock checks the source-owned preconditions for a block transition.
func ValidateBlock(item Task) error {
	if err := ValidateTaskSourceItem(item); err != nil {
		return err
	}
	if item.Status != StatusOpen && item.Status != StatusInProgress && item.Status != StatusBlocked {
		return MutationConflictError{TaskID: item.ID, Reason: "only open or inactive in-progress work can be blocked"}
	}
	return nil
}
