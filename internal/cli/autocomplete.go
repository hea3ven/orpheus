package cli

import (
	"context"
	"os"
	"sort"
	"strings"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/revieweval"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/spf13/cobra"
)

// configureCompletions adds best-effort, read-only value completion to the CLI.
// Completion callbacks deliberately discard data-source failures: a shell must
// remain responsive even when a repository or configuration file is unavailable.
func configureCompletions(root *cobra.Command, opts *rootOptions) {
	complete := newCompletionProvider(opts)
	configureRepoCompletions(commandAt(root, "repo"), complete)
	configureTaskCompletions(commandAt(root, "task"), complete)
	registerCompletion(commandAt(root, "status"), "sort", fixedCompletion(taskViewSortValues()...))
	registerCompletion(commandAt(commandAt(root, "task"), "list"), "sort", fixedCompletion(taskViewSortValues()...))

	agentReviewAdd := commandAt(commandAt(commandAt(root, "agent"), "review"), "add")
	registerCompletion(agentReviewAdd, "type", fixedCompletion("blocking", "advisory", "separate-task"))

	evalReviewContext := commandAt(commandAt(root, "eval"), "review-context")
	registerCompletion(evalReviewContext, "harness", commaSeparatedFixedCompletion(revieweval.HarnessPi, revieweval.HarnessCodex, "all"))
	registerCompletion(evalReviewContext, "variant", commaSeparatedFixedCompletion(revieweval.VariantLegacy, revieweval.VariantExhaustive, "all"))
	registerCompletion(evalReviewContext, "scenario", commaSeparatedFixedCompletion(revieweval.ScenarioGeneral, revieweval.ScenarioArchitecture, "all"))
}

func configureRepoCompletions(repo *cobra.Command, complete completionProvider) {
	repoConfig := commandAt(repo, "config")
	commandAt(repoConfig, "get").ValidArgsFunction = complete.repoConfigArguments
	commandAt(repoConfig, "set").ValidArgsFunction = complete.repoConfigArguments
	commandAt(repo, "beads-dir").ValidArgsFunction = complete.repositories
	// repo add deliberately has no argument completion function, preserving normal
	// directory completion from the generated shell scripts.
}

func configureTaskCompletions(task *cobra.Command, complete completionProvider) {
	taskList := commandAt(task, "list")
	taskCreate := commandAt(task, "create")
	taskShow := commandAt(task, "show")
	taskStats := commandAt(task, "stats")
	taskDir := commandAt(task, "dir")
	taskRun := commandAt(task, "run")
	taskDone := commandAt(task, "done")
	taskReview := commandAt(task, "review")
	taskReviewShow := commandAt(taskReview, "show")
	taskSync := commandAt(task, "sync")
	taskEdit := commandAt(task, "edit")
	taskStart := commandAt(task, "start")
	taskClose := commandAt(task, "close")

	taskShow.ValidArgsFunction = complete.inspectItems
	taskStats.ValidArgsFunction = complete.inspectItems
	taskDir.ValidArgsFunction = complete.activeTasks
	taskRun.ValidArgsFunction = complete.activeTasks
	taskDone.ValidArgsFunction = complete.activeTasks
	taskReview.ValidArgsFunction = complete.activeTasks
	taskReviewShow.ValidArgsFunction = complete.inspectItems
	taskSync.ValidArgsFunction = complete.activeTasks
	taskEdit.ValidArgsFunction = complete.activeItems
	taskStart.ValidArgsFunction = complete.openEpics
	taskClose.ValidArgsFunction = complete.inProgressEpics

	registerCompletion(taskList, "repo", complete.repositories)
	registerCompletion(taskCreate, "repo", complete.repositories)
	registerCompletion(taskCreate, "type", fixedCompletion("task", "epic"))
	registerCompletion(taskCreate, "parent", complete.activeEpicsForRepository)
	registerCompletion(taskCreate, "blocked-by", complete.dependenciesForRepository)
	registerCompletion(taskStats, "repo", complete.repositories)
	registerCompletion(taskStats, "group", fixedCompletion("day", "week", "month"))
	registerCompletion(taskStats, "view", fixedCompletion(
		"throughput", "implementation", "review", "consumption", "implementation-model", "reviewer-model", "model-pair",
	))
	registerCompletion(taskRun, "agent", complete.agentProfiles)
	registerCompletion(taskRun, "pipeline", complete.pipelinesForTask)
	registerCompletion(taskReview, "pipeline", complete.pipelinesForTask)
	registerCompletion(taskEdit, "parent", complete.activeEpicsForTask)
	registerCompletion(taskEdit, "add-block", complete.dependenciesForTask)
	registerCompletion(taskEdit, "remove-block", complete.dependenciesForTask)
}

