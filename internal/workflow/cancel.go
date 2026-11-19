package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/finalization"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// CancelBackend records cancellation without satisfying blocking dependencies.
type CancelBackend interface {
	task.Getter
	Cancel(ctx context.Context, id, reason string) error
}

// CancelService retires tasks without advancing their delivery workflow.
type CancelService struct {
	Paths state.Paths
	Store interface {
		Load(repoID, taskID string) (taskstate.TaskState, error)
	}
	CleanupGit   finalization.ClosedTaskWorktreeGit
	ProcessProbe phaseexec.ProcessProbe
	Logger       *slog.Logger
}

// CancelResult reports the source outcome and any separate worktree cleanup.
type CancelResult struct {
	Changed          bool
	AlreadyCancelled bool
	ManualCleanup    bool
	Cleanup          finalization.WorktreeCleanupResult
}

// Cancel records the terminal source outcome before attempting native cleanup.
// Confirmation authorizes cleanup of only the disclosed dedicated worktree.
func (s CancelService) Cancel(ctx context.Context, repo task.Repository, backend CancelBackend, id, reason string, confirm func(string) (bool, error)) (CancelResult, error) {
	var result CancelResult
	item, err := backend.Get(ctx, id)
	if err != nil {
		return result, err
	}
	if item.ID != id || item.IssueType != task.IssueTypeTask {
		return result, fmt.Errorf("%s is not an ordinary task", id)
	}
	if item.Status == task.StatusCancelled {
		result.AlreadyCancelled = true
		return result, nil
	}
	if item.Status == task.StatusClosed {
		return result, errors.New("task is already closed")
	}
	snapshot, err := s.Store.Load(repo.ID, id)
	if err != nil {
		return result, err
	}
	if err := s.checkCancellation(item, snapshot.Work); err != nil {
		return result, err
	}
	directory, err := cancellationWorktree(s.Paths, repo, item, snapshot)
	if err != nil {
		return result, err
	}
	if directory != "" {
		if confirm == nil {
			return result, errors.New("dedicated worktree requires confirmation")
		}
		ok, err := confirm(directory)
		if err != nil || !ok {
			return result, err
		}
	}

	err = state.WithGlobalMutationLockLogger(ctx, s.Paths, "cancel task", s.Logger, func() error {
		if err := backend.Cancel(ctx, id, strings.TrimSpace(reason)); err != nil {
			return err
		}
		result.Changed = true
		if directory == "" {
			return nil
		}
		_, recorded := taskstate.GitFactsFor(snapshot)
		result.ManualCleanup = !recorded
		result.Cleanup = s.cleanupCancelledTask(ctx, repo, backend, id, directory, snapshot)
		return nil
	})
	return result, err
}

func (s CancelService) cleanupCancelledTask(ctx context.Context, repo task.Repository, backend CancelBackend, id, directory string, snapshot taskstate.TaskState) finalization.WorktreeCleanupResult {
	confirmed, err := backend.Get(ctx, id)
	if err != nil || confirmed.ID != id || confirmed.Status != task.StatusCancelled {
		reason := "task source did not verify cancellation after the write"
		if err != nil {
			reason += ": " + err.Error()
		}
		return finalization.WorktreeCleanupResult{Outcome: finalization.WorktreeCleanupUnsafe, Worktree: directory, Reason: reason}
	}
	result := finalization.CleanClosedTaskWorktree(ctx, finalization.ClosedTaskWorktreeCleanupOptions{
		Paths: s.Paths, Repository: repo, Task: confirmed, TaskState: snapshot, Git: s.CleanupGit, Fix: true,
	})
	if result.Worktree == "" {
		result.Worktree = directory
	}
	return result
}

func cancellationWorktree(paths state.Paths, repo task.Repository, item task.Task, snapshot taskstate.TaskState) (string, error) {
	facts, _ := taskstate.GitFactsFor(snapshot)
	directory, _ := taskstate.WorkDirectoryFor(snapshot)
	for _, path := range []string{facts.Worktree, directory.Path, item.OrpheusMetadata().Worktree} {
		if path = strings.TrimSpace(path); path != "" && filepath.Clean(path) != filepath.Clean(repo.Path) {
			return path, nil
		}
	}
	// Preparation can be interrupted after creating the native checkout but
	// before recording its identity. Disclose it, but never remove it without
	// the same ownership evidence used for finalization.
	expected, err := gitmeta.ExpectedTaskWorktree(gitmeta.TaskWorktreeOptions{
		RepoID: repo.ID, RepoName: repo.Name, RepoPath: repo.Path, DefaultBranch: repo.DefaultBranch, TaskID: item.ID, Paths: paths,
	})
	if err != nil {
		return "", err
	}
	_, err = os.Lstat(expected.WorktreePath)
	switch {
	case err == nil:
		return expected.WorktreePath, nil
	case errors.Is(err, os.ErrNotExist):
		return "", nil
	default:
		return "", fmt.Errorf("inspect %s: %w", expected.WorktreePath, err)
	}
}

