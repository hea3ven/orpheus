package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// VerifyRevision refuses a clean but wrong HEAD/ref and a wrong retention ref.
// It also verifies index, actual tracked content and executable/symlink modes,
// including changes Git status hides through flags or core.filemode=false.
// Ignored untracked files are deliberately outside task output.
func VerifyRevision(ctx context.Context, dir string, expected CheckoutIdentity, revision Revision) error {
	if !validObjectID(expected.Commit) || revision.Commit != expected.Commit || !validObjectID(revision.Tree) || !strings.HasPrefix(revision.RetainedRef, "refs/orpheus/") {
		return fmt.Errorf("review requires an identified, retained revision")
	}
	if err := verifyRevisionIdentity(ctx, dir, expected, revision); err != nil {
		return err
	}
	if err := requireNoGitOperation(ctx, dir); err != nil {
		return err
	}
	if err := checkRevisionIndexFlags(ctx, dir); err != nil {
		return err
	}
	tree, err := runGitContext(ctx, dir, "write-tree")
	if err != nil {
		return fmt.Errorf("inspect revision index: %w", err)
	}
	if strings.TrimSpace(tree) != revision.Tree {
		return fmt.Errorf("review index does not match the recorded revision tree")
	}
	untracked, err := UntrackedFiles(ctx, dir)
	if err != nil {
		return err
	}
	if len(untracked) != 0 {
		return fmt.Errorf("review checkout contains untracked output: %q", untracked[0])
	}
	if err := verifyRevisionFiles(ctx, dir, expected.Commit); err != nil {
		return err
	}
	return verifyRevisionIdentity(ctx, dir, expected, revision)
}

func verifyRevisionIdentity(ctx context.Context, dir string, expected CheckoutIdentity, revision Revision) error {
	root, err := worktreeRoot(ctx, dir)
	if err != nil {
		return err
	}
	if root != dir {
		return fmt.Errorf("review requires the recorded worktree root, got %q", root)
	}
	head, err := (CandidateOperations{}).CheckoutIdentity(ctx, dir)
	if err != nil {
		return err
	}
	if head != expected {
		return fmt.Errorf("review requires HEAD %s at %q, got %s at %q", expected.Commit, expected.Ref, head.Commit, head.Ref)
	}
	return VerifyRetainedRevision(ctx, dir, revision)
}

// VerifyRetainedRevision verifies native commit/tree identity and its owned ref
// without requiring HEAD or the checkout to represent that historical revision.
// It does not establish a safe review checkout or authorize any external effect.
func VerifyRetainedRevision(ctx context.Context, dir string, revision Revision) error {
	if !validObjectID(revision.Commit) || !validObjectID(revision.Tree) || !strings.HasPrefix(revision.RetainedRef, "refs/orpheus/") {
		return fmt.Errorf("revision requires native commit/tree identity and an owned retention ref")
	}
	retained, err := optionalRevisionRef(ctx, dir, revision.RetainedRef)
	if err != nil {
		return err
	}
	if retained != revision.Commit {
		return fmt.Errorf("revision retention ref no longer identifies the recorded commit")
	}
	tree, err := revisionTree(ctx, dir, revision.Commit)
	if err != nil {
		return err
	}
	if tree != revision.Tree {
		return fmt.Errorf("revision tree differs from the recorded commit tree")
	}
	return nil
}

func checkRevisionIndexFlags(ctx context.Context, dir string) error {
	entries, err := (CandidateOperations{}).IndexEntries(ctx, dir)
	if err != nil {
		return err
	}
	for _, entry := range splitNUL(entries) {
		// H is an ordinary cached entry. S and lowercase tags hide checkout
		// changes; unmerged entries cannot describe ordinary committed output.
		if !strings.HasPrefix(entry, "H ") {
			return fmt.Errorf("revision requires an ordinary index without skip-worktree, assume-unchanged or unresolved entries")
		}
	}
	return nil
}

func requireNoGitOperation(ctx context.Context, dir string) error {
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "sequencer", "index.lock"} {
		output, err := runGitContext(ctx, dir, "rev-parse", "--path-format=absolute", "--git-path", name)
		if err != nil {
			return fmt.Errorf("inspect unfinished Git operation: %w", err)
		}
		_, err = os.Lstat(strings.TrimSpace(output))
		if err == nil {
			return fmt.Errorf("revision requires settled Git state; %s exists", name)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect unfinished Git operation %s: %w", name, err)
		}
	}
	return nil
}

func verifyRevisionFiles(ctx context.Context, dir, commit string) error {
	output, err := runGitContext(ctx, dir, "ls-tree", "-rz", "--full-tree", commit)
	if err != nil {
		return fmt.Errorf("inspect revision files: %w", err)
	}
	for _, entry := range splitNUL([]byte(output)) {
		metadata, path, ok := strings.Cut(entry, "\t")
		parts := strings.Fields(metadata)
		if !ok || len(parts) != 3 || parts[1] != "blob" {
			return fmt.Errorf("revision has unsupported tree entry %q; submodules require operator handling", entry)
		}
		data, err := revisionFileData(dir, path, parts[0])
		if err != nil {
			return err
		}
		args := []string{"hash-object", "--stdin"}
		if parts[0] != "120000" {
			args = append(args, "--path="+path)
		}
		hash, err := runGitCommand(ctx, loggerFromContext(ctx), dir, "verify_revision_content", string(data), args...)
		if err != nil {
			return fmt.Errorf("verify revision content %q: %w", path, err)
		}
		if strings.TrimSpace(hash) != parts[2] {
			return fmt.Errorf("review tracked content %q differs from its committed revision", path)
		}
	}
	return nil
}

func revisionFileData(dir, path, mode string) ([]byte, error) {
	full, err := revisionFilePath(dir, path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(full)
	if err != nil {
		return nil, fmt.Errorf("inspect revision path %q: %w", path, err)
	}
	if mode == "120000" {
		if info.Mode()&os.ModeSymlink == 0 {
			return nil, fmt.Errorf("review path %q is no longer a symlink", path)
		}
		target, err := os.Readlink(full)
		return []byte(target), err
	}
	if (mode != "100644" && mode != "100755") || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("review path %q has unsupported mode %s", path, info.Mode())
	}
	if (info.Mode().Perm()&0o111 != 0) != (mode == "100755") {
		return nil, fmt.Errorf("review executable mode %q differs from its committed revision", path)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("read revision path %q: %w", path, err)
	}
	return data, nil
}

func revisionFilePath(dir, path string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(path))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe revision path %q", path)
	}
	full := filepath.Join(dir, clean)
	for parent := filepath.Dir(full); parent != dir; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil {
			return "", fmt.Errorf("inspect revision path %q parent: %w", path, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("unsafe revision path %q parent", path)
		}
	}
	return full, nil
}
