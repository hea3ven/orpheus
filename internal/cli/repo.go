package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/hea3ven/orpheus/internal/beads"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/spf13/cobra"
)

const (
	repoAddLockOperation    = "repo add"
	repoConfigLockOperation = "repo config"

	summaryGuidanceStyleCustom = "custom"
	summaryGuidanceStylePrompt = "Summary guidance style (typed, capitalized, custom)"
)

var isTerminal = readerIsTerminal

func newRepoCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage registered repositories",
		Args:  cobra.NoArgs,
	}

	cmd.AddCommand(
		newRepoAddCommand(opts),
		newRepoConfigCommand(opts),
		newRepoListCommand(opts),
		newRepoBeadsDirCommand(opts),
	)
	return cmd
}

func newRepoConfigCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect or update repository configuration",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newRepoConfigGetCommand(opts), newRepoConfigSetCommand(opts))
	return cmd
}

const (
	repoConfigSummaryGuidance = "summary-guidance"
	repoConfigSummaryStyle    = "summary-style"
	repoConfigTitleTemplate   = "title-template"

	repoConfigReviewPipeline            = "review-pipeline"
	repoConfigReviewPipelineAliasPrefix = "review-pipeline-alias."
)

func newRepoConfigGetCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "get <repo-id-name-or-prefix> [config-name]",
		Short: "Show repository configuration",
		Long: "Show repository configuration.\n\n" +
			"Supported config names are summary-guidance, summary-style, title-template, " +
			"review-pipeline, and review-pipeline-alias.<alias>.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(command *cobra.Command, args []string) error {
			deps, err := opts.invocation(command)
			if err != nil {
				return err
			}
			paths := deps.paths
			registryCtx, err := loadRegistryContextFromInvocation(deps)
			if err != nil {
				return err
			}
			repo, err := registryCtx.Registry.Resolve(args[0])
			if err != nil {
				return err
			}
			configName := ""
			if len(args) == 2 {
				configName, err = normalizeRepoConfigName(args[1])
				if err != nil {
					return err
				}
			}
			reviewConfig, err := loadRepoConfigReviewConfig(paths, configName)
			if err != nil {
				return err
			}
			return renderRepoConfig(command, repo, configName, reviewConfig)
		},
	}
}

func newRepoConfigSetCommand(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "set <repo-id-name-or-prefix> <config-name> <config-value>",
		Short: "Set or clear repository configuration",
		Long: "Set one repository configuration value. Pass an empty config value to clear it.\n\n" +
			"review-pipeline and review-pipeline-alias.<alias> values must name a configured " +
			"global reviews.pipelines entry. Setting an alias to an empty value deletes it.",
		Args: cobra.ExactArgs(3),
		RunE: func(command *cobra.Command, args []string) error {
			return runRepoConfigSet(command, opts, args[0], args[1], args[2])
		},
	}
}

func runRepoConfigSet(command *cobra.Command, opts *rootOptions, token string, configName string, value string) error {
	configName, err := normalizeRepoConfigName(configName)
	if err != nil {
		return err
	}
	value = strings.TrimSpace(value)
	if err := validateRepoConfigValue(configName, value); err != nil {
		return err
	}

	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "repo_config_set"),
		slog.String("token", token),
		slog.String("config_name", configName),
	)

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	paths := deps.paths
	store := deps.registryStore
	return state.WithGlobalMutationLockLogger(command.Context(), paths, repoConfigLockOperation, deps.logger, func() error {
		registryCtx, err := loadRegistryContextFromStore(store)
		if err != nil {
			return err
		}
		var reviewConfig review.Config
		if isReviewPipelineConfigName(configName) {
			reviewConfig, err = review.LoadConfig(paths)
			if err != nil {
				return err
			}
			if err := validateRepoReviewConfigValue(reviewConfig, configName, value); err != nil {
				return err
			}
		}
		repo, err := registryCtx.Registry.Resolve(token)
		if err != nil {
			return err
		}
		updatedRepo := setRepoConfigValue(repo, configName, value)
		if err := replaceRepo(&registryCtx.Registry, updatedRepo); err != nil {
			return err
		}
		if err := registryCtx.Store.Save(registryCtx.Registry); err != nil {
			return err
		}
		logger.DebugContext(command.Context(), "saved repository configuration", slog.String("repo_id", updatedRepo.ID))
		return renderRepoConfig(command, updatedRepo, configName, reviewConfig)
	})
}

