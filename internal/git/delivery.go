package git

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/task"
)

// PublishedRevision observes the actual origin branch. Its tree is read from the
// named native object; unavailable objects fail instead of substituting HEAD.
func PublishedRevision(ctx context.Context, dir, branch string) (Revision, error) {
	commit, err := ObserveRemoteBranch(ctx, dir, branch)
	if err != nil {
		return Revision{}, err
	}
	tree, err := revisionTree(ctx, dir, commit)
	if err != nil {
		return Revision{}, err
	}
	return Revision{Commit: commit, Tree: tree}, nil
}

// DirectRevisionDelivery consumes already committed reviewed code. A local merge
// is not delivery: its tree and the independently observed pushed ref must match.
// A disconnected retry can recover only the exact ordinary two-parent merge.
func DirectRevisionDelivery(ctx context.Context, repo task.Repository, dir, branch, destination, destinationCommit, retention string, output Revision) (Revision, error) {
	if err := verifyDirectDeliverySource(ctx, repo, dir, branch, destination, retention, output); err != nil {
		return Revision{}, err
	}

	remote, err := ObserveRemoteBranch(ctx, repo.Path, destination)
	if err != nil {
		return Revision{}, err
	}
	// The only acceptable changed remote is this exact recovered merge, checked
	// again below by native parent identity and tree, never mere containment.
	if remote != destinationCommit {
		if err := verifyDirectMergeCode(ctx, repo.Path, remote, destinationCommit, output); err != nil {
			return Revision{}, fmt.Errorf("integration destination changed since review: %w", err)
		}
	}
	merge, err := MergeTaskBranchIntoDestination(ctx, repo, destination, branch)
	if err != nil {
		return Revision{}, err
	}
	tree, err := revisionTree(ctx, repo.Path, merge)
	if err != nil {
		return Revision{}, err
	}
	if tree != output.Tree {
		return Revision{}, fmt.Errorf("local integration tree differs from reviewed output; destination was not pushed")
	}
	if err := verifyDirectMergeCode(ctx, repo.Path, merge, destinationCommit, output); err != nil {
		return Revision{}, err
	}
	if err := createRevisionRef(ctx, repo.Path, retention, merge); err != nil {
		return Revision{}, err
	}
	local := Revision{Commit: merge, Tree: tree, RetainedRef: retention}
	if err := VerifyRevision(ctx, repo.Path, CheckoutIdentity{Commit: merge, Ref: "refs/heads/" + destination}, local); err != nil {
		return Revision{}, err
	}
	pushErr := PushDefaultBranch(ctx, repo.Path, destination)
	observed, err := PublishedRevision(ctx, repo.Path, destination)
	if err != nil {
		return Revision{}, errors.Join(pushErr, err)
	}
	if observed.Commit != merge || observed.Tree != output.Tree {
		return Revision{}, errors.Join(pushErr, fmt.Errorf("origin destination does not exactly match the reviewed integration result"))
	}
	return observed, nil
}

func verifyDirectDeliverySource(ctx context.Context, repo task.Repository, dir, branch, destination, retention string, output Revision) error {
	if !strings.HasPrefix(retention, "refs/orpheus/") {
		return errors.New("direct delivery requires an owned retention ref")
	}
	if _, err := runGitContext(ctx, repo.Path, "check-ref-format", retention); err != nil {
		return err
	}
	if branch == destination {
		return fmt.Errorf("direct delivery requires a separate task branch")
	}
	if err := VerifyRetainedRevision(ctx, dir, output); err != nil {
		return err
	}
	source, err := refCommit(ctx, dir, "refs/heads/"+branch)
	if err != nil {
		return err
	}
	if source != output.Commit {
		return errors.New("task branch no longer names the reviewed output")
	}
	current, err := (CandidateOperations{}).CheckoutIdentity(ctx, dir)
	if err != nil {
		return err
	}
	if current.Ref == "refs/heads/"+branch {
		if err := VerifyRevision(ctx, dir, current, output); err != nil {
			return err
		}
	} else if dir != repo.Path || current.Ref != "refs/heads/"+destination {
		return fmt.Errorf("direct delivery checkout is neither its reviewed task branch nor its recoverable destination")
	}
	return nil
}

func verifyDirectMergeCode(ctx context.Context, dir, commit, parent string, output Revision) error {
	parents, err := runGitContext(ctx, dir, "show", "--no-patch", "--format=%P", commit)
	if err != nil {
		return err
	}
	fields := strings.Fields(parents)
	if len(fields) != 2 || fields[0] != parent || fields[1] != output.Commit {
		return fmt.Errorf("integration parents differ from reviewed destination and output; destination was not pushed")
	}
	tree, err := revisionTree(ctx, dir, commit)
	if err != nil {
		return err
	}
	if tree != output.Tree {
		return fmt.Errorf("integration tree differs from reviewed output; destination was not pushed")
	}
	return nil
}
