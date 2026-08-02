package task

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/publication"
)

// CreateBackend is the backend-neutral capability required to validate graph
// references and create one task-tracker item.
type CreateBackend interface {
	Getter
	CreateMutator
}

// CreateBackendFactory constructs a creation backend for one repository source.
type CreateBackendFactory func(RepositorySource) (CreateBackend, error)

// CreateRequest is a backend-neutral request to create one task or epic.
type CreateRequest struct {
	Title              string
	Description        string
	Design             string
	AcceptanceCriteria string
	ExternalRef        string
	IssueType          IssueType
	ParentID           string
	BlockingIDs        []string
}

// CreationSourceOptions captures the public repository-selection inputs.
// Repository takes precedence over ActiveRepositoryID and CurrentDirectory.
type CreationSourceOptions struct {
	Repository         string
	ActiveRepositoryID string
	CurrentDirectory   string
}

// CreateService applies Orpheus creation policy independently of Cobra and any
// concrete task source.
type CreateService struct {
	Sources        []RepositorySource
	BackendFactory CreateBackendFactory
}

// ResolveCreationSource selects the repository for a new item.
func ResolveCreationSource(sources []RepositorySource, opts CreationSourceOptions) (RepositorySource, error) {
	if token := strings.TrimSpace(opts.Repository); token != "" {
		return resolveCreationSourceToken(sources, token, "--repo")
	}
	if repoID := strings.TrimSpace(opts.ActiveRepositoryID); repoID != "" {
		return resolveCreationSourceToken(sources, repoID, "active Orpheus repository context")
	}

	cwd := strings.TrimSpace(opts.CurrentDirectory)
	if cwd == "" {
		return RepositorySource{}, errors.New("cannot determine repository for task creation; pass --repo <repository> or run from inside one registered repository")
	}
	absoluteCWD, err := filepath.Abs(cwd)
	if err != nil {
		return RepositorySource{}, fmt.Errorf("resolve current directory for task creation: %w", err)
	}

	matches := make([]RepositorySource, 0, 1)
	for _, source := range sources {
		inside, err := pathIsInside(absoluteCWD, source.Repository.Path)
		if err != nil {
			return RepositorySource{}, fmt.Errorf("compare current directory to registered repository %s: %w", source.Repository.ID, err)
		}
		if inside {
			matches = append(matches, source)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return RepositorySource{}, fmt.Errorf("cannot determine repository for task creation from current directory %q; pass --repo <repository> or run from inside one registered repository", absoluteCWD)
	default:
		ids := make([]string, 0, len(matches))
		for _, source := range matches {
			ids = append(ids, source.Repository.ID)
		}
		return RepositorySource{}, fmt.Errorf("cannot determine repository for task creation from current directory %q: it belongs to multiple registered repositories (%s); pass --repo <repository>", absoluteCWD, strings.Join(ids, ", "))
	}
}

// Create validates one request and creates it in source. It intentionally does
// not attempt to make graph creation atomic: each invocation creates one item.
func (s CreateService) Create(ctx context.Context, source RepositorySource, request CreateRequest) (Task, error) {
	if s.BackendFactory == nil {
		return Task{}, errors.New("create task: backend factory is required")
	}
	if err := sourceIsConfigured(s.Sources, source); err != nil {
		return Task{}, err
	}

	opts, err := NormalizeCreateOptions(CreateOptions(request))
	if err != nil {
		return Task{}, err
	}
	if err := validateRequiredExternalRef(source.Repository.TitleTemplate, opts.IssueType, opts.ExternalRef); err != nil {
		return Task{}, err
	}
	backend, err := s.BackendFactory(source)
	if err != nil {
		return Task{}, creationFailure{message: fmt.Sprintf("cannot prepare task creation in repository %s", source.Repository.ID), cause: err}
	}
	if err := s.validateRelations(ctx, source, backend, opts); err != nil {
		return Task{}, err
	}
	created, err := backend.Create(ctx, opts)
	if err != nil {
		return Task{}, creationFailure{message: fmt.Sprintf("cannot create %s in repository %s", opts.IssueType, source.Repository.ID), cause: err}
	}
	if created.IssueType == IssueTypeUnknown {
		created.IssueType = opts.IssueType
	}
	return created, nil
}

// NormalizeCreateOptions applies the backend-neutral creation validation policy.
func NormalizeCreateOptions(opts CreateOptions) (CreateOptions, error) {
	normalized := CreateOptions{
		Title:              strings.TrimSpace(opts.Title),
		Description:        strings.TrimSpace(opts.Description),
		Design:             strings.TrimSpace(opts.Design),
		AcceptanceCriteria: strings.TrimSpace(opts.AcceptanceCriteria),
		ExternalRef:        strings.TrimSpace(opts.ExternalRef),
		IssueType:          opts.IssueType,
		ParentID:           strings.TrimSpace(opts.ParentID),
		BlockingIDs:        normalizeIDs(opts.BlockingIDs),
	}
	if normalized.Title == "" {
		return CreateOptions{}, errors.New("title is required")
	}
	if normalized.Description == "" {
		return CreateOptions{}, errors.New("description is required")
	}
	if normalized.AcceptanceCriteria == "" {
		return CreateOptions{}, errors.New("acceptance criteria is required")
	}
	if normalized.IssueType == IssueTypeUnknown {
		normalized.IssueType = IssueTypeTask
	}
	if normalized.IssueType != IssueTypeTask && normalized.IssueType != IssueTypeEpic {
		return CreateOptions{}, fmt.Errorf("unsupported item type %q; expected task or epic", normalized.IssueType)
	}
	return normalized, nil
}

// validateRequiredExternalRef applies the repository publication policy to
// ordinary tasks. Epics are planning items and are not published through the
// ordinary task workflow.
func validateRequiredExternalRef(template string, issueType IssueType, externalRef string) error {
	if issueType != IssueTypeTask || !publication.RequiresExternalRef(template) || strings.TrimSpace(externalRef) != "" {
		return nil
	}
	return errors.New("publication title template requires a task external reference; provide --external-ref <reference>")
}

func (s CreateService) validateRelations(ctx context.Context, source RepositorySource, backend CreateBackend, opts CreateOptions) error {
	if opts.ParentID != "" {
		if err := s.validateReferenceRepository(source, opts.ParentID, "parent"); err != nil {
			return err
		}
		parent, err := getCreationReference(ctx, backend, source, opts.ParentID, "parent")
		if err != nil {
			return err
		}
		if parent.IssueType != IssueTypeEpic {
			return fmt.Errorf("parent %q in repository %s must be an epic", opts.ParentID, source.Repository.ID)
		}
		if parent.Status == StatusClosed {
			return fmt.Errorf("parent epic %q in repository %s is closed", opts.ParentID, source.Repository.ID)
		}
	}
	for _, dependencyID := range opts.BlockingIDs {
		if err := s.validateReferenceRepository(source, dependencyID, "blocking dependency"); err != nil {
			return err
		}
		dependency, err := getCreationReference(ctx, backend, source, dependencyID, "blocking dependency")
		if err != nil {
			return err
		}
		if dependency.IssueType != IssueTypeTask && dependency.IssueType != IssueTypeEpic {
			return fmt.Errorf("blocking dependency %q in repository %s must be a task or epic", dependencyID, source.Repository.ID)
		}
	}
	return nil
}

func (s CreateService) validateReferenceRepository(source RepositorySource, id string, relation string) error {
	var owner RepositorySource
	longestPrefix := 0
	for _, candidate := range s.Sources {
		prefix := strings.TrimSpace(candidate.Repository.TaskIDPrefix)
		if prefix == "" || !strings.HasPrefix(id, prefix+"-") || len(prefix) <= longestPrefix {
			continue
		}
		owner = candidate
		longestPrefix = len(prefix)
	}
	if owner.Repository.ID != "" && owner.Repository.ID != source.Repository.ID {
		return fmt.Errorf("%s %q belongs to repository %s, not selected repository %s", relation, id, owner.Repository.ID, source.Repository.ID)
	}
	return nil
}

func resolveCreationSourceToken(sources []RepositorySource, token string, origin string) (RepositorySource, error) {
	for _, source := range sources {
		repo := source.Repository
		if token == repo.ID || token == repo.Name || token == repo.TaskIDPrefix {
			return source, nil
		}
	}
	return RepositorySource{}, fmt.Errorf("%s repository %q is not registered; run `orpheus repo list` or pass a registered --repo value", origin, token)
}

func sourceIsConfigured(sources []RepositorySource, source RepositorySource) error {
	for _, candidate := range sources {
		if candidate.Repository.ID == source.Repository.ID {
			return nil
		}
	}
	return fmt.Errorf("create task: repository %q is not configured", source.Repository.ID)
}

func getCreationReference(ctx context.Context, backend Getter, source RepositorySource, id string, relation string) (Task, error) {
	item, err := backend.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Task{}, fmt.Errorf("%s %q was not found in repository %s", relation, id, source.Repository.ID)
		}
		return Task{}, creationFailure{message: fmt.Sprintf("cannot inspect %s %q in repository %s", relation, id, source.Repository.ID), cause: err}
	}
	return item, nil
}

