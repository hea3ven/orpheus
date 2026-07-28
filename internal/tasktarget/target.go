package tasktarget

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pathutil"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// TargetKind identifies an Orpheus task execution target.
type TargetKind string

const (
	// TargetUnknown means a branch/worktree pair does not match a supported target.
	TargetUnknown TargetKind = ""

	// TargetWorktreeTeam means work runs in Orpheus' deterministic task worktree and becomes PR-ready.
	TargetWorktreeTeam TargetKind = "worktree"

	// TargetRepoRootTeam means work runs in the registered repo root on a task branch and becomes PR-ready.
	TargetRepoRootTeam TargetKind = "repo-root"

	// TargetMainSolo means work runs in the registered repo root and becomes local-review-ready.
	TargetMainSolo TargetKind = "main"
)

// DisplayName returns the agent/operator-facing target name.
func (k TargetKind) DisplayName() string {
	switch k {
	case TargetWorktreeTeam:
		return "worktree/team"
	case TargetRepoRootTeam:
		return "repo-root/team"
	case TargetMainSolo:
		return "main/solo"
	default:
		return string(k)
	}
}

// Target describes a concrete branch/worktree pair for a supported execution target.
type Target struct {
	Kind     TargetKind
	Branch   string
	Worktree string
}

// ExpectedTargets describes the supported execution targets for one task.
type ExpectedTargets struct {
	MainSolo     Target
	WorktreeTeam Target
	RepoRootTeam Target
}

// ExpectedTargetsForTask returns the strict execution targets used when dispatching or validating an active run.
func ExpectedTargetsForTask(repo task.Repository, taskID string, paths state.Paths) (ExpectedTargets, error) {
	repoTarget, err := gitmeta.ExpectedRepoRoot(gitmeta.RepoRootOptions{
		RepoID:        repo.ID,
		RepoName:      repo.Name,
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
	})
	if err != nil {
		return ExpectedTargets{}, fmt.Errorf("resolve registered repo-root target: %w", err)
	}
	worktreeTarget, err := gitmeta.ExpectedTaskWorktree(gitmeta.TaskWorktreeOptions{
		RepoID:        repo.ID,
		RepoName:      repo.Name,
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
		TaskID:        taskID,
		Paths:         paths,
	})
	if err != nil {
		return ExpectedTargets{}, fmt.Errorf("resolve deterministic task worktree target: %w", err)
	}
	repoRootTaskTarget, err := gitmeta.ExpectedRepoRootTaskBranch(gitmeta.TaskWorktreeOptions{
		RepoID:        repo.ID,
		RepoName:      repo.Name,
		RepoPath:      repo.Path,
		DefaultBranch: repo.DefaultBranch,
		TaskID:        taskID,
		Paths:         paths,
	})
	if err != nil {
		return ExpectedTargets{}, fmt.Errorf("resolve repo-root task branch target: %w", err)
	}

	return ExpectedTargets{
		MainSolo: Target{
			Kind:     TargetMainSolo,
			Branch:   repoTarget.Branch,
			Worktree: cleanPath(repoTarget.WorktreePath),
		},
		WorktreeTeam: Target{
			Kind:     TargetWorktreeTeam,
			Branch:   worktreeTarget.Branch,
			Worktree: cleanPath(worktreeTarget.WorktreePath),
		},
		RepoRootTeam: Target{
			Kind:     TargetRepoRootTeam,
			Branch:   repoRootTaskTarget.Branch,
			Worktree: cleanPath(repoRootTaskTarget.WorktreePath),
		},
	}, nil
}

