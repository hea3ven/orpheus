package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

const (
	envRepoID   = "ORPHEUS_REPO_ID"
	envTaskID   = "ORPHEUS_TASK_ID"
	envWorktree = "ORPHEUS_WORKTREE"
	envBranch   = "ORPHEUS_BRANCH"
)

// ExecutionTarget identifies the validated workflow target for an active agent run.
type ExecutionTarget = workflow.TargetKind

const (
	// ExecutionTargetWorktree means the agent runs in Orpheus' deterministic task worktree.
	ExecutionTargetWorktree = workflow.TargetWorktreeTeam

	// ExecutionTargetMain means the agent runs in the registered repo root on the default branch.
	ExecutionTargetMain = workflow.TargetMainSolo
)

// ContextBackend is the backend-neutral read capability needed by agent context.
type ContextBackend interface {
	Get(ctx context.Context, id string) (taskmodel.Task, error)
}

// ContextBackendFactory creates a task backend for one registered repository source.
type ContextBackendFactory func(taskmodel.RepositorySource) (ContextBackend, error)

// ActiveContextResolver validates and resolves the active Orpheus agent context.
type ActiveContextResolver struct {
	Paths          state.Paths
	Registry       registry.Registry
	Sources        []taskmodel.RepositorySource
	BackendFactory ContextBackendFactory
	RunStore       taskstate.Service

	Env map[string]string
	CWD string
}

// ActiveContext is the backend-neutral execution contract rendered for agents.
type ActiveContext struct {
	Repository ContextRepository
	Task       ContextTask
	Run        ContextRun
	Target     ContextTarget
}

// ContextRepository describes the registered repository for an active context.
type ContextRepository struct {
	ID            string
	Name          string
	Root          string
	DefaultBranch string
}

// ContextTask describes the backend-neutral task details agents need.
type ContextTask struct {
	ID                 string
	Title              string
	Description        string
	AcceptanceCriteria string
}

// ContextRun describes the active Orpheus run attempt.
type ContextRun struct {
	Attempt    int
	Agent      string
	Completion *taskstate.Completion
}

// ContextTarget describes the validated execution target.
type ContextTarget struct {
	Kind             ExecutionTarget
	Branch           string
	Path             string
	CurrentDirectory string
}

type agentEnvironment struct {
	RepoID   string
	TaskID   string
	Worktree string
	Branch   string
}

type targetCandidate struct {
	Kind   ExecutionTarget
	Branch string
	Path   string
}

