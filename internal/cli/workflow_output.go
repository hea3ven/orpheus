package cli

import (
	"fmt"

	"github.com/hea3ven/orpheus/internal/review"
	"github.com/spf13/cobra"
)

func reviewOutputFactory(mode taskReviewOutputModeResult) func(bool) review.StepOutput {
	return func(rolling bool) review.StepOutput {
		output := review.StepOutput{Stdout: mode.stdout, Stderr: mode.stderr}
		if !rolling || !mode.interactive {
			return output
		}
		tail := newRollingTail(mode.stderr, mode.width, mode.widthFunc)
		stdout := newTailStreamWriter(tail, tailStreamStdout)
		stderr := newTailStreamWriter(tail, tailStreamStderr)
		output.Stdout, output.Stderr = stdout, stderr
		output.Finish = func(disposition review.OutputDisposition) {
			stdout.flush()
			stderr.flush()
			finish := tailFinishClear
			switch disposition {
			case review.OutputTail:
				finish = tailFinishLive
			case review.OutputExpanded:
				finish = tailFinishExpanded
			}
			tail.finish(finish)
		}
		return output
	}
}

func reviewNoticeRenderer(command *cobra.Command, taskID string) func(review.Notice, review.Step) error {
	return func(notice review.Notice, step review.Step) error {
		output := command.ErrOrStderr()
		var err error
		switch notice {
		case review.NoticeManualPending:
			_, err = fmt.Fprintf(output, "Review for %s is waiting for manual step %q. Resume with `orpheus task run %s`.\n", taskID, step.Name, taskID)
		case review.NoticeBlocked:
			_, err = fmt.Fprintf(output, "Review blocked for %s by %s %q.\n", taskID, step.Kind, step.Name)
		case review.NoticeDecisionPaused:
			_, err = fmt.Fprintf(output, "Automated blocker decisions for %s are paused; resume with `orpheus task run %s`.\n", taskID, taskID)
		case review.NoticeDecisionInterrupted:
			_, err = fmt.Fprintf(output, "Automated blocker decisions for %s were interrupted; run `orpheus task run %s` to continue.\n", taskID, taskID)
		}
		return err
	}
}
