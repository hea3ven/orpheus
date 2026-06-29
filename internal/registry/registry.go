// Package registry persists Orpheus' machine-local repository registry.
package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/publication"
	"github.com/hea3ven/orpheus/internal/state"
)

const (
	registryFile = "registry.yaml"

	// BeadsModeLocal means bd commands should run in the registered repo path.
	BeadsModeLocal = "local"

	// BeadsModeManaged means bd commands should run in Orpheus-managed state.
	BeadsModeManaged = "managed"

	// SummaryGuidanceStyleTyped guides agents to write conventional typed summaries.
	SummaryGuidanceStyleTyped = "typed"

	// SummaryGuidanceStyleCapitalized guides agents to write capitalized plain-English summaries.
	SummaryGuidanceStyleCapitalized = "capitalized"
)

// Repo is a repository record stored in the Orpheus registry.
type Repo struct {
	ID                   string `yaml:"id"`
	Name                 string `yaml:"name"`
	Path                 string `yaml:"path"`
	Remote               string `yaml:"remote,omitempty"`
	DefaultBranch        string `yaml:"default_branch,omitempty"`
	BeadsMode            string `yaml:"beads_mode,omitempty"`
	BeadsPrefix          string `yaml:"beads_prefix,omitempty"`
	SummaryGuidance      string `yaml:"summary_guidance,omitempty"`
	SummaryGuidanceStyle string `yaml:"summary_guidance_style,omitempty"`
	TitleTemplate        string `yaml:"title_template,omitempty"`
	ReviewPipeline       string `yaml:"review_pipeline,omitempty"`
}

// PublicationPolicy is the resolved publication configuration for a repository.
// Its values are safe for consumers to use without applying compatibility
// defaults for older registry entries themselves.
type PublicationPolicy struct {
	SummaryGuidance      string
	SummaryGuidanceStyle string
	TitleTemplate        string
}

// EffectivePublicationPolicy returns a repository's publication policy with
// compatibility defaults applied. A custom guidance string overrides the named
// style when agents are instructed how to write completion summaries.
func (r Repo) EffectivePublicationPolicy() PublicationPolicy {
	style := strings.TrimSpace(r.SummaryGuidanceStyle)
	if style != SummaryGuidanceStyleCapitalized {
		style = SummaryGuidanceStyleTyped
	}

	return PublicationPolicy{
		SummaryGuidance:      strings.TrimSpace(r.SummaryGuidance),
		SummaryGuidanceStyle: style,
		TitleTemplate:        strings.TrimSpace(r.TitleTemplate),
	}
}

// Registry is the human-editable YAML schema for registered repositories.
type Registry struct {
	Repos []Repo `yaml:"repos"`
}

// Store persists the registry under the Orpheus data root.
type Store struct {
	paths state.Paths
}

// NewStore returns a YAML-backed registry store using the supplied Orpheus paths.
func NewStore(paths state.Paths) Store {
	return Store{paths: paths}
}

// ManagedBeadsDir returns the deterministic Orpheus-managed Beads workspace for repoID.
func (s Store) ManagedBeadsDir(repoID string) (string, error) {
	return ManagedBeadsDir(s.paths, repoID)
}

// BeadsDir returns the directory where Beads commands should run for repo.
func (s Store) BeadsDir(repo Repo) (string, error) {
	return BeadsDir(s.paths, repo)
}

// ManagedBeadsDir returns the deterministic Orpheus-managed Beads workspace for repoID.
func ManagedBeadsDir(paths state.Paths, repoID string) (string, error) {
	repoID = strings.TrimSpace(repoID)
	if repoID == "" {
		return "", errors.New("repo id is required")
	}
	if repoID == "." || repoID == ".." || strings.ContainsAny(repoID, `/\\`) || filepath.VolumeName(repoID) != "" {
		return "", fmt.Errorf("repo id %q cannot be used in managed Beads path", repoID)
	}
	return paths.DataPath(filepath.Join("repos", repoID, "beads"))
}

// BeadsDir returns the directory where Beads commands should run for repo.
func BeadsDir(paths state.Paths, repo Repo) (string, error) {
	normalizedRepo, err := normalizeRepo(repo)
	if err != nil {
		return "", err
	}

	switch normalizedRepo.BeadsMode {
	case BeadsModeLocal:
		return normalizedRepo.Path, nil
	case BeadsModeManaged:
		return ManagedBeadsDir(paths, normalizedRepo.ID)
	case "":
		return "", fmt.Errorf("repo %q has no beads_mode; register it again or edit %s", normalizedRepo.ID, registryFile)
	default:
		return "", fmt.Errorf("repo %q has unsupported beads_mode %q", normalizedRepo.ID, normalizedRepo.BeadsMode)
	}
}

