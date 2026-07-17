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
	"github.com/hea3ven/orpheus/internal/tasktarget"
)

const (
	envRepoID        = "ORPHEUS_REPO_ID"
	envTaskID        = "ORPHEUS_TASK_ID"
	envWorktree      = "ORPHEUS_WORKTREE"
	envBranch        = "ORPHEUS_BRANCH"
	envAgentPurpose  = "ORPHEUS_AGENT_PURPOSE"
	envConflictFiles = "ORPHEUS_CONFLICT_FILES"
	envReviewAttempt = "ORPHEUS_REVIEW_ATTEMPT"
	envReviewStep    = "ORPHEUS_REVIEW_STEP"
)

// ExecutionTarget identifies the validated workflow target for an active agent run.
type ExecutionTarget = tasktarget.TargetKind

const (
	// ExecutionTargetWorktree means the agent runs in Orpheus' deterministic task worktree.
	ExecutionTargetWorktree = tasktarget.TargetWorktreeTeam

	// ExecutionTargetRepoRoot means the agent runs in the registered repo root on a task branch.
	ExecutionTargetRepoRoot = tasktarget.TargetRepoRootTeam

	// ExecutionTargetMain means the agent runs in the registered repo root on the default branch.
	ExecutionTargetMain = tasktarget.TargetMainSolo
)

// ContextBackend is the backend-neutral read capability needed by agent context.
type ContextBackend interface {
	Get(ctx context.Context, id string) (taskmodel.Task, error)
}

// ContextBackendFactory creates a task backend for one registered repository source.
type ContextBackendFactory func(taskmodel.RepositorySource) (ContextBackend, error)

// ContextStateLoader is the task-state read capability needed by agent context.
type ContextStateLoader interface {
	Load(repoID, taskID string) (taskstate.TaskState, error)
}

// ActiveContextResolver validates and resolves the active Orpheus agent context.
type ActiveContextResolver struct {
	Paths          state.Paths
	Registry       registry.Registry
	Sources        []taskmodel.RepositorySource
	BackendFactory ContextBackendFactory
	RunStore       ContextStateLoader

	Env map[string]string
	CWD string
}

// ActiveContext is the backend-neutral execution contract rendered for agents.
type ActiveContext struct {
	Repository ContextRepository
	Task       ContextTask
	Run        ContextRun
	Target     ContextTarget
	FollowUp   *ContextFollowUp
}

// ConflictResolutionContext is the agent-facing contract for sync conflict repair.
type ConflictResolutionContext struct {
	Repository    ContextRepository
	Task          ContextTask
	Target        ContextTarget
	PRURL         string
	ConflictFiles []string
}

// ContextRepository describes the registered repository for an active context.
type ContextRepository struct {
	ID                   string
	Name                 string
	Root                 string
	DefaultBranch        string
	SummaryGuidance      string
	SummaryGuidanceStyle string
}

// ContextTask describes the backend-neutral task details agents need.
type ContextTask struct {
	ID                 string
	Title              string
	ExternalRef        string
	Description        string
	AcceptanceCriteria string
}

// ContextRun describes the active Orpheus run attempt.
type ContextRun struct {
	Attempt    int
	Agent      string
	Execution  taskstate.AgentExecution
	Completion *taskstate.Completion
}

// ContextFollowUp describes targeted review blockers for a continuation run.
type ContextFollowUp struct {
	ReviewAttempt int
	Findings      []ContextReviewFinding
}

