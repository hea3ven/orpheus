package workflow

import (
	"context"
	"fmt"

	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"

	"github.com/hea3ven/orpheus/internal/workflow/finalization"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
)

func (s SyncService) workflowStore() phaseexec.Store {
	if s.Store != nil {
		return s.Store
	}
	return workstate.NewStore(s.Paths)
}

func saveSyncSnapshot(store phaseexec.Store, snapshot *work.State) error {
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
	if !finalization.Pending(*snapshot, target.source.Repository.Path) {
		return s.skip(target, taskstate.RunAttempt{}, "task is closed"), nil
	}
	result := s.skip(target, taskstate.RunAttempt{}, "")
	if err := s.notify(target, ""); err != nil {
		return result, err
	}
	phase := finalization.Service{Paths: s.Paths, Repository: target.source.Repository, Task: target.task, Backend: target.backend, Git: s.CleanupGit}
	runner := phaseexec.Runner{Store: s.workflowStore(), Paths: s.Paths, RepositoryID: target.source.Repository.ID, TaskID: target.task.ID, Logger: s.Logger}
	current, outcome, err := phase.Run(ctx, *snapshot, runner.ExecuteHeld)
	*snapshot = current
	if err != nil {
		return SyncResult{}, err
	}
	result.Task, result.WorktreeCleanup = outcome.Task, outcome.Cleanup
	result.Status, result.Reason = SyncStatusFinalized, "delivery accepted; backend task was closed"
	if outcome.PRURL != "" {
		result.PRURL = outcome.PRURL
		result.Status, result.Reason = SyncStatusPRMerged, "PR is merged; backend task was closed"
	}
	return result, nil
}
