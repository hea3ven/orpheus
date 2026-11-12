package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

func newTaskResetCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{Use: "reset <task-id>", Short: "Confirm a destructive restart for the next task run", Args: cobra.ExactArgs(1), RunE: func(command *cobra.Command, args []string) error {
		controller, err := workflowControllerForCommand(command, opts)
		if err != nil {
			return err
		}
		deps, err := opts.invocation(command)
		if err != nil {
			return err
		}
		scheduled, err := controller.Reset(command.Context(), args[0], func(d workflow.ResetDisclosure) (bool, error) {
			return confirmTaskReset(command, deps.terminal, d)
		})
		if err != nil {
			return err
		}
		if !scheduled {
			_, err = fmt.Fprintln(command.OutOrStdout(), "Restart not scheduled.")
		} else {
			_, err = fmt.Fprintf(command.OutOrStdout(), "Restart scheduled. Run task run %s to begin Preparation.\n", args[0])
		}
		return err
	}}
}

func confirmTaskReset(command *cobra.Command, terminal TerminalCapabilities, d workflow.ResetDisclosure) (bool, error) {
	if _, err := fmt.Fprintf(command.ErrOrStderr(), "Task: %s — %s\nDirectory: %s\nBranch: %s\n\nCurrent changes:\n%s\nThe next task run will discard tracked edits and non-ignored untracked files,\nincluding any edits made before that run. Ignored files may be overwritten or\nremoved when needed to restore the baseline. Other ignored files and recorded\nworkflow history will remain. No backup of discarded changes will be made.\nPreparation will fetch and restart from the latest remote default branch.\nNothing in the checkout will change now. Remote branches and PRs will not change.\n\n", d.TaskID, d.Title, d.Directory, d.Branch, d.Changes); err != nil {
		return false, err
	}
	if err := command.Context().Err(); err != nil {
		return false, err
	}
	if !terminal.withDefaults().InputIsTerminal(command.InOrStdin()) {
		return false, fmt.Errorf("task reset %s requires confirmation from an interactive terminal; no restart scheduled", d.TaskID)
	}
	if _, err := fmt.Fprint(command.ErrOrStderr(), "Schedule this restart? [y/N] "); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(command.InOrStdin()).ReadString('\n')
	if errors.Is(err, io.EOF) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := command.Context().Err(); err != nil {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}
