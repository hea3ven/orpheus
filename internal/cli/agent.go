package cli

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

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
	var commitSubject string
	var commitBody string
	var commitBodyFile string
	var publicationCommitSubject string
	var publicationCommitBody string
	var publicationCommitBodyFile string
	var prDescription string
	var prDescriptionFile string
	var technicalExplanation string
	var technicalExplanationFile string
	cmd := &cobra.Command{
		Use:   "done",
		Short: "Record local effort completion and optional cumulative publication updates",
		Long:  "Record this effort's local commit subject/body and reviewer-facing technical explanation.\n\nThe first handoff requires a cumulative PR description and seeds workflow publication commit text\nfrom the local message unless explicitly overridden. Later handoffs replace only supplied publication\nfields. Publication content is read-only after the first operator proceed; review approval alone does\nnot lock it. Each body/description accepts one inline or file source. This records neither an exit\nnor approval: Orpheus captures local work only after the agent exits successfully.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			return runAgentDone(command, opts, agentDoneOptions{
				commitSubject:             commitSubject,
				commitBody:                commitBody,
				commitBodyFile:            commitBodyFile,
				publicationCommitSubject:  publicationCommitSubject,
				publicationCommitBody:     publicationCommitBody,
				publicationCommitBodyFile: publicationCommitBodyFile,
				prDescription:             prDescription,
				prDescriptionFile:         prDescriptionFile,
				technicalExplanation:      technicalExplanation,
				technicalExplanationFile:  technicalExplanationFile,
			})
		},
	}
	cmd.Flags().StringVar(&commitSubject, "commit-subject", "", "subject of this effort's local commit")
	cmd.Flags().StringVar(&commitBody, "commit-body", "", "body of this effort's local commit")
	cmd.Flags().StringVar(&commitBodyFile, "commit-body-file", "", "path to local commit body")
	cmd.Flags().StringVar(&publicationCommitSubject, "publication-commit-subject", "", "replace cumulative publication commit subject before operator proceed")
	cmd.Flags().StringVar(&publicationCommitBody, "publication-commit-body", "", "replace cumulative publication commit body before operator proceed")
	cmd.Flags().StringVar(&publicationCommitBodyFile, "publication-commit-body-file", "", "path to cumulative publication commit body")
	cmd.Flags().StringVar(&prDescription, "pr-description", "", "cumulative PR description (required on first handoff)")
	cmd.Flags().StringVar(&prDescriptionFile, "pr-description-file", "", "path to cumulative PR description")
	cmd.Flags().StringVar(&technicalExplanation, "technical-explanation", "", "markdown technical explanation of code changes")
	cmd.Flags().StringVar(&technicalExplanationFile, "technical-explanation-file", "", "path to markdown technical explanation of code changes")
	_ = cmd.MarkFlagRequired("commit-subject")
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
			"standalone follow-up work that task run may create as tasks with provenance.\n\n" +
			"After the review agent exits, Orpheus records the step outcome. Operators " +
			"inspect findings with task show review and use task run for all workflow advancement.",
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
	resolver := activeAgentContextResolver(deps, taskCtx, deps.taskStateStore)
	switch strings.TrimSpace(deps.environmentValue("ORPHEUS_AGENT_PURPOSE")) {
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
		return fmt.Errorf("agent context: unsupported ORPHEUS_AGENT_PURPOSE %q", deps.environmentValue("ORPHEUS_AGENT_PURPOSE"))
	}
	activeContext, err := resolver.Resolve(command.Context())
	if err != nil {
		return fmt.Errorf("agent context: %w", err)
	}
	interactionMode := agent.AgentInteractionModeNonInteractive
	if activeContext.Run.Execution.Interactive {
		interactionMode = agent.AgentInteractionModeInteractive
	}

	_, err = fmt.Fprint(command.OutOrStdout(), agent.RenderActiveContextWithOptions(
		activeContext,
		agent.ActiveContextRenderOptions{InteractionMode: interactionMode},
	))
	return err
}

