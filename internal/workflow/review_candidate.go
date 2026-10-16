package workflow

import (
	"context"
	"fmt"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// ReviewCandidateGit supplies Git observations for task-review admission.
// Admission policy and task-state reads belong to the workflow.
type ReviewCandidateGit interface {
	HasStagedChanges(context.Context, string) (bool, error)
	HasCandidateChanges(context.Context, string) (bool, error)
	ShortStatus(context.Context, string) (string, error)
}

// LocalReviewCandidateGit observes the candidate in the local checkout.
type LocalReviewCandidateGit struct{}

// HasStagedChanges reports whether the Git index contains staged changes.
func (LocalReviewCandidateGit) HasStagedChanges(ctx context.Context, workdir string) (bool, error) {
	return gitmeta.HasStagedChanges(ctx, workdir)
}

// HasCandidateChanges reports whether the checkout contains candidate changes.
func (LocalReviewCandidateGit) HasCandidateChanges(ctx context.Context, workdir string) (bool, error) {
	return review.HasCandidateChanges(ctx, workdir)
}

// ShortStatus returns Git status for admission diagnostics.
func (LocalReviewCandidateGit) ShortStatus(ctx context.Context, workdir string) (string, error) {
	return gitmeta.ShortStatus(ctx, workdir)
}

func (s ReviewLifecycleService) inspectReviewCandidate(ctx context.Context, reviewCtx ReviewContext, workdir string) error {
	git := s.CandidateGit
	if git == nil {
		git = LocalReviewCandidateGit{}
	}
	hasStagedChanges, err := git.HasStagedChanges(ctx, workdir)
	if err != nil {
		return err
	}
	if hasStagedChanges {
		status, statusErr := git.ShortStatus(ctx, workdir)
		if statusErr != nil {
			status = "unable to read git status: " + statusErr.Error()
		}
		return fmt.Errorf("review requires a clean Git index, but staged changes are present in %q; unstage them, then rerun `orpheus task run <task-id>`\n%s", workdir, strings.TrimSpace(status))
	}
	hasCandidate, err := git.HasCandidateChanges(ctx, workdir)
	if err != nil {
		return err
	}
	if hasCandidate {
		return nil
	}
	taskState, err := s.RunStore.Load(reviewCtx.RepoID(), reviewCtx.TaskID())
	if err != nil {
		return fmt.Errorf("load task state: %w", err)
	}
	if strings.TrimSpace(taskstate.FinalizationFacts(taskState).Commit) != "" {
		return nil
	}
	return fmt.Errorf("worktree %q has no candidate changes to review and task has no recorded finalization commit", workdir)
}