// Resolve validates the active run, task metadata, environment, and cwd.
func (r ActiveContextResolver) Resolve(ctx context.Context) (ActiveContext, error) {
	if r.BackendFactory == nil {
		return ActiveContext{}, errors.New("agent context backend factory is required")
	}
	if r.RunStore == nil {
		return ActiveContext{}, errors.New("agent context run store is required")
	}

	env, err := r.resolveEnvironment()
	if err != nil {
		return ActiveContext{}, err
	}

	repo, err := registeredRepoByID(r.Registry, env.RepoID)
	if err != nil {
		return ActiveContext{}, err
	}
	source, err := repositorySourceByID(r.Sources, repo.ID)
	if err != nil {
		return ActiveContext{}, err
	}
	if err := validateRepositorySource(repo, source); err != nil {
		return ActiveContext{}, err
	}

	backend, err := r.BackendFactory(source)
	if err != nil {
		return ActiveContext{}, fmt.Errorf("create task backend for repo %s: %w", repo.ID, err)
	}
	taskItem, err := backend.Get(ctx, env.TaskID)
	if err != nil {
		return ActiveContext{}, fmt.Errorf("load task %s in repo %s: %w", env.TaskID, repo.ID, err)
	}
	if strings.TrimSpace(taskItem.ID) != env.TaskID {
		return ActiveContext{}, fmt.Errorf("task backend returned task %q, expected %q", taskItem.ID, env.TaskID)
	}
	if err := validateContextTaskStatus(taskItem); err != nil {
		return ActiveContext{}, err
	}

	run, ok, err := r.RunStore.LatestRun(repo.ID, env.TaskID)
	if err != nil {
		return ActiveContext{}, fmt.Errorf("load latest Orpheus run for task %s/%s: %w", repo.ID, env.TaskID, err)
	}
	if !ok {
		return ActiveContext{}, fmt.Errorf("task %s/%s has no Orpheus run attempts", repo.ID, env.TaskID)
	}
	if run.Status != taskstate.RunStatusRunning {
		return ActiveContext{}, fmt.Errorf(
			"latest Orpheus run attempt %d for task %s/%s is %q, expected %q",
			run.Attempt,
			repo.ID,
			env.TaskID,
			run.Status,
			taskstate.RunStatusRunning,
		)
	}

	targets, err := workflow.ExpectedTargetsForTask(source.Repository, env.TaskID, r.Paths)
	if err != nil {
		return ActiveContext{}, err
	}

	candidate, err := classifyContextTarget(taskItem.OrpheusMetadata(), targets)
	if err != nil {
		return ActiveContext{}, fmt.Errorf("task %s has inconsistent Orpheus metadata: %w", env.TaskID, err)
	}
	if err := validateEnvironmentMatchesTarget(env, candidate); err != nil {
		return ActiveContext{}, err
	}
	if err := validateRunMatchesTarget(run, candidate); err != nil {
		return ActiveContext{}, err
	}

	cwd, err := r.resolveCWD()
	if err != nil {
		return ActiveContext{}, err
	}
	if ok, err := pathInside(cwd, candidate.Path); err != nil {
		return ActiveContext{}, err
	} else if !ok {
		return ActiveContext{}, fmt.Errorf(
			"current directory %q is outside the %s execution target %q",
			cwd,
			candidate.Kind.DisplayName(),
			candidate.Path,
		)
	}

	return ActiveContext{
		Repository: ContextRepository{
			ID:            repo.ID,
			Name:          repo.Name,
			Root:          targets.MainSolo.Worktree,
			DefaultBranch: targets.MainSolo.Branch,
		},
		Task: ContextTask{
			ID:                 taskItem.ID,
			Title:              taskItem.Title,
			Description:        taskItem.Description,
			AcceptanceCriteria: taskItem.AcceptanceCriteria,
		},
		Run: ContextRun{
			Attempt:    run.Attempt,
			Agent:      run.Agent,
			Completion: cloneCompletion(run.Completion),
		},
		Target: ContextTarget{
			Kind:             candidate.Kind,
			Branch:           candidate.Branch,
			Path:             candidate.Path,
			CurrentDirectory: cwd,
		},
	}, nil
}

func cloneCompletion(completion *taskstate.Completion) *taskstate.Completion {
	if completion == nil {
		return nil
	}
	clone := *completion
	return &clone
}

func (r ActiveContextResolver) resolveEnvironment() (agentEnvironment, error) {
	repoID, err := r.requiredEnv(envRepoID)
	if err != nil {
		return agentEnvironment{}, err
	}
	taskID, err := r.requiredEnv(envTaskID)
	if err != nil {
		return agentEnvironment{}, err
	}
	worktree, err := cleanAbsPath(envWorktree, r.envValue(envWorktree))
	if err != nil {
		return agentEnvironment{}, err
	}
	branch, err := r.requiredEnv(envBranch)
	if err != nil {
		return agentEnvironment{}, err
	}

	return agentEnvironment{
		RepoID:   repoID,
		TaskID:   taskID,
		Worktree: worktree,
		Branch:   branch,
	}, nil
}

func (r ActiveContextResolver) requiredEnv(name string) (string, error) {
	value := strings.TrimSpace(r.envValue(name))
	if value == "" {
		return "", fmt.Errorf("%s is required; run this command from an Orpheus-dispatched agent environment", name)
	}
	return value, nil
}

func (r ActiveContextResolver) envValue(name string) string {
	if r.Env != nil {
		return r.Env[name]
	}
	return os.Getenv(name)
}

func (r ActiveContextResolver) resolveCWD() (string, error) {
	cwd := strings.TrimSpace(r.CWD)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory: %w", err)
		}
	}
	return cleanAbsPath("current directory", cwd)
}

func registeredRepoByID(reg registry.Registry, repoID string) (registry.Repo, error) {
	for _, repo := range reg.Repos {
		if strings.TrimSpace(repo.ID) == repoID {
			return repo, nil
		}
	}
	return registry.Repo{}, fmt.Errorf("registered repo %q was not found", repoID)
}

