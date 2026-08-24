package cli

import (
	"github.com/hea3ven/orpheus/internal/registry"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
)

type taskContext struct {
	Store      registry.Store
	Registry   registry.Registry
	Sources    []taskmodel.RepositorySource
	Aggregator taskmodel.Aggregator
}

func loadTaskContextFromInvocation(deps *invocationDependencies) (taskContext, error) {
	registryCtx, err := loadRegistryContextFromInvocation(deps)
	if err != nil {
		return taskContext{}, err
	}

	sources, err := registryCtx.Store.TaskRepositorySources(registryCtx.Registry)
	if err != nil {
		return taskContext{}, err
	}
	return newTaskContext(deps, registryCtx, sources)
}

// loadTaskListContextFromInvocation resolves an optional repository filter
// before projecting task sources, so excluded repositories never reach the
// aggregator or its backend factory.
func loadTaskListContextFromInvocation(deps *invocationDependencies, repository string) (taskContext, error) {
	registryCtx, err := loadRegistryContextFromInvocation(deps)
	if err != nil {
		return taskContext{}, err
	}
	if repository == "" {
		sources, err := registryCtx.Store.TaskRepositorySources(registryCtx.Registry)
		if err != nil {
			return taskContext{}, err
		}
		return newTaskContext(deps, registryCtx, sources)
	}

	repo, err := registryCtx.Registry.Resolve(repository)
	if err != nil {
		return taskContext{}, err
	}
	source, err := registryCtx.Store.TaskRepositorySource(repo)
	if err != nil {
		return taskContext{}, err
	}
	return newTaskContext(deps, registryCtx, []taskmodel.RepositorySource{source})
}

func newTaskContext(
	deps *invocationDependencies,
	registryCtx registryContext,
	sources []taskmodel.RepositorySource,
) (taskContext, error) {
	aggregator, err := taskmodel.NewAggregatorWithLogger(sources, deps.taskBackendFactory, deps.logger)
	if err != nil {
		return taskContext{}, err
	}

	return taskContext{
		Store:      registryCtx.Store,
		Registry:   registryCtx.Registry,
		Sources:    sources,
		Aggregator: aggregator,
	}, nil
}
