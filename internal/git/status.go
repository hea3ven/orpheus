package git

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// HasStagedChanges reports whether dir has changes staged in the Git index.
func HasStagedChanges(ctx context.Context, dir string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	output, err := runGitContext(ctx, dir, "diff", "--cached", "--quiet", "--")
	if err == nil {
		return false, nil
	}
	if gitExitCode(err) == 1 {
		return true, nil
	}
	return false, fmt.Errorf("inspect staged changes: git diff --cached --quiet: %w: %s", err, strings.TrimSpace(output))
}

// ShortStatus returns git status --short output for dir.
func ShortStatus(ctx context.Context, dir string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	output, err := runGitContext(ctx, dir, "status", "--short")
	if err != nil {
		return "", fmt.Errorf("git status --short: %w: %s", err, strings.TrimSpace(output))
	}
	return output, nil
}

// CandidateOperations runs Git commands used to capture and restore review candidates.
//
// The zero value is valid and emits no diagnostics unless the context carries a
// Git logger. Use NewCandidateOperations at diagnostic boundaries so callers
// pass logging intent explicitly instead of depending on context values.
type CandidateOperations struct {
	logger *slog.Logger
}

// NewCandidateOperations returns review-candidate Git operations with safe
// diagnostics enabled.
func NewCandidateOperations(logger *slog.Logger) CandidateOperations {
	return CandidateOperations{logger: logger}
}

func (ops CandidateOperations) loggerFor(ctx context.Context) *slog.Logger {
	if ops.logger != nil {
		return ops.logger
	}
	return loggerFromContext(ctx)
}

// CandidateStatus returns the porcelain status used to detect review candidate changes.
func CandidateStatus(ctx context.Context, dir string) ([]byte, error) {
	return CandidateOperations{}.Status(ctx, dir)
}

// Status returns the porcelain status used to detect review candidate changes.
func (ops CandidateOperations) Status(ctx context.Context, dir string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	output, err := runGitContextLogger(
		ctx,
		ops.loggerFor(ctx),
		dir,
		"candidate_status",
		"status",
		"--porcelain=v1",
		"-z",
		"--untracked-files=normal",
	)
	if err != nil {
		return nil, fmt.Errorf("read candidate status: %w: %s", err, strings.TrimSpace(output))
	}
	return []byte(output), nil
}

// BinaryDiff returns the tracked binary diff for dir without external diff drivers.
func BinaryDiff(ctx context.Context, dir string) ([]byte, error) {
	return CandidateOperations{}.BinaryDiff(ctx, dir)
}

// BinaryDiff returns the tracked binary diff for dir without external diff drivers.
func (ops CandidateOperations) BinaryDiff(ctx context.Context, dir string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	output, err := runGitContextLogger(
		ctx,
		ops.loggerFor(ctx),
		dir,
		"candidate_diff",
		"diff",
		"--binary",
		"--no-ext-diff",
	)
	if err != nil {
		return nil, fmt.Errorf("capture tracked diff: %w: %s", err, strings.TrimSpace(output))
	}
	return []byte(output), nil
}

// UntrackedFiles returns unignored untracked file paths using slash-separated Git paths.
func UntrackedFiles(ctx context.Context, dir string) ([]string, error) {
	return CandidateOperations{}.UntrackedFiles(ctx, dir)
}

// UntrackedFiles returns unignored untracked file paths using slash-separated Git paths.
func (ops CandidateOperations) UntrackedFiles(ctx context.Context, dir string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	output, err := runGitContextLogger(
		ctx,
		ops.loggerFor(ctx),
		dir,
		"candidate_untracked",
		"ls-files",
		"--others",
		"--exclude-standard",
		"-z",
	)
	if err != nil {
		return nil, fmt.Errorf("list untracked candidate files: %w: %s", err, strings.TrimSpace(output))
	}
	return splitNUL([]byte(output)), nil
}

// ResetIndexToHEAD resets the Git index to HEAD without changing tracked files.
func ResetIndexToHEAD(ctx context.Context, dir string) error {
	return CandidateOperations{}.ResetIndexToHEAD(ctx, dir)
}

// ResetIndexToHEAD resets the Git index to HEAD without changing tracked files.
func (ops CandidateOperations) ResetIndexToHEAD(ctx context.Context, dir string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	output, err := runGitContextLogger(
		ctx,
		ops.loggerFor(ctx),
		dir,
		"candidate_reset_index",
		"reset",
		"--mixed",
		"HEAD",
		"--",
	)
	if err != nil {
		return fmt.Errorf("reset Git index: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

// CleanUntrackedFiles removes untracked files and directories from dir.
func CleanUntrackedFiles(ctx context.Context, dir string) error {
	return CandidateOperations{}.CleanUntrackedFiles(ctx, dir)
}

// CleanUntrackedFiles removes untracked files and directories from dir.
func (ops CandidateOperations) CleanUntrackedFiles(ctx context.Context, dir string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	output, err := runGitContextLogger(
		ctx,
		ops.loggerFor(ctx),
		dir,
		"candidate_clean_untracked",
		"clean",
		"-fd",
		"--",
	)
	if err != nil {
		return fmt.Errorf("remove new untracked files: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

// RestoreTrackedFilesFromHEAD restores tracked worktree files from HEAD.
func RestoreTrackedFilesFromHEAD(ctx context.Context, dir string) error {
	return CandidateOperations{}.RestoreTrackedFilesFromHEAD(ctx, dir)
}

// RestoreTrackedFilesFromHEAD restores tracked worktree files from HEAD.
func (ops CandidateOperations) RestoreTrackedFilesFromHEAD(ctx context.Context, dir string) error {
	if ctx == nil {
		ctx = context.Background()
	}

	output, err := runGitContextLogger(
		ctx,
		ops.loggerFor(ctx),
		dir,
		"candidate_restore_tracked",
		"restore",
		"--worktree",
		"--source=HEAD",
		"--",
		".",
	)
	if err != nil {
		return fmt.Errorf("restore tracked files from HEAD: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

// ApplyBinaryPatch applies a binary patch to dir with whitespace warnings disabled.
func ApplyBinaryPatch(ctx context.Context, dir string, patch []byte) error {
	return CandidateOperations{}.ApplyBinaryPatch(ctx, dir, patch)
}

// ApplyBinaryPatch applies a binary patch to dir with whitespace warnings disabled.
func (ops CandidateOperations) ApplyBinaryPatch(ctx context.Context, dir string, patch []byte) error {
	if ctx == nil {
		ctx = context.Background()
	}

	output, err := runGitContextWithInputLogger(
		ctx,
		ops.loggerFor(ctx),
		dir,
		"candidate_apply_patch",
		string(patch),
		"apply",
		"--binary",
		"--whitespace=nowarn",
	)
	if err != nil {
		return fmt.Errorf("reapply tracked candidate patch: %w: %s", err, strings.TrimSpace(output))
	}
	return nil
}

func splitNUL(output []byte) []string {
	if len(output) == 0 {
		return nil
	}
	parts := bytes.Split(output, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		paths = append(paths, string(part))
	}
	return paths
}
