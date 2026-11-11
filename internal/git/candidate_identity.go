package git

import (
	"context"
	"fmt"
	"strings"
)

// CheckoutIdentity identifies both HEAD's commit and its attachment. A clean
// checkout on a different branch is not the same review input.
type CheckoutIdentity struct {
	Commit string
	Ref    string
}

// CheckoutIdentity observes a checkout's actual commit and symbolic HEAD.
func (ops CandidateOperations) CheckoutIdentity(ctx context.Context, dir string) (CheckoutIdentity, error) {
	head, err := HeadCommit(ctx, dir)
	if err != nil {
		return CheckoutIdentity{}, err
	}
	ref, err := runGitContextLogger(ctx, ops.loggerFor(ctx), dir, "candidate_head_ref", "symbolic-ref", "--quiet", "HEAD")
	if err != nil && gitExitCode(err) != 1 {
		return CheckoutIdentity{}, fmt.Errorf("read candidate HEAD attachment: %w: %s", err, strings.TrimSpace(ref))
	}
	return CheckoutIdentity{Commit: head, Ref: strings.TrimSpace(ref)}, nil
}

// RestoreCheckoutIdentity restores only the captured branch (or detached HEAD),
// without moving a different branch that a reviewer may have selected. Reset
// restores the tracked tree and index and cleans non-ignored untracked paths.
// Ignored paths obstructing restoration cause refusal rather than data loss.
func (ops CandidateOperations) RestoreCheckoutIdentity(ctx context.Context, dir string, identity CheckoutIdentity) error {
	if identity.Commit == "" || (identity.Ref != "" && !strings.HasPrefix(identity.Ref, "refs/heads/")) {
		return fmt.Errorf("invalid captured checkout identity")
	}
	root, err := worktreeRoot(ctx, dir)
	if err != nil {
		return err
	}
	if root != dir {
		return fmt.Errorf("review checkout was redirected from %q to %q; refusing restoration", dir, root)
	}
	ignored, err := protectedIgnoredReviewFiles(ctx, dir, identity.Commit)
	if err != nil {
		return err
	}
	if err := restoreReviewHEAD(ctx, dir, identity); err != nil {
		return err
	}
	if output, err := runGitContext(ctx, dir, "reset", "--hard", identity.Commit, "--"); err != nil {
		return fmt.Errorf("restore review checkout: %w: %s", err, strings.TrimSpace(output))
	}
	// Preserve both the restored ignore rules and paths ignored before reset.
	// These are names for clean's exclusions, not content snapshots.
	args := []string{"clean", "-fd"}
	escape := strings.NewReplacer(`\`, `\\`, `*`, `\*`, `?`, `\?`, `[`, `\[`, `]`, `\]`)
	for _, path := range ignored {
		args = append(args, "-e", "/"+escape.Replace(path))
	}
	if output, err := runGitContext(ctx, dir, args...); err != nil {
		return fmt.Errorf("clean review checkout: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func restoreReviewHEAD(ctx context.Context, dir string, identity CheckoutIdentity) error {
	if identity.Ref == "" {
		if _, err := runGitContext(ctx, dir, "update-ref", "--no-deref", "HEAD", identity.Commit); err != nil {
			return fmt.Errorf("restore detached review HEAD: %w", err)
		}
		return nil
	}
	current, err := gitRefCommit(ctx, dir, identity.Ref)
	if err != nil {
		return err
	}
	if _, err := runGitContext(ctx, dir, "update-ref", identity.Ref, identity.Commit, current); err != nil {
		return fmt.Errorf("restore review branch: %w", err)
	}
	if _, err := runGitContext(ctx, dir, "symbolic-ref", "HEAD", identity.Ref); err != nil {
		return fmt.Errorf("restore review HEAD attachment: %w", err)
	}
	return nil
}

// A hard reset may overwrite ignored files that obstruct its target. Refuse
// these collisions before touching refs, the index, or the checkout.
func protectedIgnoredReviewFiles(ctx context.Context, dir, commit string) ([]string, error) {
	ignored, err := runGitContext(ctx, dir, "ls-files", "--others", "--ignored", "--exclude-standard", "-z")
	if err != nil {
		return nil, fmt.Errorf("inspect ignored files before rollback: %w", err)
	}
	if ignored == "" {
		return nil, nil
	}
	tree, err := runGitContext(ctx, dir, "ls-tree", "-r", "--name-only", "-z", commit)
	if err != nil {
		return nil, fmt.Errorf("inspect rollback target: %w", err)
	}
	targets := splitNUL([]byte(tree))
	paths := splitNUL([]byte(ignored))
	for _, path := range paths {
		for _, target := range targets {
			if path == target || strings.HasPrefix(path, target+"/") || strings.HasPrefix(target, path+"/") {
				return nil, fmt.Errorf("ignored path %q obstructs rollback target %q; preserve or relocate it before retrying", path, target)
			}
		}
	}
	return paths, nil
}
