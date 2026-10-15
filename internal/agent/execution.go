package agent

import (
	"context"
	"errors"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// ExecutionOptions describes one attached execution and its usage correlation.
// Workflow owners still decide whether the execution satisfies their purpose.
type ExecutionOptions struct {
	Command agentexec.Command
	Launch  agentexec.LaunchOptions
	Usage   UsageCaptureOptions

	// BeforeStart lets the frontend present the execution before launching it.
	// A presentation failure prevents launch and session discovery.
	BeforeStart  func() error
	CaptureUsage func(UsageCaptureOptions) taskstate.RecordRunUsageOptions
}

// ExecutionResult keeps the process outcome independent of best-effort usage.
// Started is true even if recording the child's PID failed after startup.
type ExecutionResult struct {
	Started bool
	Err     error
	Usage   taskstate.RecordRunUsageOptions
}

// Execute launches once and attempts usage capture after every started process,
// including failed and canceled executions. It never discovers sessions for a
// process that did not start. Durable state and workflow success belong to callers.
func Execute(ctx context.Context, launcher agentexec.Launcher, opts ExecutionOptions) ExecutionResult {
	result := ExecutionResult{Usage: taskstate.RecordRunUsageOptions{
		UsageCapture: taskstate.AgentUsageCapture{
			Status: taskstate.UsageCaptureUnknown,
			Reason: "agent process failed before usage capture",
		},
	}}
	if opts.BeforeStart != nil {
		if err := opts.BeforeStart(); err != nil {
			result.Err = err
			return result
		}
	}
	if launcher == nil {
		result.Err = errors.New("agent launcher is required")
		return result
	}
	onStart := opts.Launch.OnStart
	opts.Launch.OnStart = func(pid int) error {
		result.Started = true
		if onStart != nil {
			return onStart(pid)
		}
		return nil
	}
	result.Err = launcher.Run(ctx, opts.Command, opts.Launch)
	if !result.Started {
		if result.Err == nil {
			result.Err = errors.New("agent launcher returned without recording process startup")
		}
		return result
	}

	capture := opts.CaptureUsage
	if capture == nil {
		capture = CaptureUsage
	}
	opts.Usage.Harness = opts.Command.Harness
	opts.Usage.ExecutionDir = opts.Launch.Dir
	// Accounting must remain possible after cancellation of the attached process.
	opts.Usage.Context = context.WithoutCancel(ctx)
	result.Usage = capture(opts.Usage)
	return result
}
