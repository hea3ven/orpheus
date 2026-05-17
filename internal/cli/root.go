package cli

import "github.com/spf13/cobra"

// NewRootCommand constructs the root Orpheus CLI command.
func NewRootCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "orpheus",
		Short: "Coordinate AI coding-agent work across repositories",
		Long: `Orpheus is a CLI-first orchestration layer for coordinating AI coding agents
across tasks, worktrees, and pull requests while keeping the human operator in
control.`,
		Args:          cobra.NoArgs,
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(command *cobra.Command, args []string) error {
			return command.Help()
		},
	}
}
