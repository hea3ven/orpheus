package cli

import (
	"log/slog"

	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/spf13/cobra"
)

type rootOptions struct {
	verbose        bool
	logger         *slog.Logger
	invocationDeps *invocationDependencies
}

func (o *rootOptions) loggingConfig() logging.Config {
	return logging.Config{Verbose: o.verbose}
}

func (o *rootOptions) configureLogging(command *cobra.Command) {
	o.logger = logging.New(command.ErrOrStderr(), o.loggingConfig())
}

func (o *rootOptions) log() *slog.Logger {
	if o.logger == nil {
		return logging.Discard()
	}
	return o.logger
}

// NewRootCommand constructs the root Orpheus CLI command.
func NewRootCommand() *cobra.Command {
	opts := &rootOptions{logger: logging.Discard()}

	cmd := &cobra.Command{
		Use:   "orpheus",
		Short: "Coordinate AI coding-agent work across repositories",
		Long: `Orpheus is a CLI-first orchestration layer for coordinating AI coding agents
across tasks, worktrees, and pull requests while keeping the human operator in
control.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(command *cobra.Command, args []string) error {
			opts.configureLogging(command)
			_, err := opts.invocation(command)
			return err
		},
		RunE: func(command *cobra.Command, args []string) error {
			opts.log().DebugContext(
				command.Context(),
				"rendering root help",
				slog.String("component", "cli"),
				slog.String("operation", "help"),
			)
			return command.Help()
		},
	}

	cmd.PersistentFlags().BoolVarP(
		&opts.verbose,
		"verbose",
		"v",
		false,
		"enable debug diagnostic logs on stderr",
	)

	cmd.AddCommand(
		newRepoCommand(opts),
		newTaskCommand(opts),
		newStatusCommand(opts),
		newAgentCommand(opts),
		newDoctorCommand(opts),
	)

	return cmd
}
