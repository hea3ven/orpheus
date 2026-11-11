package git

import (
	"context"
	"fmt"
	"strings"
)

// PublicationInputRequest selects existing committed output, never stages or
// commits files. Accepted permits only the existing direct-merge recovery checkout.
type PublicationInputRequest struct {
	Directory, Branch, Destination, Ref string
	DeliveryRef, AcceptedDestination    string
	Reviewed                            Revision
	Accepted                            *Revision
}

// PublicationInputObservation keeps native input and comparison separate from
// review approval. A rewritten commit has no implied agent producer.
type PublicationInputObservation struct {
	Recovering   bool
	Revision     Revision
	Relationship string
	Changes      string
}

// ObservePublicationInput retains the clean owned task-branch HEAD and describes
// its relationship and patch to the reviewed commit, without changing the checkout.
func ObservePublicationInput(ctx context.Context, req PublicationInputRequest) (PublicationInputObservation, error) {
	if !strings.HasPrefix(req.Ref, "refs/orpheus/") {
		return PublicationInputObservation{}, fmt.Errorf("publication input requires an owned retention namespace")
	}
	if err := VerifyRetainedRevision(ctx, req.Directory, req.Reviewed); err != nil {
		return PublicationInputObservation{}, err
	}
	head, err := (CandidateOperations{}).CheckoutIdentity(ctx, req.Directory)
	if err != nil {
		return PublicationInputObservation{}, err
	}
	input, err := selectPublicationHead(ctx, req, head)
	if err != nil {
		return PublicationInputObservation{}, err
	}
	recovering := false
	if req.Accepted != nil && req.DeliveryRef != "" {
		merge, err := optionalRevisionRef(ctx, req.Directory, req.DeliveryRef)
		if err != nil {
			return PublicationInputObservation{}, err
		}
		if merge != "" {
			if err := verifyDirectMergeCode(ctx, req.Directory, merge, req.AcceptedDestination, *req.Accepted); err != nil {
				return PublicationInputObservation{}, err
			}
			recovering = true
		}
	}
	relationship := "reviewed"
	var changes string
	if input.Commit != req.Reviewed.Commit {
		relationship, err = publicationRelationship(ctx, req.Directory, req.Reviewed.Commit, input.Commit)
		if err != nil {
			return PublicationInputObservation{}, err
		}
		changes, err = runGitContext(ctx, req.Directory, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--stat", "--patch", req.Reviewed.Commit, input.Commit, "--")
		if err != nil {
			return PublicationInputObservation{}, err
		}
		if changes == "" {
			changes = "No tree differences; commit identity changed."
		}
	}
	return PublicationInputObservation{Recovering: recovering, Revision: input, Relationship: relationship, Changes: changes}, nil
}

func selectPublicationHead(ctx context.Context, req PublicationInputRequest, head CheckoutIdentity) (Revision, error) {
	root, err := worktreeRoot(ctx, req.Directory)
	if err != nil {
		return Revision{}, err
	}
	if root != req.Directory {
		return Revision{}, fmt.Errorf("publication requires exact recorded worktree root %q, got %q", req.Directory, root)
	}
	if err := CheckCheckout(ctx, req.Directory, head); err != nil {
		return Revision{}, err
	}
	if head.Ref != "refs/heads/"+req.Branch {
		// A repo-root direct merge can have checked out the destination before a
		// disconnected push. Its delivery adapter still verifies the merge and
		// clean destination before any retry; it may not select different input.
		if req.Accepted != nil && head.Ref == "refs/heads/"+req.Destination {
			branch, err := refCommit(ctx, req.Directory, "refs/heads/"+req.Branch)
			if err == nil && branch == req.Accepted.Commit {
				return *req.Accepted, VerifyRetainedRevision(ctx, req.Directory, *req.Accepted)
			}
		}
		return Revision{}, fmt.Errorf("publication requires HEAD attached to owned task branch %q", req.Branch)
	}
	tree, err := revisionTree(ctx, req.Directory, head.Commit)
	if err != nil {
		return Revision{}, err
	}
	input := Revision{Commit: head.Commit, Tree: tree, RetainedRef: req.Ref + "/" + head.Commit}
	if head.Commit == req.Reviewed.Commit {
		input.RetainedRef = req.Reviewed.RetainedRef
	} else if err := createRevisionRef(ctx, req.Directory, input.RetainedRef, head.Commit); err != nil {
		return Revision{}, err
	}
	return input, VerifyRetainedRevision(ctx, req.Directory, input)
}

func publicationRelationship(ctx context.Context, dir, reviewed, input string) (string, error) {
	base, err := runGitContext(ctx, dir, "merge-base", reviewed, input)
	if err != nil {
		if gitExitCode(err) == 1 {
			return "unrelated history", nil
		}
		return "", err
	}
	switch strings.TrimSpace(base) {
	case reviewed:
		return "additional commits after review", nil
	case input:
		return "earlier commit than reviewed output", nil
	default:
		return "amended or rewritten history", nil
	}
}
