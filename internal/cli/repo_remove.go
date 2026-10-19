package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pathutil"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

// RepositoryRemovalGit supplies discovery and non-forced removal of owned
// worktrees. Discovery must include paths without task or task-state records.
type RepositoryRemovalGit interface {
	workflow.ClosedTaskWorktreeGit
	DiscoverRepositoryWorktrees(context.Context, gitmeta.TaskWorktreeOptions) ([]gitmeta.TaskWorktreeOptions, error)
}

func newRepoRemoveCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "remove <repo-id-name-or-prefix>",
		Short: "Delete a repository registration and its Orpheus-owned local data",
		Long: "Permanently delete repository-specific task state, execution/review history, usage, managed Gig or Beads storage, and owned worktrees, then unregister the repository.\n\n" +
			"Ensure no task is running in this repository and no other command is mutating it. Removal holds only the global mutation lock; it does not check running executions. " +
			"Cross-repository dependencies and epic membership must be removed first. " +
			"Worktrees are removed through Git without force, including unfinished tasks. Dirty, locked, missing-but-registered, or unsafe worktrees require manual repair and retry; successfully removed worktrees stay removed. " +
			"History, managed tasks, and registration remain until all worktrees are removed. Later data or registry failures report recovery instructions; deleted data cannot be restored by retrying. " +
			"The original checkout, branches, remotes, unrelated worktrees, and repository-local Beads are preserved. Register again to start fresh, optionally with a different managed task prefix.",
		Args: cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			deps, err := opts.invocation(command)
			if err != nil {
				return err
			}
			err = state.WithGlobalMutationLockLogger(command.Context(), deps.paths, "repo remove", deps.logger, func() error {
				return removeRepository(command.Context(), deps, args[0])
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "Removed repository %s and its Orpheus-owned local data.\n", args[0])
			return err
		},
	}
}

func removeRepository(ctx context.Context, deps *invocationDependencies, token string) error {
	reg, err := deps.registryStore.Load()
	if err != nil {
		return err
	}
	repo, err := reg.Resolve(token)
	if err != nil {
		return err
	}
	rel, err := repositoryRemovalDataPath(deps.paths, reg, repo)
	if err != nil {
		return err
	}
	sources, err := deps.registryStore.TaskRepositorySources(reg)
	if err != nil {
		return fmt.Errorf("cannot complete relationship check; repair registered task sources before retrying: %w", err)
	}
	if err := task.CheckRepositoryRemovalRelationships(ctx, sources, deps.taskBackendFactory, repo.ID); err != nil {
		return err
	}
	git := deps.repositoryRemovalGit
	if git == nil {
		git = gitmeta.LocalClosedTaskWorktreeGit{}
	}
	if err := removeRepositoryWorktrees(ctx, deps.paths, repo, git); err != nil {
		return fmt.Errorf("repository %s: task history, managed source data, and registration retained; some worktrees may already be removed; repair the listed paths and retry `orpheus repo remove %s`: %w", repo.ID, repo.ID, err)
	}
	if err := deps.paths.RemoveDataTree(rel); err != nil {
		return fmt.Errorf("repository %s: worktrees removed, but data cleanup at %s is incomplete; registration retained; inspect remaining data and repair deletion permissions before retrying; if managed task storage was deleted, the relationship check will refuse retry: recover from your own backup or manually remove the registry entry in registry.yaml after verifying cleanup: %w", repo.ID, rel, err)
	}
	remaining := registry.Registry{Repos: make([]registry.Repo, 0, len(reg.Repos)-1)}
	for _, candidate := range reg.Repos {
		if candidate.ID != repo.ID {
			remaining.Repos = append(remaining.Repos, candidate)
		}
	}
	if err := deps.registryStore.Save(remaining); err != nil {
		return fmt.Errorf("repository %s: worktrees and Orpheus-owned data deleted, but registry persistence failed; registration remains in registry.yaml; repair registry write access and retry (local Beads), or manually remove only this entry (deleted managed storage cannot pass the relationship check); deleted tasks and history are not restored: %w", repo.ID, err)
	}
	return nil
}

func repositoryRemovalDataPath(paths state.Paths, reg registry.Registry, repo registry.Repo) (string, error) {
	if repo.ID == "." || repo.ID == ".." || strings.ContainsAny(repo.ID, `/\`) || filepath.VolumeName(repo.ID) != "" {
		return "", fmt.Errorf("repository id %q is unsafe for data deletion; repair registry.yaml", repo.ID)
	}
	rel := filepath.Join("repos", repo.ID)
	if err := paths.CheckDataTree(rel); err != nil {
		return "", fmt.Errorf("cannot establish repository data ownership: %w", err)
	}
	dir, err := paths.DataPath(rel)
	if err != nil {
		return "", err
	}
	for _, registered := range reg.Repos {
		checkout, err := pathutil.CanonicalAbs(registered.Path)
		if err != nil {
			return "", err
		}
		if checkout == dir || strings.HasPrefix(checkout, dir+string(filepath.Separator)) {
			return "", fmt.Errorf("registered checkout %q for %s is inside removal directory %q; move and reconfigure the checkout before retrying", checkout, registered.ID, dir)
		}
	}
	return rel, nil
}

func removeRepositoryWorktrees(ctx context.Context, paths state.Paths, repo registry.Repo, git RepositoryRemovalGit) error {
	worktrees, err := git.DiscoverRepositoryWorktrees(ctx, gitmeta.TaskWorktreeOptions{
		RepoID: repo.ID, RepoName: repo.Name, RepoPath: repo.Path, DefaultBranch: repo.DefaultBranch, Paths: paths,
	})
	if err != nil {
		return fmt.Errorf("cannot establish safe worktree ownership; inspect and repair Git worktree registrations and owned paths: %w", err)
	}
	var failures []error
	for _, worktree := range worktrees {
		inspection := git.InspectClosedTaskWorktree(ctx, worktree)
		if inspection.Outcome != gitmeta.ClosedTaskWorktreeEligible && inspection.Outcome != gitmeta.ClosedTaskWorktreeAbsent {
			failures = append(failures, repositoryWorktreeRepair(inspection.Worktree, inspection.Reason))
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	for _, worktree := range worktrees {
		removal := git.RemoveClosedTaskWorktree(ctx, worktree)
		if removal.Outcome != gitmeta.ClosedTaskWorktreeRemoved && removal.Outcome != gitmeta.ClosedTaskWorktreeAbsent {
			failures = append(failures, repositoryWorktreeRepair(removal.Worktree, removal.Reason))
		}
	}
	return errors.Join(failures...)
}

func repositoryWorktreeRepair(path, reason string) error {
	return fmt.Errorf("worktree %q: %s; preserve/commit changes, unlock if appropriate, or repair ownership/absent Git registration manually with `git worktree list`, `git worktree repair`, or `git worktree prune --dry-run` before retrying (do not delete the directory blindly)", path, reason)
}
