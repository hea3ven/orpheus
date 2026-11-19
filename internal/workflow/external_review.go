package workflow

import (
	"context"
	"fmt"

	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/externalreview"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

func (s SyncService) advanceExternalReview(ctx context.Context, target syncTarget, policy SyncBranchUpdatePolicy, current work.State) (SyncResult, error) {
	runner := phaseexec.Runner{Store: s.Store, Paths: s.Paths, RepositoryID: target.source.Repository.ID, TaskID: target.task.ID, Logger: s.Logger}
	phase := externalreview.Service{Paths: s.Paths, Repository: target.source.Repository, Task: target.task, Git: s.Git, PRProvider: s.PRProvider, ConflictResolver: s.ConflictResolver, ProcessProbe: s.ProcessProbe, Checkpoint: runner.CommitHeld, Logger: s.Logger, Observed: func(prState pullrequest.State) error { return s.notify(target, prState) }}
	for {
		selected, err := phase.Next(current, externalreview.Intent{UpdatePolicy: gitBranchUpdatePolicy(policy), RecoveryOnly: target.task.Status == task.StatusClosed})
		if err != nil {
			return SyncResult{}, err
		}
		if op, ok := selected.Operation(); ok {
			current, err = runner.ExecuteHeld(ctx, current, op)
			if err != nil {
				return SyncResult{}, err
			}
			continue
		}
		outcome, _ := selected.Outcome()
		switch outcome := outcome.(type) {
		case externalreview.Reconciled:
			return s.skip(target, taskstate.RunAttempt{}, "task is closed"), nil
		case externalreview.Open:
			result := s.skip(target, taskstate.RunAttempt{}, outcome.Reason)
			result.Status = SyncStatusAlreadyInReview
			if outcome.Updated {
				result.Status = SyncStatusBranchUpdated
			}
			result.PRURL = prURLFromTask(target.task)
			if outcome.PRURL != "" {
				result.PRURL = outcome.PRURL
			}
			return result, nil
		case externalreview.Merged:
			if err := s.notify(target, pullrequest.StateMerged); err != nil {
				return SyncResult{}, err
			}
			return s.consumePROutcome(ctx, target, &current)
		case externalreview.ClosedUnmerged:
			if err := s.notify(target, pullrequest.StateClosed); err != nil {
				return SyncResult{}, err
			}
			return SyncResult{}, fmt.Errorf("task %s PR %s is closed without merge; operator direction is required; no backend state was changed", target.task.ID, outcome.PRURL)
		default:
			return SyncResult{}, fmt.Errorf("unsupported external review outcome %T", outcome)
		}
	}
}