func commandAt(command *cobra.Command, names ...string) *cobra.Command {
	for _, name := range names {
		for _, child := range command.Commands() {
			if child.Name() == name {
				command = child
				break
			}
		}
	}
	return command
}

func registerCompletion(command *cobra.Command, flag string, function cobra.CompletionFunc) {
	if err := command.RegisterFlagCompletionFunc(flag, function); err != nil {
		panic(err)
	}
}

func fixedCompletion(values ...string) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		return filterCompletions(values, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
}

func commaSeparatedFixedCompletion(values ...string) cobra.CompletionFunc {
	return func(_ *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
		prefix := ""
		if separator := strings.LastIndex(toComplete, ","); separator >= 0 {
			prefix, toComplete = toComplete[:separator+1], toComplete[separator+1:]
		}
		completions := filterCompletions(values, strings.TrimSpace(toComplete))
		for i := range completions {
			completions[i] = prefix + completions[i]
		}
		return completions, cobra.ShellCompDirectiveNoFileComp
	}
}

type completionProvider struct {
	opts *rootOptions
}

func newCompletionProvider(opts *rootOptions) completionProvider {
	return completionProvider{opts: opts}
}

func (p completionProvider) repositories(command *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	registry, ok := p.registry(command)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	values := make([]string, 0, len(registry.Repos))
	for _, repo := range registry.Repos {
		values = append(values, completionWithDescription(repo.ID, repo.Name+" ("+repo.Path+")"))
	}
	return filterCompletions(values, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func (p completionProvider) repoConfigArguments(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	registry, ok := p.registry(command)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	if len(args) == 0 {
		values := make([]string, 0, len(registry.Repos))
		for _, repo := range registry.Repos {
			values = append(values, completionWithDescription(repo.ID, repo.Name+" ("+repo.Path+")"))
		}
		return filterCompletions(values, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
	if len(args) == 1 {
		values := repoConfigNames(registry, args[0])
		return filterCompletions(values, toComplete), cobra.ShellCompDirectiveNoFileComp
	}
	if len(args) == 2 && command.Name() == "set" {
		return p.repoConfigValues(command, args[1], toComplete)
	}
	return nil, cobra.ShellCompDirectiveNoFileComp
}

func repoConfigNames(reg registry.Registry, repository string) []string {
	values := []string{
		repoConfigSummaryGuidance,
		repoConfigSummaryStyle,
		repoConfigTitleTemplate,
		repoConfigBranchTemplate,
		repoConfigIntegrationFlow,
		repoConfigIncludePRReviewProcess,
		repoConfigReviewPipeline,
	}
	if repo, err := reg.Resolve(repository); err == nil {
		for alias := range repo.ReviewPipelineAliases {
			values = append(values, repoConfigReviewPipelineAliasPrefix+alias)
		}
	}
	return normalizedCompletions(values)
}

func (p completionProvider) repoConfigValues(command *cobra.Command, name string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	values := []string(nil)
	switch name {
	case repoConfigSummaryStyle:
		values = []string{registry.SummaryGuidanceStyleTyped, registry.SummaryGuidanceStyleCapitalized}
	case repoConfigIntegrationFlow:
		values = []string{string(publication.IntegrationFlowPullRequest), string(publication.IntegrationFlowDirectMerge)}
	case repoConfigIncludePRReviewProcess:
		values = []string{"true", "false"}
	default:
		if name == repoConfigReviewPipeline || isReviewPipelineConfigName(name) {
			if deps, ok := p.dependencies(command); ok {
				if config, err := review.LoadConfig(deps.paths); err == nil {
					for pipeline := range config.Pipelines {
						values = append(values, pipeline)
					}
				}
			}
		}
	}
	return filterCompletions(values, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func (p completionProvider) inspectItems(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 || taskStatsAggregateFlagsSelected(command) {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return p.tasks(command, toComplete, isTaskOrEpic, "")
}

func taskStatsAggregateFlagsSelected(command *cobra.Command) bool {
	if command.Name() != "stats" {
		return false
	}
	for _, flag := range []string{"group", "view", "from", "to", "repo"} {
		if current := command.Flags().Lookup(flag); current != nil && current.Changed {
			return true
		}
	}
	return false
}

func (p completionProvider) activeItems(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return p.tasks(command, toComplete, isActiveTaskOrEpic, "")
}

func (p completionProvider) activeTasks(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return p.tasks(command, toComplete, isActiveTask, "")
}

func (p completionProvider) openEpics(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return p.tasks(command, toComplete, func(item taskmodel.Task) bool {
		return item.IssueType == taskmodel.IssueTypeEpic && item.Status == taskmodel.StatusOpen
	}, "")
}

func (p completionProvider) inProgressEpics(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return p.tasks(command, toComplete, func(item taskmodel.Task) bool {
		return item.IssueType == taskmodel.IssueTypeEpic && item.Status == taskmodel.StatusInProgress
	}, "")
}

func (p completionProvider) activeEpicsForRepository(command *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	repository, ok := p.selectedRepository(command)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return p.tasks(command, toComplete, func(item taskmodel.Task) bool {
		return isActiveTaskOrEpic(item) && item.IssueType == taskmodel.IssueTypeEpic
	}, repository)
}

func (p completionProvider) dependenciesForRepository(command *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	repository, ok := p.selectedRepository(command)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return p.tasks(command, toComplete, isActiveTaskOrEpic, repository)
}

func (p completionProvider) activeEpicsForTask(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	repository := p.repositoryForTaskArgument(command, args)
	exclude := ""
	if len(args) > 0 {
		exclude = strings.TrimSpace(args[0])
	}
	return p.tasks(command, toComplete, func(item taskmodel.Task) bool {
		return item.ID != exclude && isActiveTaskOrEpic(item) && item.IssueType == taskmodel.IssueTypeEpic
	}, repository)
}

func (p completionProvider) dependenciesForTask(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	repository := p.repositoryForTaskArgument(command, args)
	exclude := ""
	if len(args) > 0 {
		exclude = strings.TrimSpace(args[0])
	}
	return p.tasks(command, toComplete, func(item taskmodel.Task) bool {
		return item.ID != exclude && isActiveTaskOrEpic(item)
	}, repository)
}

func isTaskOrEpic(item taskmodel.Task) bool {
	return item.IssueType == taskmodel.IssueTypeTask || item.IssueType == taskmodel.IssueTypeEpic
}

func isActiveTaskOrEpic(item taskmodel.Task) bool {
	return item.Status != taskmodel.StatusClosed && isTaskOrEpic(item)
}

func isActiveTask(item taskmodel.Task) bool {
	return item.Status != taskmodel.StatusClosed && item.IssueType == taskmodel.IssueTypeTask
}

func (p completionProvider) tasks(command *cobra.Command, toComplete string, include func(taskmodel.Task) bool, repository string) ([]cobra.Completion, cobra.ShellCompDirective) {
	deps, ok := p.dependencies(command)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	registryCtx, err := loadRegistryContextFromInvocation(deps)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	sources := completionTaskRepositorySources(registryCtx)
	aggregator, err := taskmodel.NewAggregatorWithLogger(sources, deps.taskBackendFactory, nil)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	snapshot := aggregator.Snapshot(context.Background())
	values := make([]string, 0)
	for _, repoSnapshot := range snapshot.Repositories {
		if repository != "" && repoSnapshot.Repository.ID != repository {
			continue
		}
		for _, item := range repoSnapshot.Tasks {
			if include(item) {
				values = append(values, completionWithDescription(item.ID, item.Title+" ("+repoSnapshot.Repository.ID+")"))
			}
		}
	}
	return filterCompletions(values, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func (p completionProvider) agentProfiles(command *cobra.Command, _ []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	deps, ok := p.dependencies(command)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	config, err := agent.LoadConfig(deps.paths)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	values := make([]string, 0, len(config.Agents))
	for name := range config.Agents {
		values = append(values, name)
	}
	return filterCompletions(values, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func (p completionProvider) pipelinesForTask(command *cobra.Command, args []string, toComplete string) ([]cobra.Completion, cobra.ShellCompDirective) {
	deps, ok := p.dependencies(command)
	if !ok {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	values := make([]string, 0)
	if config, err := review.LoadConfig(deps.paths); err == nil {
		for name := range config.Pipelines {
			values = append(values, name)
		}
	}
	if registry, ok := p.registry(command); ok {
		if repoID := p.repositoryForTaskArgument(command, args); repoID != "" {
			for _, repo := range registry.Repos {
				if repo.ID != repoID {
					continue
				}
				for alias := range repo.ReviewPipelineAliases {
					values = append(values, alias)
				}
			}
		}
	}
	return filterCompletions(values, toComplete), cobra.ShellCompDirectiveNoFileComp
}

func (p completionProvider) registry(command *cobra.Command) (registry.Registry, bool) {
	deps, ok := p.dependencies(command)
	if !ok {
		return registry.Registry{}, false
	}
	registryCtx, err := loadRegistryContextFromInvocation(deps)
	if err != nil {
		return registry.Registry{}, false
	}
	return registryCtx.Registry, true
}

func (p completionProvider) dependencies(command *cobra.Command) (*invocationDependencies, bool) {
	deps, err := p.opts.invocation(command)
	if err != nil {
		return nil, false
	}
	return deps, true
}

// completionTaskRepositorySources projects each registered repository independently.
// Completion is best-effort, so an outdated or otherwise unprojectable entry must
// not prevent snapshots from healthy repositories.
func completionTaskRepositorySources(registryCtx registryContext) []taskmodel.RepositorySource {
	sources := make([]taskmodel.RepositorySource, 0, len(registryCtx.Registry.Repos))
	for _, repo := range registryCtx.Registry.Repos {
		source, err := registryCtx.Store.TaskRepositorySource(repo)
		if err != nil {
			continue
		}
		sources = append(sources, source)
	}
	return sources
}

// selectedRepository applies the same precedence as task creation. If that
// selection is ambiguous or unavailable, relation completion returns nothing
// rather than suggesting cross-repository values that creation would reject.
func (p completionProvider) selectedRepository(command *cobra.Command) (string, bool) {
	deps, ok := p.dependencies(command)
	if !ok {
		return "", false
	}
	registryCtx, err := loadRegistryContextFromInvocation(deps)
	if err != nil {
		return "", false
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	repository, err := taskmodel.ResolveCreationSource(completionTaskRepositorySources(registryCtx), taskmodel.CreationSourceOptions{
		Repository:         flagString(command, "repo"),
		ActiveRepositoryID: deps.environmentValue("ORPHEUS_REPO_ID"),
		CurrentDirectory:   cwd,
	})
	if err != nil {
		return "", false
	}
	return repository.Repository.ID, true
}

func flagString(command *cobra.Command, name string) string {
	value, err := command.Flags().GetString(name)
	if err != nil {
		return ""
	}
	return value
}

func (p completionProvider) repositoryForTaskArgument(command *cobra.Command, args []string) string {
	if len(args) == 0 {
		return ""
	}
	registry, ok := p.registry(command)
	if !ok {
		return ""
	}
	for _, repo := range registry.Repos {
		prefix := strings.TrimSpace(repo.BeadsPrefix)
		if prefix != "" && strings.HasPrefix(strings.TrimSpace(args[0]), prefix+"-") {
			return repo.ID
		}
	}
	return ""
}

const completionDescriptionMaxRunes = 96

func completionWithDescription(value string, description string) string {
	value = strings.TrimSpace(value)
	descriptionRunes := []rune(strings.Join(strings.Fields(description), " "))
	if len(descriptionRunes) > completionDescriptionMaxRunes {
		descriptionRunes = append(descriptionRunes[:completionDescriptionMaxRunes-len("...")], '.', '.', '.')
	}
	return cobra.CompletionWithDesc(value, string(descriptionRunes))
}

func filterCompletions(values []string, toComplete string) []cobra.Completion {
	values = normalizedCompletions(values)
	filtered := make([]cobra.Completion, 0, len(values))
	for _, value := range values {
		choice, _, _ := strings.Cut(value, "\t")
		if strings.HasPrefix(choice, toComplete) {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

func normalizedCompletions(values []string) []string {
	unique := make(map[string]string, len(values))
	for _, value := range values {
		choice, _, _ := strings.Cut(value, "\t")
		choice = strings.TrimSpace(choice)
		if choice == "" {
			continue
		}
		if _, exists := unique[choice]; !exists {
			unique[choice] = value
		}
	}
	choices := make([]string, 0, len(unique))
	for _, value := range unique {
		choices = append(choices, value)
	}
	sort.Slice(choices, func(i, j int) bool {
		left, _, _ := strings.Cut(choices[i], "\t")
		right, _, _ := strings.Cut(choices[j], "\t")
		return left < right
	})
	return choices
}