type creationFailure struct {
	message string
	cause   error
}

func (e creationFailure) Error() string { return e.message }

func (e creationFailure) Unwrap() error { return e.cause }

func normalizeIDs(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	ids := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, value := range raw {
		id := strings.TrimSpace(value)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids
}

// trimStringPointer preserves nil while normalizing identifier and single-line
// fields supplied by the command line.
func trimStringPointer(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	return &trimmed
}

// copyStringPointer preserves long-form planning content verbatim, including
// meaningful Markdown indentation and trailing newlines.
func copyStringPointer(s *string) *string {
	if s == nil {
		return nil
	}
	value := *s
	return &value
}

// NormalizeUpdateOptions applies the backend-neutral update validation policy.
func NormalizeUpdateOptions(opts UpdateOptions) (UpdateOptions, error) {
	normalized := UpdateOptions{
		ID:                 strings.TrimSpace(opts.ID),
		Title:              trimStringPointer(opts.Title),
		Description:        copyStringPointer(opts.Description),
		Design:             copyStringPointer(opts.Design),
		AcceptanceCriteria: copyStringPointer(opts.AcceptanceCriteria),
		ExternalRef:        trimStringPointer(opts.ExternalRef),
		ParentID:           trimStringPointer(opts.ParentID),
		AddBlockingIDs:     normalizeIDs(opts.AddBlockingIDs),
		RemoveBlockingIDs:  normalizeIDs(opts.RemoveBlockingIDs),
	}

	if normalized.ID == "" {
		return UpdateOptions{}, errors.New("task id is required")
	}

	// Check for conflicting add/remove of the same dependency
	addSet := make(map[string]struct{}, len(normalized.AddBlockingIDs))
	for _, id := range normalized.AddBlockingIDs {
		addSet[id] = struct{}{}
	}
	for _, id := range normalized.RemoveBlockingIDs {
		if _, exists := addSet[id]; exists {
			return UpdateOptions{}, fmt.Errorf("dependency %q requested for both addition and removal", id)
		}
	}

	return normalized, nil
}

func pathIsInside(path string, root string) (bool, error) {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false, err
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}
