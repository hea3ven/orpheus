package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	gitadapter "github.com/hea3ven/orpheus/internal/git"
)

// workingTreeSnapshot identifies raw on-disk bytes and executable/symlink modes.
// The parentless commit has fixed metadata, so identical trees have identical IDs.
type workingTreeSnapshot struct {
	Revision     string `json:"revision"`
	Tree         string `json:"tree"`
	HeadRevision string `json:"head_revision"`
	Bundle       string `json:"bundle"`
}

func sourceGit(ctx context.Context, root string, args ...string) (string, error) {
	env := gitRoutingEnvironment(os.Environ())
	env = append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_TERMINAL_PROMPT=0")
	command := gitadapter.Command{Directory: root, Args: append([]string{"-c", "core.fsmonitor=false"}, args...)}
	result, err := gitadapter.NewCommandRunner(env).Run(ctx, command)
	if err != nil {
		return result.Stdout, fmt.Errorf("read source git %s: %w: %s", args, err, result.Stderr)
	}
	return result.Stdout, nil
}

func gitRoutingEnvironment(environment []string) []string {
	var result []string
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR":
			continue
		}
		result = append(result, entry)
	}
	return result
}

func resolveSourceCommit(ctx context.Context, root, ref string) (string, error) {
	output, err := sourceGit(ctx, root, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	revision := strings.TrimSpace(output)
	if !commitPattern.MatchString(revision) {
		return "", errors.New("source revision did not resolve to an exact commit")
	}
	return revision, nil
}

func (runner *comparisonRunner) prepareWorkingTree(ctx context.Context, baseRef string) (workingTreeSnapshot, string, error) {
	var snapshot workingTreeSnapshot
	source, err := sourceGit(ctx, runner.root, "rev-parse", "--show-toplevel")
	if err != nil {
		return snapshot, "", err
	}
	source, err = filepath.EvalSymlinks(strings.TrimSpace(source))
	if err != nil {
		return snapshot, "", err
	}
	base, err := resolveSourceCommit(ctx, source, baseRef)
	if err != nil {
		return snapshot, "", fmt.Errorf("resolve explicit base %q: %w", baseRef, err)
	}
	snapshot.HeadRevision, err = resolveSourceCommit(ctx, source, "HEAD")
	if err != nil {
		return snapshot, base, err
	}
	paths, err := snapshotPaths(ctx, source, snapshot.HeadRevision)
	if err != nil {
		return snapshot, base, err
	}
	if err := runner.initSnapshotRepository(ctx, source, base); err != nil {
		return snapshot, base, err
	}
	snapshot.Tree, err = runner.writeSnapshotTree(ctx, source, paths)
	if err != nil {
		return snapshot, base, err
	}
	snapshot.Revision, err = runner.runGitInput(ctx, runner.root, "working-tree timing snapshot\n", "commit-tree", snapshot.Tree)
	snapshot.Revision = strings.TrimSpace(snapshot.Revision)
	if err != nil {
		return snapshot, base, err
	}
	snapshot.Bundle = filepath.Join(filepath.Dir(runner.output), "candidate.bundle")
	if err := runner.saveSnapshot(ctx, snapshot); err != nil {
		return snapshot, base, err
	}
	return snapshot, base, runner.prepareCheckouts(ctx, base, snapshot.Revision)
}

func snapshotPaths(ctx context.Context, source, head string) ([]string, error) {
	paths := make(map[string]bool)
	for _, args := range [][]string{{"ls-tree", "-r", "-z", "--full-tree", head}, {"ls-files", "--stage", "-z"}} {
		output, err := sourceGit(ctx, source, args...)
		if err != nil {
			return nil, err
		}
		if err := addSnapshotEntries(paths, output); err != nil {
			return nil, err
		}
	}
	output, err := sourceGit(ctx, source, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, path := range strings.Split(output, "\x00") {
		if path != "" {
			paths[path] = true
		}
	}
	return sortedKeys(paths), nil
}

func addSnapshotEntries(paths map[string]bool, output string) error {
	for _, entry := range strings.Split(output, "\x00") {
		if entry == "" {
			continue
		}
		header, path, ok := strings.Cut(entry, "\t")
		if !ok || path == "" {
			return errors.New("malformed source tree/index entry")
		}
		if strings.HasPrefix(header, "160000 ") {
			return fmt.Errorf("working-tree timing does not support submodules: %s", path)
		}
		paths[path] = true
	}
	return nil
}

func (runner *comparisonRunner) initSnapshotRepository(ctx context.Context, source, base string) error {
	format, err := sourceGit(ctx, source, "rev-parse", "--show-object-format")
	if err != nil {
		return err
	}
	runner.root = filepath.Join(runner.work, "repository.git")
	if err := os.MkdirAll(runner.work, 0o755); err != nil {
		return err
	}
	if err := runner.gitCommand(ctx, runner.work, "init", "--bare", "--template=", "--object-format="+strings.TrimSpace(format), runner.root); err != nil {
		return err
	}
	// Only the explicit base history is copied; no refs or objects are written
	// to the source repository, and the snapshot has no historical parents.
	if err := runner.gitCommand(ctx, runner.root, "-c", "protocol.file.allow=always", "fetch", "--no-tags", "--no-write-fetch-head", source, base); err != nil {
		return err
	}
	// Measurement checkouts must retain the raw bytes, not run repository clean
	// or smudge filters or perform line-ending/encoding transformations.
	if err := os.MkdirAll(filepath.Join(runner.root, "info"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runner.root, "info", "attributes"), []byte("* -filter -text -ident -working-tree-encoding\n"), 0o644)
}

func (runner comparisonRunner) writeSnapshotTree(ctx context.Context, source string, paths []string) (string, error) {
	var index strings.Builder
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		contents, mode, err := readSnapshotFile(source, path)
		if err != nil {
			return "", fmt.Errorf("snapshot %q: %w", path, err)
		}
		if mode == "" {
			continue
		}
		blob, err := runner.runGitInput(ctx, runner.root, string(contents), "hash-object", "-w", "--stdin", "--no-filters")
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(&index, "%s %s\t%s\x00", mode, strings.TrimSpace(blob), path)
	}
	if _, err := runner.runGitInput(ctx, runner.root, index.String(), "update-index", "-z", "--index-info"); err != nil {
		return "", err
	}
	tree, err := runner.runGit(ctx, runner.root, "write-tree")
	return strings.TrimSpace(tree), err
}

func readSnapshotFile(root, path string) ([]byte, string, error) {
	if !filepath.IsLocal(path) || strings.HasSuffix(path, "/") {
		return nil, "", errors.New("unsafe path or unsupported nested repository")
	}
	file := filepath.Join(root, path)
	parent, err := filepath.EvalSymlinks(filepath.Dir(file))
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if parent != filepath.Dir(file) {
		return nil, "", errors.New("tracked parent directory is a symlink")
	}
	info, err := os.Lstat(file)
	if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	return snapshotFileContents(file, info)
}

func snapshotFileContents(file string, info fs.FileInfo) ([]byte, string, error) {
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(file)
		return []byte(target), "120000", err
	}
	if info.IsDir() {
		return nil, "", nil // A tracked file replaced by a directory is deleted.
	}
	if !info.Mode().IsRegular() {
		return nil, "", errors.New("snapshot requires a regular file or symlink")
	}
	mode := "100644"
	if info.Mode()&0o111 != 0 {
		mode = "100755"
	}
	contents, err := os.ReadFile(file)
	return contents, mode, err
}

func (runner comparisonRunner) saveSnapshot(ctx context.Context, snapshot workingTreeSnapshot) error {
	if err := runner.gitCommand(ctx, runner.root, "update-ref", "refs/heads/candidate", snapshot.Revision); err != nil {
		return err
	}
	return runner.gitCommand(ctx, runner.root, "bundle", "create", snapshot.Bundle, "refs/heads/candidate")
}

func (runner *comparisonRunner) buildCollector(ctx context.Context) error {
	runner.collector = filepath.Join(runner.work, "collector")
	log, err := os.Create(filepath.Join(filepath.Dir(runner.output), "build.log"))
	if err != nil {
		return err
	}
	defer func() { _ = log.Close() }()
	cmd := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-o", runner.collector, "./cmd/testcoverage")
	cmd.Dir, cmd.Env = filepath.Join(runner.work, "merge"), comparisonEnvironment(runner.work, "collector")
	cmd.Stdout, cmd.Stderr = log, log
	configureProcessCancellation(cmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build snapshot collector (see %s): %w", log.Name(), err)
	}
	return nil
}
