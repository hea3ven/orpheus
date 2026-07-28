package registry

import (
	"fmt"

	"github.com/hea3ven/orpheus/internal/task"
)

// TaskRepositorySources projects registered repositories into validated task sources.
// It is the sole translation boundary from persisted registry records to the
// backend-neutral task model.
func (s Store) TaskRepositorySources(reg Registry) ([]task.RepositorySource, error) {
	normalized, err := reg.normalized()
	if err != nil {
		return nil, fmt.Errorf("validate registered repositories: %w", err)
	}

	sources := make([]task.RepositorySource, 0, len(normalized.Repos))
	for _, repo := range normalized.Repos {
		source, err := s.TaskRepositorySource(repo)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// TaskRepositorySource projects one registered repository into its task source.
func (s Store) TaskRepositorySource(repo Repo) (task.RepositorySource, error) {
	normalizedRepo, err := normalizeRepo(repo)
	if err != nil {
		return task.RepositorySource{}, fmt.Errorf("validate registered repository: %w", err)
	}
	beadsDir, err := s.BeadsDir(normalizedRepo)
	if err != nil {
		return task.RepositorySource{}, err
	}
	return task.RepositorySource{
		Repository: task.Repository{
			ID:                     normalizedRepo.ID,
			Name:                   normalizedRepo.Name,
			TaskIDPrefix:           normalizedRepo.BeadsPrefix,
			Path:                   normalizedRepo.Path,
			DefaultBranch:          normalizedRepo.DefaultBranch,
			TitleTemplate:          normalizedRepo.TitleTemplate,
			IncludePRReviewProcess: cloneBoolPtr(normalizedRepo.IncludePRReviewProcess),
			ReviewPipeline:         normalizedRepo.ReviewPipeline,
			ReviewPipelineAliases:  cloneStringMap(normalizedRepo.ReviewPipelineAliases),
		},
		BackendDir: beadsDir,
	}, nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	clone := make(map[string]string, len(values))
	for key, value := range values {
		clone[key] = value
	}
	return clone
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
