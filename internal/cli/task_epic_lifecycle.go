package cli

import (
	"errors"
	"fmt"

	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

func newTaskStartCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start <epic-id>",
		Short: "Start an eligible epic",
		Long:  "Start an open epic after confirming its parent epic and blocking dependencies permit activation. Use `task run` for ordinary task workflow transitions.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskEpicLifecycle(command, opts, args[0], true)
		},
	}
	return cmd
}

func newTaskCloseCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "close <epic-id>",
		Short: "Close a completed epic",
		Long:  "Close an in-progress epic only after confirming every direct child is closed. Use `task run` for ordinary task workflow transitions.",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskEpicLifecycle(command, opts, args[0], false)
		},
	}
	return cmd
}

func runTaskEpicLifecycle(command *cobra.Command, opts *rootOptions, taskID string, start bool) error {
	operation := "task close"
	if start {
		operation = "task start"
	}
	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	resolvedCtx, err := resolveTaskRunContextFromInvocation(deps, taskID)
	if err != nil {
		return err
	}
	backend, err := deps.taskBackendFactory(resolvedCtx.Resolved.Source)
	if err != nil {
		return epicLifecycleCommandFailure{
			message: fmt.Sprintf("%s %s: cannot prepare task operations for repository %s", operation, resolvedCtx.Resolved.TaskID, resolvedCtx.Resolved.Source.Repository.ID),
			cause:   err,
		}
	}
	lifecycleBackend, ok := backend.(workflow.EpicLifecycleBackend)
	if !ok {
		return fmt.Errorf("%s %s: task source for repository %s does not support epic lifecycle commands", operation, resolvedCtx.Resolved.TaskID, resolvedCtx.Resolved.Source.Repository.ID)
	}

	service := workflow.EpicLifecycleService{}
	var result workflow.EpicLifecycleResult
	if start {
		result, err = service.Start(command.Context(), lifecycleBackend, resolvedCtx.Resolved.TaskID)
	} else {
		result, err = service.Close(command.Context(), lifecycleBackend, resolvedCtx.Resolved.TaskID)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", operation, resolvedCtx.Resolved.TaskID, epicLifecycleGuidance(err, resolvedCtx.Resolved.TaskID))
	}
	return renderEpicLifecycleResult(command, resolvedCtx.Resolved.TaskID, start, result)
}

func epicLifecycleGuidance(err error, taskID string) error {
	if !isOrdinaryTaskLifecycleError(err) {
		return err
	}
	return fmt.Errorf("%w; use `orpheus task run %s` for the normal task workflow", err, taskID)
}

func isOrdinaryTaskLifecycleError(err error) bool {
	return errors.Is(err, workflow.ErrNotEpic)
}

// epicLifecycleCommandFailure retains backend details for diagnostics without
// exposing task-source implementation details in task-facing command errors.
type epicLifecycleCommandFailure struct {
	message string
	cause   error
}

func (e epicLifecycleCommandFailure) Error() string { return e.message }

func (e epicLifecycleCommandFailure) Unwrap() error { return e.cause }

func renderEpicLifecycleResult(command *cobra.Command, taskID string, start bool, result workflow.EpicLifecycleResult) error {
	if start {
		if result.Changed {
			_, err := fmt.Fprintf(command.OutOrStdout(), "Epic %s started.\n", taskID)
			return err
		}
		_, err := fmt.Fprintf(command.OutOrStdout(), "Epic %s is already in progress.\n", taskID)
		return err
	}
	if result.Changed {
		_, err := fmt.Fprintf(command.OutOrStdout(), "Epic %s closed.\n", taskID)
		return err
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "Epic %s is already closed.\n", taskID)
	return err
}