func withAgentReviewMutationLock(command *cobra.Command, paths state.Paths, logger *slog.Logger, mutate func() error) error {
	var err error
	for retry := 0; retry < 100; retry++ {
		err = state.WithGlobalMutationLockLogger(command.Context(), paths, "agent review finding", logger, mutate)
		if err == nil || !errors.Is(err, os.ErrExist) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
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

type agentReviewAddContext struct {
	paths  state.Paths
	store  taskstate.Store
	review agent.ReviewContext
}

func runAgentReviewAdd(command *cobra.Command, opts *rootOptions, addOpts agentReviewAddOptions) error {
	logger := opts.log().With(
		slog.String("component", "cli"),
		slog.String("operation", "agent_review_add"),
	)
	logger.DebugContext(command.Context(), "resolving active review agent context")

	addContext, err := resolveAgentReviewAddContext(command, opts)
	if err != nil {
		return err
	}
	finding, err := buildAgentReviewFinding(addContext.review, addOpts)
	if err != nil {
		return fmt.Errorf("agent review add: %w", err)
	}
	reviewAttempt, err := recordAgentReviewFinding(
		command,
		addContext,
		logger,
		finding,
	)
	if err != nil {
		return fmt.Errorf("agent review add: %w", err)
	}
	logger.DebugContext(
		command.Context(),
		"recorded review finding",
		slog.String("repo_id", addContext.review.Repository.ID),
		slog.String("task_id", addContext.review.Task.ID),
		slog.Int("review_attempt", addContext.review.Review.Attempt),
		slog.String("finding_type", string(finding.Type)),
	)
	return renderAgentReviewAddResult(command.OutOrStdout(), addOpts.findingType, reviewAttempt, addContext.review.Task.ID)
}

func resolveAgentReviewAddContext(command *cobra.Command, opts *rootOptions) (agentReviewAddContext, error) {
	deps, err := opts.invocation(command)
	if err != nil {
		return agentReviewAddContext{}, err
	}
	taskCtx, err := loadTaskContextFromInvocation(deps)
	if err != nil {
		return agentReviewAddContext{}, err
	}
	reviewContext, err := activeAgentContextResolver(deps, taskCtx, deps.taskStateStore).ResolveReview(command.Context())
	if err != nil {
		return agentReviewAddContext{}, fmt.Errorf("agent review add: %w", err)
	}
	return agentReviewAddContext{
		paths:  deps.paths,
		store:  deps.taskStateStore,
		review: reviewContext,
	}, nil
}

func recordAgentReviewFinding(
	command *cobra.Command,
	ctx agentReviewAddContext,
	logger *slog.Logger,
	finding taskstate.ReviewFinding,
) (taskstate.ReviewAttempt, error) {
	var reviewAttempt taskstate.ReviewAttempt
	err := withAgentReviewMutationLock(command, ctx.paths, logger, func() error {
		var err error
		reviewAttempt, err = ctx.store.RecordReviewFinding(ctx.review.Repository.ID, ctx.review.Task.ID, ctx.review.Review.Attempt, finding)
		return err
	})
	return reviewAttempt, err
}

func renderAgentReviewAddResult(
	output io.Writer,
	findingType string,
	reviewAttempt taskstate.ReviewAttempt,
	taskID string,
) error {
	number := 0
	for _, finding := range reviewAttempt.Findings {
		if !finding.Superseded {
			number++
		}
	}
	_, err := fmt.Fprintf(
		output,
		"Recorded %s review finding %d for %s.\n",
		findingType,
		number,
		taskID,
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
		Actor:           &ctx.Review.Address,
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
	commitBodyFile            string
	publicationCommitSubject  string
	publicationCommitBody     string
	publicationCommitBodyFile string
	commitSubject             string
	commitBody                string
	prDescription             string
	prDescriptionFile         string
	technicalExplanation      string
	technicalExplanationFile  string
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

	commitBody, err := resolveDoneField(command, "commit-body", doneOpts.commitBody, doneOpts.commitBodyFile)
	if err != nil {
		return fmt.Errorf("agent done: %w", err)
	}
	publicationBody, err := resolveOptionalDoneText(command, "publication-commit-body", doneOpts.publicationCommitBody, doneOpts.publicationCommitBodyFile)
	if err != nil {
		return fmt.Errorf("agent done: %w", err)
	}
	prDescription, err := resolveOptionalDoneText(command, "pr-description", doneOpts.prDescription, doneOpts.prDescriptionFile)
	if err != nil {
		return fmt.Errorf("agent done: %w", err)
	}
	if command.Flags().Changed("publication-commit-subject") && strings.TrimSpace(doneOpts.publicationCommitSubject) == "" {
		return errors.New("publication commit subject is required when supplied")
	}
	technicalExplanation, err := resolveDoneField(command, "technical-explanation", doneOpts.technicalExplanation, doneOpts.technicalExplanationFile)
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
		CommitSubject:            doneOpts.commitSubject,
		CommitBody:               commitBody,
		PublicationCommitSubject: doneOpts.publicationCommitSubject,
		PublicationCommitBody:    publicationBody,
		PRDescription:            prDescription,
		TechnicalExplanation:     technicalExplanation,
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
		Git:      deps.agentGit,
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
			"Recorded completion for %s; advance the workflow with `orpheus task run %s`.\n",
			completed.Context.Task.ID,
			completed.Context.Task.ID,
		)
		return err
	}
	_, err := fmt.Fprintf(command.OutOrStdout(), "Recorded completion for %s; ready for local review.\n", completed.Context.Task.ID)
	return err
}

func resolveOptionalDoneText(command *cobra.Command, flag, inline, filePath string) (string, error) {
	if !command.Flags().Changed(flag) && !command.Flags().Changed(flag+"-file") {
		return "", nil
	}
	return resolveDoneField(command, flag, inline, filePath)
}

func resolveDoneField(command *cobra.Command, flag, inline, filePath string) (string, error) {
	if command.Flags().Changed(flag) && command.Flags().Changed(flag+"-file") {
		return "", fmt.Errorf("use exactly one of --%s or --%s-file", flag, flag)
	}
	label := strings.ReplaceAll(flag, "-", " ")
	if flag == "pr-description" {
		label = "PR description"
	}
	return resolveExactlyOneText(label, inline, filePath)
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
		Env:      deps.environment,
		CWD:      deps.agentCWD,
	}
}