// NewRepoFromPath derives the minimal M1 repo identity from a path basename.
func NewRepoFromPath(inputPath string) (Repo, error) {
	normalizedPath, err := NormalizePath(inputPath)
	if err != nil {
		return Repo{}, err
	}

	base := filepath.Base(normalizedPath)
	if base == "." || filepath.Dir(normalizedPath) == normalizedPath {
		return Repo{}, fmt.Errorf("derive repo identity from %q: path must name a repository directory", inputPath)
	}

	return Repo{
		ID:   base,
		Name: base,
		Path: normalizedPath,
	}, nil
}

// NormalizePath converts an input path into the canonical form used for duplicate detection.
func NormalizePath(inputPath string) (string, error) {
	if strings.TrimSpace(inputPath) == "" {
		return "", errors.New("repo path is required")
	}

	absolutePath, err := filepath.Abs(inputPath)
	if err != nil {
		return "", fmt.Errorf("normalize repo path %q: %w", inputPath, err)
	}
	return filepath.Clean(absolutePath), nil
}

// ValidateSummaryGuidanceStyle checks whether style is one of the supported named styles.
// An empty style preserves compatibility with registry entries created before named styles.
func ValidateSummaryGuidanceStyle(style string) error {
	switch strings.TrimSpace(style) {
	case "", SummaryGuidanceStyleTyped, SummaryGuidanceStyleCapitalized:
		return nil
	default:
		return fmt.Errorf(
			"repo summary_guidance_style %q is invalid; expected %q or %q",
			style,
			SummaryGuidanceStyleTyped,
			SummaryGuidanceStyleCapitalized,
		)
	}
}

// Load reads and validates the registry. Missing or empty registry state loads as empty.
func (s Store) Load() (Registry, error) {
	var registry Registry
	if err := s.paths.ReadDataYAML(registryFile, &registry); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Registry{}, nil
		}
		return Registry{}, fmt.Errorf("load repo registry: %w", err)
	}

	normalizedRegistry, err := registry.normalized()
	if err != nil {
		return Registry{}, fmt.Errorf("load repo registry: %w", err)
	}
	return normalizedRegistry, nil
}

// Save validates and writes the registry.
func (s Store) Save(registry Registry) error {
	normalizedRegistry, err := registry.normalized()
	if err != nil {
		return fmt.Errorf("save repo registry: %w", err)
	}
	if normalizedRegistry.Repos == nil {
		normalizedRegistry.Repos = []Repo{}
	}
	if err := s.paths.WriteDataYAML(registryFile, normalizedRegistry); err != nil {
		return fmt.Errorf("save repo registry: %w", err)
	}
	return nil
}

// Resolve returns the repository matching token by id, display name, or Beads prefix.
func (r Registry) Resolve(token string) (Repo, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return Repo{}, errors.New("repo id, name, or Beads prefix is required")
	}

	normalizedRegistry, err := r.normalized()
	if err != nil {
		return Repo{}, err
	}

	for _, repo := range normalizedRegistry.Repos {
		if repo.ID == token || repo.Name == token || repo.BeadsPrefix == token {
			return repo, nil
		}
	}

	return Repo{}, fmt.Errorf("repo %q is not registered by id, name, or Beads prefix; run `orpheus repo list` to see registered repositories", token)
}

// Add validates and appends a repository record to the registry.
func (r *Registry) Add(repo Repo) error {
	normalizedRepo, err := normalizeRepo(repo)
	if err != nil {
		return err
	}

	repos := make([]Repo, 0, len(r.Repos)+1)
	repos = append(repos, r.Repos...)
	repos = append(repos, normalizedRepo)

	candidate := Registry{Repos: repos}
	normalizedRegistry, err := candidate.normalized()
	if err != nil {
		return err
	}

	r.Repos = normalizedRegistry.Repos
	return nil
}

