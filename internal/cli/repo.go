package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/hea3ven/orpheus/internal/beads"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/spf13/cobra"
)

var (
	inspectLocalBeads      = beads.InspectLocal
	initializeManagedBeads = beads.InitializeManaged
)

func newRepoCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage registered repositories",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(newRepoAddCommand(opts), newRepoListCommand(opts), newRepoBeadsDirCommand(opts))
	return cmd
}

func newRepoAddCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <path>",
		Short: "Register a repository path",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			logger := opts.log().With(
				slog.String("component", "cli"),
				slog.String("operation", "repo_add"),
			)
			logger.DebugContext(command.Context(), "starting repo registration", slog.String("input_path", args[0]))

			store, err := newRegistryStoreFromEnvironment()
			if err != nil {
				return err
			}
			logger.DebugContext(command.Context(), "resolved registry store")

			gitInspection, err := gitmeta.Inspect(args[0])
			if err != nil {
				return err
			}
			logger.DebugContext(
				command.Context(),
				"inspected git repository",
				slog.String("repo_root", gitInspection.Root),
				slog.Bool("remote_detected", gitInspection.RemoteCandidate != ""),
				slog.String("remote_candidate_name", gitInspection.RemoteCandidateName),
				slog.String("default_branch_candidate", gitInspection.DefaultBranchCandidate),
				slog.String("default_branch_source", string(gitInspection.DefaultBranchSource)),
			)

			repo, err := registry.NewRepoFromPath(gitInspection.Root)
			if err != nil {
				return err
			}
			logger.DebugContext(command.Context(), "derived repo identity", slog.String("repo_id", repo.ID), slog.String("repo_name", repo.Name))

			remote, defaultBranch, err := confirmGitValues(command, gitInspection)
			if err != nil {
				return err
			}
			repo.Remote = remote
			repo.DefaultBranch = defaultBranch
			logger.DebugContext(
				command.Context(),
				"confirmed git values",
				slog.Bool("remote_set", repo.Remote != ""),
				slog.String("default_branch", repo.DefaultBranch),
			)

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
				logger.DebugContext(
					command.Context(),
					"selected managed Beads mode",
					slog.String("beads_prefix", repo.BeadsPrefix),
				)
			} else {
				repo.BeadsMode = registry.BeadsModeLocal
				repo.BeadsPrefix = beadsInspection.Prefix
				logger.DebugContext(
					command.Context(),
					"detected repo-local Beads mode",
					slog.String("beads_dir", beadsInspection.BeadsDir),
					slog.String("beads_prefix", repo.BeadsPrefix),
				)
			}

			registryCtx, err := loadRegistryContextFromStore(store)
			if err != nil {
				return err
			}
			reg := registryCtx.Registry
			if err := reg.Add(repo); err != nil {
				return err
			}
			logger.DebugContext(command.Context(), "validated registry update", slog.Int("repo_count", len(reg.Repos)))

			var managedDir string
			if managed {
				managedDir, err = registryCtx.Store.ManagedBeadsDir(repo.ID)
				if err != nil {
					return err
				}
				logger.DebugContext(command.Context(), "initializing managed Beads", slog.String("beads_dir", managedDir))
				if err := initializeManagedBeads(managedDir, repo.BeadsPrefix); err != nil {
					return err
				}
			}

			if err := registryCtx.Store.Save(reg); err != nil {
				if managed {
					return fmt.Errorf("managed Beads was initialized at %q, but saving the repo registry failed; remove that directory before retrying if you do not want to keep it: %w", managedDir, err)
				}
				return err
			}
			logger.DebugContext(
				command.Context(),
				"saved repo registration",
				slog.String("repo_id", repo.ID),
				slog.String("beads_mode", repo.BeadsMode),
				slog.String("beads_prefix", repo.BeadsPrefix),
			)

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

func newRepoListCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered repositories",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			logger := opts.log().With(
				slog.String("component", "cli"),
				slog.String("operation", "repo_list"),
			)
			logger.DebugContext(command.Context(), "loading registered repos")

			registryCtx, err := loadRegistryContext()
			if err != nil {
				return err
			}
			reg := registryCtx.Registry
			logger.DebugContext(command.Context(), "loaded registered repos", slog.Int("repo_count", len(reg.Repos)))

			rows := make([][]string, 0, len(reg.Repos))
			for _, repo := range reg.Repos {
				rows = append(rows, []string{
					repo.ID,
					repo.Name,
					repo.Path,
					repo.Remote,
					repo.DefaultBranch,
					repo.BeadsMode,
					repo.BeadsPrefix,
				})
			}
			return renderTable(
				command.OutOrStdout(),
				[]string{"ID", "NAME", "PATH", "REMOTE", "DEFAULT_BRANCH", "BEADS_MODE", "BEADS_PREFIX"},
				rows,
			)
		},
	}
	return cmd
}

func newRepoBeadsDirCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "beads-dir <repo-id-name-or-prefix>",
		Short: "Print the Beads directory for a registered repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			logger := opts.log().With(
				slog.String("component", "cli"),
				slog.String("operation", "repo_beads_dir"),
				slog.String("token", args[0]),
			)
			logger.DebugContext(command.Context(), "resolving repo Beads directory")

			registryCtx, err := loadRegistryContext()
			if err != nil {
				return err
			}

			repo, err := registryCtx.Registry.Resolve(args[0])
			if err != nil {
				return err
			}

			beadsDir, err := registryCtx.Store.BeadsDir(repo)
			if err != nil {
				return err
			}
			logger.DebugContext(
				command.Context(),
				"resolved repo Beads directory",
				slog.String("repo_id", repo.ID),
				slog.String("beads_mode", repo.BeadsMode),
				slog.String("beads_prefix", repo.BeadsPrefix),
				slog.String("beads_dir", beadsDir),
			)

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
