package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	// Exit is present only when the launcher observed and reaped this child.
	// A nil error or an agent completion cannot substitute for this evidence.
	Exit  *agentexec.ProcessExit
	Err   error
	Usage taskstate.RecordRunUsageOptions
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
	result.Started, result.Exit, result.Err = runObservedProcess(ctx, launcher, opts.Command, opts.Launch)
	if !result.Started {
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

// runObservedProcess keeps direct-child evidence independent of telemetry and
// refuses missing or contradictory launcher observations as successful exits.
func runObservedProcess(ctx context.Context, launcher agentexec.Launcher, command agentexec.Command, launch agentexec.LaunchOptions) (started bool, exit *agentexec.ProcessExit, err error) {
	onStart, onExit := launch.OnStart, launch.OnExit
	var childPID, exits int
	var startedAt time.Time
	var observationErr error
	launch.OnStart = func(pid int) error {
		started = true
		childPID, startedAt = pid, time.Now().UTC()
		if onStart != nil {
			return onStart(pid)
		}
		return nil
	}
	launch.OnExit = func(observed agentexec.ProcessExit) {
		exits++
		if exits != 1 || childPID <= 0 || observed.PID != childPID || observed.Code < -1 || observed.At.IsZero() || observed.At.Before(startedAt) {
			observationErr = errors.New("agent launcher reported invalid or conflicting process exit evidence")
			exit = nil
			return
		}
		exit = &observed
	}
	err = launcher.Run(ctx, command, launch)
	err = errors.Join(err, observationErr)
	if !started {
		if err == nil {
			err = errors.New("agent launcher returned without recording process startup")
		}
		return started, exit, err
	}
	if exits == 0 {
		err = errors.Join(err, errors.New("agent launcher returned without observing process exit"))
	}
	if exit != nil {
		if exit.Code != 0 && err == nil {
			err = fmt.Errorf("agent process exited with status %d", exit.Code)
		}
		if onExit != nil {
			onExit(*exit)
		}
	}
	return started, exit, err
}
