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
	Context ActiveContext
	Run     taskstate.RunAttempt
}

// CompletionRunStore persists completion facts on a run attempt.
type CompletionRunStore interface {
	CompleteRun(repoID, taskID string, attempt int, opts taskstate.CompleteRunOptions) (taskstate.RunAttempt, error)
}

// GitStateReader provides the read-only Git checks needed before completion.
type GitStateReader interface {
	CurrentBranch(ctx context.Context, dir string) (string, error)
	HasWorkingTreeChanges(ctx context.Context, dir string) (bool, error)
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

// CompletionService validates and records agent completion for supported targets.
type CompletionService struct {
	Paths    state.Paths
	Resolver ActiveContextResolver
	RunStore CompletionRunStore
	Git      GitStateReader
}

// Complete records completion for a main/solo agent run without mutating Git.
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
		return CompleteResult{}, fmt.Errorf(
			"agent done supports main/solo runs only; active target is %s",
			activeContext.Target.Kind.DisplayName(),
		)
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
