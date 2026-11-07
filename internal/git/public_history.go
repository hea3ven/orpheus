package git

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PublicCommitRequest packages a retained input independently of the task checkout.
// Ref is the durable operation identity, recorded by the controller before entry.
type PublicCommitRequest struct {
	Directory, Branch, Parent, Ref, Message string
	Input                                   Revision
}

// PackagePublicCommit uses ordinary commit policy in an operation-owned worktree.
// The retained result and HEAD reflog reconcile a disconnected commit before retry.
// Hook-produced trees are returned honestly; the caller must not push a changed tree.
func PackagePublicCommit(ctx context.Context, req PublicCommitRequest) (Revision, error) {
	if !strings.HasPrefix(req.Ref, "refs/orpheus/") || !validObjectID(req.Parent) || strings.TrimSpace(req.Message) == "" {
		return Revision{}, errors.New("public packaging requires owned operation, exact parent and message")
	}
	if _, err := runGitContext(ctx, req.Directory, "check-ref-format", req.Ref+"/output"); err != nil {
		return Revision{}, err
	}
	if err := VerifyRevision(ctx, req.Directory, CheckoutIdentity{Commit: req.Input.Commit, Ref: "refs/heads/" + req.Branch}, req.Input); err != nil {
		return Revision{}, err
	}
	retained, err := optionalRevisionRef(ctx, req.Directory, req.Ref+"/output")
	if err != nil {
		return Revision{}, err
	}
	if retained != "" {
		return publicCommitResult(ctx, req, retained)
	}
	dir, err := publicPackagingWorktree(ctx, req)
	if err != nil {
		return Revision{}, err
	}
	head, err := (CandidateOperations{}).CheckoutIdentity(ctx, dir)
	if err != nil {
		return Revision{}, err
	}
	if head.Ref != "" {
		return Revision{}, errors.New("packaging worktree is no longer detached")
	}
	if head.Commit != req.Parent {
		return reconcilePublicCommit(ctx, req, dir, head.Commit)
	}
	// Never stage or alter the operator checkout. Populate only this owned index
	// and worktree, and refuse unresolved Git operations or unexpected dirt.
	if err := stagePublicInput(ctx, req, dir); err != nil {
		return Revision{}, err
	}
	output, commitErr := runCommand(ctx, loggerFromContext(ctx), "package_publication", Command{Directory: dir, Args: []string{"commit", "--allow-empty", "--file=-"}, Input: req.Message, Environment: map[string]string{"GIT_REFLOG_ACTION": "orpheus publication " + req.Ref}})
	if commitErr != nil {
		return Revision{}, fmt.Errorf("public commit outcome requires reconciliation before retry: %w%s", commitErr, gitOutputSuffix(output))
	}
	head, err = (CandidateOperations{}).CheckoutIdentity(ctx, dir)
	if err != nil {
		return Revision{}, err
	}
	return reconcilePublicCommit(ctx, req, dir, head.Commit)
}

func publicPackagingWorktree(ctx context.Context, req PublicCommitRequest) (string, error) {
	common, err := gitCommonDir(ctx, req.Directory)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(common, "orpheus-publications", fmt.Sprintf("%x", sha256.Sum256([]byte(req.Ref))))
	_, err = os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := createPublicPackagingWorktree(ctx, req, dir); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	root, err := worktreeRoot(ctx, dir)
	if err != nil {
		return "", err
	}
	if root != dir {
		return "", errors.New("packaging worktree identity changed")
	}
	return dir, nil
}

