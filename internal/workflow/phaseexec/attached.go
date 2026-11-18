package phaseexec

import (
	"context"
	"errors"
	"io"
	"log/slog"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

// AttachedAgents binds process effects to the owner's existing synchronous PID
// checkpoint. Phase composition supplies recording; individual runs cannot omit
// or replace it. Prepared execution still owns command and session selection.
type AttachedAgents struct {
	Launcher  agentexec.Launcher
	RecordPID func(work.AttemptRef, int) error
}

// AgentRun contains the frozen selection and invocation-scoped streams and
// accounting inputs. It deliberately excludes a caller-supplied PID callback.
type AgentRun struct {
	Prepared         agent.PreparedExecution
	Execution        taskstate.AgentExecution
	Workdir          string
	Branch           string
	Environment      []string
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
	UsageEnvironment map[string]string
	CaptureUsage     func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions
	Logger           *slog.Logger
	Attempt          int
}

// Run retains one attached launch/wait/reap operation. The child already exists
// when RecordPID runs; a checkpoint error uses the launcher's kill/reap path.
// Completion handoff, observed exit and usage remain distinct recording facts.
func (a AttachedAgents) Run(ctx context.Context, owner work.AttemptRef, run AgentRun) agent.ExecutionResult {
	if !run.Execution.Address.Equal(owner) {
		return agent.ExecutionResult{Err: errors.New("attached execution differs from reserved owner")}
	}
	return run.Prepared.Run(ctx, a.Launcher, agent.PreparedRunOptions{
		Execution: run.Execution, Workdir: run.Workdir, Branch: run.Branch,
		Environment: run.Environment, Stdin: run.Stdin, Stdout: run.Stdout, Stderr: run.Stderr,
		UsageEnvironment: run.UsageEnvironment, CaptureUsage: run.CaptureUsage,
		Logger: run.Logger, Attempt: run.Attempt,
		RecordChildPID: func(pid int) error {
			if a.RecordPID == nil {
				return errors.New("attached agent child PID recorder is required")
			}
			return a.RecordPID(owner, pid)
		},
	})
}
