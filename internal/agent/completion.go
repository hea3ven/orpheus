package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

const completionLockOperation = "agent completion"

// CompleteOptions describes the agent-authored completion payload.
type CompleteOptions struct {
	Summary string
	Details string
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

	var result CompleteResult
	err := state.WithGlobalMutationLock(s.Paths, completionLockOperation, func() error {
		completed, err := s.completeLocked(ctx, opts, gitState)
		if err != nil {
			return err
		}
		result = completed
		return nil
	})
	if err != nil {
		return CompleteResult{}, err
	}
	return result, nil
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
	details := strings.TrimSpace(opts.Details)
	if details == "" {
		return CompleteResult{}, errors.New("completion details are required")
	}

	activeContext, err := s.Resolver.Resolve(ctx)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("resolve active context: %w", err)
	}
	if activeContext.Target.Kind != ExecutionTargetMain {
		return s.completeWorktree(ctx, activeContext, summary, details, gitState)
	}
	return s.completeMain(ctx, activeContext, summary, details, gitState)
}

func (s CompletionService) completeMain(
	ctx context.Context,
	activeContext ActiveContext,
	summary string,
	details string,
	gitState GitStateReader,
) (CompleteResult, error) {
	if existing, ok, err := s.existingCompletionResult(activeContext, summary, details); ok || err != nil {
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

	run, err := s.RunStore.CompleteRun(
		activeContext.Repository.ID,
		activeContext.Task.ID,
		activeContext.Run.Attempt,
		taskstate.CompleteRunOptions{
			Summary: summary,
			Details: details,
		},
	)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("record completion: %w", err)
	}
	return CompleteResult{Context: activeContext, Run: run}, nil
}

func (s CompletionService) completeWorktree(
	ctx context.Context,
	activeContext ActiveContext,
	summary string,
	details string,
	gitState GitStateReader,
) (CompleteResult, error) {
	if existing, ok, err := s.existingCompletionResult(activeContext, summary, details); ok || err != nil {
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

	hasChanges, err := gitState.HasWorkingTreeChanges(ctx, activeContext.Target.Path)
	if err != nil {
		return CompleteResult{}, err
	}
	if !hasChanges {
		return CompleteResult{}, errors.New("working tree has no changes; make implementation changes before running agent done")
	}

	run, err := s.RunStore.CompleteRun(
		activeContext.Repository.ID,
		activeContext.Task.ID,
		activeContext.Run.Attempt,
		taskstate.CompleteRunOptions{
			Summary: summary,
			Details: details,
		},
	)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("record completion: %w", err)
	}

	if err := gitState.StageAll(ctx, activeContext.Target.Path); err != nil {
		run = s.recordCommitError(activeContext, summary, details, run, err)
		return CompleteResult{Context: activeContext, Run: run, CommitError: err}, nil
	}
	message := summary + "\n\n" + details
	commit, err := gitState.Commit(ctx, activeContext.Target.Path, message)
	if err != nil {
		run = s.recordCommitError(activeContext, summary, details, run, err)
		return CompleteResult{Context: activeContext, Run: run, CommitError: err}, nil
	}

	run, err = s.RunStore.CompleteRun(
		activeContext.Repository.ID,
		activeContext.Task.ID,
		activeContext.Run.Attempt,
		taskstate.CompleteRunOptions{
			Summary: summary,
			Details: details,
			Commit:  commit,
		},
	)
	if err != nil {
		return CompleteResult{}, fmt.Errorf("record completion commit: %w", err)
	}
	return CompleteResult{Context: activeContext, Run: run}, nil
}

func (s CompletionService) recordCommitError(
	activeContext ActiveContext,
	summary string,
	details string,
	run taskstate.RunAttempt,
	commitErr error,
) taskstate.RunAttempt {
	updated, updateErr := s.RunStore.CompleteRun(
		activeContext.Repository.ID,
		activeContext.Task.ID,
		activeContext.Run.Attempt,
		taskstate.CompleteRunOptions{
			Summary:     summary,
			Details:     details,
			CommitError: commitErr.Error(),
		},
	)
	if updateErr == nil {
		return updated
	}
	return run
}

func (s CompletionService) existingCompletionResult(
	activeContext ActiveContext,
	summary string,
	details string,
) (CompleteResult, bool, error) {
	if activeContext.Run.Completion == nil {
		return CompleteResult{}, false, nil
	}

	diagnostic, err := s.RunStore.RecordRepeatedCompletion(
		activeContext.Repository.ID,
		activeContext.Task.ID,
		activeContext.Run.Attempt,
		taskstate.RepeatedCompletionOptions{
			Summary: summary,
			Details: details,
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
			Agent:      activeContext.Run.Agent,
			Branch:     activeContext.Target.Branch,
			Worktree:   activeContext.Target.Path,
			Completion: activeContext.Run.Completion,
		},
		Repeated:           true,
		RepeatedDiagnostic: &diagnostic,
	}, true, nil
}
