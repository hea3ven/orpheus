package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

const completionLockOperation = "agent completion"

// CompleteOptions describes the agent-authored completion payload.
type CompleteOptions struct {
	Summary             string
	Description         string
	DetailedDescription string
}

// CompleteResult reports the validated context and persisted run completion.
type CompleteResult struct {
	Context            ActiveContext
	Run                taskstate.RunAttempt
	CommitError        error
	Repeated           bool
	RepeatedDiagnostic *taskstate.Event
}

// CompletionRunStore persists completion facts on a run attempt.
type CompletionRunStore interface {
	CompleteRun(repoID, taskID string, attempt int, opts taskstate.CompleteRunOptions) (taskstate.RunAttempt, error)
	RecordRepeatedCompletion(repoID, taskID string, attempt int, opts taskstate.RepeatedCompletionOptions) (taskstate.Event, error)
}

// GitStateReader provides the Git checks and commit operations needed before completion.
type GitStateReader interface {
	CurrentBranch(ctx context.Context, dir string) (string, error)
	HasWorkingTreeChanges(ctx context.Context, dir string) (bool, error)
	StageAll(ctx context.Context, dir string) error
	Commit(ctx context.Context, dir string, message string) (string, error)
}

// LocalGitState reads Git state from the local checkout.
type LocalGitState struct{}

// CurrentBranch returns the current local Git branch.
func (LocalGitState) CurrentBranch(ctx context.Context, dir string) (string, error) {
	return gitmeta.CurrentBranch(ctx, dir)
}

// HasWorkingTreeChanges reports whether the checkout has local changes.
func (LocalGitState) HasWorkingTreeChanges(ctx context.Context, dir string) (bool, error) {
	return gitmeta.HasWorkingTreeChanges(ctx, dir)
}

// StageAll stages tracked changes and untracked non-ignored files.
func (LocalGitState) StageAll(ctx context.Context, dir string) error {
	return gitmeta.StageAll(ctx, dir)
}

// Commit creates a local Git commit and returns the resulting SHA.
func (LocalGitState) Commit(ctx context.Context, dir string, message string) (string, error) {
	return gitmeta.Commit(ctx, dir, message)
}

// CompletionService validates and records agent completion for supported targets.
type CompletionService struct {
	Paths    state.Paths
	Resolver ActiveContextResolver
	RunStore CompletionRunStore
	Git      GitStateReader
	Logger   *slog.Logger
}

// Complete records completion for the active agent run.
func (s CompletionService) Complete(ctx context.Context, opts CompleteOptions) (CompleteResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.RunStore == nil {
		return CompleteResult{}, errors.New("agent completion run store is required")
	}

	gitState := s.Git
	if gitState == nil {
		gitState = LocalGitState{}
	}

	lockAttrs := s.completionLockAttrs()
	var result CompleteResult
	err := state.WithGlobalMutationLockLogger(ctx, s.Paths, completionLockOperation, s.Logger, func() error {
		completed, err := s.completeLocked(ctx, opts, gitState)
		if err != nil {
			return err
		}
		result = completed
		return nil
	}, lockAttrs...)
	if err != nil {
		return CompleteResult{}, err
	}
	return result, nil
}

func (s CompletionService) completionLockAttrs() []slog.Attr {
	repoID := strings.TrimSpace(s.Resolver.envValue(envRepoID))
	taskID := strings.TrimSpace(s.Resolver.envValue(envTaskID))
	attrs := make([]slog.Attr, 0, 3)
	if repoID != "" {
		attrs = append(attrs, slog.String("repo_id", repoID))
	}
	if taskID != "" {
		attrs = append(attrs, slog.String("task_id", taskID))
	}
	if repoID == "" || taskID == "" {
		return attrs
	}

	loader := s.Resolver.RunStore
	if loader == nil {
		if runStoreLoader, ok := s.RunStore.(ContextStateLoader); ok {
			loader = runStoreLoader
		}
	}
	if loader == nil {
		return attrs
	}

	state, err := loader.Load(repoID, taskID)
	if err != nil {
		return attrs
	}
	if run, ok := taskstate.LatestRun(state); ok {
		attrs = append(attrs, slog.Int("attempt", run.Attempt))
	}
	return attrs
}

func (s CompletionService) completeLocked(
	ctx context.Context,
	opts CompleteOptions,
	gitState GitStateReader,
) (CompleteResult, error) {
	summary := strings.TrimSpace(opts.Summary)
	if summary == "" {
		return CompleteResult{}, errors.New("completion summary is required")
	}
	description := strings.TrimSpace(opts.Description)
	if description == "" {
		return CompleteResult{}, errors.New("completion description is required")
	}
	detailedDescription := opts.DetailedDescription
	if strings.TrimSpace(detailedDescription) == "" {
		return CompleteResult{}, errors.New("completion detailed description is required")
	}

	span := logging.Start(ctx, s.Logger, "agent completion context resolution",
		slog.String("component", "agent"),
		slog.String("operation", "agent_done"),
	)
	activeContext, err := s.Resolver.Resolve(ctx)
	if err != nil {
		span.FinishError(ctx, err)
		return CompleteResult{}, fmt.Errorf("resolve active context: %w", err)
	}
	span.Finish(ctx, logging.StatusSuccess,
		slog.String("repo_id", activeContext.Repository.ID),
		slog.String("task_id", activeContext.Task.ID),
		slog.Int("attempt", activeContext.Run.Attempt),
		slog.String("target_kind", string(activeContext.Target.Kind)),
	)
	if activeContext.Target.Kind != ExecutionTargetMain {
		return s.completeWorktree(ctx, activeContext, summary, description, detailedDescription, gitState)
	}
	return s.completeMain(ctx, activeContext, summary, description, detailedDescription, gitState)
}