// ContextReviewFinding describes one review finding targeted by this run.
type ContextReviewFinding struct {
	Index           int
	Title           string
	Description     string
	SuggestedAction string
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
	if err := r.validateDependencies(); err != nil {
		return ActiveContext{}, err
	}
	env, err := r.resolveEnvironment()
	if err != nil {
		return ActiveContext{}, err
	}

	repo, source, taskItem, err := r.resolveTask(ctx, env)
	if err != nil {
		return ActiveContext{}, err
	}
	run, taskTarget, err := r.resolveRunningRun(repo.ID, env.TaskID)
	if err != nil {
		return ActiveContext{}, err
	}
	targets, candidate, err := r.resolveContextTarget(source, taskItem, env.TaskID, taskTarget)
	if err != nil {
		return ActiveContext{}, err
	}
	if err := validateEnvironmentMatchesTarget(env, candidate); err != nil {
		return ActiveContext{}, err
	}

	cwd, err := r.resolveTargetCWD(candidate)
	if err != nil {
		return ActiveContext{}, err
	}

	followUp, err := r.resolveFollowUpContext(repo.ID, env.TaskID, run)
	if err != nil {
		return ActiveContext{}, err
	}

	activeContext, err := newActiveContext(repo, targets, taskItem, run, candidate, cwd)
	if err != nil {
		return ActiveContext{}, err
	}
	activeContext.FollowUp = followUp
	return activeContext, nil
}

// ResolveConflictResolution validates the active sync conflict-repair context.
func (r ActiveContextResolver) ResolveConflictResolution(ctx context.Context) (ConflictResolutionContext, error) {
	if err := r.validateDependencies(); err != nil {
		return ConflictResolutionContext{}, err
	}
	env, err := r.resolveEnvironment()
	if err != nil {
		return ConflictResolutionContext{}, err
	}

	repo, source, taskItem, err := r.resolveConflictResolutionTask(ctx, env)
	if err != nil {
		return ConflictResolutionContext{}, err
	}
	targets, candidate, err := r.resolveConflictResolutionTarget(source, taskItem, env.TaskID)
	if err != nil {
		return ConflictResolutionContext{}, err
	}
	if err := validateEnvironmentMatchesTarget(env, candidate); err != nil {
		return ConflictResolutionContext{}, err
	}

	cwd, err := r.resolveTargetCWD(candidate)
	if err != nil {
		return ConflictResolutionContext{}, err
	}

	conflictFiles := parseConflictFiles(r.envValue(envConflictFiles))
	return newConflictResolutionContext(repo, targets, taskItem, candidate, cwd, conflictFiles), nil
}

func (r ActiveContextResolver) validateDependencies() error {
	if r.BackendFactory == nil {
		return errors.New("agent context backend factory is required")
	}
	if r.RunStore == nil {
		return errors.New("agent context run store is required")
	}
	return nil
}

func (r ActiveContextResolver) resolveTask(
	ctx context.Context,
	env agentEnvironment,
) (registry.Repo, taskmodel.RepositorySource, taskmodel.Task, error) {
	repo, err := registeredRepoByID(r.Registry, env.RepoID)
	if err != nil {
		return registry.Repo{}, taskmodel.RepositorySource{}, taskmodel.Task{}, err
	}
	source, err := repositorySourceByID(r.Sources, repo.ID)
	if err != nil {
		return registry.Repo{}, taskmodel.RepositorySource{}, taskmodel.Task{}, err
	}
	if err := validateRepositorySource(repo, source); err != nil {
		return registry.Repo{}, taskmodel.RepositorySource{}, taskmodel.Task{}, err
	}

	taskItem, err := r.loadContextTask(ctx, source, repo.ID, env.TaskID)
	if err != nil {
		return registry.Repo{}, taskmodel.RepositorySource{}, taskmodel.Task{}, err
	}
	return repo, source, taskItem, nil
}

func (r ActiveContextResolver) loadContextTask(
	ctx context.Context,
	source taskmodel.RepositorySource,
	repoID string,
	taskID string,
) (taskmodel.Task, error) {
	backend, err := r.BackendFactory(source)
	if err != nil {
		return taskmodel.Task{}, fmt.Errorf("create task backend for repo %s: %w", repoID, err)
	}
	taskItem, err := backend.Get(ctx, taskID)
	if err != nil {
		return taskmodel.Task{}, fmt.Errorf("load task %s in repo %s: %w", taskID, repoID, err)
	}
	if strings.TrimSpace(taskItem.ID) != taskID {
		return taskmodel.Task{}, fmt.Errorf("task backend returned task %q, expected %q", taskItem.ID, taskID)
	}
	if err := validateContextTaskStatus(taskItem); err != nil {
		return taskmodel.Task{}, err
	}
	return taskItem, nil
}

