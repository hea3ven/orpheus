package cli

import (
	"fmt"
	"log/slog"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/spf13/cobra"
)

func newAgentCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Agent-facing Orpheus commands",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(newAgentContextCommand(opts), newAgentDoneCommand(opts))
	return cmd
}

func newAgentContextCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "context",
		Short: "Render the active agent execution context",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runAgentContext(command, opts)
		},
	}
	return cmd
}

func newAgentDoneCommand(opts *rootOptions) *cobra.Command {
	var summary string
	var details string
	cmd := &cobra.Command{
		Use:   "done",
		Short: "Record active agent completion for local review",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runAgentDone(command, opts, summary, details)
		},
	}
	cmd.Flags().StringVar(&summary, "summary", "", "short completion summary")
	cmd.Flags().StringVar(&details, "details", "", "detailed completion notes")
	_ = cmd.MarkFlagRequired("summary")
	_ = cmd.MarkFlagRequired("details")
	return cmd
}

func runAgentContext(command *cobra.Command, opts *rootOptions) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "agent_context"),
	)
	logger.DebugContext(command.Context(), "resolving active agent context")

	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}

	resolver := activeAgentContextResolver(paths, taskCtx, taskstate.NewStore(paths))
	activeContext, err := resolver.Resolve(command.Context())
	if err != nil {
		return fmt.Errorf("agent context: %w", err)
	}

	_, err = fmt.Fprint(command.OutOrStdout(), agent.RenderActiveContext(activeContext))
	return err
}

func runAgentDone(command *cobra.Command, opts *rootOptions, summary string, details string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "agent_done"),
	)
	logger.DebugContext(command.Context(), "resolving active agent completion context")

	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}

	store := taskstate.NewStore(paths)
	service := agent.CompletionService{
		Paths:    paths,
		Resolver: activeAgentContextResolver(paths, taskCtx, store),
		RunStore: store,
	}
	completed, err := service.Complete(command.Context(), agent.CompleteOptions{
		Summary: summary,
		Details: details,
	})
	if err != nil {
		return fmt.Errorf("agent done: %w", err)
	}

	logger.DebugContext(
		command.Context(),
		"recorded agent completion",
		slog.String("repo_id", completed.Context.Repository.ID),
		slog.String("task_id", completed.Context.Task.ID),
		slog.Int("attempt", completed.Run.Attempt),
	)
	_, err = fmt.Fprintf(command.OutOrStdout(), "Recorded completion for %s; ready for local review.\n", completed.Context.Task.ID)
	return err
}

func activeAgentContextResolver(
	paths state.Paths,
	taskCtx taskContext,
	runStore taskstate.Service,
) agent.ActiveContextResolver {
	return agent.ActiveContextResolver{
		Paths:    paths,
		Registry: taskCtx.Registry,
		Sources:  taskCtx.Sources,
		BackendFactory: func(source taskmodel.RepositorySource) (agent.ContextBackend, error) {
			return newBeadsTaskBackend(source.BackendDir)
		},
		RunStore: runStore,
	}
}
