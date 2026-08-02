package cli

import "github.com/hea3ven/orpheus/internal/registry"

type registryContext struct {
	Store    registry.Store
	Registry registry.Registry
}

func loadRegistryContextFromInvocation(deps *invocationDependencies) (registryContext, error) {
	return loadRegistryContextFromStore(deps.registryStore)
}

func loadRegistryContextFromStore(store registry.Store) (registryContext, error) {
	reg, err := store.Load()
	if err != nil {
		return registryContext{}, err
	}

	return registryContext{
		Store:    store,
		Registry: reg,
	}, nil
}
