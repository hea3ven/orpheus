package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/hea3ven/orpheus/internal/beads"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/spf13/cobra"
)

var (
	inspectLocalBeads      = beads.InspectLocal
	initializeManagedBeads = beads.InitializeManaged
)

func newRepoCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage registered repositories",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(newRepoAddCommand(), newRepoListCommand(), newRepoBeadsDirCommand())
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

			gitInspection, err := gitmeta.Inspect(args[0])
			if err != nil {
				return err
			}

			repo, err := registry.NewRepoFromPath(gitInspection.Root)
			if err != nil {
				return err
			}

			remote, defaultBranch, err := confirmGitValues(command, gitInspection)
			if err != nil {
				return err
			}
			repo.Remote = remote
			repo.DefaultBranch = defaultBranch

			managed := false
			beadsInspection, err := inspectLocalBeads(gitInspection.Root)
			if err != nil {
				if !errors.Is(err, beads.ErrNoLocal) {
					return err
				}

				prefix, err := confirmManagedBeadsPrefix(command, repo.ID)
				if err != nil {
					return err
				}
				repo.BeadsMode = registry.BeadsModeManaged
				repo.BeadsPrefix = prefix
				managed = true
			} else {
				repo.BeadsMode = registry.BeadsModeLocal
				repo.BeadsPrefix = beadsInspection.Prefix
			}

			reg, err := store.Load()
			if err != nil {
				return err
			}
			if err := reg.Add(repo); err != nil {
				return err
			}

			var managedDir string
			if managed {
				managedDir, err = store.ManagedBeadsDir(repo.ID)
				if err != nil {
					return err
				}
				if err := initializeManagedBeads(managedDir, repo.BeadsPrefix); err != nil {
					return err
				}
			}

			if err := store.Save(reg); err != nil {
				if managed {
					return fmt.Errorf("managed Beads was initialized at %q, but saving the repo registry failed; remove that directory before retrying if you do not want to keep it: %w", managedDir, err)
				}
				return err
			}

			_, err = fmt.Fprintf(
				command.OutOrStdout(),
				"Added repo %s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				repo.ID,
				repo.Name,
				repo.Path,
				repo.Remote,
				repo.DefaultBranch,
				repo.BeadsMode,
				repo.BeadsPrefix,
			)
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
			if _, err := fmt.Fprintln(writer, "ID\tNAME\tPATH\tREMOTE\tDEFAULT_BRANCH\tBEADS_MODE\tBEADS_PREFIX"); err != nil {
				return err
			}
			for _, repo := range reg.Repos {
				if _, err := fmt.Fprintf(
					writer,
					"%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					repo.ID,
					repo.Name,
					repo.Path,
					repo.Remote,
					repo.DefaultBranch,
					repo.BeadsMode,
					repo.BeadsPrefix,
				); err != nil {
					return err
				}
			}
			return writer.Flush()
		},
	}
	return cmd
}

func newRepoBeadsDirCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "beads-dir <repo-id-name-or-prefix>",
		Short: "Print the Beads directory for a registered repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			store, err := newRegistryStoreFromEnvironment()
			if err != nil {
				return err
			}

			reg, err := store.Load()
			if err != nil {
				return err
			}

			repo, err := reg.Resolve(args[0])
			if err != nil {
				return err
			}

			beadsDir, err := store.BeadsDir(repo)
			if err != nil {
				return err
			}

			_, err = fmt.Fprintln(command.OutOrStdout(), beadsDir)
			return err
		},
	}
	return cmd
}

func confirmGitValues(command *cobra.Command, inspection gitmeta.Inspection) (string, string, error) {
	input := command.InOrStdin()
	wizard := repoAddWizard{
		reader: bufio.NewReader(input),
		output: command.ErrOrStderr(),
	}

	if !readerIsTerminal(input) {
		remote, defaultBranch, err := confirmedGitValuesFromInspection(inspection)
		if err != nil {
			return "", "", err
		}
		emitGitInspectionWarnings(command.ErrOrStderr(), inspection)
		return remote, defaultBranch, nil
	}

	if err := wizard.presentInspection(inspection); err != nil {
		return "", "", err
	}

	remote, err := wizard.promptValue("Git remote", inspection.RemoteCandidate, false)
	if err != nil {
		return "", "", err
	}
	defaultBranch, err := wizard.promptValue("Default branch", inspection.DefaultBranchCandidate, true)
	if err != nil {
		return "", "", err
	}

	return strings.TrimSpace(remote), strings.TrimSpace(defaultBranch), nil
}

