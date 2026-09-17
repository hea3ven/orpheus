package cli

import (
	"context"
	"log/slog"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/beads"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/review"
	"github.com/hea3ven/orpheus/internal/state"
	taskmodel "github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

// CommandOptions supplies invocation inputs and external collaborators.
// The zero value uses the process environment and production adapters.
// Registry and task-state stores are always constructed by the CLI.
type CommandOptions struct {
	// Paths overrides XDG resolution. Supply a value constructed by state.
	Paths *state.Paths
	// Environment replaces, rather than extends, the process environment.
	// Nil uses the process environment; an empty map provides no inherited values.
	// The selected Paths always determine the XDG roots passed to children.
	Environment map[string]string
	// AgentWorkingDirectory overrides current-directory discovery for agent commands.
	// Empty uses the process working directory.
	AgentWorkingDirectory string
	// TaskWorkingDirectory overrides current-directory discovery for task done inference.
	// Empty uses the process working directory.
	TaskWorkingDirectory string
	Dependencies         Dependencies
}

// Dependencies supplies external effects used by CLI workflows. Nil fields use
// production adapters. Collaborators are shared by reference and must remain
// valid for the lifetime of commands constructed with them.
type Dependencies struct {
	PRProvider         pullrequest.Provider
	SyncGit            workflow.SyncGit
	CleanupGit         workflow.ClosedTaskWorktreeGit
	ReviewEffects      review.Effects
	ReviewStatus       func(context.Context, string) (string, error)
	FinalizationGit    workflow.FinalizationGit
	TaskBackendFactory taskmodel.BackendFactory
	InspectGit         func(context.Context, string) (gitmeta.Inspection, error)
	InspectLocalBeads  func(string, ...slog.Attr) (beads.LocalInspection, error)
	InitializeBeads    func(string, string, ...slog.Attr) error
	AgentLauncher      agentexec.Launcher
	DispatchGit        workflow.DispatchGit
	AgentGit           agent.GitStateReader
	ReviewCandidate    workflow.ReviewCandidateInspector
	ReviewPipeline     func(review.PipelineRunOptions) (review.PipelineOutcome, error)
	ProcessProbe       workflow.ProcessProbe
	// CaptureUsage reads session usage after implementation and repair dispatch.
	CaptureUsage func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions
}

func (o CommandOptions) resolvePaths() (state.Paths, error) {
	if o.Paths != nil {
		return *o.Paths, nil
	}
	if o.Environment != nil {
		return state.Resolve(state.ResolveOptions{HomeDir: o.Environment["HOME"], Env: o.Environment})
	}
	return state.ResolveFromEnvironment()
}

func (d Dependencies) applyTo(invocation *invocationDependencies) {
	invocation.prProvider = d.PRProvider
	invocation.syncGit = d.SyncGit
	invocation.cleanupGit = d.CleanupGit
	invocation.reviewEffects = d.ReviewEffects
	invocation.reviewStatus = d.ReviewStatus
	invocation.finalizationGit = d.FinalizationGit
	if d.CaptureUsage != nil {
		invocation.captureUsage = d.CaptureUsage
	}
	if d.TaskBackendFactory != nil {
		invocation.taskBackendFactory = d.TaskBackendFactory
	}
	if d.InspectGit != nil {
		invocation.inspectGit = d.InspectGit
	}
	if d.InspectLocalBeads != nil {
		invocation.inspectLocalBeads = d.InspectLocalBeads
	}
	if d.InitializeBeads != nil {
		invocation.initializeBeads = d.InitializeBeads
	}
	if d.AgentLauncher != nil {
		invocation.agentLauncher = d.AgentLauncher
	}
	if d.DispatchGit != nil {
		invocation.dispatchGit = d.DispatchGit
	}
	if d.AgentGit != nil {
		invocation.agentGit = d.AgentGit
	}
	if d.ReviewCandidate != nil {
		invocation.reviewCandidate = d.ReviewCandidate
	}
	if d.ReviewPipeline != nil {
		invocation.reviewPipeline = d.ReviewPipeline
	}
	if d.ProcessProbe != nil {
		invocation.processProbe = d.ProcessProbe
	}
}
