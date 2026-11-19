package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/hea3ven/orpheus/internal/workflow/finalization"
	"github.com/spf13/cobra"
)

func newTaskCancelCommand(opts *rootOptions) *cobra.Command {
	var reason string
	cmd := &cobra.Command{
		Use:   "cancel <task-id>",
		Short: "Retire a task without marking it done",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskCancel(command, opts, args[0], reason)
		},
	}
	cmd.Flags().StringVar(&reason, "reason", "", "optional reason for cancellation")
	return cmd
}

func runTaskCancel(command *cobra.Command, opts *rootOptions, id, reason string) error {
	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	resolved, err := resolveTaskRunContextFromInvocation(deps, id)
	if err != nil {
		return err
	}
	source := resolved.Resolved.Source
	backend, err := invocationTaskBackend[workflow.CancelBackend](deps, source)
	if err != nil {
		return err
	}
	service := workflow.CancelService{Paths: deps.paths, Store: deps.taskStateStore, CleanupGit: deps.cleanupGit, ProcessProbe: deps.processProbe, Logger: deps.logger}
	result, err := service.Cancel(command.Context(), source.Repository, backend, resolved.Resolved.TaskID, reason, func(directory string) (bool, error) {
		return confirmTaskCancel(command, deps.terminal, directory)
	})
	if err != nil {
		return fmt.Errorf("task cancel %s: %w", id, err)
	}
	return renderTaskCancellation(command, resolved.Resolved.TaskID, result)
}

func confirmTaskCancel(command *cobra.Command, terminal TerminalCapabilities, directory string) (bool, error) {
	if _, err := fmt.Fprintf(command.ErrOrStderr(), "Dedicated worktree: %s\n", directory); err != nil {
		return false, err
	}
	if !terminal.withDefaults().InputIsTerminal(command.InOrStdin()) {
		return false, errors.New("cancellation with a dedicated worktree requires confirmation from an interactive terminal")
	}
	if _, err := fmt.Fprint(command.ErrOrStderr(), "Cancel the task and ask Git to remove this worktree? [y/N] "); err != nil {
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

func renderTaskCancellation(command *cobra.Command, id string, result workflow.CancelResult) error {
	output := command.OutOrStdout()
	if result.AlreadyCancelled {
		_, err := fmt.Fprintf(output, "Task %s is already cancelled.\n", id)
		return err
	}
	if !result.Changed {
		_, err := fmt.Fprintln(output, "Task not cancelled.")
		return err
	}
	if _, err := fmt.Fprintf(output, "Task %s cancelled.\n", id); err != nil {
		return err
	}
	switch result.Cleanup.Outcome {
	case finalization.WorktreeCleanupRemoved:
		_, err := fmt.Fprintf(output, "Removed worktree %s.\n", result.Cleanup.Worktree)
		return err
	case finalization.WorktreeCleanupFailed, finalization.WorktreeCleanupUnsafe:
		guidance := "Resolve the issue, then run `orpheus doctor --fix` to retry verified cleanup."
		if result.ManualCleanup {
			guidance = "Orpheus has no recorded Git target. Inspect this checkout and remove it manually if appropriate."
		}
		_, err := fmt.Fprintf(command.ErrOrStderr(), "Worktree retained at %s: %s\n%s\n", result.Cleanup.Worktree, result.Cleanup.Reason, guidance)
		return err
	default:
		return nil
	}
}
