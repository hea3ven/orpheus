package cli

import (
	"fmt"
	"os"
	"strings"

	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/spf13/cobra"
)

type taskCreateInput struct {
	title                  string
	description            string
	descriptionFile        string
	design                 string
	designFile             string
	acceptanceCriteria     string
	acceptanceCriteriaFile string
	externalRef            string
	issueType              string
	parent                 string
	blocking               []string
	repository             string
}

func newTaskCreateCommand(opts *rootOptions) *cobra.Command {
	var input taskCreateInput
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create one task or epic",
		Long:  "Create one task or epic in a registered repository. Repository selection uses --repo, then the active Orpheus repository context, then an unambiguous registered repository containing the current directory.",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return runTaskCreate(command, opts, input)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&input.title, "title", "", "title for the new item")
	flags.StringVar(&input.description, "description", "", "Markdown description")
	flags.StringVar(&input.descriptionFile, "description-file", "", "file containing the Markdown description")
	flags.StringVar(&input.design, "design", "", "optional Markdown design notes")
	flags.StringVar(&input.designFile, "design-file", "", "file containing optional Markdown design notes")
	flags.StringVar(&input.acceptanceCriteria, "acceptance", "", "acceptance criteria")
	flags.StringVar(&input.acceptanceCriteriaFile, "acceptance-file", "", "file containing acceptance criteria")
	flags.StringVar(&input.externalRef, "external-ref", "", "optional external tracking reference")
	flags.StringVar(&input.issueType, "type", string(taskmodel.IssueTypeTask), "item type: task or epic")
	flags.StringVar(&input.parent, "parent", "", "optional parent epic id")
	flags.StringArrayVar(&input.blocking, "blocked-by", nil, "task or epic that blocks this item; repeatable")
	flags.StringVar(&input.repository, "repo", "", "registered repository id, name, or task prefix")
	return cmd
}

func runTaskCreate(command *cobra.Command, root *rootOptions, input taskCreateInput) error {
	deps, err := root.invocation(command)
	if err != nil {
		return err
	}
	registryCtx, err := loadRegistryContextFromInvocation(deps)
	if err != nil {
		return err
	}
	sources, err := registryCtx.Store.TaskRepositorySources(registryCtx.Registry)
	if err != nil {
		return err
	}
	request, err := input.request()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve current directory for task creation: %w", err)
	}
	source, err := taskmodel.ResolveCreationSource(sources, taskmodel.CreationSourceOptions{
		Repository:         input.repository,
		ActiveRepositoryID: os.Getenv("ORPHEUS_REPO_ID"),
		CurrentDirectory:   cwd,
	})
	if err != nil {
		return err
	}
	service := taskmodel.CreateService{
		Sources: sources,
		BackendFactory: func(source taskmodel.RepositorySource) (taskmodel.CreateBackend, error) {
			backend, err := deps.taskBackendFactory(source)
			if err != nil {
				return nil, err
			}
			creationBackend, ok := backend.(taskmodel.CreateBackend)
			if !ok {
				return nil, fmt.Errorf("backend for repository %s does not support creation", source.Repository.ID)
			}
			return creationBackend, nil
		},
	}
	created, err := service.Create(command.Context(), source, request)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(command.OutOrStdout(), "Created %s %s.\n", created.IssueType, created.ID)
	return err
}

func (input taskCreateInput) request() (taskmodel.CreateRequest, error) {
	description, err := resolveCreateContent(input.description, input.descriptionFile, "--description", "--description-file")
	if err != nil {
		return taskmodel.CreateRequest{}, err
	}
	design, err := resolveCreateContent(input.design, input.designFile, "--design", "--design-file")
	if err != nil {
		return taskmodel.CreateRequest{}, err
	}
	acceptance, err := resolveCreateContent(input.acceptanceCriteria, input.acceptanceCriteriaFile, "--acceptance", "--acceptance-file")
	if err != nil {
		return taskmodel.CreateRequest{}, err
	}
	return taskmodel.CreateRequest{
		Title:              input.title,
		Description:        description,
		Design:             design,
		AcceptanceCriteria: acceptance,
		ExternalRef:        input.externalRef,
		IssueType:          taskmodel.IssueType(strings.TrimSpace(input.issueType)),
		ParentID:           input.parent,
		BlockingIDs:        input.blocking,
	}, nil
}

func resolveCreateContent(value string, file string, valueFlag string, fileFlag string) (string, error) {
	if strings.TrimSpace(value) != "" && strings.TrimSpace(file) != "" {
		return "", fmt.Errorf("%s cannot be combined with %s", valueFlag, fileFlag)
	}
	if strings.TrimSpace(file) == "" {
		return value, nil
	}
	content, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("read %s %q: %w", fileFlag, file, err)
	}
	return string(content), nil
}
