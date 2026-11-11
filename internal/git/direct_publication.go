package git

import (
	"context"
	"errors"
	"fmt"

	"github.com/hea3ven/orpheus/internal/task"
)

// DirectIntegrationRequest binds local integration and push to a synchronized
// public head and one fixed destination checkpoint. It never pushes the source.
type DirectIntegrationRequest struct {
	Repository                                        task.Repository
	Directory, Branch, Destination, DestinationCommit string
	Source                                            Revision
}

// DirectIntegrationCheckpoints observes the live local branches independently of
// the recorded integration. Source changes never substitute a new delivery input.
type DirectIntegrationCheckpoints struct {
	Source      Revision
	Destination Revision
}

// BeginLocalTaskBranchSync uses the ordinary merge machinery without fetching,
// fast-forwarding or pushing a remote task branch. Only the destination is fetched.
func BeginLocalTaskBranchSync(ctx context.Context, opts TaskBranchSyncOptions, destinationCommit string) (TaskBranchSyncResult, error) {
	plan, err := newTaskBranchSyncPlan(opts)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	if err := validateTaskBranchSyncCheckout(ctx, plan); err != nil {
		return TaskBranchSyncResult{}, err
	}
	if err := requireCleanRepoRootFor(ctx, plan.Worktree, "direct publication sync"); err != nil {
		return TaskBranchSyncResult{}, err
	}
	if err := requireCleanRepoRootFor(ctx, plan.RepoPath, "direct publication sync"); err != nil {
		return TaskBranchSyncResult{}, err
	}
	if _, err := prepareDefaultBranchRef(ctx, plan.Worktree, plan.DefaultBranch); err != nil {
		return TaskBranchSyncResult{}, err
	}
	actual, err := refCommit(ctx, plan.Worktree, "refs/remotes/origin/"+plan.DefaultBranch)
	if err != nil {
		return TaskBranchSyncResult{}, err
	}
	if actual != destinationCommit {
		return TaskBranchSyncResult{}, errors.New("destination advanced; reconcile synchronization and consent before delivery")
	}
	return beginFetchedTaskBranchConflictResolution(ctx, plan, newTaskBranchSyncResult(plan))
}

// PrepareDirectIntegration creates a no-ff merge, but has no remote-write effect.
// Its caller must save the returned actual result before requesting a push.
func PrepareDirectIntegration(ctx context.Context, req DirectIntegrationRequest) (Revision, error) {
	current, err := InspectPublicationCheckout(ctx, req.Directory, req.Branch)
	if err != nil {
		return Revision{}, err
	}
	if current.Commit != req.Source.Commit || current.Tree != req.Source.Tree {
		return Revision{}, errors.New("synchronized public checkout changed before integration")
	}
	commit, err := mergeTaskBranchIntoDestination(ctx, req.Repository, req.Destination, req.Branch, req.DestinationCommit)
	if err != nil {
		return Revision{}, err
	}
	if err := verifyDirectMergeCode(ctx, req.Repository.Path, commit, req.DestinationCommit, req.Source); err != nil {
		return Revision{}, err
	}
	return Revision{Commit: commit, Tree: req.Source.Tree}, nil
}