func normalizeRepoConfigName(value string) (string, error) {
	name := strings.TrimSpace(value)
	switch name {
	case repoConfigSummaryGuidance,
		repoConfigSummaryStyle,
		repoConfigTitleTemplate,
		repoConfigReviewPipeline:
		return name, nil
	default:
		if alias, ok := repoConfigAliasName(name); ok {
			if alias == "" {
				return "", fmt.Errorf("unknown repo config %q; review pipeline alias name is required", value)
			}
			return repoConfigReviewPipelineAliasPrefix + alias, nil
		}
		return "", fmt.Errorf(
			"unknown repo config %q; expected %q, %q, %q, %q, or %q<alias>",
			value,
			repoConfigSummaryGuidance,
			repoConfigSummaryStyle,
			repoConfigTitleTemplate,
			repoConfigReviewPipeline,
			repoConfigReviewPipelineAliasPrefix,
		)
	}
}

func validateRepoConfigValue(name string, value string) error {
	switch name {
	case repoConfigSummaryStyle:
		if value == "" {
			return nil
		}
		return registry.ValidateSummaryGuidanceStyle(value)
	case repoConfigTitleTemplate:
		return publication.ValidateTitleTemplate(value)
	default:
		return nil
	}
}

func loadRepoConfigReviewConfig(paths state.Paths, name string) (review.Config, error) {
	if name != "" && !isReviewPipelineConfigName(name) {
		return review.Config{}, nil
	}
	return review.LoadConfig(paths)
}

func isReviewPipelineConfigName(name string) bool {
	if name == repoConfigReviewPipeline {
		return true
	}
	_, ok := repoConfigAliasName(name)
	return ok
}

func validateRepoReviewConfigValue(config review.Config, name string, value string) error {
	if !isReviewPipelineConfigName(name) {
		return nil
	}
	if value == "" {
		return nil
	}
	if config.HasPipeline(value) {
		return nil
	}
	return fmt.Errorf(
		"repo config %s target %q does not match a configured global review pipeline; configured pipelines: %s",
		name,
		value,
		strings.Join(config.PipelineNames(), ", "),
	)
}

func setRepoConfigValue(repo registry.Repo, name string, value string) registry.Repo {
	switch name {
	case repoConfigSummaryGuidance:
		repo.SummaryGuidance = value
	case repoConfigSummaryStyle:
		repo.SummaryGuidanceStyle = value
	case repoConfigTitleTemplate:
		repo.TitleTemplate = value
	case repoConfigReviewPipeline:
		repo.ReviewPipeline = value
	default:
		if alias, ok := repoConfigAliasName(name); ok {
			repo.ReviewPipelineAliases = setReviewPipelineAlias(repo.ReviewPipelineAliases, alias, value)
		}
	}
	return repo
}

func setReviewPipelineAlias(aliases map[string]string, alias string, value string) map[string]string {
	updated := make(map[string]string, len(aliases)+1)
	for name, target := range aliases {
		updated[name] = target
	}
	if value == "" {
		delete(updated, alias)
		if len(updated) == 0 {
			return nil
		}
		return updated
	}
	updated[alias] = value
	return updated
}

func repoConfigAliasName(name string) (string, bool) {
	if !strings.HasPrefix(name, repoConfigReviewPipelineAliasPrefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(name, repoConfigReviewPipelineAliasPrefix)), true
}

func replaceRepo(reg *registry.Registry, updated registry.Repo) error {
	for index, repo := range reg.Repos {
		if repo.ID == updated.ID {
			reg.Repos[index] = updated
			return reg.Validate()
		}
	}
	return fmt.Errorf("repo %q is not registered", updated.ID)
}

