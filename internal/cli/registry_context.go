package cli

import (
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
)

type registryContext struct {
	Store    registry.Store
	Registry registry.Registry
}

func loadRegistryContext() (registryContext, error) {
	store, err := newRegistryStoreFromEnvironment()
	if err != nil {
		return registryContext{}, err
	}
	return loadRegistryContextFromStore(store)
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

func newRegistryStoreFromEnvironment() (registry.Store, error) {
	store, _, err := newRegistryStoreWithPathsFromEnvironment()
	if err != nil {
		return registry.Store{}, err
	}
	return store, nil
}

func newRegistryStoreWithPathsFromEnvironment() (registry.Store, state.Paths, error) {
	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		return registry.Store{}, state.Paths{}, err
	}
	return registry.NewStore(paths), paths, nil
}