func (r ActiveContextResolver) resolveConflictResolutionTask(
	ctx context.Context,
	env agentEnvironment,
) (registry.Repo, taskmodel.RepositorySource, taskmodel.Task, error) {
	repo, err := registeredRepoByID(r.Registry, env.RepoID)
	if err != nil {
		return registry.Repo{}, taskmodel.RepositorySource{}, taskmodel.Task{}, err
	}
	source, err := repositorySourceByID(r.Sources, repo.ID)
	if err != nil {
		return registry.Repo{}, taskmodel.RepositorySource{}, taskmodel.Task{}, err
	}
	if err := validateRepositorySource(repo, source); err != nil {
		return registry.Repo{}, taskmodel.RepositorySource{}, taskmodel.Task{}, err
	}

	backend, err := r.BackendFactory(source)
	if err != nil {
		return registry.Repo{}, taskmodel.RepositorySource{}, taskmodel.Task{}, fmt.Errorf("create task backend for repo %s: %w", repo.ID, err)
	}
	taskItem, err := backend.Get(ctx, env.TaskID)
	if err != nil {
		return registry.Repo{}, taskmodel.RepositorySource{}, taskmodel.Task{}, fmt.Errorf("load task %s in repo %s: %w", env.TaskID, repo.ID, err)
	}
	if strings.TrimSpace(taskItem.ID) != env.TaskID {
		return registry.Repo{}, taskmodel.RepositorySource{}, taskmodel.Task{}, fmt.Errorf("task backend returned task %q, expected %q", taskItem.ID, env.TaskID)
	}
	if taskItem.Status != taskmodel.StatusInProgress {
		return registry.Repo{}, taskmodel.RepositorySource{}, taskmodel.Task{}, fmt.Errorf("task %s is %s, expected in_progress for conflict resolution", taskItem.ID, taskItem.Status)
	}
	return repo, source, taskItem, nil
}

func (r ActiveContextResolver) resolveRunningRun(
	repoID string,
	taskID string,
) (taskstate.RunAttempt, taskstate.TaskTarget, error) {
	state, err := r.RunStore.Load(repoID, taskID)
	if err != nil {
		return taskstate.RunAttempt{}, taskstate.TaskTarget{}, fmt.Errorf(
			"load latest Orpheus run for task %s/%s: %w",
			repoID,
			taskID,
			err,
		)
	}
	run, ok := taskstate.LatestRun(state)
	if !ok {
		return taskstate.RunAttempt{}, taskstate.TaskTarget{}, fmt.Errorf(
			"task %s/%s has no Orpheus run attempts",
			repoID,
			taskID,
		)
	}
	if run.Status != taskstate.RunStatusRunning {
		return taskstate.RunAttempt{}, taskstate.TaskTarget{}, fmt.Errorf(
			"latest Orpheus run attempt %d for task %s/%s is %q, expected %q",
			run.Attempt,
			repoID,
			taskID,
			run.Status,
			taskstate.RunStatusRunning,
		)
	}
	target, ok := taskstate.Target(state)
	if !ok {
		return taskstate.RunAttempt{}, taskstate.TaskTarget{}, fmt.Errorf(
			"task %s/%s has no taskstate target",
			repoID,
			taskID,
		)
	}
	return run, target, nil
}

