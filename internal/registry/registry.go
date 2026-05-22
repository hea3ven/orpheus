// Package registry persists Orpheus' machine-local repository registry.
package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/state"
)

const (
	registryFile = "registry.yaml"

	// BeadsModeLocal means bd commands should run in the registered repo path.
	BeadsModeLocal = "local"

	// BeadsModeManaged means bd commands should run in Orpheus-managed state.
	BeadsModeManaged = "managed"
)

// Repo is a repository record stored in the Orpheus registry.
type Repo struct {
	ID            string `yaml:"id"`
	Name          string `yaml:"name"`
	Path          string `yaml:"path"`
	Remote        string `yaml:"remote,omitempty"`
	DefaultBranch string `yaml:"default_branch,omitempty"`
	BeadsMode     string `yaml:"beads_mode,omitempty"`
	BeadsPrefix   string `yaml:"beads_prefix,omitempty"`
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
