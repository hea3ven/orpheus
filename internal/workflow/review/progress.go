package review

import "fmt"

// Notice identifies a semantic stopping boundary; frontends supply prose.
type Notice string

const (
	NoticeManualPending       Notice = "manual_pending"
	NoticeBlocked             Notice = "blocked"
	NoticeDecisionPaused      Notice = "decision_paused"
	NoticeDecisionInterrupted Notice = "decision_interrupted"
)

func reportNotice(opts PipelineRunOptions, notice Notice, step Step) error {
	if opts.Report != nil {
		if err := opts.Report(notice, step); err != nil {
			return fmt.Errorf("%w: %w", ErrPresentationUnavailable, err)
		}
	}
	return nil
}
