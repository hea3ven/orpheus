// Package preparation establishes the checkout and retained baseline selected by workflow.
package preparation

import (
	"context"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
)

// Git prepares repository-root and worktree execution targets.
type Git interface {
	PrepareBaseline(context.Context, string, string, string, *gitmeta.Revision) (gitmeta.Revision, error)
	SetupRepoRootTaskBranch(context.Context, gitmeta.TaskWorktreeOptions) (gitmeta.TaskWorktreeSetupResult, error)
	SetupTaskWorktree(context.Context, gitmeta.TaskWorktreeOptions) (gitmeta.TaskWorktreeSetupResult, error)
}

// ResetGit separates authorized destructive cleanup from ordinary setup.
type ResetGit interface {
	InspectWorkflowReset(context.Context, gitmeta.WorkflowResetRequest) (string, error)
	ResolveWorkflowReset(context.Context, gitmeta.WorkflowResetRequest) (gitmeta.Revision, error)
	ApplyWorkflowReset(context.Context, gitmeta.WorkflowResetRequest) error
}

// LocalGit uses the native Git adapters.
type LocalGit struct{}

func (LocalGit) PrepareBaseline(ctx context.Context, dir, branch, ref string, recorded *gitmeta.Revision) (gitmeta.Revision, error) {
	return gitmeta.PrepareBaseline(ctx, dir, branch, ref, recorded)
}
func (LocalGit) SetupRepoRootTaskBranch(ctx context.Context, opts gitmeta.TaskWorktreeOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	return gitmeta.SetupRepoRootTaskBranch(ctx, opts)
}
func (LocalGit) SetupTaskWorktree(ctx context.Context, opts gitmeta.TaskWorktreeOptions) (gitmeta.TaskWorktreeSetupResult, error) {
	return gitmeta.SetupTaskWorktree(ctx, opts)
}
func (LocalGit) InspectWorkflowReset(ctx context.Context, req gitmeta.WorkflowResetRequest) (string, error) {
	return gitmeta.InspectWorkflowReset(ctx, req)
}
func (LocalGit) ResolveWorkflowReset(ctx context.Context, req gitmeta.WorkflowResetRequest) (gitmeta.Revision, error) {
	return gitmeta.ResolveWorkflowReset(ctx, req)
}
func (LocalGit) ApplyWorkflowReset(ctx context.Context, req gitmeta.WorkflowResetRequest) error {
	return gitmeta.ApplyWorkflowReset(ctx, req)
}