func (s CancelService) checkCancellation(item task.Task, snapshot *work.State) error {
	metadataURL := strings.TrimSpace(item.OrpheusMetadata().PRURL)
	closedURL := false
	if snapshot != nil {
		for _, w := range snapshot.Workflows {
			observed := make(map[int]work.PRState)
			for _, phase := range w.Phases {
				if external, ok := phase.(*work.ExternalReviewPhase); ok && external.Outcome != nil {
					observed[external.Publication.Phase] = external.Outcome.State
				}
			}
			for i, phase := range w.Phases {
				if err := s.checkCancellationExecution(phase); err != nil {
					return err
				}
				if supersededPackaging(w.Phases, i) {
					continue
				}
				url, err := cancellationPublication(phase, observed[i])
				if err != nil {
					return err
				}
				closedURL = closedURL || (url != "" && metadataURL == url)
			}
		}
	}
	if metadataURL != "" && !closedURL {
		return fmt.Errorf("PR %s has no verified closed-unmerged outcome; run `orpheus task sync`", metadataURL)
	}
	return nil
}

// Manual repair appends a replacement plan immediately after the failed
// packaging phase, with the same review. Without recorded public output the
// earlier operation could not reach a push, PR creation, or direct integration.
func supersededPackaging(phases []work.Phase, index int) bool {
	p, ok := phases[index].(*work.PublicationPhase)
	if !ok || p.Operation == nil || p.Operation.Public != nil || p.Delivery != nil || index+1 == len(phases) {
		return false
	}
	next, ok := phases[index+1].(*work.PublicationPhase)
	return ok && next.Review == p.Review
}

// cancellationPublication returns the PR URL only when closed without delivery.
func cancellationPublication(phase work.Phase, outcome work.PRState) (string, error) {
	if _, finalizing := phase.(*work.FinalizationPhase); finalizing {
		return "", errors.New("delivery must follow normal finalization; run `orpheus task sync`")
	}
	p, ok := phase.(*work.PublicationPhase)
	if !ok {
		return "", nil
	}
	switch delivery := p.Delivery.(type) {
	case *work.DirectDelivery:
		return "", errors.New("delivery must follow normal finalization; run `orpheus task sync`")
	case *work.PullRequestDelivery:
		switch outcome {
		case work.PRClosedUnmerged:
			return delivery.PullRequest.URL, nil
		case work.PRMerged:
			return "", errors.New("merged PR must follow normal finalization; run `orpheus task sync`")
		default:
			return "", fmt.Errorf("PR %s may still be open; run `orpheus task sync` to record its outcome", delivery.PullRequest.URL)
		}
	case nil:
		if p.Operation != nil || p.DirectSync != nil || p.Integration != nil {
			return "", errors.New("unresolved publication; reconcile it before cancelling")
		}
	}
	return "", nil
}

func (s CancelService) checkCancellationExecution(phase work.Phase) error {
	switch p := phase.(type) {
	case *work.ImplementationPhase:
		return cancellationExecution("implementation", &p.Execution, s.ProcessProbe)
	case *work.ReviewPhase:
		for _, step := range p.Steps {
			if err := cancellationReviewExecution(step, p.Abort != nil, s.ProcessProbe); err != nil {
				return err
			}
		}
	case *work.PublicationPhase:
		if p.DirectSync != nil {
			return cancellationExecution("sync resolver", p.DirectSync.Execution, s.ProcessProbe)
		}
	case *work.ExternalReviewPhase:
		for _, sync := range p.BranchSyncs {
			if sync.ConflictResolution != nil {
				if err := cancellationExecution("sync resolver", &sync.ConflictResolution.Execution, s.ProcessProbe); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func cancellationReviewExecution(step work.ReviewStep, aborted bool, probe phaseexec.ProcessProbe) error {
	switch step := step.(type) {
	case *work.AgentReviewStep:
		return cancellationExecution("review", &step.Execution, probe)
	case *work.CheckStep:
		if step.Result == nil && step.StartFailure == nil {
			return errors.New("review check execution is unverifiable")
		}
	case *work.ManualReviewStep:
		if step.InspectionPending != nil {
			if *step.InspectionPending {
				return errors.New("manual review command execution is unverifiable")
			}
			return nil
		}
		if step.CommandResult == nil && step.Decision == nil && len(step.CommandInterruptions) == 0 && !aborted {
			return errors.New("manual review command execution is unverifiable")
		}
	}
	return nil
}

func cancellationExecution(kind string, execution *work.AgentExecution, probe phaseexec.ProcessProbe) error {
	if execution == nil {
		return nil
	}
	if _, running := execution.Process.(*work.RunningProcess); !running {
		return nil
	}
	inspection := phaseexec.InspectAttachedExecution(taskstate.AgentExecution{SupervisorPID: execution.SupervisorPID, ChildPID: execution.ChildPID}, probe)
	if inspection.Condition == phaseexec.AttachedExecutionRecoverable {
		return nil
	}
	return fmt.Errorf("%s execution is %s (%s); inspect `orpheus doctor` before retrying", kind, inspection.Condition, inspection.Reason)
}