func renderRepoConfig(command *cobra.Command, repo registry.Repo, configName string, reviewConfig review.Config) error {
	policy := repo.EffectivePublicationPolicy()
	rows := [][]string{
		{repoConfigSummaryGuidance, displayConfigValue(repo.SummaryGuidance), effectiveSummaryGuidance(policy)},
		{repoConfigSummaryStyle, displayConfigValue(repo.SummaryGuidanceStyle), effectiveSummaryGuidanceStyle(policy)},
		{repoConfigTitleTemplate, displayConfigValue(repo.TitleTemplate), effectiveTitleTemplate(policy.TitleTemplate)},
		{repoConfigReviewPipeline, displayConfigValue(repo.ReviewPipeline), effectiveReviewPipeline(repo, reviewConfig)},
	}
	rows = append(rows, reviewPipelineAliasRows(repo)...)
	switch configName {
	case repoConfigSummaryGuidance:
		rows = rows[:1]
	case repoConfigSummaryStyle:
		rows = rows[1:2]
	case repoConfigTitleTemplate:
		rows = rows[2:3]
	case repoConfigReviewPipeline:
		rows = rows[3:4]
	default:
		if alias, ok := repoConfigAliasName(configName); ok {
			rows = [][]string{reviewPipelineAliasRow(alias, repo.ReviewPipelineAliases[alias])}
		}
	}
	return renderTable(command.OutOrStdout(), []string{"CONFIG", "STORED", "EFFECTIVE"}, rows)
}

func reviewPipelineAliasRows(repo registry.Repo) [][]string {
	aliases := make([]string, 0, len(repo.ReviewPipelineAliases))
	for alias := range repo.ReviewPipelineAliases {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)

	rows := make([][]string, 0, len(aliases))
	for _, alias := range aliases {
		rows = append(rows, reviewPipelineAliasRow(alias, repo.ReviewPipelineAliases[alias]))
	}
	return rows
}

func reviewPipelineAliasRow(alias string, target string) []string {
	return []string{repoConfigReviewPipelineAliasPrefix + alias, displayConfigValue(target), displayConfigValue(target)}
}

func displayConfigValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "(not set)"
	}
	return value
}

func effectiveTitleTemplate(template string) string {
	if strings.TrimSpace(template) == "" {
		return "completion summary"
	}
	return template
}

func effectiveSummaryGuidance(policy registry.PublicationPolicy) string {
	if policy.SummaryGuidance != "" {
		return policy.SummaryGuidance
	}
	return policy.SummaryGuidanceStyle + " style"
}

func effectiveSummaryGuidanceStyle(policy registry.PublicationPolicy) string {
	if policy.SummaryGuidance != "" {
		return "overridden by custom guidance"
	}
	return policy.SummaryGuidanceStyle
}

func effectiveReviewPipeline(repo registry.Repo, config review.Config) string {
	pipeline, err := review.ResolvePipeline(config, "", repo.ReviewPipeline)
	if err != nil {
		return "invalid: " + err.Error()
	}
	return pipeline.Name
}

func newRepoAddCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <path>",
		Short: "Register a repository path",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			return runRepoAdd(command, opts, args[0])
		},
	}
	return cmd
}

func runRepoAdd(command *cobra.Command, opts *rootOptions, inputPath string) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "repo_add"),
	)
	logger.DebugContext(command.Context(), "starting repo registration", slog.String("input_path", inputPath))

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	store := deps.registryStore
	paths := deps.paths
	logger.DebugContext(command.Context(), "resolved registry store")

	gitInspection, err := deps.inspectGit(command.Context(), inputPath)
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

	if err := configureRepoGitValues(command, &repo, gitInspection, logger); err != nil {
		return err
	}
	managed, err := configureRepoBeads(command, deps, &repo, gitInspection.Root, logger)
	if err != nil {
		return err
	}
	if err := configureRepoSummaryGuidance(command, &repo, logger); err != nil {
		return err
	}
	if err := configureRepoTitleTemplate(command, &repo, logger); err != nil {
		return err
	}

	if err := registerInspectedRepo(command, deps, paths, store, repo, managed, logger); err != nil {
		return err
	}
	return renderRepoAdded(command, repo)
}

func registerInspectedRepo(
	command *cobra.Command,
	deps *invocationDependencies,
	paths state.Paths,
	store registry.Store,
	repo registry.Repo,
	managed bool,
	logger *slog.Logger,
) error {
	return state.WithGlobalMutationLockLogger(command.Context(), paths, repoAddLockOperation, deps.logger, func() error {
		return registerInspectedRepoLocked(command, deps, store, repo, managed, logger)
	})
}