func confirmManagedBeadsPrefix(command *cobra.Command, defaultPrefix string) (string, error) {
	input := command.InOrStdin()
	defaultPrefix = strings.TrimSpace(defaultPrefix)
	if defaultPrefix == "" {
		return "", errors.New("managed Beads prefix is required")
	}

	if !readerIsTerminal(input) {
		return defaultPrefix, nil
	}

	wizard := repoAddWizard{
		reader: bufio.NewReader(input),
		output: command.ErrOrStderr(),
	}
	if _, err := fmt.Fprintln(wizard.output, "No repo-local Beads setup detected; Orpheus will initialize managed Beads state."); err != nil {
		return "", err
	}
	prefix, err := wizard.promptValue("Beads prefix", defaultPrefix, true)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(prefix), nil
}

func confirmedGitValuesFromInspection(inspection gitmeta.Inspection) (string, string, error) {
	if inspection.RemoteErr != nil && !errors.Is(inspection.RemoteErr, gitmeta.ErrNoRemote) {
		return "", "", fmt.Errorf("detect git remote: %w", inspection.RemoteErr)
	}

	defaultBranch := strings.TrimSpace(inspection.DefaultBranchCandidate)
	if defaultBranch == "" {
		if inspection.DefaultBranchErr != nil {
			return "", "", fmt.Errorf("detect git default branch: %w", inspection.DefaultBranchErr)
		}
		return "", "", errors.New("detect git default branch: no default branch candidate found")
	}

	return strings.TrimSpace(inspection.RemoteCandidate), defaultBranch, nil
}

func emitGitInspectionWarnings(output io.Writer, inspection gitmeta.Inspection) {
	if inspection.RemoteErr != nil {
		if errors.Is(inspection.RemoteErr, gitmeta.ErrNoRemote) {
			_, _ = fmt.Fprintln(output, "No Git remote detected; storing repo without a remote URL.")
		} else {
			_, _ = fmt.Fprintf(output, "Could not detect Git remote; storing repo without a remote URL: %v\n", inspection.RemoteErr)
		}
	}

	if inspection.DefaultBranchSource == gitmeta.DefaultBranchSourceCurrentBranch {
		_, _ = fmt.Fprintf(
			output,
			"Git origin/HEAD not configured; using current branch %q as the default branch.\n",
			inspection.DefaultBranchCandidate,
		)
	}
}

type repoAddWizard struct {
	reader *bufio.Reader
	output io.Writer
}

func (w repoAddWizard) presentInspection(inspection gitmeta.Inspection) error {
	if _, err := fmt.Fprintln(w.output, "Detected Git repository values. Press Enter to accept a value, or type a replacement."); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w.output, "  repository path: %s\n", inspection.Root); err != nil {
		return err
	}
	if inspection.RemoteErr != nil {
		if errors.Is(inspection.RemoteErr, gitmeta.ErrNoRemote) {
			if _, err := fmt.Fprintln(w.output, "  git remote: not detected"); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintf(w.output, "  git remote: not detected (%v)\n", inspection.RemoteErr); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(w.output, "  git remote: %s (%s)\n", inspection.RemoteCandidate, inspection.RemoteCandidateName); err != nil {
		return err
	}

	if inspection.DefaultBranchErr != nil {
		_, err := fmt.Fprintf(w.output, "  default branch: not detected (%v)\n", inspection.DefaultBranchErr)
		return err
	}

	_, err := fmt.Fprintf(
		w.output,
		"  default branch: %s (from %s)\n",
		inspection.DefaultBranchCandidate,
		inspection.DefaultBranchSource,
	)
	return err
}

func (w repoAddWizard) promptValue(label string, defaultValue string, required bool) (string, error) {
	defaultValue = strings.TrimSpace(defaultValue)
	if defaultValue == "" {
		if required {
			if _, err := fmt.Fprintf(w.output, "%s: ", label); err != nil {
				return "", err
			}
		} else if _, err := fmt.Fprintf(w.output, "%s (optional): ", label); err != nil {
			return "", err
		}
	} else if _, err := fmt.Fprintf(w.output, "%s [%s]: ", label, defaultValue); err != nil {
		return "", err
	}

	line, err := w.reader.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return "", fmt.Errorf("read %s prompt: %w", strings.ToLower(label), err)
	}

	value := strings.TrimSpace(line)
	if value == "" {
		value = defaultValue
	}
	if required && value == "" {
		return "", fmt.Errorf("%s is required", strings.ToLower(label))
	}
	return value, nil
}

func readerIsTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}

	stat, err := file.Stat()
	if err != nil {
		return false
	}
	return stat.Mode()&os.ModeCharDevice != 0
}

func newRegistryStoreFromEnvironment() (registry.Store, error) {
	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return registry.Store{}, err
	}
	return registry.NewStore(paths), nil
}
