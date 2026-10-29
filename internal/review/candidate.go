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
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
)

// CaptureCommittedCandidate admits only the identified retained revision, then
// protects its checkout and index through the existing read-only rollback boundary.
// It does not select an output, record review evidence, or authorize publication.
func CaptureCommittedCandidate(ctx context.Context, workdir string, expected gitmeta.CheckoutIdentity, revision gitmeta.Revision, logger *slog.Logger) (CandidateCheck, error) {
	if err := gitmeta.VerifyRevision(ctx, workdir, expected, revision); err != nil {
		return nil, err
	}
	snapshot, err := captureCandidateSnapshot(ctx, workdir, logger)
	if err != nil {
		return nil, err
	}
	return func() error {
		mutationErr := restoreCandidateIfMutated(ctx, snapshot, logger)
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := gitmeta.VerifyRevision(cleanup, workdir, expected, revision); err != nil {
			return errors.Join(mutationErr, fmt.Errorf("review revision boundary could not be restored; manual repair required in %q: %w", workdir, err))
		}
		return mutationErr
	}, nil
}

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
	untracked []snapshotFile
	tracked   []snapshotFile
	identity  gitmeta.CheckoutIdentity
	index     []byte
	indexPath string
	entries   []byte
}

type snapshotFile struct {
	path          string
	mode          fs.FileMode
	data          []byte
	symlinkTarget string
	isSymlink     bool
	absent        bool
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
				slog.Int("untracked_count", len(snapshot.untracked)),
			)
		}
		span.FinishError(ctx, err, finishAttrs...)
	}()

	gitOps := gitmeta.NewCandidateOperations(logger)
	snapshot, err = captureCheckoutSnapshot(ctx, gitOps, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	tracked, err := captureTrackedFiles(ctx, gitOps, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	status, err := candidateStatus(ctx, gitOps, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	untracked, err := captureUntrackedFiles(ctx, gitOps, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	snapshot.status = status
	snapshot.tracked, snapshot.untracked = tracked, untracked
	return snapshot, nil
}

func captureCheckoutSnapshot(ctx context.Context, gitOps gitmeta.CandidateOperations, workdir string) (candidateSnapshot, error) {
	identity, err := gitOps.CheckoutIdentity(ctx, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	indexPath, err := gitOps.IndexPath(ctx, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	index, err := os.ReadFile(indexPath)
	if err != nil {
		return candidateSnapshot{}, fmt.Errorf("capture review index: %w", err)
	}
	entries, err := gitOps.IndexEntries(ctx, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	return candidateSnapshot{workdir: workdir, identity: identity, index: index, indexPath: indexPath, entries: entries}, nil
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

func captureTrackedFiles(ctx context.Context, gitOps gitmeta.CandidateOperations, workdir string) ([]snapshotFile, error) {
	paths, err := gitOps.TrackedFiles(ctx, workdir)
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
	fullPath, err := snapshotPath(workdir, path)
	if err != nil {
		return snapshotFile{}, err
	}
	info, err := os.Lstat(fullPath)
	if errors.Is(err, os.ErrNotExist) {
		return snapshotFile{path: path, absent: true}, nil
	}
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

	// Cancellation or an unreadable mutated index must not prevent rollback.
	// Inspect and restore through an independently bounded cleanup context.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	gitOps := gitmeta.NewCandidateOperations(logger)
	current, inspectionErr := captureCandidateSnapshot(cleanupCtx, snapshot.workdir, logger, attrs...)
	if inspectionErr == nil && candidateSnapshotsEqual(current, snapshot) {
		return nil
	}

	mutated = true
	restoreSpan := logging.Start(ctx, logger, "candidate snapshot restoration", candidateDiagnosticAttrs(snapshot.workdir, attrs...)...)
	restoreErr := restoreCandidateSnapshot(cleanupCtx, gitOps, snapshot)
	if restoreErr != nil {
		restoreSpan.FinishError(ctx, restoreErr)
		finalErr = errors.Join(inspectionErr, restoreErr)
		return fmt.Errorf(
			"review step mutated candidate changes and automatic restore failed: %w; "+
				"manual repair required in %q: inspect `git status --short`, restore the intended "+
				"candidate changes, then rerun `orpheus task run`",
			finalErr,
			snapshot.workdir,
		)
	}
	if err := verifyRestoredCandidateSnapshot(cleanupCtx, snapshot, logger, attrs...); err != nil {
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
			"candidate changes, then rerun `orpheus task run`",
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
		a.identity != b.identity ||
		!bytes.Equal(a.entries, b.entries) ||
		len(a.tracked) != len(b.tracked) ||
		len(a.untracked) != len(b.untracked) {
		return false
	}
	for i := range a.tracked {
		if !snapshotFilesEqual(a.tracked[i], b.tracked[i]) {
			return false
		}
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
		a.absent == b.absent &&
		bytes.Equal(a.data, b.data)
}

func restoreCandidateSnapshot(ctx context.Context, gitOps gitmeta.CandidateOperations, snapshot candidateSnapshot) error {
	currentIndex, err := gitOps.IndexPath(ctx, snapshot.workdir)
	if err != nil {
		return err
	}
	if currentIndex != snapshot.indexPath {
		return fmt.Errorf("review index location changed; refusing restoration into a different repository")
	}
	if err := restoreSnapshotIndex(snapshot); err != nil {
		return err
	}
	if err := gitOps.RestoreCheckoutIdentity(ctx, snapshot.workdir, snapshot.identity); err != nil {
		return err
	}
	if err := gitOps.CleanUntrackedFiles(ctx, snapshot.workdir); err != nil {
		return err
	}
	for _, file := range snapshot.tracked {
		if err := restoreSnapshotFile(snapshot.workdir, file); err != nil {
			return err
		}
	}
	for _, file := range snapshot.untracked {
		if err := restoreSnapshotFile(snapshot.workdir, file); err != nil {
			return err
		}
	}
	return restoreSnapshotIndex(snapshot)
}

func restoreSnapshotFile(workdir string, file snapshotFile) error {
	fullPath, err := snapshotPath(workdir, file.path)
	if err != nil {
		return err
	}
	if err := os.Remove(fullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove mutated review path %q: %w", file.path, err)
	}
	if file.absent {
		return nil
	}
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
	if err := os.Chmod(fullPath, file.mode.Perm()); err != nil {
		return fmt.Errorf("restore untracked file %q mode: %w", file.path, err)
	}
	return nil
}
