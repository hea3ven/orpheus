package review

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
)

// HasCandidateChanges reports whether the review worktree contains candidate changes.
func HasCandidateChanges(ctx context.Context, workdir string) (bool, error) {
	status, err := gitmeta.CandidateStatus(ctx, workdir)
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(status)) > 0, nil
}

type candidateSnapshot struct {
	workdir   string
	status    []byte
	patch     []byte
	untracked []snapshotFile
}

type snapshotFile struct {
	path          string
	mode          fs.FileMode
	data          []byte
	symlinkTarget string
	isSymlink     bool
}

func captureCandidateSnapshot(
	ctx context.Context,
	workdir string,
	logger *slog.Logger,
	attrs ...slog.Attr,
) (candidateSnapshot, error) {
	spanAttrs := candidateDiagnosticAttrs(workdir, attrs...)
	span := logging.Start(ctx, logger, "candidate snapshot capture", spanAttrs...)
	var snapshot candidateSnapshot
	var err error
	defer func() {
		finishAttrs := []slog.Attr{}
		if err == nil {
			finishAttrs = append(finishAttrs,
				slog.Bool("has_status", len(bytes.TrimSpace(snapshot.status)) > 0),
				slog.Bool("has_patch", len(bytes.TrimSpace(snapshot.patch)) > 0),
				slog.Int("untracked_count", len(snapshot.untracked)),
			)
		}
		span.FinishError(ctx, err, finishAttrs...)
	}()

	gitOps := gitmeta.NewCandidateOperations(logger)
	status, err := candidateStatus(ctx, gitOps, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	patch, err := gitOps.BinaryDiff(ctx, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	untracked, err := captureUntrackedFiles(ctx, gitOps, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	snapshot = candidateSnapshot{
		workdir:   workdir,
		status:    status,
		patch:     patch,
		untracked: untracked,
	}
	return snapshot, nil
}

func candidateStatus(ctx context.Context, gitOps gitmeta.CandidateOperations, workdir string) ([]byte, error) {
	return gitOps.Status(ctx, workdir)
}

func captureUntrackedFiles(ctx context.Context, gitOps gitmeta.CandidateOperations, workdir string) ([]snapshotFile, error) {
	paths, err := gitOps.UntrackedFiles(ctx, workdir)
	if err != nil {
		return nil, err
	}
	files := make([]snapshotFile, 0, len(paths))
	for _, path := range paths {
		file, err := captureSnapshotFile(workdir, path)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	return files, nil
}

func captureSnapshotFile(workdir string, path string) (snapshotFile, error) {
	fullPath := filepath.Join(workdir, filepath.FromSlash(path))
	info, err := os.Lstat(fullPath)
	if err != nil {
		return snapshotFile{}, fmt.Errorf("snapshot untracked file %q: %w", path, err)
	}
	file := snapshotFile{
		path: path,
		mode: info.Mode(),
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(fullPath)
		if err != nil {
			return snapshotFile{}, fmt.Errorf("snapshot untracked symlink %q: %w", path, err)
		}
		file.symlinkTarget = target
		file.isSymlink = true
		return file, nil
	}
	if !info.Mode().IsRegular() {
		return snapshotFile{}, fmt.Errorf("snapshot untracked file %q: unsupported mode %s", path, info.Mode())
	}
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return snapshotFile{}, fmt.Errorf("snapshot untracked file %q: %w", path, err)
	}
	file.data = data
	return file, nil
}

func restoreCandidateIfMutated(
	ctx context.Context,
	snapshot candidateSnapshot,
	logger *slog.Logger,
	attrs ...slog.Attr,
) error {
	span := logging.Start(ctx, logger, "candidate mutation check", candidateDiagnosticAttrs(snapshot.workdir, attrs...)...)
	var mutated bool
	var restored bool
	var finalErr error
	defer func() {
		finishAttrs := []slog.Attr{slog.Bool("mutated", mutated), slog.Bool("restored", restored)}
		span.Finish(ctx, candidateMutationStatus(mutated, restored, finalErr), finishAttrs...)
	}()

	gitOps := gitmeta.NewCandidateOperations(logger)
	currentStatus, err := candidateStatus(ctx, gitOps, snapshot.workdir)
	if err != nil {
		finalErr = err
		return err
	}
	if bytes.Equal(currentStatus, snapshot.status) {
		current, err := captureCandidateSnapshot(ctx, snapshot.workdir, logger, attrs...)
		if err != nil {
			finalErr = err
			return err
		}
		if candidateSnapshotsEqual(current, snapshot) {
			return nil
		}
	}

	mutated = true
	restoreSpan := logging.Start(ctx, logger, "candidate snapshot restoration", candidateDiagnosticAttrs(snapshot.workdir, attrs...)...)
	restoreErr := restoreCandidateSnapshot(ctx, gitOps, snapshot)
	if restoreErr != nil {
		restoreSpan.FinishError(ctx, restoreErr)
		finalErr = restoreErr
		return fmt.Errorf(
			"review step mutated candidate changes and automatic restore failed: %w; "+
				"manual repair required in %q: inspect `git status --short`, restore the intended "+
				"candidate changes, then rerun `orpheus task review`",
			restoreErr,
			snapshot.workdir,
		)
	}
	if err := verifyRestoredCandidateSnapshot(ctx, snapshot, logger, attrs...); err != nil {
		restoreSpan.FinishError(ctx, err)
		finalErr = err
		return err
	}

	restored = true
	restoreSpan.Finish(ctx, logging.StatusSuccess)
	finalErr = errors.New("review step mutated candidate changes; restored the pre-step snapshot and marked review failed")
	return finalErr
}

func verifyRestoredCandidateSnapshot(
	ctx context.Context,
	snapshot candidateSnapshot,
	logger *slog.Logger,
	attrs ...slog.Attr,
) error {
	restoredSnapshot, err := captureCandidateSnapshot(ctx, snapshot.workdir, logger, attrs...)
	if err != nil {
		return err
	}
	if candidateSnapshotsEqual(restoredSnapshot, snapshot) {
		return nil
	}
	return fmt.Errorf(
		"review step mutated candidate changes and automatic restore did not return the worktree to the pre-step snapshot; "+
			"manual repair required in %q: inspect `git status --short`, restore the intended "+
			"candidate changes, then rerun `orpheus task review`",
		snapshot.workdir,
	)
}

func candidateDiagnosticAttrs(workdir string, attrs ...slog.Attr) []slog.Attr {
	out := []slog.Attr{
		slog.String("component", "review"),
		slog.String("operation", "candidate_snapshot"),
		slog.String("cwd", workdir),
	}
	return append(out, attrs...)
}

func candidateMutationStatus(mutated bool, restored bool, err error) string {
	if err == nil {
		return logging.StatusSuccess
	}
	if mutated && restored {
		return "restored_mutation"
	}
	return logging.StatusFailure
}

func candidateSnapshotsEqual(a, b candidateSnapshot) bool {
	if a.workdir != b.workdir ||
		!bytes.Equal(a.status, b.status) ||
		!bytes.Equal(a.patch, b.patch) ||
		len(a.untracked) != len(b.untracked) {
		return false
	}
	for i := range a.untracked {
		if !snapshotFilesEqual(a.untracked[i], b.untracked[i]) {
			return false
		}
	}
	return true
}

func snapshotFilesEqual(a, b snapshotFile) bool {
	return a.path == b.path &&
		a.mode == b.mode &&
		a.symlinkTarget == b.symlinkTarget &&
		a.isSymlink == b.isSymlink &&
		bytes.Equal(a.data, b.data)
}

func restoreCandidateSnapshot(ctx context.Context, gitOps gitmeta.CandidateOperations, snapshot candidateSnapshot) error {
	if err := gitOps.ResetIndexToHEAD(ctx, snapshot.workdir); err != nil {
		return err
	}
	if err := gitOps.CleanUntrackedFiles(ctx, snapshot.workdir); err != nil {
		return err
	}
	if err := gitOps.RestoreTrackedFilesFromHEAD(ctx, snapshot.workdir); err != nil {
		return err
	}
	if len(bytes.TrimSpace(snapshot.patch)) > 0 {
		err := gitOps.ApplyBinaryPatch(
			ctx,
			snapshot.workdir,
			snapshot.patch,
		)
		if err != nil {
			return err
		}
	}
	for _, file := range snapshot.untracked {
		if err := restoreSnapshotFile(snapshot.workdir, file); err != nil {
			return err
		}
	}
	return nil
}

func restoreSnapshotFile(workdir string, file snapshotFile) error {
	fullPath := filepath.Join(workdir, filepath.FromSlash(file.path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("restore untracked file %q: %w", file.path, err)
	}
	if file.isSymlink {
		if err := os.Symlink(file.symlinkTarget, fullPath); err != nil {
			return fmt.Errorf("restore untracked symlink %q: %w", file.path, err)
		}
		return nil
	}
	if err := os.WriteFile(fullPath, file.data, file.mode.Perm()); err != nil {
		return fmt.Errorf("restore untracked file %q: %w", file.path, err)
	}
	return nil
}
