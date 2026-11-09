package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PublicationRecovery is a disclosure of the checkout that restoration discards.
// It is a normal HEAD/status/diff observation, not a reconstruction classifier.
type PublicationRecovery struct {
	Head    CheckoutIdentity
	Changes string
}

// InspectPublicationRecovery describes the saved target and current changes.
func InspectPublicationRecovery(ctx context.Context, req PublicCommitRequest) (PublicationRecovery, error) {
	saved := req.Input
	saved.RetainedRef = req.Ref + "/input"
	if err := VerifyRetainedRevision(ctx, req.Directory, saved); err != nil {
		return PublicationRecovery{}, err
	}
	root, err := worktreeRoot(ctx, req.Directory)
	if err != nil {
		return PublicationRecovery{}, err
	}
	if root != req.Directory {
		return PublicationRecovery{}, errors.New("recovery requires the owned worktree root")
	}
	head, err := (CandidateOperations{}).CheckoutIdentity(ctx, req.Directory)
	if err != nil {
		return PublicationRecovery{}, err
	}
	if head.Ref != "refs/heads/"+req.Branch {
		return PublicationRecovery{}, errors.New("recovery requires the owned task branch; repair the checkout manually")
	}
	status, err := runGitContext(ctx, req.Directory, "status", "--short", "--untracked-files=all")
	if err != nil {
		return PublicationRecovery{}, err
	}
	diff, err := runGitContext(ctx, req.Directory, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--stat", "--patch", saved.Commit, "--")
	if err != nil {
		return PublicationRecovery{}, err
	}
	staged, err := runGitContext(ctx, req.Directory, "diff", "--cached", "--no-ext-diff", "--no-textconv", "--no-color", "--stat", "--patch", saved.Commit, "--")
	return PublicationRecovery{Head: head, Changes: fmt.Sprintf("Reset HEAD %s to saved input %s. Tracked changes and these untracked paths will be discarded; ignored files are kept.\n%s\nCheckout differences:\n%s\nIndex differences:\n%s", head.Commit, saved.Commit, status, diff, staged)}, err
}

// RestorePublicationInput runs only after explicit confirmation of this disclosure.
func RestorePublicationInput(ctx context.Context, req PublicCommitRequest, expected PublicationRecovery) error {
	actual, err := InspectPublicationRecovery(ctx, req)
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("recovery checkout changed; inspect and confirm restoration again")
	}
	if _, err := runGitContext(ctx, req.Directory, "reset", "--hard", req.Input.Commit); err != nil {
		return err
	}
	_, err = runGitContext(ctx, req.Directory, "clean", "-fd")
	return err
}

// ReleasePrivatePublicationRefs removes only exact workflow-owned namespaces.
// It never removes operator refs, prunes objects, or expires shared reflogs.
func ReleasePrivatePublicationRefs(ctx context.Context, dir string, namespaces []string) error {
	for _, prefix := range namespaces {
		parts := strings.Split(strings.TrimSuffix(prefix, "/"), "/")
		if len(parts) != 6 || parts[0] != "refs" || parts[1] != "orpheus" || (parts[2] != "revisions" && parts[2] != "publication-inputs") || parts[3] == "" || parts[4] == "" || parts[5] == "" || !strings.HasSuffix(prefix, "/") {
			return errors.New("private history cleanup requires workflow-owned namespaces")
		}
		output, err := runGitContext(ctx, dir, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", "--", prefix)
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
			if line == "" {
				continue
			}
			parts := strings.Split(line, "\x00")
			if len(parts) != 3 || !strings.HasPrefix(parts[0], prefix) || parts[2] != "" {
				return errors.New("private history ref changed ownership; inspect before cleanup")
			}
			if _, err := runGitContext(ctx, dir, "update-ref", "-d", parts[0], parts[1]); err != nil {
				return err
			}
		}
	}
	return nil
}
