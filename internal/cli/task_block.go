package cli

import (
	"fmt"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

func newTaskBlockCommand(opts *rootOptions, blocked bool) *cobra.Command {
	verb, short := "block", "Block a task or epic"
	long := "Mark open or safely inactive in-progress work natively blocked, preserving execution and review history. " +
		"Active or unverifiable executions and review/publication boundaries are refused. " +
		"Blocking an epic prevents new child starts without stopping existing executions. " +
		"This is not a persistent manual hold: native automatic reopening still applies."
	if !blocked {
		verb, short = "unblock", "Unblock a task or epic"
		long = "Restore a currently blocked item's recorded pre-block lifecycle, or open when restoration information is absent. " +
			"Native automatic reopening takes precedence. Dependencies are not removed and existing workflow gates still apply."
	}
	return &cobra.Command{
		Use:   verb + " <id>",
		Short: short,
		Long:  long,
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runTaskBlock(command, opts, args[0], blocked)
		},
	}
}

func runTaskBlock(command *cobra.Command, opts *rootOptions, id string, blocked bool) error {
	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	resolved, err := resolveTaskRunContextFromInvocation(deps, id)
	if err != nil {
		return err
	}
	return state.WithGlobalMutationLockLogger(command.Context(), deps.paths, "task blocking", deps.logger, func() error {
		backend, err := deps.taskBackendFactory(resolved.Resolved.Source)
		if err != nil {
			return fmt.Errorf("cannot prepare task source for %s: %w", id, err)
		}
		mutator, ok := backend.(workflow.BlockBackend)
		if !ok {
			return fmt.Errorf("task source for repository %s does not support blocking", resolved.Resolved.Source.Repository.ID)
		}
		service := workflow.BlockService{Store: deps.taskStateStore, Probe: deps.processProbe}
		changed, err := service.SetBlocked(command.Context(), mutator, resolved.Resolved.Source.Repository.ID, resolved.Resolved.TaskID, blocked)
		if err != nil {
			return err
		}
		status := "blocked"
		if !blocked {
			status = "unblocked"
		}
		if !changed {
			_, err = fmt.Fprintf(command.OutOrStdout(), "Item %s is already %s.\n", resolved.Resolved.TaskID, status)
		} else {
			_, err = fmt.Fprintf(command.OutOrStdout(), "Item %s %s.\n", resolved.Resolved.TaskID, status)
		}
		return err
	})
}
