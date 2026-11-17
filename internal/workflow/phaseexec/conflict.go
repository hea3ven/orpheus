package phaseexec

import (
	"context"
	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// ConflictResolver resolves merge conflicts left in a PR branch worktree.
type ConflictResolver interface {
	PrepareSyncConflictResolution(
		ctx context.Context,
		opts ConflictResolutionOptions,
	) (PreparedConflictResolution, error)
}

// PreparedConflictResolution describes a selected conflict-repair agent launch.
type PreparedConflictResolution struct {
	Execution taskstate.AgentExecution
	Execute   func(context.Context, taskstate.AgentExecution) agent.ExecutionResult
}

// ConflictResolutionOptions describes one conflicted open-PR branch repair.
type ConflictResolutionOptions struct {
	Address       work.AttemptRef
	Repository    task.Repository
	Task          task.Task
	Branch        string
	Worktree      string
	DefaultBranch string
	PRURL         string
	ConflictFiles []string
	// RecordChildPID persists the direct resolver PID at the launcher boundary.
	RecordChildPID func(int) error
}
