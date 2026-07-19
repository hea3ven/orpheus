package cli

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

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

	cmd.AddCommand(newAgentContextCommand(opts), newAgentDoneCommand(opts), newAgentReviewCommand(opts))
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
	var description string
	var detailedDescription string
	var detailedDescriptionFile string
	var technicalExplanation string
	var technicalExplanationFile string
	cmd := &cobra.Command{
		Use:   "done",
		Short: "Record active agent completion",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runAgentDone(command, opts, agentDoneOptions{
				summary:                  summary,
				description:              description,
				detailedDescription:      detailedDescription,
				detailedDescriptionFile:  detailedDescriptionFile,
				technicalExplanation:     technicalExplanation,
				technicalExplanationFile: technicalExplanationFile,
			})
		},
	}
	cmd.Flags().StringVar(&summary, "summary", "", "short completion summary")
	cmd.Flags().StringVar(&description, "description", "", "concise commit-body completion description")
	cmd.Flags().StringVar(&detailedDescription, "detailed-description", "", "markdown pull request body")
	cmd.Flags().StringVar(&detailedDescriptionFile, "detailed-description-file", "", "path to markdown pull request body")
	cmd.Flags().StringVar(&technicalExplanation, "technical-explanation", "", "markdown technical explanation of code changes")
	cmd.Flags().StringVar(&technicalExplanationFile, "technical-explanation-file", "", "path to markdown technical explanation of code changes")
	_ = cmd.MarkFlagRequired("summary")
	_ = cmd.MarkFlagRequired("description")
	return cmd
}

func newAgentReviewCommand(opts *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Agent-facing review commands",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newAgentReviewAddCommand(opts))
	return cmd
}

func newAgentReviewAddCommand(opts *rootOptions) *cobra.Command {
	var findingType string
	var title string
	var description string
	var descriptionFile string
	var suggestedAction string
	var taskTitle string
	var taskDescription string
	var taskDescriptionFile string
	var taskAcceptanceCriteria string
	var taskAcceptanceCriteriaFile string

	cmd := &cobra.Command{
		Use:   "add",
		Short: "Record a finding from an active review agent",
		Long: "Record a finding from an active review agent.\n\n" +
			"Use this only from an attached agent_review pipeline step. Blocking findings " +
			"stop approval and should include --suggested-action for the follow-up task run. " +
			"Advisory findings are recorded without blocking. Separate-task findings propose " +
			"standalone follow-up work that task review may create as Beads with provenance.\n\n" +
			"After the review agent exits, task review records the step outcome. Operators " +
			"inspect findings with task review show, run task run for open blockers, and " +
			"rerun task review after follow-up completion.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runAgentReviewAdd(command, opts, agentReviewAddOptions{
				findingType:                findingType,
				title:                      title,
				description:                description,
				descriptionFile:            descriptionFile,
				suggestedAction:            suggestedAction,
				taskTitle:                  taskTitle,
				taskDescription:            taskDescription,
				taskDescriptionFile:        taskDescriptionFile,
				taskAcceptanceCriteria:     taskAcceptanceCriteria,
				taskAcceptanceCriteriaFile: taskAcceptanceCriteriaFile,
			})
		},
	}
	cmd.Flags().StringVar(&findingType, "type", "", "finding type: blocking, advisory, or separate-task")
	cmd.Flags().StringVar(&title, "title", "", "finding title")
	cmd.Flags().StringVar(&description, "description", "", "finding description")
	cmd.Flags().StringVar(&descriptionFile, "description-file", "", "path to finding description")
	cmd.Flags().StringVar(&suggestedAction, "suggested-action", "", "suggested action for blocking findings")
	cmd.Flags().StringVar(&taskTitle, "task-title", "", "separate task title")
	cmd.Flags().StringVar(&taskDescription, "task-description", "", "separate task description")
	cmd.Flags().StringVar(&taskDescriptionFile, "task-description-file", "", "path to separate task description")
	cmd.Flags().StringVar(&taskAcceptanceCriteria, "task-acceptance-criteria", "", "separate task acceptance criteria")
	cmd.Flags().StringVar(&taskAcceptanceCriteriaFile, "task-acceptance-criteria-file", "", "path to separate task acceptance criteria")
	_ = cmd.MarkFlagRequired("type")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func runAgentContext(command *cobra.Command, opts *rootOptions) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "agent_context"),
	)
	logger.DebugContext(command.Context(), "resolving active agent context")

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContextFromInvocation(deps)
	if err != nil {
		return err
	}
	paths := deps.paths

	resolver := activeAgentContextResolver(deps, taskCtx, deps.taskStateStore)
	switch strings.TrimSpace(os.Getenv("ORPHEUS_AGENT_PURPOSE")) {
	case "", "implementation":
	case "review":
		reviewContext, err := resolver.ResolveReview(command.Context())
		if err != nil {
			return fmt.Errorf("agent context: %w", err)
		}
		_, err = fmt.Fprint(command.OutOrStdout(), agent.RenderReviewContext(reviewContext))
		return err
	case "conflict_resolution":
		conflictContext, err := resolver.ResolveConflictResolution(command.Context())
		if err != nil {
			return fmt.Errorf("agent context: %w", err)
		}
		_, err = fmt.Fprint(command.OutOrStdout(), agent.RenderConflictResolutionContext(conflictContext))
		return err
	default:
		return fmt.Errorf("agent context: unsupported ORPHEUS_AGENT_PURPOSE %q", os.Getenv("ORPHEUS_AGENT_PURPOSE"))
	}
	activeContext, err := resolver.Resolve(command.Context())
	if err != nil {
		return fmt.Errorf("agent context: %w", err)
	}
	interactionMode, err := activeAgentInteractionMode(paths, activeContext.Run.Agent)
	if err != nil {
		return fmt.Errorf("agent context: %w", err)
	}

	_, err = fmt.Fprint(command.OutOrStdout(), agent.RenderActiveContextWithOptions(
		activeContext,
		agent.ActiveContextRenderOptions{
			InteractionMode: interactionMode,
		},
	))
	return err
}

