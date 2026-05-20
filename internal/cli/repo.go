package cli

import (
	"fmt"
	"text/tabwriter"

	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/spf13/cobra"
)

func newRepoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage registered repositories",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(newRepoAddCommand(), newRepoListCommand())
	return cmd
}

func newRepoAddCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <path>",
		Short: "Register a repository path",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			store, err := newRegistryStoreFromEnvironment()
			if err != nil {
				return err
			}

			repo, err := registry.NewRepoFromPath(args[0])
			if err != nil {
				return err
			}

			reg, err := store.Load()
			if err != nil {
				return err
			}
			if err := reg.Add(repo); err != nil {
				return err
			}
			if err := store.Save(reg); err != nil {
				return err
			}

			_, err = fmt.Fprintf(command.OutOrStdout(), "Added repo %s\t%s\t%s\n", repo.ID, repo.Name, repo.Path)
			return err
		},
	}
	return cmd
}

func newRepoListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered repositories",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			store, err := newRegistryStoreFromEnvironment()
			if err != nil {
				return err
			}

			reg, err := store.Load()
			if err != nil {
				return err
			}

			writer := tabwriter.NewWriter(command.OutOrStdout(), 0, 0, 2, ' ', 0)
			if _, err := fmt.Fprintln(writer, "ID\tNAME\tPATH"); err != nil {
				return err
			}
			for _, repo := range reg.Repos {
				if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\n", repo.ID, repo.Name, repo.Path); err != nil {
					return err
				}
			}
			return writer.Flush()
		},
	}
	return cmd
}

func newRegistryStoreFromEnvironment() (registry.Store, error) {
	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return registry.Store{}, err
	}
	return registry.NewStore(paths), nil
}