func (s CompletionService) completeMain(
	ctx context.Context,
	activeContext ActiveContext,
	summary string,
	description string,
	detailedDescription string,
	gitState GitStateReader,
) (CompleteResult, error) {
	if existing, ok, err := s.existingCompletionResult(activeContext, summary, description, detailedDescription); ok || err != nil {
		return existing, err
	}

	currentBranch, err := gitState.CurrentBranch(ctx, activeContext.Repository.Root)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("inspect current Git branch: %w", err)
	}
	if currentBranch != activeContext.Repository.DefaultBranch {
		return CompleteResult{}, fmt.Errorf(
			"current Git branch is %q, expected registered default branch %q",
			currentBranch,
			activeContext.Repository.DefaultBranch,
		)
	}

	hasChanges, err := gitState.HasWorkingTreeChanges(ctx, activeContext.Repository.Root)
	if err != nil {
		return CompleteResult{}, err
	}
	if !hasChanges {
		return CompleteResult{}, errors.New("working tree has no changes; make implementation changes before running agent done")
	}

	span := s.startCompletionPersistence(ctx, activeContext)
	run, err := s.RunStore.CompleteRun(
		activeContext.Repository.ID,
		activeContext.Task.ID,
		activeContext.Run.Attempt,
		taskstate.CompleteRunOptions{
			Summary:             summary,
			Description:         description,
			DetailedDescription: detailedDescription,
		},
	)
	if err != nil {
		span.FinishError(ctx, err)
		return CompleteResult{}, fmt.Errorf("record completion: %w", err)
	}
	span.Finish(ctx, logging.StatusSuccess)
	return CompleteResult{Context: activeContext, Run: run}, nil
}

func (s CompletionService) completeWorktree(
	ctx context.Context,
	activeContext ActiveContext,
	summary string,
	description string,
	detailedDescription string,
	gitState GitStateReader,
) (CompleteResult, error) {
	if existing, ok, err := s.existingCompletionResult(activeContext, summary, description, detailedDescription); ok || err != nil {
		return existing, err
	}

	currentBranch, err := gitState.CurrentBranch(ctx, activeContext.Target.Path)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("inspect current Git branch: %w", err)
	}
	if currentBranch != activeContext.Target.Branch {
		return CompleteResult{}, fmt.Errorf(
			"current Git branch is %q, expected task branch %q",
			currentBranch,
			activeContext.Target.Branch,
		)
	}

	span := s.startCompletionPersistence(ctx, activeContext)
	run, err := s.RunStore.CompleteRun(
		activeContext.Repository.ID,
		activeContext.Task.ID,
		activeContext.Run.Attempt,
		taskstate.CompleteRunOptions{
			Summary:             summary,
			Description:         description,
			DetailedDescription: detailedDescription,
		},
	)
	if err != nil {
		span.FinishError(ctx, err)
		return CompleteResult{}, fmt.Errorf("record completion: %w", err)
	}
	span.Finish(ctx, logging.StatusSuccess)
	return CompleteResult{Context: activeContext, Run: run}, nil
}

func (s CompletionService) startCompletionPersistence(ctx context.Context, activeContext ActiveContext) logging.Span {
	return logging.Start(ctx, s.Logger, "agent completion persistence",
		slog.String("component", "agent"),
		slog.String("operation", "agent_done"),
		slog.String("repo_id", activeContext.Repository.ID),
		slog.String("task_id", activeContext.Task.ID),
		slog.Int("attempt", activeContext.Run.Attempt),
	)
}

func (s CompletionService) existingCompletionResult(
	activeContext ActiveContext,
	summary string,
	description string,
	detailedDescription string,
) (CompleteResult, bool, error) {
	if activeContext.Run.Completion == nil {
		return CompleteResult{}, false, nil
	}

	diagnostic, err := s.RunStore.RecordRepeatedCompletion(
		activeContext.Repository.ID,
		activeContext.Task.ID,
		activeContext.Run.Attempt,
		taskstate.RepeatedCompletionOptions{
			Summary:             summary,
			Description:         description,
			DetailedDescription: detailedDescription,
		},
	)
	if err != nil {
		return CompleteResult{}, true, fmt.Errorf("record repeated completion diagnostic: %w", err)
	}

	return CompleteResult{
		Context: activeContext,
		Run: taskstate.RunAttempt{
			Attempt:    activeContext.Run.Attempt,
			Status:     taskstate.RunStatusRunning,
			Execution:  activeContext.Run.Execution,
			Completion: activeContext.Run.Completion,
		},
		Repeated:           true,
		RepeatedDiagnostic: &diagnostic,
	}, true, nil
}
