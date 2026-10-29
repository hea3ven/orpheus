package git

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Revision names committed output and the owned ref that keeps it reachable.
// Reused records that capture selected its exact unchanged input, not a new commit.
type Revision struct {
	Commit      string
	Tree        string
	RetainedRef string
	Reused      bool
}

// RevisionCapture describes an effort whose completion and successful process
// boundary have already been validated by the caller. Ref must be unique to the
// stable workflow/phase address, never a mutable task-wide "latest" ref.
type RevisionCapture struct {
	Directory string
	Input     CheckoutIdentity
	Ref       string
	Message   string
}

// CaptureRevision stages completed output and uses ordinary git commit. Hooks,
// signing and repository configuration are not bypassed. Owned input/result refs
// and the commit's reflog action allow an interrupted caller to reconcile before
// repeating a commit. Errors never authorize review or discard checkout changes.
// The caller must hold exclusive work-directory mutation ownership throughout.
func CaptureRevision(ctx context.Context, opts RevisionCapture) (Revision, error) {
	if err := validateRevisionCapture(ctx, opts); err != nil {
		return Revision{}, err
	}
	checkpoint, err := optionalRevisionRef(ctx, opts.Directory, opts.Ref+"/input")
	if err != nil {
		return Revision{}, err
	}
	if checkpoint != "" && checkpoint != opts.Input.Commit {
		return Revision{}, fmt.Errorf("revision capture input ref differs from this effort's input")
	}
	retained, err := optionalRevisionRef(ctx, opts.Directory, opts.Ref+"/output")
	if err != nil {
		return Revision{}, err
	}
	if retained != "" {
		if checkpoint == "" {
			return Revision{}, fmt.Errorf("retained revision has no capture input checkpoint")
		}
		return verifyCapturedRevision(ctx, opts, retained)
	}
	head, err := (CandidateOperations{}).CheckoutIdentity(ctx, opts.Directory)
	if err != nil {
		return Revision{}, err
	}
	if head.Ref != opts.Input.Ref {
		return Revision{}, fmt.Errorf("revision capture requires HEAD attached to %q, got %q", opts.Input.Ref, head.Ref)
	}
	if head.Commit != opts.Input.Commit {
		if checkpoint == "" {
			return Revision{}, fmt.Errorf("revision capture requires input commit %s, got %s", opts.Input.Commit, head.Commit)
		}
		return reconcileRevisionCommit(ctx, opts, head.Commit)
	}
	if err := createRevisionRef(ctx, opts.Directory, opts.Ref+"/input", opts.Input.Commit); err != nil {
		return Revision{}, err
	}
	if err := stageRevision(ctx, opts); err != nil {
		return Revision{}, err
	}
	return commitRevision(ctx, opts)
}

func validateRevisionCapture(ctx context.Context, opts RevisionCapture) error {
	if !strings.HasPrefix(opts.Input.Ref, "refs/heads/") || !validObjectID(opts.Input.Commit) || strings.TrimSpace(opts.Message) == "" {
		return fmt.Errorf("revision capture requires an attached input commit and completion message")
	}
	if !strings.HasPrefix(opts.Ref, "refs/orpheus/") || strings.HasSuffix(opts.Ref, "/") {
		return fmt.Errorf("revision capture requires an owned refs/orpheus ref namespace")
	}
	for _, ref := range []string{opts.Input.Ref, opts.Ref + "/input", opts.Ref + "/output"} {
		if output, err := runGitContext(ctx, opts.Directory, "check-ref-format", ref); err != nil {
			return fmt.Errorf("invalid revision ref %q: %w: %s", ref, err, strings.TrimSpace(output))
		}
	}
	root, err := worktreeRoot(ctx, opts.Directory)
	if err != nil {
		return err
	}
	if root != opts.Directory {
		return fmt.Errorf("revision capture requires the exact worktree root %q, got %q", root, opts.Directory)
	}
	return nil
}

func stageRevision(ctx context.Context, opts RevisionCapture) error {
	if err := checkRevisionIndexFlags(ctx, opts.Directory); err != nil {
		return err
	}
	if err := requireNoGitOperation(ctx, opts.Directory); err != nil {
		return err
	}
	return StageAll(ctx, opts.Directory)
}

