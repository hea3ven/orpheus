package cli

import (
	"context"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/beads"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
	"github.com/spf13/cobra"
)

type invocationDependencies struct {
	prProvider         pullrequest.Provider
	syncGit            workflow.SyncGit
	cleanupGit         workflow.ClosedTaskWorktreeGit
	reviewEffects      review.Effects
	reviewStatus       func(context.Context, string) (string, error)
	finalizationGit    workflow.FinalizationGit
	paths              state.Paths
	logger             *slog.Logger
	registryStore      registry.Store
	taskBackendFactory taskmodel.BackendFactory
	inspectGit         func(context.Context, string) (gitmeta.Inspection, error)
	inspectLocalBeads  func(string, ...slog.Attr) (beads.LocalInspection, error)
	initializeBeads    func(string, string, ...slog.Attr) error
	taskStateStore     taskstate.Store
	environment        map[string]string
	agentLauncher      agentexec.Launcher
	dispatchGit        workflow.DispatchGit
	agentGit           agent.GitStateReader
	reviewCandidate    workflow.ReviewCandidateInspector
	reviewPipeline     func(review.PipelineRunOptions) (review.PipelineOutcome, error)
	processProbe       workflow.ProcessProbe
	agentCWD           string
	taskCWD            string
	captureUsage       func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions
}

func newInvocationDependencies(command *cobra.Command, logger *slog.Logger, options CommandOptions) (*invocationDependencies, error) {
	ctx := command.Context()
	span := logging.Start(ctx, logger, "xdg path resolution",
		slog.String("component", "state"),
		slog.String("operation", "resolve_paths"),
	)
	paths, err := options.resolvePaths()
	if err != nil {
		span.FinishError(ctx, err)
		return nil, err
	}
	resolvedEnvironment := make(map[string]string, 2)
	paths.PropagateEnvironment(resolvedEnvironment)
	span.Finish(ctx, logging.StatusSuccess,
		slog.String("xdg_config_home", resolvedEnvironment["XDG_CONFIG_HOME"]),
		slog.String("xdg_data_home", resolvedEnvironment["XDG_DATA_HOME"]),
	)

	environment := options.Environment
	if environment == nil {
		environment = invocationEnvironmentSnapshot()
	}
	deps := newInvocationDependenciesWithPaths(paths, logger, environment)
	deps.agentCWD = options.AgentWorkingDirectory
	deps.taskCWD = options.TaskWorkingDirectory
	options.Dependencies.applyTo(deps)
	return deps, nil
}

func newInvocationDependenciesWithPaths(paths state.Paths, logger *slog.Logger, environment map[string]string) *invocationDependencies {
	environment = maps.Clone(environment)
	if environment == nil {
		environment = make(map[string]string)
	}
	paths.PropagateEnvironment(environment)
	deps := &invocationDependencies{
		paths:           paths,
		logger:          logger,
		registryStore:   registry.NewStoreWithLogger(paths, logger),
		environment:     environment,
		agentLauncher:   agentexec.AttachedLauncher{Environment: environmentEntries(environment)},
		dispatchGit:     workflow.LocalDispatchGit{},
		agentGit:        agent.LocalGitState{},
		reviewCandidate: workflow.LocalReviewCandidateInspector{},
		reviewPipeline:  review.RunPipeline,
		processProbe:    agentexec.ProbePID,
		captureUsage:    agent.CaptureUsage,
	}
	deps.taskBackendFactory = func(source taskmodel.RepositorySource) (taskmodel.ReadBackend, error) {
		return beads.NewTaskBackendForSourceWithRunner(source, beads.CommandRunner{
			Logger:      logger,
			Environment: environmentEntries(deps.environment),
			DiagnosticAttrs: []slog.Attr{
				slog.String("repo_id", source.Repository.ID),
			},
		}, logger)
	}
	deps.inspectGit = func(ctx context.Context, path string) (gitmeta.Inspection, error) {
		return gitmeta.InspectWithLogger(ctx, path, logger)
	}
	deps.inspectLocalBeads = func(path string, attrs ...slog.Attr) (beads.LocalInspection, error) {
		runner := beads.NewInspectLocalRunner(logger, attrs...)
		if commandRunner, ok := runner.(beads.CommandRunner); ok {
			commandRunner.Environment = environmentEntries(deps.environment)
			runner = commandRunner
		}
		return beads.InspectLocalWithRunner(path, runner)
	}
	deps.initializeBeads = func(path string, prefix string, attrs ...slog.Attr) error {
		return beads.InitializeManagedWithRunner(path, prefix, beads.CommandRunner{
			Logger:          logger,
			DiagnosticAttrs: attrs,
			Environment:     environmentEntries(deps.environment),
		})
	}
	deps.taskStateStore = taskstate.NewStoreWithLogger(paths, logger)
	return deps
}

func invocationEnvironmentSnapshot() map[string]string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for key, value := range agent.UsageCaptureEnvironment() {
		values[key] = value
	}
	return values
}

func environmentEntries(values map[string]string) []string {
	entries := make([]string, 0, len(values))
	for key, value := range values {
		entries = append(entries, key+"="+value)
	}
	slices.Sort(entries)
	return entries
}

func stateEnvironmentEntries(paths state.Paths) []string {
	environment := make(map[string]string, 2)
	paths.PropagateEnvironment(environment)
	return environmentEntries(environment)
}

func (d *invocationDependencies) environmentValue(name string) string {
	return d.environment[name]
}

func (d *invocationDependencies) resumeSessionsEnabled() bool {
	return d.environmentValue("ORPHEUS_RESUME_SESSIONS") == "1"
}

func (d *invocationDependencies) invocationEnvironment(values []string) []string {
	values = append([]string{}, values...)
	return append(values, stateEnvironmentEntries(d.paths)...)
}

func (d *invocationDependencies) executable(name string) string {
	path := d.environmentValue("PATH")
	for _, directory := range filepath.SplitList(path) {
		candidate := filepath.Join(directory, name)
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			absolute, absErr := filepath.Abs(candidate)
			if absErr == nil {
				return absolute
			}
		}
	}
	return filepath.Join(os.TempDir(), "orpheus-missing-executable", filepath.Base(name))
}

func (d *invocationDependencies) usageCaptureEnvironment() map[string]string {
	values := make(map[string]string, 4)
	for _, key := range []string{"CODEX_HOME", "HOME", "PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR"} {
		if value, ok := d.environment[key]; ok {
			values[key] = value
		}
	}
	return values
}

func (o *rootOptions) invocation(command *cobra.Command) (*invocationDependencies, error) {
	if o.invocationDeps != nil {
		return o.invocationDeps, nil
	}
	deps, err := newInvocationDependencies(command, o.log(), o.commandOptions)
	if err != nil {
		return nil, err
	}
	o.invocationDeps = deps
	return deps, nil
}
