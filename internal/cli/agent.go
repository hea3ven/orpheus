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

	cmd.AddCommand(newAgentContextCommand(opts), newAgentDoneCommand(opts))
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
	activeContext, err := resolver.Resolve(command.Context())
	if err != nil {
		return fmt.Errorf("agent context: %w", err)
	}

	_, err = fmt.Fprint(command.OutOrStdout(), agent.RenderActiveContext(activeContext))
	return err
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
			"Recorded completion for %s; ready for feature-branch publication with `orpheus task done %s`.\n",
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
