package review

import (
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

func pipelineReview(opts PipelineRunOptions, owner work.AttemptRef) (taskstate.ReviewAttempt, error) {
	if _, _, err := currentReview(opts.current, owner); err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	projected, err := taskstate.Project(*opts.current)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	latest, _ := taskstate.LatestReview(projected)
	return latest, nil
}
