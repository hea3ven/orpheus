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

	cmd.AddCommand(newAgentContextCommand(opts))
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

	resolver := agent.ActiveContextResolver{
		Paths:    paths,
		Registry: taskCtx.Registry,
		Sources:  taskCtx.Sources,
		BackendFactory: func(source taskmodel.RepositorySource) (agent.ContextBackend, error) {
			return newBeadsTaskBackend(source.BackendDir)
		},
		RunStore: taskstate.NewStore(paths),
	}
	activeContext, err := resolver.Resolve(command.Context())
	if err != nil {
		return fmt.Errorf("agent context: %w", err)
	}

	_, err = fmt.Fprint(command.OutOrStdout(), agent.RenderActiveContext(activeContext))
	return err
}
