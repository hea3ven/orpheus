package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// BlockBackend supplies native task-source blocking and restoration.
type BlockBackend interface {
	task.Getter
	task.BlockMutator
}

// BlockStateReader reads execution and review history without changing it.
type BlockStateReader interface {
	Load(repoID, taskID string) (taskstate.TaskState, error)
}

// BlockService applies blocking safety policy. The caller holds the managed
// mutation lock across inspection and the source write. Child executions are
// deliberately not inspected or stopped when blocking their parent epic.
type BlockService struct {
	Store BlockStateReader
	Probe ProcessProbe
}

// SetBlocked blocks or explicitly unblocks an item without advancing workflow.
// An already satisfied request is a no-op, including after native reopening.
func (s BlockService) SetBlocked(ctx context.Context, backend BlockBackend, repoID, id string, blocked bool) (bool, error) {
	if backend == nil || s.Store == nil {
		return false, errors.New("blocking requires a task source and execution history reader")
	}
	item, err := backend.Get(ctx, id)
	if err != nil {
		return false, fmt.Errorf("cannot inspect item %s; use `orpheus task show %s` before retrying: %w", id, id, err)
	}
	if err := task.ValidateTaskSourceItem(item); err != nil {
		return false, err
	}
	if blocked {
		if err := task.ValidateBlock(item); err != nil {
			return false, err
		}
		if item.Status == task.StatusBlocked {
			return false, nil
		}
	} else if item.Status != task.StatusBlocked {
		return false, nil
	}
	local, err := s.Store.Load(repoID, id)
	if err != nil {
		return false, fmt.Errorf("cannot verify execution history; run `orpheus doctor` before retrying: %w", err)
	}
	if err := s.verifyInactive(item, local); err != nil {
		return false, err
	}
	if blocked {
		err = backend.Block(ctx, id)
	} else {
		err = backend.Unblock(ctx, id)
	}
	if err != nil {
		return false, fmt.Errorf("cannot change blocking for %s; inspect `orpheus task show %s` before retrying: %w", id, id, err)
	}
	return true, nil
}

func (s BlockService) verifyInactive(item task.Task, local taskstate.TaskState) error {
	if strings.TrimSpace(item.OrpheusMetadata().PRURL) != "" || local.Finalization != nil || local.ActiveSyncConflict != nil {
		return fmt.Errorf("item %s is at a publication boundary; continue its existing workflow with `orpheus task run %s` or `orpheus task sync %s`", item.ID, item.ID, item.ID)
	}
	if err := s.verifyExecutions(item.ID, local); err != nil {
		return err
	}
	if run, ok := taskstate.LatestRun(local); ok && run.Completion != nil {
		return blockReviewBoundary(item.ID)
	}
	// Historical reviews may remain on an unfinished repair, but a review
	// still waiting for an operator decision is never safely inactive work.
	if review, ok := taskstate.LatestReview(local); ok {
		run, hasRun := taskstate.LatestRun(local)
		if review.Status != taskstate.ReviewStatusBlocked && review.Status != taskstate.ReviewStatusFailed && review.Status != taskstate.ReviewStatusAborted {
			return blockReviewBoundary(item.ID)
		}
		if !hasRun || run.ReviewFollowUp == nil || run.ReviewFollowUp.ReviewAttempt != review.Attempt {
			return blockReviewBoundary(item.ID)
		}
	}
	if len(local.Runs) == 0 && (strings.TrimSpace(item.OrpheusMetadata().Branch) != "" || strings.TrimSpace(item.OrpheusMetadata().Worktree) != "") {
		return fmt.Errorf("item %s has execution pointers but no run history; inspect `orpheus doctor` before retrying", item.ID)
	}
	return nil
}

// Inspect all recorded running executions, not just the latest attempt.
// Proven absent executions are safe without reconciling or rewriting history.
func (s BlockService) verifyExecutions(id string, local taskstate.TaskState) error {
	for _, run := range local.Runs {
		if run.Status == taskstate.RunStatusRunning || run.Execution.Status == taskstate.RunStatusRunning {
			if err := s.verifyExecution(id, run.Execution); err != nil {
				return err
			}
		}
	}
	for _, review := range local.Reviews {
		for _, step := range review.Steps {
			if step.Execution != nil && step.Execution.Status == taskstate.RunStatusRunning {
				if err := s.verifyExecution(id, *step.Execution); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s BlockService) verifyExecution(id string, execution taskstate.AgentExecution) error {
	inspection := InspectAttachedExecution(execution, s.Probe)
	if inspection.Condition == AttachedExecutionRecoverable {
		return nil
	}
	return fmt.Errorf("item %s has an active or unverifiable execution (%s); wait for it to finish or inspect `orpheus doctor` before retrying", id, inspection.Reason)
}

func blockReviewBoundary(id string) error {
	return fmt.Errorf("item %s is at a review boundary; continue review or repair with `orpheus task run %s` instead of changing blocking", id, id)
}
