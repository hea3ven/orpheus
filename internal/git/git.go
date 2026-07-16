// Package git inspects local repositories and prepares deterministic task worktrees.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/logging"
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
	return InspectWithLogger(context.Background(), inputPath, nil)
}

// InspectWithLogger discovers local Git repository metadata and emits safe diagnostics.
func InspectWithLogger(ctx context.Context, inputPath string, logger *slog.Logger) (Inspection, error) {
	root, err := discoverRootWithLogger(ctx, inputPath, logger)
	if err != nil {
		return Inspection{}, err
	}

	inspection := Inspection{Root: root}

	remotes, err := listRemotesWithLogger(ctx, root, logger)
	if err != nil {
		inspection.RemoteErr = err
	} else {
		inspection.Remotes = remotes
		candidate := chooseRemoteCandidate(remotes)
		inspection.RemoteCandidate = candidate.URL
		inspection.RemoteCandidateName = candidate.Name
	}

	currentBranch, currentBranchErr := currentBranchWithLogger(ctx, root, logger)
	if currentBranchErr == nil {
		inspection.CurrentBranch = currentBranch
	}

	branch, source, err := defaultBranchWithLogger(ctx, root, currentBranch, currentBranchErr, logger)
	if err != nil {
		inspection.DefaultBranchErr = err
	} else {
		inspection.DefaultBranchCandidate = branch
		inspection.DefaultBranchSource = source
	}

	return inspection, nil
}

func discoverRootWithLogger(ctx context.Context, inputPath string, logger *slog.Logger) (string, error) {
	if strings.TrimSpace(inputPath) == "" {
		return "", fmt.Errorf("inspect git repository: %w: path is required", ErrNotRepository)
	}

	output, err := runGitContextLogger(ctx, logger, inputPath, "rev_parse_root", "rev-parse", "--show-toplevel")
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

func listRemotesWithLogger(ctx context.Context, root string, logger *slog.Logger) ([]Remote, error) {
	output, err := runGitContextLogger(ctx, logger, root, "list_remotes", "config", "--get-regexp", `^remote\..*\.url$`)
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

func defaultBranchWithLogger(
	ctx context.Context,
	root string,
	current string,
	currentErr error,
	logger *slog.Logger,
) (string, DefaultBranchSource, error) {
	originHEAD, err := originHEADBranchWithLogger(ctx, root, logger)
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

func originHEADBranchWithLogger(ctx context.Context, root string, logger *slog.Logger) (string, error) {
	output, err := runGitContextLogger(ctx, logger, root, "origin_head", "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
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

func currentBranchWithLogger(ctx context.Context, root string, logger *slog.Logger) (string, error) {
	output, err := runGitContextLogger(ctx, logger, root, "current_branch", "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read current branch: %w", err)
	}

	branch := strings.TrimSpace(output)
	if branch == "" {
		return "", errors.New("read current branch: git returned an empty branch")
	}
	return branch, nil
}

func runGitContext(ctx context.Context, dir string, args ...string) (string, error) {
	return runGitContextLogger(ctx, nil, dir, "git", args...)
}

func runGitContextLogger(ctx context.Context, logger *slog.Logger, dir string, operation string, args ...string) (string, error) {
	span := logging.Start(ctx, logger, "git command",
		slog.String("component", "git"),
		slog.String("operation", operation),
		slog.String("cwd", dir),
	)
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
	finishAttrs := gitExitAttrs(command, err)
	if err != nil && expectedGitAbsence(operation, output, err) {
		span.Finish(ctx, logging.StatusExpectedAbsence, finishAttrs...)
	} else {
		span.FinishError(ctx, err, finishAttrs...)
	}
	return output, err
}

func gitExitAttrs(command *exec.Cmd, err error) []slog.Attr {
	if command != nil && command.ProcessState != nil {
		return []slog.Attr{slog.Int("exit_code", command.ProcessState.ExitCode())}
	}
	if exitCode, ok := logging.ExitCode(err); ok {
		return []slog.Attr{slog.Int("exit_code", exitCode)}
	}
	return nil
}

func expectedGitAbsence(operation string, output string, err error) bool {
	exitCode, hasExitCode := logging.ExitCode(err)
	switch operation {
	case "list_remotes":
		return hasExitCode && exitCode == 1 && strings.TrimSpace(output) == ""
	case "origin_head":
		return hasExitCode && exitCode == 1
	case "current_branch":
		return hasExitCode && exitCode == 1 && strings.TrimSpace(output) == ""
	default:
		return false
	}
}
