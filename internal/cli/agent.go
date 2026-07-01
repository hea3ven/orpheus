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

const envExperimentalInteractiveAgentGuidance = "ORPHEUS_EXPERIMENTAL_INTERACTIVE_AGENT_GUIDANCE"

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
	cmd := &cobra.Command{
		Use:   "done",
		Short: "Record active agent completion",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runAgentDone(command, opts, summary, description, detailedDescription, detailedDescriptionFile)
		},
	}
	cmd.Flags().StringVar(&summary, "summary", "", "short completion summary")
	cmd.Flags().StringVar(&description, "description", "", "concise commit-body completion description")
	cmd.Flags().StringVar(&detailedDescription, "detailed-description", "", "markdown pull request body")
	cmd.Flags().StringVar(&detailedDescriptionFile, "detailed-description-file", "", "path to markdown pull request body")
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
		Args:  cobra.NoArgs,
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

	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}

	resolver := activeAgentContextResolver(paths, taskCtx, taskstate.NewStore(paths))
	switch strings.TrimSpace(os.Getenv("ORPHEUS_AGENT_PURPOSE")) {
	case "", "implementation":
	case "review":
		reviewContext, err := resolver.ResolveReview(command.Context())
		if err != nil {
			return fmt.Errorf("agent context: %w", err)
		}
		_, err = fmt.Fprint(command.OutOrStdout(), agent.RenderReviewContext(reviewContext))
		return err
	default:
		return fmt.Errorf("agent context: unsupported ORPHEUS_AGENT_PURPOSE %q", os.Getenv("ORPHEUS_AGENT_PURPOSE"))
	}
	activeContext, err := resolver.Resolve(command.Context())
	if err != nil {
		return fmt.Errorf("agent context: %w", err)
	}

	_, err = fmt.Fprint(command.OutOrStdout(), agent.RenderActiveContextWithOptions(
		activeContext,
		agent.ActiveContextRenderOptions{
			InteractiveAgentGuidance: experimentalInteractiveAgentGuidanceEnabled(),
		},
	))
	return err
}

func experimentalInteractiveAgentGuidanceEnabled() bool {
	return strings.TrimSpace(os.Getenv(envExperimentalInteractiveAgentGuidance)) == "1"
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

	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}
	store := taskstate.NewStore(paths)
	resolver := activeAgentContextResolver(paths, taskCtx, store)
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

func runAgentDone(
	command *cobra.Command,
	opts *rootOptions,
	summary string,
	description string,
	detailedDescription string,
	detailedDescriptionFile string,
) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "agent_done"),
	)
	logger.DebugContext(command.Context(), "resolving active agent completion context")

	detailedDescription, err := resolveDetailedDescription(detailedDescription, detailedDescriptionFile)
	if err != nil {
		return fmt.Errorf("agent done: %w", err)
	}

	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return err
	}
	taskCtx, err := loadTaskContext()
	if err != nil {
		return err
	}

	service := newAgentCompletionService(paths, taskCtx)
	completed, err := service.Complete(command.Context(), agent.CompleteOptions{
		Summary:             summary,
		Description:         description,
		DetailedDescription: detailedDescription,
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

func newAgentCompletionService(paths state.Paths, taskCtx taskContext) agent.CompletionService {
	store := taskstate.NewStore(paths)
	return agent.CompletionService{
		Paths:    paths,
		Resolver: activeAgentContextResolver(paths, taskCtx, store),
		RunStore: store,
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
	hasInline := strings.TrimSpace(inline) != ""
	filePath = strings.TrimSpace(filePath)
	hasFile := filePath != ""
	switch {
	case hasInline && hasFile:
		return "", errors.New("use exactly one of --detailed-description or --detailed-description-file")
	case !hasInline && !hasFile:
		return "", errors.New("detailed description is required; use --detailed-description or --detailed-description-file")
	case hasInline:
		return inline, nil
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read detailed description file: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", errors.New("detailed description is required; file is empty")
	}
	return string(data), nil
}

func activeAgentContextResolver(
	paths state.Paths,
	taskCtx taskContext,
	runStore taskstate.Service,
) agent.ActiveContextResolver {
	return agent.ActiveContextResolver{
		Paths:    paths,
		Registry: taskCtx.Registry,
		Sources:  taskCtx.Sources,
		BackendFactory: func(source taskmodel.RepositorySource) (agent.ContextBackend, error) {
			return newBeadsTaskBackend(source.BackendDir)
		},
		RunStore: runStore,
	}
}