func commitRevision(ctx context.Context, opts RevisionCapture) (Revision, error) {
	tree, err := revisionTree(ctx, opts.Directory, opts.Input.Commit)
	if err != nil {
		return Revision{}, err
	}
	staged, err := runGitContext(ctx, opts.Directory, "write-tree")
	if err != nil {
		return Revision{}, fmt.Errorf("inspect staged revision: %w", err)
	}
	if strings.TrimSpace(staged) == tree {
		return retainRevision(ctx, opts, opts.Input.Commit)
	}
	output, commitErr := runCommand(ctx, loggerFromContext(ctx), "capture_revision", Command{
		Directory: opts.Directory, Args: []string{"commit", "--file=-"}, Input: opts.Message,
		Environment: map[string]string{"GIT_REFLOG_ACTION": revisionReflogAction(opts)},
	})
	// Even a canceled command can have committed. Reconcile through an independent
	// bounded context, but always report the original command failure to its caller.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	head, inspectErr := (CandidateOperations{}).CheckoutIdentity(cleanup, opts.Directory)
	if inspectErr != nil {
		return Revision{}, errors.Join(commitErr, inspectErr)
	}
	if head.Ref != opts.Input.Ref || head.Commit == opts.Input.Commit {
		return Revision{}, errors.Join(commitErr, fmt.Errorf("revision commit did not establish the expected branch output: %s", strings.TrimSpace(output)))
	}
	result, reconcileErr := reconcileRevisionCommit(cleanup, opts, head.Commit)
	if commitErr != nil {
		return Revision{}, errors.Join(fmt.Errorf("revision commit failed; inspect retained Git output before retry: %w: %s", commitErr, strings.TrimSpace(output)), reconcileErr)
	}
	return result, reconcileErr
}

func revisionReflogAction(opts RevisionCapture) string { return "orpheus revision " + opts.Ref }

func reconcileRevisionCommit(ctx context.Context, opts RevisionCapture, head string) (Revision, error) {
	parents, err := runGitContext(ctx, opts.Directory, "rev-list", "--parents", "-n", "1", head, "--")
	if err != nil {
		return Revision{}, err
	}
	if strings.TrimSpace(parents) != head+" "+opts.Input.Commit {
		return Revision{}, fmt.Errorf("unresolved revision capture: HEAD is not one ordinary commit on its recorded input")
	}
	entry, err := runGitContext(ctx, opts.Directory, "reflog", "show", "-1", "--format=%H%n%gs", opts.Input.Ref)
	if err != nil {
		return Revision{}, fmt.Errorf("reconcile revision reflog: %w", err)
	}
	// Git appends the subject to GIT_REFLOG_ACTION. Checking the full prefix
	// distinguishes this stable effort from an unrelated commit or branch move.
	if !strings.HasPrefix(entry, head+"\n"+revisionReflogAction(opts)+": ") {
		return Revision{}, fmt.Errorf("unresolved revision capture: branch reflog does not identify this effort; inspect Git history before retry")
	}
	return retainRevision(ctx, opts, head)
}

func retainRevision(ctx context.Context, opts RevisionCapture, head string) (Revision, error) {
	// Retain even hook output that is not yet safe to review. Verification below
	// must succeed before the caller can attach an output to workflow history.
	if err := createRevisionRef(ctx, opts.Directory, opts.Ref+"/output", head); err != nil {
		return Revision{}, err
	}
	return verifyCapturedRevision(ctx, opts, head)
}

func verifyCapturedRevision(ctx context.Context, opts RevisionCapture, head string) (Revision, error) {
	tree, err := revisionTree(ctx, opts.Directory, head)
	if err != nil {
		return Revision{}, err
	}
	result := Revision{Commit: head, Tree: tree, RetainedRef: opts.Ref + "/output", Reused: head == opts.Input.Commit}
	if err := VerifyRevision(ctx, opts.Directory, CheckoutIdentity{Commit: head, Ref: opts.Input.Ref}, result); err != nil {
		return Revision{}, fmt.Errorf("captured output is retained but not safe for review; checkout changes were preserved: %w", err)
	}
	return result, nil
}

func createRevisionRef(ctx context.Context, dir, ref, commit string) error {
	existing, err := optionalRevisionRef(ctx, dir, ref)
	if err != nil {
		return err
	}
	if existing == commit {
		return nil
	}
	if existing != "" {
		return fmt.Errorf("owned revision ref %q already retains different output", ref)
	}
	if output, err := runGitContext(ctx, dir, "update-ref", "--create-reflog", ref, commit, ""); err != nil {
		return fmt.Errorf("retain revision %q: %w: %s", ref, err, strings.TrimSpace(output))
	}
	return nil
}

func optionalRevisionRef(ctx context.Context, dir, ref string) (string, error) {
	output, err := runGitContext(ctx, dir, "for-each-ref", "--format=%(refname)%00%(objectname)%00%(symref)", "--", ref)
	if err != nil {
		return "", fmt.Errorf("inspect owned revision ref %q: %w: %s", ref, err, strings.TrimSpace(output))
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x00")
		if len(parts) != 3 {
			return "", fmt.Errorf("malformed owned revision ref %q", ref)
		}
		// for-each-ref also matches descendants. Only this exact owned ref
		// establishes retention; a symbolic link to a moving branch does not.
		if parts[0] != ref {
			continue
		}
		if parts[2] != "" {
			return "", fmt.Errorf("owned revision ref %q must directly retain its commit, not follow a symbolic ref", ref)
		}
		return parts[1], nil
	}
	return "", nil
}

func revisionTree(ctx context.Context, dir, commit string) (string, error) {
	output, err := runGitContext(ctx, dir, "rev-parse", "--verify", commit+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("inspect revision tree: %w", err)
	}
	return strings.TrimSpace(output), nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}