func createPublicPackagingWorktree(ctx context.Context, req PublicCommitRequest, dir string) error {
	if _, err := revisionTree(ctx, req.Directory, req.Parent); err != nil {
		if _, err := runGitContext(ctx, req.Directory, "fetch", "--no-tags", "origin", req.Parent); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	_, err := runGitContext(ctx, req.Directory, "worktree", "add", "--detach", dir, req.Parent)
	return err
}

func stagePublicInput(ctx context.Context, req PublicCommitRequest, dir string) error {
	if err := verifyCommittedCheckout(ctx, dir, req.Input.Commit, req.Input.Tree); err == nil {
		return nil
	}
	parentTree, err := revisionTree(ctx, dir, req.Parent)
	if err != nil {
		return err
	}
	if err := verifyCommittedCheckout(ctx, dir, req.Parent, parentTree); err != nil {
		return fmt.Errorf("packaging checkout changed; reconcile before retry: %w", err)
	}
	_, err = runGitContext(ctx, dir, "read-tree", "--reset", "-u", req.Input.Commit)
	return err
}

func reconcilePublicCommit(ctx context.Context, req PublicCommitRequest, dir, commit string) (Revision, error) {
	entry, err := runGitContext(ctx, dir, "reflog", "show", "-1", "--format=%H%n%gs", "HEAD")
	if err != nil {
		return Revision{}, err
	}
	if !strings.HasPrefix(entry, commit+"\norpheus publication "+req.Ref+": ") {
		return Revision{}, errors.New("packaging HEAD does not identify this operation; inspect before retry")
	}
	result, err := publicCommitResult(ctx, req, commit)
	if err != nil {
		return Revision{}, err
	}
	if err := createRevisionRef(ctx, req.Directory, result.RetainedRef, commit); err != nil {
		return Revision{}, err
	}
	if err := verifyCommittedCheckout(ctx, dir, commit, result.Tree); err != nil {
		return Revision{}, fmt.Errorf("public commit retained but hook checkout changes require reconciliation: %w", err)
	}
	return result, nil
}

func publicCommitResult(ctx context.Context, req PublicCommitRequest, commit string) (Revision, error) {
	parents, err := runGitContext(ctx, req.Directory, "show", "--no-patch", "--format=%P", commit)
	if err != nil {
		return Revision{}, err
	}
	if strings.TrimSpace(parents) != req.Parent {
		return Revision{}, errors.New("public commit does not append to the exact authorized parent")
	}
	tree, err := revisionTree(ctx, req.Directory, commit)
	return Revision{Commit: commit, Tree: tree, RetainedRef: req.Ref + "/output"}, err
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

// PushPublicCommit is an append-only compare-and-swap. The explicit lease closes
// the check/write race, but cannot authorize a rewrite: the sole parent must be
// the expected head (or the recorded initial baseline for branch creation).
func PushPublicCommit(ctx context.Context, req PublicCommitRequest, public Revision, expected string) error {
	if err := VerifyRevision(ctx, req.Directory, CheckoutIdentity{Commit: req.Input.Commit, Ref: "refs/heads/" + req.Branch}, req.Input); err != nil {
		return err
	}
	if err := VerifyRetainedRevision(ctx, req.Directory, public); err != nil {
		return err
	}
	actual, err := publicCommitResult(ctx, req, public.Commit)
	if err != nil {
		return err
	}
	if actual.Tree != req.Input.Tree || actual.Tree != public.Tree || (expected != "" && req.Parent != expected) {
		return errors.New("public push must preserve input and append to expected head")
	}
	remote, err := ObservePublicHead(ctx, req.Directory, req.Branch)
	if err != nil {
		return err
	}
	if remote == public.Commit {
		return nil
	}
	if remote != expected {
		return errors.New("unexpected public head; reconcile remote changes before publication")
	}
	if err := validateBranchRef(ctx, req.Directory, "public branch", req.Branch); err != nil {
		return err
	}
	output, err := runGitContext(ctx, req.Directory, "push", "--no-follow-tags", "--force-with-lease=refs/heads/"+req.Branch+":"+expected, "origin", public.Commit+":refs/heads/"+req.Branch)
	if err != nil {
		return fmt.Errorf("push append-only public commit: %w%s", err, gitOutputSuffix(output))
	}
	return nil
}
