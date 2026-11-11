package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// PublicCommitRequest packages one authorized input in the owned task checkout.
// Ref names its temporary recovery input, not a second working history.
type PublicCommitRequest struct {
	Directory, Branch, Parent, Ref, Message string
	Input                                   Revision
}

// RetainPublicationInput precedes durable operation intent and local rewriting.
func RetainPublicationInput(ctx context.Context, req PublicCommitRequest) error {
	if !strings.HasPrefix(req.Ref, "refs/orpheus/") || !validObjectID(req.Parent) || strings.TrimSpace(req.Message) == "" {
		return errors.New("publication requires owned intent, baseline and content")
	}
	if err := VerifyRevision(ctx, req.Directory, CheckoutIdentity{Commit: req.Input.Commit, Ref: "refs/heads/" + req.Branch}, req.Input); err != nil {
		return err
	}
	return createRevisionRef(ctx, req.Directory, req.Ref+"/input", req.Input.Commit)
}

// PackagePublicCommit performs the initial squash in place using ordinary hooks
// and signing. It never reconstructs an interrupted commit: without a durable
// result, the controller must request explicit restoration or manual repair.
func PackagePublicCommit(ctx context.Context, req PublicCommitRequest) (Revision, error) {
	saved := req.Input
	saved.RetainedRef = req.Ref + "/input"
	if err := VerifyRevision(ctx, req.Directory, CheckoutIdentity{Commit: saved.Commit, Ref: "refs/heads/" + req.Branch}, saved); err != nil {
		return Revision{}, err
	}
	if err := requireNoGitOperation(ctx, req.Directory); err != nil {
		return Revision{}, err
	}
	if _, err := runGitContext(ctx, req.Directory, "reset", "--soft", req.Parent); err != nil {
		return Revision{}, err
	}
	output, err := runCommand(ctx, loggerFromContext(ctx), "package_publication", Command{Directory: req.Directory, Args: []string{"commit", "--allow-empty", "--file=-"}, Input: req.Message})
	if err != nil {
		return Revision{}, fmt.Errorf("publication packaging interrupted; use task run for restore-or-manual-repair recovery: %w%s", err, gitOutputSuffix(output))
	}
	result, err := InspectPublicationCheckout(ctx, req.Directory, req.Branch)
	if err != nil {
		return Revision{}, err
	}
	if err := verifyPublicParent(ctx, req, result.Commit); err != nil {
		return Revision{}, err
	}
	if err := createRevisionRef(ctx, req.Directory, publicCheckpointNamespace(req)+"/"+result.Commit, result.Commit); err != nil {
		return Revision{}, err
	}
	return result, nil
}

// InspectPublicationCheckout verifies the actual clean task checkout without
// requiring historical private objects or retention refs to remain available.
func InspectPublicationCheckout(ctx context.Context, dir, branch string) (Revision, error) {
	root, err := worktreeRoot(ctx, dir)
	if err != nil {
		return Revision{}, err
	}
	if root != dir {
		return Revision{}, errors.New("publication requires exact owned worktree root")
	}
	head, err := (CandidateOperations{}).CheckoutIdentity(ctx, dir)
	if err != nil {
		return Revision{}, err
	}
	if head.Ref != "refs/heads/"+branch {
		return Revision{}, errors.New("publication requires HEAD on the owned task branch")
	}
	tree, err := revisionTree(ctx, dir, head.Commit)
	if err != nil {
		return Revision{}, err
	}
	if err := CheckCheckout(ctx, dir, head); err != nil {
		return Revision{}, err
	}
	return Revision{Commit: head.Commit, Tree: tree}, nil
}

func verifyPublicParent(ctx context.Context, req PublicCommitRequest, commit string) error {
	parents, err := runGitContext(ctx, req.Directory, "show", "--no-patch", "--format=%P", commit)
	if err != nil {
		return err
	}
	if strings.TrimSpace(parents) != req.Parent {
		return errors.New("public commit must have the recorded task baseline as its sole parent")
	}
	return nil
}

// ObservePublicHead distinguishes confirmed absence from transport failure.
func ObservePublicHead(ctx context.Context, dir, branch string) (string, error) {
	if err := validateBranchRef(ctx, dir, "public branch", branch); err != nil {
		return "", err
	}
	output, err := runGitContext(ctx, dir, "ls-remote", "--exit-code", "--heads", "origin", "refs/heads/"+branch)
	if gitExitCode(err) == 2 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) != 2 || !validObjectID(fields[0]) || fields[1] != "refs/heads/"+branch {
		return "", errors.New("ambiguous public branch observation")
	}
	return fields[0], nil
}

// PushPublicCommit delivers only the durably recorded initial public commit.
// A lease permits creation of an absent ref, never replacement of remote work.
func PushPublicCommit(ctx context.Context, req PublicCommitRequest, public Revision) error {
	current, err := InspectPublicationCheckout(ctx, req.Directory, req.Branch)
	if err != nil {
		return err
	}
	if current.Commit != public.Commit || current.Tree != public.Tree || public.Tree != req.Input.Tree {
		return errors.New("public checkout differs from the recorded authorized publication")
	}
	if err := verifyPublicParent(ctx, req, public.Commit); err != nil {
		return err
	}
	remote, err := ObservePublicHead(ctx, req.Directory, req.Branch)
	if err != nil {
		return err
	}
	if remote == public.Commit {
		return configurePublicationUpstream(ctx, req.Directory, req.Branch, public.Commit)
	}
	if remote != "" {
		return errors.New("unexpected public head; reconcile before publication")
	}
	output, err := runGitContext(ctx, req.Directory, "push", "--no-follow-tags", "--force-with-lease=refs/heads/"+req.Branch+":", "origin", public.Commit+":refs/heads/"+req.Branch)
	if err != nil {
		return fmt.Errorf("push initial public commit: %w%s", err, gitOutputSuffix(output))
	}
	remote, err = ObservePublicHead(ctx, req.Directory, req.Branch)
	if err != nil {
		return err
	}
	if remote != public.Commit {
		return errors.New("public head changed before upstream setup; reconcile before publication")
	}
	return configurePublicationUpstream(ctx, req.Directory, req.Branch, public.Commit)
}

func configurePublicationUpstream(ctx context.Context, dir, branch, commit string) error {
	// A disconnected push may have reached origin before its local tracking ref
	// was written. Rebuild that cache from the independently verified remote head.
	if _, err := runGitContext(ctx, dir, "update-ref", "--no-deref", "refs/remotes/origin/"+branch, commit); err != nil {
		return fmt.Errorf("record verified publication tracking ref: %w", err)
	}
	output, err := runGitContext(ctx, dir, "branch", "--set-upstream-to=origin/"+branch, "--", branch)
	if err != nil {
		return fmt.Errorf("configure publication upstream: %w%s", err, gitOutputSuffix(output))
	}
	return nil
}

// publicCheckpointNamespace retains live output independently of private history.
func publicCheckpointNamespace(req PublicCommitRequest) string {
	return strings.Replace(req.Ref, "/revisions/", "/publications/", 1) + "/prepared"
}
