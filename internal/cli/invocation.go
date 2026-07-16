package cli

import (
	"context"
	"log/slog"

	"github.com/hea3ven/orpheus/internal/beads"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/spf13/cobra"
)

type invocationDependencies struct {
	paths              state.Paths
	logger             *slog.Logger
	registryStore      registry.Store
	taskBackendFactory taskmodel.BackendFactory
	inspectGit         func(context.Context, string) (gitmeta.Inspection, error)
	inspectLocalBeads  func(string, ...slog.Attr) (beads.LocalInspection, error)
	initializeBeads    func(string, string, ...slog.Attr) error
	taskStateStore     taskstate.Store
}

func newInvocationDependencies(command *cobra.Command, logger *slog.Logger) (*invocationDependencies, error) {
	ctx := command.Context()
	span := logging.Start(ctx, logger, "xdg path resolution",
		slog.String("component", "state"),
		slog.String("operation", "resolve_paths"),
	)
	paths, err := state.ResolveFromEnvironment()
	if err != nil {
		span.FinishError(ctx, err)
		return nil, err
	}
	span.Finish(ctx, logging.StatusSuccess,
		slog.String("config_root", paths.ConfigRoot),
		slog.String("data_root", paths.DataRoot),
	)

	deps := &invocationDependencies{
		paths:         paths,
		logger:        logger,
		registryStore: registry.NewStoreWithLogger(paths, logger),
	}
	deps.taskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		return beads.NewTaskBackendWithRunner(source.BackendDir, beads.CommandRunner{
			Logger: logger,
			DiagnosticAttrs: []slog.Attr{
				slog.String("repo_id", source.Repository.ID),
			},
		})
	}
	deps.inspectGit = func(ctx context.Context, path string) (gitmeta.Inspection, error) {
		return gitmeta.InspectWithLogger(ctx, path, logger)
	}
	deps.inspectLocalBeads = func(path string, attrs ...slog.Attr) (beads.LocalInspection, error) {
		return beads.InspectLocalWithRunner(path, beads.NewInspectLocalRunner(logger, attrs...))
	}
	deps.initializeBeads = func(path string, prefix string, attrs ...slog.Attr) error {
		return beads.InitializeManagedWithRunner(path, prefix, beads.CommandRunner{
			Logger:          logger,
			DiagnosticAttrs: attrs,
		})
	}
	deps.taskStateStore = taskstate.NewStoreWithLogger(paths, logger)
	return deps, nil
}

func (o *rootOptions) invocation(command *cobra.Command) (*invocationDependencies, error) {
	if o.invocationDeps != nil {
		return o.invocationDeps, nil
	}
	deps, err := newInvocationDependencies(command, o.log())
	if err != nil {
		return nil, err
	}
	o.invocationDeps = deps
	return deps, nil
}
