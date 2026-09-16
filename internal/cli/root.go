package cli

import (
	"log/slog"
	"maps"

	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/spf13/cobra"
)

const skipInvocationAnnotation = "orpheus.skipInvocation"

type rootOptions struct {
	verbose        bool
	logger         *slog.Logger
	invocationDeps *invocationDependencies
	commandOptions CommandOptions
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
	return NewRootCommandWithOptions(CommandOptions{})
}

// NewRootCommandWithOptions constructs the same command tree as NewRootCommand
// with explicit invocation inputs and external collaborators. It copies the
// environment map and Paths value; storage and collaborators remain shared.
// Construct a fresh command for each invocation.
func NewRootCommandWithOptions(options CommandOptions) *cobra.Command {
	options.Environment = maps.Clone(options.Environment)
	if options.Paths != nil {
		paths := *options.Paths
		options.Paths = &paths
	}
	return newRootCommand(&rootOptions{logger: logging.Discard(), commandOptions: options})
}

func newRootCommand(opts *rootOptions) *cobra.Command {
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
			if skipsInvocation(command) {
				return nil
			}
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
		newEvalCommand(opts),
		newDoctorCommand(opts),
	)
	configureCompletions(cmd, opts)

	return cmd
}

func skipsInvocation(command *cobra.Command) bool {
	for current := command; current != nil; current = current.Parent() {
		if current.Annotations[skipInvocationAnnotation] == "true" {
			return true
		}
	}
	return false
}
