// Package pullrequest defines the narrow PR provider boundary used by workflows.
package pullrequest

import "context"

// PullRequest is the backend-neutral PR identity needed by Orpheus.
type PullRequest struct {
	URL string
}

// FindOpenByBranchRequest identifies an open PR by repository and branch pair.
type FindOpenByBranchRequest struct {
	RepositoryPath string
	HeadBranch     string
	BaseBranch     string
}

// CreateRequest describes a PR to create.
type CreateRequest struct {
	RepositoryPath string
	HeadBranch     string
	BaseBranch     string
	Title          string
	Body           string
}

// Provider creates and recovers pull requests for a repository.
type Provider interface {
	FindOpenByBranch(ctx context.Context, req FindOpenByBranchRequest) (PullRequest, bool, error)
	Create(ctx context.Context, req CreateRequest) (PullRequest, error)
}
