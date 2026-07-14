// Package git inspects local repositories and prepares deterministic task worktrees.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

var (
	// ErrNotRepository indicates the supplied path is not inside a Git worktree.
	ErrNotRepository = errors.New("not a git worktree")

	// ErrNoRemote indicates the repository has no configured Git remotes.
	ErrNoRemote = errors.New("no git remotes configured")

	// ErrNoDefaultBranch indicates no local default-branch candidate was found.
	ErrNoDefaultBranch = errors.New("no git default branch candidate found")

	// ErrMergeConflict indicates a default-branch merge into a task branch conflicts.
	ErrMergeConflict = errors.New("task branch merge conflict")
)

// DefaultBranchSource describes the local metadata used to select a default branch candidate.
type DefaultBranchSource string

const (
	// DefaultBranchSourceOriginHEAD means refs/remotes/origin/HEAD selected the branch.
	DefaultBranchSourceOriginHEAD DefaultBranchSource = "origin/HEAD"

	// DefaultBranchSourceCurrentBranch means the current local branch was used as a fallback.
	DefaultBranchSourceCurrentBranch DefaultBranchSource = "current branch"
)

// Remote describes one configured Git remote URL.
type Remote struct {
	Name string
	URL  string
}

// Inspection is the result of local-only Git repository inspection.
type Inspection struct {
	Root string

	Remotes             []Remote
	RemoteCandidate     string
	RemoteCandidateName string
	RemoteErr           error

	DefaultBranchCandidate string
	DefaultBranchSource    DefaultBranchSource
	DefaultBranchErr       error
	CurrentBranch          string
}

// Inspect discovers local Git repository metadata for a path inside a worktree.
//
// Inspect only reads local Git metadata. It does not fetch, contact remotes,
// create branches, create worktrees, or mutate the repository.
func Inspect(inputPath string) (Inspection, error) {
	root, err := discoverRoot(inputPath)
	if err != nil {
		return Inspection{}, err
	}

	inspection := Inspection{Root: root}

	remotes, err := listRemotes(root)
	if err != nil {
		inspection.RemoteErr = err
	} else {
		inspection.Remotes = remotes
		candidate := chooseRemoteCandidate(remotes)
		inspection.RemoteCandidate = candidate.URL
		inspection.RemoteCandidateName = candidate.Name
	}

	currentBranch, currentBranchErr := currentBranch(root)
	if currentBranchErr == nil {
		inspection.CurrentBranch = currentBranch
	}

	branch, source, err := defaultBranch(root, currentBranch, currentBranchErr)
	if err != nil {
		inspection.DefaultBranchErr = err
	} else {
		inspection.DefaultBranchCandidate = branch
		inspection.DefaultBranchSource = source
	}

	return inspection, nil
}

func discoverRoot(inputPath string) (string, error) {
	if strings.TrimSpace(inputPath) == "" {
		return "", fmt.Errorf("inspect git repository: %w: path is required", ErrNotRepository)
	}

	output, err := runGit(inputPath, "rev-parse", "--show-toplevel")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("inspect git repository at %q: run git rev-parse: %w", inputPath, err)
		}

		message := strings.TrimSpace(output)
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("inspect git repository at %q: %w: %s", inputPath, ErrNotRepository, message)
	}

	root := strings.TrimSpace(output)
	if root == "" {
		return "", fmt.Errorf("inspect git repository at %q: %w: git returned an empty repository root", inputPath, ErrNotRepository)
	}

	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("normalize git repository root %q: %w", root, err)
	}
	return filepath.Clean(absoluteRoot), nil
}

func listRemotes(root string) ([]Remote, error) {
	output, err := runGit(root, "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		if strings.TrimSpace(output) == "" {
			return nil, ErrNoRemote
		}
		return nil, fmt.Errorf("read git remotes: %w: %s", err, strings.TrimSpace(output))
	}

	var remotes []Remote
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		key, value, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("read git remotes: parse git config line %q", line)
		}

		name, ok := remoteNameFromConfigKey(key)
		if !ok {
			continue
		}

		url := strings.TrimSpace(value)
		if url == "" {
			continue
		}
		remotes = append(remotes, Remote{Name: name, URL: url})
	}

	if len(remotes) == 0 {
		return nil, ErrNoRemote
	}
	return remotes, nil
}

func remoteNameFromConfigKey(key string) (string, bool) {
	const prefix = "remote."
	const suffix = ".url"
	if !strings.HasPrefix(key, prefix) || !strings.HasSuffix(key, suffix) {
		return "", false
	}

	name := strings.TrimSuffix(strings.TrimPrefix(key, prefix), suffix)
	if name == "" {
		return "", false
	}
	return name, true
}

func chooseRemoteCandidate(remotes []Remote) Remote {
	for _, remote := range remotes {
		if remote.Name == "origin" {
			return remote
		}
	}
	return remotes[0]
}

func defaultBranch(root string, current string, currentErr error) (string, DefaultBranchSource, error) {
	originHEAD, err := originHEADBranch(root)
	if err == nil {
		return originHEAD, DefaultBranchSourceOriginHEAD, nil
	}

	if currentErr == nil && current != "" {
		return current, DefaultBranchSourceCurrentBranch, nil
	}

	if currentErr != nil {
		return "", "", fmt.Errorf("%w: origin/HEAD is missing and current branch could not be read: %w", ErrNoDefaultBranch, currentErr)
	}
	return "", "", fmt.Errorf("%w: origin/HEAD is missing and current branch is empty", ErrNoDefaultBranch)
}

func originHEADBranch(root string) (string, error) {
	output, err := runGit(root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", fmt.Errorf("read origin/HEAD: %w", err)
	}

	branch := strings.TrimSpace(output)
	if branch == "" {
		return "", errors.New("read origin/HEAD: git returned an empty symbolic ref")
	}

	const prefix = "origin/"
	branch = strings.TrimPrefix(branch, prefix)
	if branch == "" {
		return "", errors.New("read origin/HEAD: symbolic ref does not name a branch")
	}
	return branch, nil
}

func currentBranch(root string) (string, error) {
	output, err := runGit(root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read current branch: %w", err)
	}

	branch := strings.TrimSpace(output)
	if branch == "" {
		return "", errors.New("read current branch: git returned an empty branch")
	}
	return branch, nil
}

func runGit(dir string, args ...string) (string, error) {
	return runGitContext(context.Background(), dir, args...)
}

func runGitContext(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" && !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		output += stderr.String()
	}
	return output, err
}
