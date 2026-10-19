package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DiscoverRepositoryWorktrees inventories the owned worktree directory and Git
// registrations, independently of task-source and task-state records. TaskID and
// Branch in opts are ignored. Each result can use the non-forced cleanup boundary.
func (LocalClosedTaskWorktreeGit) DiscoverRepositoryWorktrees(ctx context.Context, opts TaskWorktreeOptions) ([]TaskWorktreeOptions, error) {
	repoID, err := cleanPathComponent("repo id", opts.RepoID)
	if err != nil {
		return nil, err
	}
	rel := filepath.Join("repos", repoID)
	if err := opts.Paths.CheckDataTree(rel); err != nil {
		return nil, err
	}
	if err := opts.Paths.CheckDataTree(filepath.Join(rel, "worktrees")); err != nil {
		return nil, err
	}
	repoDir, err := opts.Paths.DataPath(rel)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(repoDir, "worktrees")
	repoRoot, err := worktreeRoot(ctx, opts.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("inspect registered checkout: %w", err)
	}
	if repoRoot != filepath.Clean(opts.RepoPath) {
		return nil, fmt.Errorf("registered checkout %q is not its Git root %q", opts.RepoPath, repoRoot)
	}
	output, err := runGitContext(ctx, repoRoot, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return nil, fmt.Errorf("discover repository worktrees: %w%s", err, gitOutputSuffix(output))
	}
	branches, err := repositoryWorktreeBranches(output, repoDir, root)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("list owned worktree directory %q: %w", root, err)
	}
	for _, entry := range entries {
		if _, ok := branches[entry.Name()]; !ok {
			branches[entry.Name()] = ""
		}
	}
	return repositoryWorktreeCleanupOptions(opts, root, branches)
}

func repositoryWorktreeCleanupOptions(opts TaskWorktreeOptions, root string, branches map[string]string) ([]TaskWorktreeOptions, error) {
	ids := make([]string, 0, len(branches))
	for id := range branches {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]TaskWorktreeOptions, 0, len(ids))
	for _, id := range ids {
		candidate := opts
		candidate.TaskID, candidate.Branch = id, branches[id]
		path := filepath.Join(root, id)
		plan, err := newTaskWorktreePlan(candidate)
		if err != nil {
			return nil, fmt.Errorf("worktree %q cannot be addressed safely; repair its name or repository configuration before retrying removal: %w", path, err)
		}
		// Cleanup normalizes task IDs; it must still address the exact discovered path.
		if plan.WorktreePath != path {
			return nil, fmt.Errorf("worktree %q resolves to a different cleanup path %q; repair its name before retrying removal (use `git worktree move` for registered worktrees)", path, plan.WorktreePath)
		}
		result = append(result, candidate)
	}
	return result, nil
}

func repositoryWorktreeBranches(output, repoDir, root string) (map[string]string, error) {
	branches := make(map[string]string)
	var failures []error
	for _, record := range strings.Split(output, "\x00\x00") {
		var path, branch string
		for _, field := range strings.Split(record, "\x00") {
			if value, ok := strings.CutPrefix(field, "worktree "); ok {
				path = filepath.Clean(value)
			}
			if value, ok := strings.CutPrefix(field, "branch refs/heads/"); ok {
				branch = value
			}
		}
		if path == "" || (path != repoDir && !strings.HasPrefix(path, repoDir+string(filepath.Separator))) {
			continue
		}
		if filepath.Dir(path) != root {
			failures = append(failures, fmt.Errorf("worktree %q is inside repository data but outside the owned worktree layout; move it with Git before retrying removal", path))
			continue
		}
		if branch == "" {
			failures = append(failures, fmt.Errorf("worktree %q has no branch identity; repair it with Git before retrying removal", path))
			continue
		}
		branches[filepath.Base(path)] = branch
	}
	return branches, errors.Join(failures...)
}
