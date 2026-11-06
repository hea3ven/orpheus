package cli

import (
	"fmt"

	"github.com/hea3ven/orpheus/internal/review"
	"github.com/spf13/cobra"
)

func requireOperatorInput(command *cobra.Command, terminal TerminalCapabilities, taskID string) error {
	if err := command.Context().Err(); err != nil {
		return operatorInteractionError(taskID, err)
	}
	if !terminal.withDefaults().InputIsTerminal(command.InOrStdin()) {
		return operatorInteractionError(taskID, nil)
	}
	return nil
}

func operatorInteractionError(taskID string, cause error) error {
	err := fmt.Errorf("%w for task %s; pending work is retained; rerun `orpheus task run %s` from an interactive terminal", review.ErrInteractionRequired, taskID, taskID)
	if cause != nil {
		return fmt.Errorf("%w: %w", err, cause)
	}
	return err
}
