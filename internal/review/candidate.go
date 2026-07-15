package review

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
)

// HasCandidateChanges reports whether the review worktree contains candidate changes.
func HasCandidateChanges(ctx context.Context, workdir string) (bool, error) {
	status, err := candidateStatus(ctx, workdir)
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

func captureCandidateSnapshot(ctx context.Context, workdir string) (candidateSnapshot, error) {
	status, err := candidateStatus(ctx, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	patch, err := gitmeta.BinaryDiff(ctx, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	untracked, err := captureUntrackedFiles(ctx, workdir)
	if err != nil {
		return candidateSnapshot{}, err
	}
	return candidateSnapshot{
		workdir:   workdir,
		status:    status,
		patch:     patch,
		untracked: untracked,
	}, nil
}

func candidateStatus(ctx context.Context, workdir string) ([]byte, error) {
	return gitmeta.CandidateStatus(ctx, workdir)
}

func captureUntrackedFiles(ctx context.Context, workdir string) ([]snapshotFile, error) {
	paths, err := gitmeta.UntrackedFiles(ctx, workdir)
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

func restoreCandidateIfMutated(ctx context.Context, snapshot candidateSnapshot) error {
	currentStatus, err := candidateStatus(ctx, snapshot.workdir)
	if err != nil {
		return err
	}
	if bytes.Equal(currentStatus, snapshot.status) {
		current, err := captureCandidateSnapshot(ctx, snapshot.workdir)
		if err != nil {
			return err
		}
		if candidateSnapshotsEqual(current, snapshot) {
			return nil
		}
	}
	if err := restoreCandidateSnapshot(ctx, snapshot); err != nil {
		return fmt.Errorf(
			"review step mutated candidate changes and automatic restore failed: %w; "+
				"manual repair required in %q: inspect `git status --short`, restore the intended "+
				"candidate changes, then rerun `orpheus task review`",
			err,
			snapshot.workdir,
		)
	}
	restored, err := captureCandidateSnapshot(ctx, snapshot.workdir)
	if err != nil {
		return err
	}
	if !candidateSnapshotsEqual(restored, snapshot) {
		return fmt.Errorf(
			"review step mutated candidate changes and automatic restore did not return the worktree to the pre-step snapshot; "+
				"manual repair required in %q: inspect `git status --short`, restore the intended "+
				"candidate changes, then rerun `orpheus task review`",
			snapshot.workdir,
		)
	}
	return errors.New("review step mutated candidate changes; restored the pre-step snapshot and marked review failed")
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

func restoreCandidateSnapshot(ctx context.Context, snapshot candidateSnapshot) error {
	if err := gitmeta.ResetIndexToHEAD(ctx, snapshot.workdir); err != nil {
		return err
	}
	if err := gitmeta.CleanUntrackedFiles(ctx, snapshot.workdir); err != nil {
		return err
	}
	if err := gitmeta.RestoreTrackedFilesFromHEAD(ctx, snapshot.workdir); err != nil {
		return err
	}
	if len(bytes.TrimSpace(snapshot.patch)) > 0 {
		err := gitmeta.ApplyBinaryPatch(
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