// ClassifyMetadataTarget matches Orpheus task metadata against exact expected execution targets.
func ClassifyMetadataTarget(metadata task.OrpheusMetadata, targets ExpectedTargets) (Target, error) {
	targets = cleanExpectedTargets(targets)
	if !metadata.HasBranch || strings.TrimSpace(metadata.Branch) == "" {
		return Target{}, fmt.Errorf("%s is missing", task.MetadataBranch)
	}
	if !metadata.HasWorktree || strings.TrimSpace(metadata.Worktree) == "" {
		return Target{}, fmt.Errorf("%s is missing", task.MetadataWorktree)
	}

	metadataWorktree, err := cleanAbsPath(task.MetadataWorktree, metadata.Worktree)
	if err != nil {
		return Target{}, err
	}
	metadataBranch := strings.TrimSpace(metadata.Branch)

	matchesMain := metadataBranch == targets.MainSolo.Branch && metadataWorktree == targets.MainSolo.Worktree
	matchesWorktree := metadataBranch == targets.WorktreeTeam.Branch &&
		metadataWorktree == targets.WorktreeTeam.Worktree
	matchesRepoRootTeam := metadataBranch == targets.RepoRootTeam.Branch &&
		metadataWorktree == targets.RepoRootTeam.Worktree
	matchCount := 0
	for _, matches := range []bool{matchesMain, matchesWorktree, matchesRepoRootTeam} {
		if matches {
			matchCount++
		}
	}
	switch {
	case matchCount > 1:
		return Target{}, fmt.Errorf(
			"%s and %s match multiple supported execution targets",
			task.MetadataBranch,
			task.MetadataWorktree,
		)
	case matchesMain:
		return targets.MainSolo, nil
	case matchesWorktree:
		return targets.WorktreeTeam, nil
	case matchesRepoRootTeam:
		return targets.RepoRootTeam, nil
	default:
		return Target{}, fmt.Errorf(
			"%s=%q and %s=%q do not match repo-root default target (%s=%q, %s=%q), worktree target (%s=%q, %s=%q), or repo-root task branch target (%s=%q, %s=%q)",
			task.MetadataBranch,
			metadata.Branch,
			task.MetadataWorktree,
			metadata.Worktree,
			task.MetadataBranch,
			targets.MainSolo.Branch,
			task.MetadataWorktree,
			targets.MainSolo.Worktree,
			task.MetadataBranch,
			targets.WorktreeTeam.Branch,
			task.MetadataWorktree,
			targets.WorktreeTeam.Worktree,
			task.MetadataBranch,
			targets.RepoRootTeam.Branch,
			task.MetadataWorktree,
			targets.RepoRootTeam.Worktree,
		)
	}
}

// ClassifyRunTarget classifies a branch/worktree pair using repository-level target rules.
func ClassifyRunTarget(repo task.Repository, branch string, worktree string) TargetKind {
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	repoRoot := cleanPath(repo.Path)
	branch = strings.TrimSpace(branch)
	worktree = cleanPath(worktree)

	if branch == "" || worktree == "" || defaultBranch == "" || repoRoot == "" {
		return TargetUnknown
	}
	if branch == defaultBranch && worktree == repoRoot {
		return TargetMainSolo
	}
	if branch != defaultBranch && worktree == repoRoot {
		return TargetRepoRootTeam
	}
	if branch != defaultBranch && worktree != repoRoot {
		return TargetWorktreeTeam
	}
	return TargetUnknown
}

// ClassifyGitFacts matches current taskstate Git facts against exact expected execution targets.
func ClassifyGitFacts(taskTarget taskstate.GitFacts, targets ExpectedTargets) (Target, error) {
	targets = cleanExpectedTargets(targets)
	branch := strings.TrimSpace(taskTarget.Branch)
	if branch == "" {
		return Target{}, errors.New("taskstate target branch is missing")
	}
	worktree, err := cleanAbsPath("taskstate target worktree", taskTarget.Worktree)
	if err != nil {
		return Target{}, err
	}

	switch {
	case branch == targets.MainSolo.Branch && worktree == targets.MainSolo.Worktree:
		return targets.MainSolo, nil
	case branch == targets.WorktreeTeam.Branch && worktree == targets.WorktreeTeam.Worktree:
		return targets.WorktreeTeam, nil
	case branch == targets.RepoRootTeam.Branch && worktree == targets.RepoRootTeam.Worktree:
		return targets.RepoRootTeam, nil
	default:
		return Target{}, fmt.Errorf(
			"taskstate target branch/worktree %q/%q does not match an expected workflow target",
			taskTarget.Branch,
			taskTarget.Worktree,
		)
	}
}

func cleanExpectedTargets(targets ExpectedTargets) ExpectedTargets {
	targets.MainSolo.Worktree = cleanPath(targets.MainSolo.Worktree)
	targets.WorktreeTeam.Worktree = cleanPath(targets.WorktreeTeam.Worktree)
	targets.RepoRootTeam.Worktree = cleanPath(targets.RepoRootTeam.Worktree)
	return targets
}

func cleanAbsPath(label string, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be absolute, got %q", label, path)
	}
	canonicalPath, err := pathutil.CanonicalAbs(path)
	if err != nil {
		return "", fmt.Errorf("normalize %s %q: %w", label, path, err)
	}
	return canonicalPath, nil
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		if canonicalPath, err := pathutil.CanonicalAbs(path); err == nil {
			return canonicalPath
		}
	}
	return filepath.Clean(path)
}