// Validate checks required fields and registry-wide uniqueness constraints.
func (r Registry) Validate() error {
	ids := map[string]struct{}{}
	names := map[string]struct{}{}
	paths := map[string]struct{}{}
	prefixes := map[string]struct{}{}
	identifiers := map[string]identifierOwner{}

	for index, repo := range r.Repos {
		normalizedRepo, err := normalizeRepo(repo)
		if err != nil {
			return fmt.Errorf("repo[%d]: %w", index, err)
		}

		if _, ok := paths[normalizedRepo.Path]; ok {
			return fmt.Errorf("duplicate repo path %q: repository is already registered", normalizedRepo.Path)
		}
		paths[normalizedRepo.Path] = struct{}{}

		if _, ok := ids[normalizedRepo.ID]; ok {
			return fmt.Errorf("duplicate repo id %q: choose a different repository directory name or edit %s", normalizedRepo.ID, registryFile)
		}
		ids[normalizedRepo.ID] = struct{}{}

		if _, ok := names[normalizedRepo.Name]; ok {
			return fmt.Errorf("duplicate repo name %q: choose a different repository directory name or edit %s", normalizedRepo.Name, registryFile)
		}
		names[normalizedRepo.Name] = struct{}{}

		if normalizedRepo.BeadsPrefix != "" {
			if _, ok := prefixes[normalizedRepo.BeadsPrefix]; ok {
				return fmt.Errorf("duplicate beads prefix %q: Beads prefixes must be unique across registered repositories", normalizedRepo.BeadsPrefix)
			}
			prefixes[normalizedRepo.BeadsPrefix] = struct{}{}
		}

		if err := addIdentifier(identifiers, normalizedRepo.ID, "id", index); err != nil {
			return err
		}
		if err := addIdentifier(identifiers, normalizedRepo.Name, "name", index); err != nil {
			return err
		}
		if normalizedRepo.BeadsPrefix != "" {
			if err := addIdentifier(identifiers, normalizedRepo.BeadsPrefix, "beads_prefix", index); err != nil {
				return err
			}
		}
	}

	return nil
}

type identifierOwner struct {
	index int
	field string
}

func addIdentifier(identifiers map[string]identifierOwner, value string, field string, index int) error {
	owner, ok := identifiers[value]
	if ok {
		if owner.index == index {
			return nil
		}
		return fmt.Errorf(
			"repo %s %q collides with repo[%d] %s: repo ids, names, and Beads prefixes must be unique to avoid ambiguous references",
			field,
			value,
			owner.index,
			owner.field,
		)
	}
	identifiers[value] = identifierOwner{index: index, field: field}
	return nil
}

func (r Registry) normalized() (Registry, error) {
	normalizedRepos := make([]Repo, 0, len(r.Repos))
	for index, repo := range r.Repos {
		normalizedRepo, err := normalizeRepo(repo)
		if err != nil {
			return Registry{}, fmt.Errorf("repo[%d]: %w", index, err)
		}
		normalizedRepos = append(normalizedRepos, normalizedRepo)
	}

	normalizedRegistry := Registry{Repos: normalizedRepos}
	if err := normalizedRegistry.Validate(); err != nil {
		return Registry{}, err
	}
	return normalizedRegistry, nil
}

func normalizeRepo(repo Repo) (Repo, error) {
	repo.ID = strings.TrimSpace(repo.ID)
	repo.Name = strings.TrimSpace(repo.Name)
	repo.Remote = strings.TrimSpace(repo.Remote)
	repo.DefaultBranch = strings.TrimSpace(repo.DefaultBranch)
	repo.BeadsMode = strings.TrimSpace(repo.BeadsMode)
	repo.BeadsPrefix = strings.TrimSpace(repo.BeadsPrefix)
	repo.SummaryGuidance = strings.TrimSpace(repo.SummaryGuidance)
	repo.SummaryGuidanceStyle = strings.TrimSpace(repo.SummaryGuidanceStyle)
	if err := ValidateSummaryGuidanceStyle(repo.SummaryGuidanceStyle); err != nil {
		return Repo{}, err
	}
	repo.TitleTemplate = strings.TrimSpace(repo.TitleTemplate)
	if err := publication.ValidateTitleTemplate(repo.TitleTemplate); err != nil {
		return Repo{}, fmt.Errorf("repo title_template is invalid: %w", err)
	}
	repo.ReviewPipeline = strings.TrimSpace(repo.ReviewPipeline)
	if repo.ID == "" {
		return Repo{}, errors.New("repo id is required")
	}
	if repo.Name == "" {
		return Repo{}, errors.New("repo name is required")
	}
	if strings.TrimSpace(repo.Path) == "" {
		return Repo{}, errors.New("repo path is required")
	}
	if !filepath.IsAbs(repo.Path) {
		return Repo{}, fmt.Errorf("repo path %q must be absolute", repo.Path)
	}
	switch repo.BeadsMode {
	case "", BeadsModeLocal, BeadsModeManaged:
	default:
		return Repo{}, fmt.Errorf("repo beads_mode %q is invalid; expected %q or %q", repo.BeadsMode, BeadsModeLocal, BeadsModeManaged)
	}
	if repo.BeadsMode != "" && repo.BeadsPrefix == "" {
		return Repo{}, fmt.Errorf("repo beads_prefix is required when beads_mode is %q", repo.BeadsMode)
	}
	if repo.BeadsMode == "" && repo.BeadsPrefix != "" {
		return Repo{}, errors.New("repo beads_mode is required when beads_prefix is set")
	}
	repo.Path = filepath.Clean(repo.Path)
	return repo, nil
}