func activeAgentInteractionMode(paths state.Paths, agentName string) (agent.AgentInteractionMode, error) {
	config, err := agent.LoadConfig(paths)
	if err != nil {
		return agent.AgentInteractionModeUnspecified, err
	}
	_, profile, err := config.ResolveImplementerProfile(agentName)
	if err != nil {
		return agent.AgentInteractionModeUnspecified, fmt.Errorf("resolve agent profile: %w", err)
	}
	if profile.Interactive {
		return agent.AgentInteractionModeInteractive, nil
	}
	return agent.AgentInteractionModeNonInteractive, nil
}

type agentReviewAddOptions struct {
	findingType                string
	title                      string
	description                string
	descriptionFile            string
	suggestedAction            string
	taskTitle                  string
	taskDescription            string
	taskDescriptionFile        string
	taskAcceptanceCriteria     string
	taskAcceptanceCriteriaFile string
}

func runAgentReviewAdd(command *cobra.Command, opts *rootOptions, addOpts agentReviewAddOptions) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "agent_review_add"),
	)
	logger.DebugContext(command.Context(), "resolving active review agent context")

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContextFromInvocation(deps)
	if err != nil {
		return err
	}
	store := deps.taskStateStore
	resolver := activeAgentContextResolver(deps, taskCtx, store)
	reviewContext, err := resolver.ResolveReview(command.Context())
	if err != nil {
		return fmt.Errorf("agent review add: %w", err)
	}

	finding, err := buildAgentReviewFinding(reviewContext, addOpts)
	if err != nil {
		return fmt.Errorf("agent review add: %w", err)
	}
	reviewAttempt, err := store.RecordReviewFinding(
		reviewContext.Repository.ID,
		reviewContext.Task.ID,
		reviewContext.Review.Attempt,
		finding,
	)
	if err != nil {
		return fmt.Errorf("agent review add: %w", err)
	}
	logger.DebugContext(
		command.Context(),
		"recorded review finding",
		slog.String("repo_id", reviewContext.Repository.ID),
		slog.String("task_id", reviewContext.Task.ID),
		slog.Int("review_attempt", reviewContext.Review.Attempt),
		slog.String("finding_type", string(finding.Type)),
	)
	_, err = fmt.Fprintf(
		command.OutOrStdout(),
		"Recorded %s review finding %d for %s.\n",
		addOpts.findingType,
		len(reviewAttempt.Findings),
		reviewContext.Task.ID,
	)
	return err
}