func (r ActiveContextResolver) resolveFollowUpContext(
	repoID string,
	taskID string,
	run taskstate.RunAttempt,
) (*ContextFollowUp, error) {
	if run.ReviewFollowUp == nil {
		return nil, nil
	}

	state, err := r.RunStore.Load(repoID, taskID)
	if err != nil {
		return nil, fmt.Errorf("load review follow-up context for task %s/%s: %w", repoID, taskID, err)
	}
	latestReview, ok := taskstate.LatestReview(state)
	if !ok || latestReview.Attempt != run.ReviewFollowUp.ReviewAttempt {
		return nil, fmt.Errorf("latest review attempt for task %s/%s no longer matches follow-up run attempt %d", repoID, taskID, run.Attempt)
	}

	findings := make([]ContextReviewFinding, 0, len(run.ReviewFollowUp.FindingIndexes))
	for _, index := range run.ReviewFollowUp.FindingIndexes {
		if index < 0 || index >= len(latestReview.Findings) {
			return nil, fmt.Errorf("review follow-up finding index %d is out of range for task %s/%s", index, repoID, taskID)
		}
		finding := latestReview.Findings[index]
		if finding.TargetedByRunAttempt != run.Attempt {
			return nil, fmt.Errorf("review finding %d for task %s/%s is not targeted by run attempt %d", index, repoID, taskID, run.Attempt)
		}
		findings = append(findings, ContextReviewFinding{
			Index:           index,
			Title:           finding.Title,
			Description:     finding.Description,
			SuggestedAction: finding.SuggestedAction,
		})
	}
	return &ContextFollowUp{
		ReviewAttempt: latestReview.Attempt,
		Findings:      findings,
	}, nil
}

func (r ActiveContextResolver) resolveContextTarget(
	source taskmodel.RepositorySource,
	taskItem taskmodel.Task,
	taskID string,
	taskTarget taskstate.TaskTarget,
) (tasktarget.ExpectedTargets, targetCandidate, error) {
	targets, err := tasktarget.ExpectedTargetsForTask(source.Repository, taskID, r.Paths)
	if err != nil {
		return tasktarget.ExpectedTargets{}, targetCandidate{}, err
	}
	candidate, err := classifyContextTarget(source.Repository, taskTarget)
	if err != nil {
		return tasktarget.ExpectedTargets{}, targetCandidate{}, fmt.Errorf(
			"task %s has inconsistent taskstate target: %w",
			taskID,
			err,
		)
	}
	if _, err := tasktarget.ClassifyMetadataTarget(taskItem.OrpheusMetadata(), targets); err != nil {
		return tasktarget.ExpectedTargets{}, targetCandidate{}, fmt.Errorf(
			"task %s has inconsistent Orpheus metadata: %w",
			taskID,
			err,
		)
	}
	return targets, candidate, nil
}

func (r ActiveContextResolver) resolveConflictResolutionTarget(
	source taskmodel.RepositorySource,
	taskItem taskmodel.Task,
	taskID string,
) (tasktarget.ExpectedTargets, targetCandidate, error) {
	targets, err := tasktarget.ExpectedTargetsForTask(source.Repository, taskID, r.Paths)
	if err != nil {
		return tasktarget.ExpectedTargets{}, targetCandidate{}, err
	}
	candidate, err := tasktarget.ClassifyMetadataTarget(taskItem.OrpheusMetadata(), targets)
	if err != nil {
		return tasktarget.ExpectedTargets{}, targetCandidate{}, fmt.Errorf(
			"task %s has inconsistent Orpheus metadata: %w",
			taskID,
			err,
		)
	}
	if candidate.Kind != tasktarget.TargetWorktreeTeam && candidate.Kind != tasktarget.TargetRepoRootTeam {
		return tasktarget.ExpectedTargets{}, targetCandidate{}, fmt.Errorf(
			"task %s target is %s, expected an Orpheus-managed PR branch for sync conflict resolution",
			taskID,
			candidate.Kind.DisplayName(),
		)
	}
	return targets, targetCandidate{
		Kind:   candidate.Kind,
		Branch: candidate.Branch,
		Path:   candidate.Worktree,
	}, nil
}

