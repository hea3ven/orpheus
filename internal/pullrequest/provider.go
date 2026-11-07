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
	URL        string
	State      State
	HeadBranch string
	BaseBranch string
	HeadCommit string
	Title      string
	Body       string
}

// DiagnosticContext carries safe workflow correlation for PR provider diagnostics.
type DiagnosticContext struct {
	RepoID string
	TaskID string
	Branch string
	HasPR  bool
}

// FindByBranchRequest searches by repository/head across all lifecycle states.
// BaseBranch is the expected destination context, not a discovery filter.
type FindByBranchRequest struct {
	RepositoryPath string
	HeadBranch     string
	BaseBranch     string
	Diagnostics    DiagnosticContext
}

// CreateRequest describes a PR to create.
type CreateRequest struct {
	RepositoryPath string
	HeadBranch     string
	BaseBranch     string
	Title          string
	Body           string
	Diagnostics    DiagnosticContext
}

// UpdateRequest replaces only authored publication text, never the branch pair.
type UpdateRequest struct {
	RepositoryPath, URL, Title, Body string
	Diagnostics                      DiagnosticContext
}

// StatusByURLRequest identifies a pull request by URL.
type StatusByURLRequest struct {
	URL         string
	Diagnostics DiagnosticContext
}

// Provider creates and reconciles pull requests for a repository.
type Provider interface {
	FindByBranch(ctx context.Context, req FindByBranchRequest) (PullRequest, bool, error)
	Create(ctx context.Context, req CreateRequest) (PullRequest, error)
	Update(ctx context.Context, req UpdateRequest) error
	StatusByURL(ctx context.Context, req StatusByURLRequest) (PullRequestStatus, error)
}
