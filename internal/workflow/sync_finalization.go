package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hea3ven/orpheus/internal/task"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
)

func (s SyncService) workflowStore() WorkStore {
	if s.Store != nil {
		return s.Store
	}
	return workstate.NewStore(s.Paths)
}

func saveSyncSnapshot(store WorkStore, snapshot *work.State) error {
	version, err := store.Save(*snapshot, snapshot.Version)
	if err == nil {
		snapshot.Version = version
	}
	return err
}

// consumePROutcome advances only the controller-selected external review. The
// terminal observation is already durable before Finalization is reserved.
func (s SyncService) consumePROutcome(ctx context.Context, target syncTarget, snapshot *work.State) (SyncResult, error) {
	w := &snapshot.Workflows[len(snapshot.Workflows)-1]
	external := w.LastPhase().(*work.ExternalReviewPhase)
	delivery := w.Phases[external.Publication.Phase].(*work.PublicationPhase).Delivery.(*work.PullRequestDelivery)
	if external.Outcome.State == work.PRClosedUnmerged {
		return SyncResult{}, fmt.Errorf("task %s PR %s is closed without merge; operator direction is required; no backend state was changed", target.task.ID, delivery.PullRequest.URL)
	}
	w.Phases = append(w.Phases, &work.FinalizationPhase{Acceptance: &work.PRAcceptance{ExternalReview: len(w.Phases) - 1, ObservedAt: external.Outcome.ObservedAt, Evidence: external.Outcome.Evidence}})
	if err := saveSyncSnapshot(s.workflowStore(), snapshot); err != nil {
		return SyncResult{}, err
	}
	return s.finalizeAcceptedTask(ctx, target, snapshot)
}

func (s SyncService) finalizeAcceptedTask(ctx context.Context, target syncTarget, snapshot *work.State) (SyncResult, error) {
	w := &snapshot.Workflows[len(snapshot.Workflows)-1]
	view, err := taskstate.Project(*snapshot)
	if err != nil {
		return SyncResult{}, err
	}
	if !finalizationPending(view, target.source.Repository.Path) {
		return s.skip(target, taskstate.RunAttempt{}, "task is closed"), nil
	}
	result := s.skip(target, taskstate.RunAttempt{}, "")
	result.Status = SyncStatusFinalized
	result.Reason = "delivery accepted; backend task was closed"
	if accepted, ok := w.LastPhase().(*work.FinalizationPhase).Acceptance.(*work.PRAcceptance); ok {
		external := w.Phases[accepted.ExternalReview].(*work.ExternalReviewPhase)
		result.PRURL = w.Phases[external.Publication.Phase].(*work.PublicationPhase).Delivery.(*work.PullRequestDelivery).PullRequest.URL
		result.Status = SyncStatusPRMerged
		result.Reason = "PR is merged; backend task was closed"
	}
	if err := s.notify(target, ""); err != nil {
		return result, err
	}
	if err := s.closeAcceptedTask(ctx, s.workflowStore(), snapshot, w, target.backend, target.task); err != nil {
		return SyncResult{}, fmt.Errorf("finalize task %s source closure: %w", target.task.ID, err)
	}
	observed, err := target.backend.Get(ctx, target.task.ID)
	if err != nil {
		return SyncResult{}, err
	}
	result.Task = observed
	result.WorktreeCleanup = s.cleanupFinalizedWorktree(ctx, s.workflowStore(), target.source.Repository, observed, target.backend, w.Context().Directory)
	return result, nil
}

func finalizationPending(view taskstate.TaskState, repoPath string) bool {
	if view.Work == nil || len(view.Work.Workflows) == 0 {
		return false
	}
	w := &view.Work.Workflows[len(view.Work.Workflows)-1]
	if publication, ok := w.LastPhase().(*work.PublicationPhase); ok {
		_, delivered := publication.Delivery.(*work.DirectDelivery)
		return delivered
	}
	final, ok := w.LastPhase().(*work.FinalizationPhase)
	if !ok {
		return false
	}
	if final.Closure == nil {
		return true
	}
	if w.Context().Directory == repoPath {
		return false
	}
	for _, observation := range final.Cleanup {
		if _, removed := observation.(*work.WorktreeRemoved); removed {
			return false
		}
	}
	return true
}

func (s SyncService) closeAcceptedTask(ctx context.Context, store WorkStore, snapshot *work.State, w *work.Workflow, backend task.SyncBackend, item task.Task) error {
	final, ok := w.LastPhase().(*work.FinalizationPhase)
	if !ok {
		return errors.New("closure handler requires a controller-selected finalization phase")
	}
	if final.Closure != nil {
		return nil
	}
	if final.ClosureIntentAt == nil {
		now := time.Now().UTC()
		final.ClosureIntentAt = &now
		version, err := store.Save(*snapshot, snapshot.Version)
		if err != nil {
			return fmt.Errorf("record source closure intent: %w", err)
		}
		snapshot.Version = version
	}
	// Read the authoritative lifecycle before every attempt, including retries
	// after ambiguous responses or a failed local closure receipt.
	current, err := backend.Get(ctx, item.ID)
	if err != nil {
		return err
	}
	if current.Status != task.StatusClosed {
		if err := backend.Close(ctx, item.ID); err != nil {
			return err
		}
	}
	observed, err := backend.Get(ctx, item.ID)
	if err != nil {
		return err
	}
	if observed.Status != task.StatusClosed {
		return errors.New("task-source closure was not independently observed")
	}
	reason := taskstate.CloseReasonPRMerged
	if _, direct := final.Acceptance.(*work.DirectAcceptance); direct {
		reason = "destination_published"
	}
	final.Closure = &work.TaskClosure{At: time.Now().UTC(), Reason: reason, Evidence: "Task source independently reports closed after recorded delivery acceptance"}
	version, err := store.Save(*snapshot, snapshot.Version)
	if err != nil {
		return err
	}
	snapshot.Version = version
	return nil
}
