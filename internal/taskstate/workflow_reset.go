package taskstate

import (
	"errors"
	"github.com/hea3ven/orpheus/internal/work"
)

// ScheduleReset appends empty work; only the confirmed controller operation calls it.
func (s Store) ScheduleReset(repoID, taskID string) error {
	_, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		if len(snapshot.Workflows) == 0 || snapshot.Workflows[len(snapshot.Workflows)-1].Context() == nil && snapshot.ResetContext() == nil {
			return false, errors.New("workflow reset requires an established task checkout")
		}
		at := s.nowUTC()
		snapshot.Workflows = append(snapshot.Workflows, work.Workflow{ResetRequestedAt: &at})
		return true, nil
	})
	return err
}

// RecordResetTarget persists the selected baseline before destructive effects.
func (s Store) RecordResetTarget(repoID, taskID string, target work.WorkContext) error {
	_, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		w := snapshot.ActiveWorkflow()
		if w == nil || w.ResetRequestedAt == nil || len(w.Phases) != 0 {
			return false, errors.New("reset target requires pending, unresolved Preparation")
		}
		w.Phases = append(w.Phases, &work.PreparationPhase{ResetTarget: &target})
		return true, nil
	})
	return err
}