func repositorySourceByID(sources []taskmodel.RepositorySource, repoID string) (taskmodel.RepositorySource, error) {
	for _, source := range sources {
		if strings.TrimSpace(source.Repository.ID) == repoID {
			return source, nil
		}
	}
	return taskmodel.RepositorySource{}, fmt.Errorf("registered repo %q has no task source", repoID)
}

func validateRepositorySource(repo registry.Repo, source taskmodel.RepositorySource) error {
	sourcePath, err := cleanAbsPath("task source repo path", source.Repository.Path)
	if err != nil {
		return err
	}
	repoPath, err := cleanAbsPath("registered repo path", repo.Path)
	if err != nil {
		return err
	}
	if sourcePath != repoPath {
		return fmt.Errorf("task source repo path is %q, expected registered repo path %q", source.Repository.Path, repo.Path)
	}

	sourceBranch := strings.TrimSpace(source.Repository.DefaultBranch)
	repoBranch := strings.TrimSpace(repo.DefaultBranch)
	if sourceBranch != repoBranch {
		return fmt.Errorf("task source default branch is %q, expected registered default branch %q", sourceBranch, repoBranch)
	}
	return nil
}

func validateContextTaskStatus(taskItem taskmodel.Task) error {
	metadata := taskItem.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" {
		return fmt.Errorf("task %s already has a pull request URL recorded", taskItem.ID)
	}

	switch taskItem.Status {
	case taskmodel.StatusInProgress:
		return nil
	case taskmodel.StatusClosed:
		return fmt.Errorf("task %s is closed; refusing to render active agent context", taskItem.ID)
	default:
		status := strings.TrimSpace(string(taskItem.Status))
		if status == "" {
			status = "unknown"
		}
		return fmt.Errorf("task %s is %s, expected in_progress for active agent context", taskItem.ID, status)
	}
}

func classifyContextTarget(
	metadata taskmodel.OrpheusMetadata,
	targets workflow.ExpectedTargets,
) (targetCandidate, error) {
	target, err := workflow.ClassifyMetadataTarget(metadata, targets)
	if err != nil {
		return targetCandidate{}, err
	}
	return targetCandidate{
		Kind:   target.Kind,
		Branch: target.Branch,
		Path:   target.Worktree,
	}, nil
}

func validateEnvironmentMatchesTarget(env agentEnvironment, target targetCandidate) error {
	if env.Branch != target.Branch {
		return fmt.Errorf(
			"%s is %q, expected %q for %s execution target",
			envBranch,
			env.Branch,
			target.Branch,
			target.Kind.DisplayName(),
		)
	}
	if env.Worktree != target.Path {
		return fmt.Errorf(
			"%s is %q, expected %q for %s execution target",
			envWorktree,
			env.Worktree,
			target.Path,
			target.Kind.DisplayName(),
		)
	}
	return nil
}

func validateRunMatchesTarget(run taskstate.RunAttempt, target targetCandidate) error {
	if strings.TrimSpace(run.Branch) != target.Branch {
		return fmt.Errorf(
			"latest Orpheus run attempt %d branch is %q, expected %q for %s execution target",
			run.Attempt,
			run.Branch,
			target.Branch,
			target.Kind.DisplayName(),
		)
	}

	runWorktree, err := cleanAbsPath("latest Orpheus run worktree", run.Worktree)
	if err != nil {
		return err
	}
	if runWorktree != target.Path {
		return fmt.Errorf(
			"latest Orpheus run attempt %d worktree is %q, expected %q for %s execution target",
			run.Attempt,
			run.Worktree,
			target.Path,
			target.Kind.DisplayName(),
		)
	}
	return nil
}

func cleanAbsPath(label string, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%s must be absolute, got %q", label, path)
	}
	return filepath.Clean(path), nil
}

func pathInside(path string, root string) (bool, error) {
	cleanPath, err := cleanAbsPath("path", path)
	if err != nil {
		return false, err
	}
	cleanRoot, err := cleanAbsPath("execution target path", root)
	if err != nil {
		return false, err
	}

	rel, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil {
		return false, fmt.Errorf("compare current directory with execution target: %w", err)
	}
	isInside := rel == "." ||
		(rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel))
	return isInside, nil
}