// PushDirectIntegration reconciles ambiguous responses against the exact recorded
// merge. Remote advancement never authorizes overwrite or a second local merge.
func PushDirectIntegration(ctx context.Context, req DirectIntegrationRequest, result Revision) (Revision, error) {
	if err := verifyDirectMergeCode(ctx, req.Repository.Path, result.Commit, req.DestinationCommit, req.Source); err != nil {
		return Revision{}, err
	}
	remote, err := ObserveRemoteBranch(ctx, req.Repository.Path, req.Destination)
	if err != nil {
		return Revision{}, err
	}
	if remote == result.Commit {
		return PublishedRevision(ctx, req.Repository.Path, req.Destination)
	}
	if remote != req.DestinationCommit {
		return Revision{}, errors.New("destination advanced after synchronization; reconcile before delivery, without overwrite")
	}
	head, err := InspectPublicationCheckout(ctx, req.Repository.Path, req.Destination)
	if err != nil {
		return Revision{}, err
	}
	if head.Commit != result.Commit || head.Tree != result.Tree {
		return Revision{}, errors.New("destination checkout differs from recorded integration; repair manually before retry")
	}
	source, err := refCommit(ctx, req.Repository.Path, "refs/heads/"+req.Branch)
	if err != nil {
		return Revision{}, err
	}
	if source != req.Source.Commit {
		return Revision{}, errors.New("public task branch changed after integration")
	}
	if req.Directory != req.Repository.Path {
		actual, err := InspectPublicationCheckout(ctx, req.Directory, req.Branch)
		if err != nil {
			return Revision{}, err
		}
		if actual.Commit != req.Source.Commit || actual.Tree != req.Source.Tree {
			return Revision{}, errors.New("task checkout changed after integration")
		}
	}
	pushErr := pushExactCommit(ctx, req.Repository.Path, req.Destination, result.Commit)
	actual, err := PublishedRevision(ctx, req.Repository.Path, req.Destination)
	if err != nil {
		return Revision{}, errors.Join(pushErr, err)
	}
	if actual.Commit != result.Commit || actual.Tree != result.Tree {
		return Revision{}, errors.Join(pushErr, fmt.Errorf("destination delivery does not match recorded integration"))
	}
	return actual, nil
}

// ResumeDirectTaskCheckout returns a restored repo-root destination checkout to
// its existing public task branch. It never discards files or rewrites that branch.
func ResumeDirectTaskCheckout(ctx context.Context, dir, branch string) error {
	if err := requireCleanRepoRootFor(ctx, dir, "resume direct publication"); err != nil {
		return err
	}
	_, err := runGitContext(ctx, dir, "checkout", branch)
	return err
}

// DescribePublicChanges compares retained public checkpoints, not released review objects.
func DescribePublicChanges(ctx context.Context, dir, from, to string) (string, error) {
	if !validObjectID(from) || !validObjectID(to) {
		return "", errors.New("public comparison requires exact identities")
	}
	return runGitContext(ctx, dir, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--stat", "--patch", from, to, "--")
}

// InspectDirectIntegrationCheckpoints reads both affected clean checkouts without
// switching branches. In repo-root mode either the task or destination can be
// checked out while the operator reconciles an undelivered integration.
func InspectDirectIntegrationCheckpoints(ctx context.Context, req DirectIntegrationRequest) (DirectIntegrationCheckpoints, error) {
	head, err := (CandidateOperations{}).CheckoutIdentity(ctx, req.Repository.Path)
	if err != nil {
		return DirectIntegrationCheckpoints{}, err
	}
	rootBranch := req.Destination
	if req.Directory == req.Repository.Path && head.Ref == "refs/heads/"+req.Branch {
		rootBranch = req.Branch
	}
	if _, err := InspectPublicationCheckout(ctx, req.Repository.Path, rootBranch); err != nil {
		return DirectIntegrationCheckpoints{}, err
	}
	source, err := inspectLocalBranchRevision(ctx, req.Repository.Path, req.Branch)
	if err != nil {
		return DirectIntegrationCheckpoints{}, err
	}
	destination, err := inspectLocalBranchRevision(ctx, req.Repository.Path, req.Destination)
	if err != nil {
		return DirectIntegrationCheckpoints{}, err
	}
	if req.Directory != req.Repository.Path {
		current, err := InspectPublicationCheckout(ctx, req.Directory, req.Branch)
		if err != nil {
			return DirectIntegrationCheckpoints{}, err
		}
		if current.Commit != source.Commit || current.Tree != source.Tree {
			return DirectIntegrationCheckpoints{}, errors.New("task branch changed while inspecting integration checkpoints")
		}
	}
	return DirectIntegrationCheckpoints{Source: source, Destination: destination}, nil
}

func inspectLocalBranchRevision(ctx context.Context, dir, branch string) (Revision, error) {
	if err := validateBranchRef(ctx, dir, "integration checkpoint branch", branch); err != nil {
		return Revision{}, err
	}
	commit, err := refCommit(ctx, dir, "refs/heads/"+branch)
	if err != nil {
		return Revision{}, err
	}
	tree, err := revisionTree(ctx, dir, commit)
	if err != nil {
		return Revision{}, err
	}
	return Revision{Commit: commit, Tree: tree}, nil
}