func buildAgentReviewFinding(ctx agent.ReviewContext, opts agentReviewAddOptions) (taskstate.ReviewFinding, error) {
	findingType, err := parseAgentReviewFindingType(opts.findingType)
	if err != nil {
		return taskstate.ReviewFinding{}, err
	}
	title := strings.TrimSpace(opts.title)
	if title == "" {
		return taskstate.ReviewFinding{}, errors.New("title is required")
	}
	description, err := resolveExactlyOneText("description", opts.description, opts.descriptionFile)
	if err != nil {
		return taskstate.ReviewFinding{}, err
	}
	suggestedAction := strings.TrimSpace(opts.suggestedAction)
	if findingType == taskstate.FindingTypeBlocking && suggestedAction == "" {
		return taskstate.ReviewFinding{}, errors.New("blocking findings require --suggested-action")
	}

	finding := taskstate.ReviewFinding{
		Type:            findingType,
		Title:           title,
		Description:     description,
		Step:            ctx.Review.Step,
		SuggestedAction: suggestedAction,
	}
	if findingType == taskstate.FindingTypeSeparateTask {
		taskProposal, err := buildSeparateTaskProposal(opts)
		if err != nil {
			return taskstate.ReviewFinding{}, err
		}
		finding.TaskProposal = taskProposal
	}
	return finding, nil
}

func parseAgentReviewFindingType(raw string) (taskstate.FindingType, error) {
	switch strings.TrimSpace(raw) {
	case "blocking":
		return taskstate.FindingTypeBlocking, nil
	case "advisory":
		return taskstate.FindingTypeAdvisory, nil
	case "separate-task":
		return taskstate.FindingTypeSeparateTask, nil
	default:
		return "", errors.New("type must be exactly one of blocking, advisory, or separate-task")
	}
}

func buildSeparateTaskProposal(opts agentReviewAddOptions) (taskstate.ReviewTaskProposal, error) {
	title := strings.TrimSpace(opts.taskTitle)
	if title == "" {
		return taskstate.ReviewTaskProposal{}, errors.New("separate-task findings require --task-title")
	}
	description, err := resolveExactlyOneText("task description", opts.taskDescription, opts.taskDescriptionFile)
	if err != nil {
		return taskstate.ReviewTaskProposal{}, fmt.Errorf("separate-task findings require %w", err)
	}
	acceptanceCriteria, err := resolveExactlyOneText(
		"task acceptance criteria",
		opts.taskAcceptanceCriteria,
		opts.taskAcceptanceCriteriaFile,
	)
	if err != nil {
		return taskstate.ReviewTaskProposal{}, fmt.Errorf("separate-task findings require %w", err)
	}
	return taskstate.ReviewTaskProposal{
		Title:              title,
		Description:        description,
		AcceptanceCriteria: acceptanceCriteria,
	}, nil
}

func resolveExactlyOneText(label string, inline string, filePath string) (string, error) {
	hasInline := strings.TrimSpace(inline) != ""
	filePath = strings.TrimSpace(filePath)
	hasFile := filePath != ""
	switch {
	case hasInline && hasFile:
		return "", fmt.Errorf("use exactly one of --%s or --%s-file", flagLabel(label), flagLabel(label))
	case !hasInline && !hasFile:
		return "", fmt.Errorf("%s is required", label)
	case hasInline:
		return inline, nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read %s file: %w", label, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("%s is required; file is empty", label)
	}
	return string(data), nil
}

func flagLabel(label string) string {
	return strings.ReplaceAll(label, " ", "-")
}

type agentDoneOptions struct {
	summary                  string
	description              string
	detailedDescription      string
	detailedDescriptionFile  string
	technicalExplanation     string
	technicalExplanationFile string
}

