// Package pullrequest defines the narrow PR provider boundary used by workflows.
package pullrequest

import "context"

// PullRequest is the backend-neutral PR identity needed by Orpheus.
type PullRequest struct {
	URL string
}

// State is the backend-neutral lifecycle state of a pull request.
type State string

const (
	// StateOpen means the pull request is still open for review.
	StateOpen State = "open"

	// StateMerged means the pull request was merged.
	StateMerged State = "merged"

	// StateClosed means the pull request was closed without merge.
	StateClosed State = "closed"
)

// PullRequestStatus is the backend-neutral PR state observed from the provider.
type PullRequestStatus struct {
	URL   string
	State State
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

// StatusByURLRequest identifies a pull request by URL.
type StatusByURLRequest struct {
	URL string
}

// Provider creates and recovers pull requests for a repository.
type Provider interface {
	FindOpenByBranch(ctx context.Context, req FindOpenByBranchRequest) (PullRequest, bool, error)
	Create(ctx context.Context, req CreateRequest) (PullRequest, error)
	StatusByURL(ctx context.Context, req StatusByURLRequest) (PullRequestStatus, error)
}
