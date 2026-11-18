package git

import (
	"context"
	"fmt"
	"strings"
)

// ReviewRevisionRequest identifies the checkout and the implementation handoff's
// retained commit. Ref is the namespace of the new review, not a branch ref.
type ReviewRevisionRequest struct {
	Directory, Branch, Ref string
	Implementation         Revision
}

// ReviewRevisionSelection describes the selected tip and any history not in
// the implementation commit. An empty Relationship means the tip is unchanged.
type ReviewRevisionSelection struct {
	Revision     Revision
	Relationship string
	AddedCommits []string
}

func SelectReviewRevision(ctx context.Context, req ReviewRevisionRequest) (ReviewRevisionSelection, error) {
	if err := VerifyRetainedRevision(ctx, req.Directory, req.Implementation); err != nil {
		return ReviewRevisionSelection{}, err
	}
	head, err := inspectTaskCheckout(ctx, req.Directory, req.Branch)
	if err != nil {
		return ReviewRevisionSelection{}, fmt.Errorf("select review revision: %w", err)
	}
	if head.Commit == req.Implementation.Commit {
		return ReviewRevisionSelection{Revision: req.Implementation}, nil
	}
	if !strings.HasPrefix(req.Ref, "refs/orpheus/") {
		return ReviewRevisionSelection{}, fmt.Errorf("review selection requires an owned retention namespace")
	}
	relation, err := compareCommitHistory(ctx, req.Directory, req.Implementation.Commit, head.Commit)
	if err != nil {
		return ReviewRevisionSelection{}, err
	}
	added, err := runGitContext(ctx, req.Directory, "log", "--no-notes", "--no-show-signature", "--no-color",
		"--format=%h %s", req.Implementation.Commit+".."+head.Commit, "--")
	if err != nil {
		return ReviewRevisionSelection{}, fmt.Errorf("list commits added to review history: %w", err)
	}
	head.RetainedRef = req.Ref + "/review-input/" + head.Commit
	if err := createRevisionRef(ctx, req.Directory, head.RetainedRef, head.Commit); err != nil {
		return ReviewRevisionSelection{}, err
	}
	selection := ReviewRevisionSelection{Revision: head, Relationship: string(relation)}
	switch relation {
	case historyAdvanced:
		selection.Relationship = "additional commits after implementation"
	case historyEarlier:
		selection.Relationship = "earlier commit than implementation output"
	}
	if commits := strings.TrimSpace(added); commits != "" {
		selection.AddedCommits = strings.Split(commits, "\n")
	}
	return selection, nil
}
