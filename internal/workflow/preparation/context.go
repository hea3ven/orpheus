package preparation

import (
	"errors"
	"reflect"
	"time"

	"github.com/hea3ven/orpheus/internal/work"
)

// establish consumes reset authorization only after the exact selected context
// has been observed. It changes only the supplied candidate.
func establish(snapshot *work.State, context *work.WorkContext, at time.Time) (bool, error) {
	if context == nil {
		return false, errors.New("preparation requires observed baseline commit, tree and owned retention ref")
	}
	if len(snapshot.Workflows) == 0 {
		snapshot.Workflows = append(snapshot.Workflows, work.Workflow{})
	}
	w := snapshot.ActiveWorkflow()
	if w == nil {
		return false, errors.New("closed work requires an explicit workflow reset")
	}
	if established := w.Context(); established != nil {
		if !reflect.DeepEqual(*established, *context) {
			return false, errors.New("observed preparation differs from this workflow's immutable context")
		}
		return false, nil
	}
	copyOf := *context
	if p, ok := w.LastPhase().(*work.PreparationPhase); ok && p.ResetTarget != nil {
		if !reflect.DeepEqual(*p.ResetTarget, copyOf) {
			return false, errors.New("preparation differs from resolved reset target")
		}
		p.Context, p.EstablishedAt = &copyOf, &at
	} else {
		w.Phases = append(w.Phases, &work.PreparationPhase{Context: &copyOf, EstablishedAt: &at})
	}
	w.ResetRequestedAt = nil
	return true, nil
}