func (r ActiveContextResolver) resolveTargetCWD(candidate targetCandidate) (string, error) {
	cwd, err := r.resolveCWD()
	if err != nil {
		return "", err
	}
	ok, err := pathInside(cwd, candidate.Path)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf(
			"current directory %q is outside the %s execution target %q",
			cwd,
			candidate.Kind.DisplayName(),
			candidate.Path,
		)
	}
	return cwd, nil
}

func newActiveContext(
	repo registry.Repo,
	targets tasktarget.ExpectedTargets,
	taskItem taskmodel.Task,
	run taskstate.RunAttempt,
	candidate targetCandidate,
	cwd string,
) (ActiveContext, error) {
	if err := registry.ValidateSummaryGuidanceStyle(repo.SummaryGuidanceStyle); err != nil {
		return ActiveContext{}, err
	}

	return ActiveContext{
		Repository: ContextRepository{
			ID:                   repo.ID,
			Name:                 repo.Name,
			Root:                 targets.MainSolo.Worktree,
			DefaultBranch:        targets.MainSolo.Branch,
			SummaryGuidance:      repo.SummaryGuidance,
			SummaryGuidanceStyle: strings.TrimSpace(repo.SummaryGuidanceStyle),
		},
		Task: ContextTask{
			ID:                 taskItem.ID,
			Title:              taskItem.Title,
			ExternalRef:        taskItem.ExternalRef,
			Description:        taskItem.Description,
			AcceptanceCriteria: taskItem.AcceptanceCriteria,
		},
		Run: ContextRun{
			Attempt:    run.Attempt,
			Agent:      run.Execution.Agent,
			Execution:  run.Execution,
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

func newConflictResolutionContext(
	repo registry.Repo,
	targets tasktarget.ExpectedTargets,
	taskItem taskmodel.Task,
	candidate targetCandidate,
	cwd string,
	conflictFiles []string,
) ConflictResolutionContext {
	metadata := taskItem.OrpheusMetadata()
	prURL := ""
	if metadata.HasPRURL {
		prURL = strings.TrimSpace(metadata.PRURL)
	}
	return ConflictResolutionContext{
		Repository: ContextRepository{
			ID:            repo.ID,
			Name:          repo.Name,
			Root:          targets.MainSolo.Worktree,
			DefaultBranch: targets.MainSolo.Branch,
		},
		Task: ContextTask{
			ID:                 taskItem.ID,
			Title:              taskItem.Title,
			ExternalRef:        taskItem.ExternalRef,
			Description:        taskItem.Description,
			AcceptanceCriteria: taskItem.AcceptanceCriteria,
		},
		Target: ContextTarget{
			Kind:             candidate.Kind,
			Branch:           candidate.Branch,
			Path:             candidate.Path,
			CurrentDirectory: cwd,
		},
		PRURL:         prURL,
		ConflictFiles: append([]string{}, conflictFiles...),
	}
}

func parseConflictFiles(value string) []string {
	files := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n") {
		file := strings.TrimSpace(line)
		if file != "" {
			files = append(files, file)
		}
	}
	return files
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

func classifyContextTarget(repo taskmodel.Repository, taskTarget taskstate.TaskTarget) (targetCandidate, error) {
	branch := strings.TrimSpace(taskTarget.Branch)
	worktree, err := cleanAbsPath("taskstate target worktree", taskTarget.Worktree)
	if err != nil {
		return targetCandidate{}, err
	}
	kind := tasktarget.ClassifyRunTarget(repo, branch, worktree)
	if kind == tasktarget.TargetUnknown {
		return targetCandidate{}, fmt.Errorf("branch %q and worktree %q do not match a supported execution target", branch, worktree)
	}
	return targetCandidate{
		Kind:   kind,
		Branch: branch,
		Path:   worktree,
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
