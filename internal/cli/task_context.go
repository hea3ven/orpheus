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

func loadTaskContext() (taskContext, error) {
	registryCtx, err := loadRegistryContext()
	if err != nil {
		return taskContext{}, err
	}

	sources, err := taskRepositorySources(registryCtx.Store, registryCtx.Registry)
	if err != nil {
		return taskContext{}, err
	}

	aggregator, err := taskmodel.NewAggregator(
		sources,
		func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
			return newBeadsTaskBackend(source.BackendDir)
		},
	)
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
