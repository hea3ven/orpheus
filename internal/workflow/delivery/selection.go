package delivery

import (
	"errors"
	"fmt"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/work"
)

type committedPublication struct {
	workflowIndex, phaseIndex int
	workflow                  *work.Workflow
	publication               *work.PublicationPhase
	prepared                  *work.WorkContext
	output                    *work.CodeOutput
	destination               *work.Destination
}

func selectCommittedPublication(snapshot *work.State, item task.Task) (committedPublication, error) {
	if err := snapshot.Validate(); err != nil {
		return committedPublication{}, err
	}
	if len(snapshot.Workflows) == 0 {
		return committedPublication{}, ErrConsentRequired
	}
	wi := len(snapshot.Workflows) - 1
	w := &snapshot.Workflows[wi]
	pi, err := publicationPhaseIndex(w)
	if err != nil {
		return committedPublication{}, err
	}
	p := w.Phases[pi].(*work.PublicationPhase)
	if p.Consent == nil {
		return committedPublication{}, ErrConsentRequired
	}
	review := w.Phases[p.Review.Phase].(*work.ReviewPhase)
	latest, exists := w.LatestOutput()
	if !exists || latest != review.Scope.RevisionRef(p.Review.Phase) {
		return committedPublication{}, errors.New("newer local output requires its own review and publication authorization")
	}
	prepared := w.Context()
	metadata := item.OrpheusMetadata()
	if !metadata.HasBranch || !metadata.HasWorktree || metadata.Branch != prepared.TaskBranch || metadata.Worktree != prepared.Directory {
		return committedPublication{}, errors.New("task-source execution target differs from the authorized workflow")
	}
	if item.Status != task.StatusInProgress {
		return committedPublication{}, fmt.Errorf("task %s is %s, expected in_progress for publication", item.ID, item.Status)
	}
	output := &p.Input.Code
	destination := &p.Destination
	if destination.Branch == prepared.TaskBranch {
		return committedPublication{}, errors.New("publication destination cannot be the task branch")
	}

	return committedPublication{workflowIndex: wi, phaseIndex: pi, workflow: w, publication: p, prepared: prepared, output: output, destination: destination}, nil
}