func runAgentDone(
	command *cobra.Command,
	opts *rootOptions,
	doneOpts agentDoneOptions,
) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "agent_done"),
	)
	logger.DebugContext(command.Context(), "resolving active agent completion context")

	detailedDescription, err := resolveDetailedDescription(doneOpts.detailedDescription, doneOpts.detailedDescriptionFile)
	if err != nil {
		return fmt.Errorf("agent done: %w", err)
	}
	technicalExplanation, err := resolveTechnicalExplanation(doneOpts.technicalExplanation, doneOpts.technicalExplanationFile)
	if err != nil {
		return fmt.Errorf("agent done: %w", err)
	}

	deps, err := opts.invocation(command)
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContextFromInvocation(deps)
	if err != nil {
		return err
	}

	service := newAgentCompletionService(deps, taskCtx)
	completed, err := service.Complete(command.Context(), agent.CompleteOptions{
		Summary:              doneOpts.summary,
		Description:          doneOpts.description,
		DetailedDescription:  detailedDescription,
		TechnicalExplanation: technicalExplanation,
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
	return renderAgentDoneResult(command, completed)
}

func newAgentCompletionService(deps *invocationDependencies, taskCtx taskContext) agent.CompletionService {
	store := deps.taskStateStore
	return agent.CompletionService{
		Paths:    deps.paths,
		Resolver: activeAgentContextResolver(deps, taskCtx, store),
		RunStore: store,
		Logger:   deps.logger,
	}
}

func renderAgentDoneResult(command *cobra.Command, completed agent.CompleteResult) error {
	if completed.Repeated {
		_, err := fmt.Fprintf(
			command.OutOrStdout(),
			"Completion for %s was already recorded for this Orpheus run; no action taken. "+
				"Do not run `orpheus agent done` again after it succeeds. "+
				"The first completion remains authoritative, and post-done edits are not captured by this no-op. "+
				"A local diagnostic was recorded.\n",
			completed.Context.Task.ID,
		)
		return err
	}
	if completed.CommitError != nil {
		_, err := fmt.Fprintf(
			command.OutOrStdout(),
			"Recorded completion for %s, but commit creation failed: %v\n",
			completed.Context.Task.ID,
			completed.CommitError,
		)
		return err
	}
	if completed.Context.Target.Kind != agent.ExecutionTargetMain {
		_, err := fmt.Fprintf(
			command.OutOrStdout(),
			"Recorded completion for %s; ready for feature-branch review with `orpheus task review %s`.\n",
			completed.Context.Task.ID,
			completed.Context.Task.ID,
		)
		return err
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "Recorded completion for %s; ready for local review.\n", completed.Context.Task.ID)
	return err
}

func resolveDetailedDescription(inline string, filePath string) (string, error) {
	return resolveDoneText(doneTextOptions{
		label:      "detailed description",
		flag:       "detailed-description",
		fileFlag:   "detailed-description-file",
		readAction: "read detailed description file",
		inline:     inline,
		filePath:   filePath,
	})
}

func resolveTechnicalExplanation(inline string, filePath string) (string, error) {
	return resolveDoneText(doneTextOptions{
		label:      "technical explanation",
		flag:       "technical-explanation",
		fileFlag:   "technical-explanation-file",
		readAction: "read technical explanation file",
		inline:     inline,
		filePath:   filePath,
	})
}

type doneTextOptions struct {
	label      string
	flag       string
	fileFlag   string
	readAction string
	inline     string
	filePath   string
}

func resolveDoneText(opts doneTextOptions) (string, error) {
	hasInline := strings.TrimSpace(opts.inline) != ""
	filePath := strings.TrimSpace(opts.filePath)
	hasFile := filePath != ""
	switch {
	case hasInline && hasFile:
		return "", fmt.Errorf("use exactly one of --%s or --%s", opts.flag, opts.fileFlag)
	case !hasInline && !hasFile:
		return "", fmt.Errorf("%s is required; use --%s or --%s", opts.label, opts.flag, opts.fileFlag)
	case hasInline:
		return opts.inline, nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("%s: %w", opts.readAction, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", fmt.Errorf("%s is required; file is empty", opts.label)
	}
	return string(data), nil
}

func activeAgentContextResolver(
	deps *invocationDependencies,
	taskCtx taskContext,
	runStore agent.ContextStateLoader,
) agent.ActiveContextResolver {
	return agent.ActiveContextResolver{
		Paths:    deps.paths,
		Registry: taskCtx.Registry,
		Sources:  taskCtx.Sources,
		BackendFactory: func(source taskmodel.RepositorySource) (agent.ContextBackend, error) {
			backend, err := deps.taskBackendFactory(source)
			if err != nil {
				return nil, err
			}
			contextBackend, ok := backend.(agent.ContextBackend)
			if !ok {
				return nil, fmt.Errorf("backend for repo %s does not support agent context", source.Repository.ID)
			}
			return contextBackend, nil
		},
		RunStore: runStore,
	}
}
