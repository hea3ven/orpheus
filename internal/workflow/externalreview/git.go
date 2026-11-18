package externalreview

import (
	"context"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
)

// CompletionGit separates local merge completion from remote push.
type CompletionGit interface {
	CommitTaskBranchConflictResolution(context.Context, gitmeta.TaskBranchSyncOptions, []string) (gitmeta.TaskBranchSyncResult, error)
	PushCommittedTaskBranchConflictResolution(context.Context, gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error)
}

// RecoveryGit supplies the native observations required by every
// durable synchronization, including ordinary merges without a resolver.
type RecoveryGit interface {
	InspectTaskBranchConflictCheckpoint(context.Context, gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchConflictCheckpoint, error)
	InspectRemoteTaskBranchHead(context.Context, gitmeta.TaskBranchSyncOptions) (string, error)
	InspectTaskBranchConflictRollbackEligibility(context.Context, gitmeta.TaskBranchSyncOptions, gitmeta.TaskBranchConflictCheckpoint, string) error
	RollbackTaskBranchConflictResolution(context.Context, gitmeta.TaskBranchSyncOptions, gitmeta.TaskBranchConflictCheckpoint) error
	VerifyTaskBranchConflictRollback(context.Context, gitmeta.TaskBranchSyncOptions, gitmeta.TaskBranchConflictCheckpoint) error
}

// Git performs the Git operations used by task sync branch updates.
type Git interface {
	CompletionGit
	RecoveryGit
	BeginTaskBranchConflictResolution(context.Context, gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error)
}

// LocalGit delegates sync Git operations to the local git binary.
type LocalGit struct{}

// BeginTaskBranchConflictResolution leaves merge conflicts for a resolver.
func (LocalGit) BeginTaskBranchConflictResolution(
	ctx context.Context,
	opts gitmeta.TaskBranchSyncOptions,
) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.BeginTaskBranchConflictResolution(ctx, opts)
}

func (LocalGit) CommitTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, conflictFiles []string) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.CommitTaskBranchConflictResolution(ctx, opts, conflictFiles)
}
func (LocalGit) PushCommittedTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.PushCommittedTaskBranchConflictResolution(ctx, opts)
}

func (LocalGit) InspectTaskBranchConflictCheckpoint(ctx context.Context, opts gitmeta.TaskBranchSyncOptions) (gitmeta.TaskBranchConflictCheckpoint, error) {
	return gitmeta.InspectTaskBranchConflictCheckpoint(ctx, opts)
}
func (LocalGit) InspectRemoteTaskBranchHead(ctx context.Context, opts gitmeta.TaskBranchSyncOptions) (string, error) {
	return gitmeta.InspectRemoteTaskBranchHead(ctx, opts)
}
func (LocalGit) InspectTaskBranchConflictRollbackEligibility(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint, localHead string) error {
	return gitmeta.InspectTaskBranchConflictRollbackEligibility(ctx, opts, checkpoint, localHead)
}
func (LocalGit) RollbackTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint) error {
	return gitmeta.RollbackTaskBranchConflictResolution(ctx, opts, checkpoint)
}
func (LocalGit) VerifyTaskBranchConflictRollback(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint) error {
	return gitmeta.VerifyTaskBranchConflictRollback(ctx, opts, checkpoint)
}