func registerInspectedRepoLocked(
	command *cobra.Command,
	deps *invocationDependencies,
	store registry.Store,
	repo registry.Repo,
	managed bool,
	logger *slog.Logger,
) error {
	registryCtx, err := loadRegistryContextFromStore(store)
	if err != nil {
		return err
	}
	reg := registryCtx.Registry
	if err := reg.Add(repo); err != nil {
		return err
	}
	logger.DebugContext(command.Context(), "validated registry update", slog.Int("repo_count", len(reg.Repos)))

	managedDir, err := initializeManagedRepoBeads(command, deps, registryCtx, repo, managed, logger)
	if err != nil {
		return err
	}
	if err := saveRepoRegistration(registryCtx, reg, managed, managedDir); err != nil {
		return err
	}
	logger.DebugContext(
		command.Context(),
		"saved repo registration",
		slog.String("repo_id", repo.ID),
		slog.String("beads_mode", repo.BeadsMode),
		slog.String("beads_prefix", repo.BeadsPrefix),
	)
	return nil
}

func renderRepoAdded(command *cobra.Command, repo registry.Repo) error {
	_, err := fmt.Fprintf(
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
}

func configureRepoGitValues(
	command *cobra.Command,
	repo *registry.Repo,
	inspection gitmeta.Inspection,
	logger *slog.Logger,
) error {
	remote, defaultBranch, err := confirmGitValues(command, inspection)
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
	return nil
}

func configureRepoBeads(
	command *cobra.Command,
	deps *invocationDependencies,
	repo *registry.Repo,
	repoRoot string,
	logger *slog.Logger,
) (bool, error) {
	span := logging.Start(command.Context(), deps.logger, "local Beads inspection",
		slog.String("component", "beads"),
		slog.String("operation", "inspect_local"),
		slog.String("path", repoRoot),
		slog.String("repo_id", repo.ID),
	)
	beadsInspection, err := deps.inspectLocalBeads(repoRoot, slog.String("repo_id", repo.ID))
	if err == nil {
		span.Finish(command.Context(), logging.StatusSuccess,
			slog.String("beads_dir", beadsInspection.BeadsDir),
		)
		repo.BeadsMode = registry.BeadsModeLocal
		repo.BeadsPrefix = beadsInspection.Prefix
		logger.DebugContext(
			command.Context(),
			"detected repo-local Beads mode",
			slog.String("beads_dir", beadsInspection.BeadsDir),
			slog.String("beads_prefix", repo.BeadsPrefix),
		)
		return false, nil
	}
	if !errors.Is(err, beads.ErrNoLocal) {
		span.FinishError(command.Context(), err)
		return false, err
	}
	span.Finish(command.Context(), logging.StatusExpectedAbsence)

	prefix, err := confirmManagedBeadsPrefix(command, repo.ID)
	if err != nil {
		return false, err
	}
	repo.BeadsMode = registry.BeadsModeManaged
	repo.BeadsPrefix = prefix
	logger.DebugContext(
		command.Context(),
		"selected managed Beads mode",
		slog.String("beads_prefix", repo.BeadsPrefix),
	)
	return true, nil
}

func configureRepoSummaryGuidance(command *cobra.Command, repo *registry.Repo, logger *slog.Logger) error {
	input := command.InOrStdin()
	if !isTerminal(input) {
		repo.SummaryGuidanceStyle = registry.SummaryGuidanceStyleTyped
		return nil
	}

	wizard := repoAddWizard{
		reader: bufio.NewReader(input),
		output: command.ErrOrStderr(),
	}
	style, err := wizard.promptValue(summaryGuidanceStylePrompt, registry.SummaryGuidanceStyleTyped, true)
	if err != nil {
		return err
	}
	style = strings.TrimSpace(style)
	if style == summaryGuidanceStyleCustom {
		repo.SummaryGuidanceStyle = registry.SummaryGuidanceStyleTyped
		guidance, err := wizard.promptValue("Custom summary guidance", "", true)
		if err != nil {
			return err
		}
		repo.SummaryGuidance = strings.TrimSpace(guidance)
		logger.DebugContext(
			command.Context(),
			"configured custom summary guidance",
			slog.String("summary_guidance_style", repo.SummaryGuidanceStyle),
		)
		return nil
	}
	if err := registry.ValidateSummaryGuidanceStyle(style); err != nil {
		return err
	}
	repo.SummaryGuidanceStyle = style
	logger.DebugContext(
		command.Context(),
		"configured summary guidance",
		slog.String("summary_guidance_style", repo.SummaryGuidanceStyle),
	)
	return nil
}

func configureRepoTitleTemplate(command *cobra.Command, repo *registry.Repo, logger *slog.Logger) error {
	input := command.InOrStdin()
	if !isTerminal(input) {
		return nil
	}

	wizard := repoAddWizard{
		reader: bufio.NewReader(input),
		output: command.ErrOrStderr(),
	}
	template, err := wizard.promptValue("Publication title template", "", false)
	if err != nil {
		return err
	}
	template = strings.TrimSpace(template)
	if err := publication.ValidateTitleTemplate(template); err != nil {
		return err
	}
	repo.TitleTemplate = template
	logger.DebugContext(
		command.Context(),
		"configured publication title template",
		slog.Bool("title_template_set", repo.TitleTemplate != ""),
	)
	return nil
}

func initializeManagedRepoBeads(
	command *cobra.Command,
	deps *invocationDependencies,
	registryCtx registryContext,
	repo registry.Repo,
	managed bool,
	logger *slog.Logger,
) (string, error) {
	if !managed {
		return "", nil
	}

	managedDir, err := registryCtx.Store.ManagedBeadsDir(repo.ID)
	if err != nil {
		return "", err
	}
	logger.DebugContext(command.Context(), "initializing managed Beads", slog.String("beads_dir", managedDir))
	if err := deps.initializeBeads(managedDir, repo.BeadsPrefix, slog.String("repo_id", repo.ID)); err != nil {
		return "", err
	}
	return managedDir, nil
}

func saveRepoRegistration(registryCtx registryContext, reg registry.Registry, managed bool, managedDir string) error {
	if err := registryCtx.Store.Save(reg); err != nil {
		if managed {
			return fmt.Errorf("managed Beads was initialized at %q, but saving the repo registry failed; remove that directory before retrying if you do not want to keep it: %w", managedDir, err)
		}
		return err
	}
	return nil
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

			deps, err := opts.invocation(command)
			if err != nil {
				return err
			}
			registryCtx, err := loadRegistryContextFromInvocation(deps)
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

			deps, err := opts.invocation(command)
			if err != nil {
				return err
			}
			registryCtx, err := loadRegistryContextFromInvocation(deps)
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

	if !isTerminal(input) {
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

	if !isTerminal(input) {
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
	if err := w.presentRemoteInspection(inspection); err != nil {
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

func (w repoAddWizard) presentRemoteInspection(inspection gitmeta.Inspection) error {
	if inspection.RemoteErr == nil {
		_, err := fmt.Fprintf(w.output, "  git remote: %s (%s)\n", inspection.RemoteCandidate, inspection.RemoteCandidateName)
		return err
	}
	if errors.Is(inspection.RemoteErr, gitmeta.ErrNoRemote) {
		_, err := fmt.Fprintln(w.output, "  git remote: not detected")
		return err
	}
	_, err := fmt.Fprintf(w.output, "  git remote: not detected (%v)\n", inspection.RemoteErr)
	return err
}

func (w repoAddWizard) promptValue(label string, defaultValue string, required bool) (string, error) {
	defaultValue = strings.TrimSpace(defaultValue)
	if err := w.promptLabel(label, defaultValue, required); err != nil {
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

func (w repoAddWizard) promptLabel(label string, defaultValue string, required bool) error {
	if defaultValue != "" {
		_, err := fmt.Fprintf(w.output, "%s [%s]: ", label, defaultValue)
		return err
	}
	if required {
		_, err := fmt.Fprintf(w.output, "%s: ", label)
		return err
	}
	_, err := fmt.Fprintf(w.output, "%s (optional): ", label)
	return err
}

func readerIsTerminal(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	if !ok {
		return false
	}

	return fileDescriptorIsTerminal(file.Fd())
}

func writerIsTerminal(writer io.Writer) bool {
	return inspectWriterTerminal(writer).interactive
}

type writerTerminalInspection struct {
	interactive bool
	writerType  string
	isFile      bool
	fd          uintptr
	name        string
	statMode    string
	statError   string
}

func inspectWriterTerminal(writer io.Writer) writerTerminalInspection {
	inspection := writerTerminalInspection{writerType: fmt.Sprintf("%T", writer)}
	file, ok := writer.(*os.File)
	if !ok {
		return inspection
	}
	inspection.isFile = true
	inspection.fd = file.Fd()
	inspection.name = file.Name()

	stat, err := file.Stat()
	if err != nil {
		inspection.statError = err.Error()
	} else {
		inspection.statMode = stat.Mode().String()
	}
	inspection.interactive = fileDescriptorIsTerminal(inspection.fd)
	return inspection
}
