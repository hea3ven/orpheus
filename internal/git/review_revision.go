package git

import (
	"context"
	"fmt"
	"strings"
)

// ReviewRevisionRequest identifies the checkout and a retained comparison
// revision. Ref is the namespace of the new review, not a branch ref.
type ReviewRevisionRequest struct {
	Directory, Branch, Ref string
	Comparison             Revision
}

// ReviewRevisionSelection describes the selected tip and any history not in
// the comparison revision. An empty Relationship means the tip is unchanged.
type ReviewRevisionSelection struct {
	Revision     Revision
	Relationship string
	AddedCommits []string
}

func SelectReviewRevision(ctx context.Context, req ReviewRevisionRequest) (ReviewRevisionSelection, error) {
	if err := VerifyRetainedRevision(ctx, req.Directory, req.Comparison); err != nil {
		return ReviewRevisionSelection{}, err
	}
	head, err := inspectTaskCheckout(ctx, req.Directory, req.Branch)
	if err != nil {
		return ReviewRevisionSelection{}, fmt.Errorf("select review revision: %w", err)
	}
	if head.Commit == req.Comparison.Commit {
		return ReviewRevisionSelection{Revision: req.Comparison}, nil
	}
	if !strings.HasPrefix(req.Ref, "refs/orpheus/") {
		return ReviewRevisionSelection{}, fmt.Errorf("review selection requires an owned retention namespace")
	}
	relation, err := compareCommitHistory(ctx, req.Directory, req.Comparison.Commit, head.Commit)
	if err != nil {
		return ReviewRevisionSelection{}, err
	}
	added, err := runGitContext(ctx, req.Directory, "log", "--no-notes", "--no-show-signature", "--no-color",
		"--format=%h %s", req.Comparison.Commit+".."+head.Commit, "--")
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
		selection.Relationship = "additional commits"
	case historyEarlier:
		selection.Relationship = "earlier commit"
	}
	if commits := strings.TrimSpace(added); commits != "" {
		selection.AddedCommits = strings.Split(commits, "\n")
	}
	return selection, nil
}
