package agent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecuteAccountsForEveryStartedProcessWithoutReplacingItsOutcome(t *testing.T) {
	runtimeErr := errors.New("agent failed")
	pidErr := errors.New("PID persistence failed")
	for _, tt := range []struct {
		name   string
		runErr error
		pidErr error
		cancel bool
		usage  taskstate.RecordRunUsageOptions
	}{
		{name: "success", usage: executionCapturedUsage()},
		{name: "runtime failure", runErr: runtimeErr, usage: executionCapturedUsage()},
		{name: "cancellation with unknown usage", runErr: context.Canceled, cancel: true, usage: taskstate.RecordRunUsageOptions{
			UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureUnknown, Reason: "session unavailable"},
		}},
		{name: "deadline with ambiguous usage", runErr: context.DeadlineExceeded, usage: taskstate.RecordRunUsageOptions{
			UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureAmbiguous, Reason: "multiple sessions", CandidateCount: 2},
		}},
		{name: "PID persistence failure after startup", pidErr: pidErr, runErr: pidErr, usage: executionCapturedUsage()},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
			launch := &taskstate.AgentLaunch{Mode: taskstate.AgentLaunchResumed, SourceRunAttempt: 1}
			var launches, captures, pid int
			presented, returned := false, false
			var observedExit *agentexec.ProcessExit
			launcher := executionLauncherFunc(func(_ context.Context, _ agentexec.Command, opts agentexec.LaunchOptions) error {
				launches++
				require.True(t, presented)
				defer func() { returned = true }()
				defer func() {
					code := 0
					if tt.runErr != nil {
						code = 23
					}
					opts.OnExit(agentexec.ProcessExit{PID: 4321, Code: code, At: time.Now().UTC()})
				}()
				if err := opts.OnStart(4321); err != nil {
					return err
				}
				if tt.cancel {
					cancel()
				}
				return tt.runErr
			})

			result := agent.Execute(ctx, launcher, agent.ExecutionOptions{
				Command: agentexec.Command{Harness: "pi", Command: "pi"},
				Launch: agentexec.LaunchOptions{
					Dir: "/fixture/worktree",
					OnStart: func(childPID int) error {
						pid = childPID
						return tt.pidErr
					},
					OnExit: func(exit agentexec.ProcessExit) {
						require.True(t, returned, "forward only reconciled observations after launcher cleanup")
						observedExit = &exit
					},
				},
				BeforeStart: func() error { presented = true; return nil },
				Usage:       agent.UsageCaptureOptions{SessionName: "repair session", StartedAt: startedAt, Launch: launch, Env: map[string]string{"HOME": "/fixture/home"}},
				CaptureUsage: func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
					captures++
					require.True(t, returned, "capture must wait for process cleanup")
					require.NotNil(t, observedExit, "process observation must precede telemetry discovery")
					assert.NoError(t, opts.Context.Err(), "cancellation must not prevent accounting")
					assert.Equal(t, "pi", opts.Harness)
					assert.Equal(t, "/fixture/worktree", opts.ExecutionDir)
					assert.Equal(t, "repair session", opts.SessionName)
					assert.Equal(t, startedAt, opts.StartedAt)
					assert.Same(t, launch, opts.Launch)
					assert.Equal(t, map[string]string{"HOME": "/fixture/home"}, opts.Env)
					return tt.usage
				},
			})

			assert.True(t, result.Started)
			require.NotNil(t, result.Exit)
			assert.Equal(t, observedExit, result.Exit)
			assert.Equal(t, 4321, result.Exit.PID)
			if tt.runErr == nil {
				assert.Zero(t, result.Exit.Code)
			} else {
				assert.Equal(t, 23, result.Exit.Code)
			}
			assert.False(t, result.Exit.At.IsZero())
			assert.ErrorIs(t, result.Err, tt.runErr)
			assert.Equal(t, tt.usage, result.Usage)
			assert.Equal(t, 4321, pid)
			assert.Equal(t, 1, launches, "telemetry must never rerun an agent")
			assert.Equal(t, 1, captures)
		})
	}
}

func TestExecuteDoesNotDiscoverSessionsBeforeProcessStartup(t *testing.T) {
	presentationErr := errors.New("presentation unavailable")
	startErr := &agentexec.StartError{Err: errors.New("executable missing")}
	for _, tt := range []struct {
		name       string
		beforeErr  error
		launchErr  error
		noLauncher bool
		wantErr    error
	}{
		{name: "presentation failure", beforeErr: presentationErr, wantErr: presentationErr},
		{name: "start failure", launchErr: startErr, wantErr: startErr},
		{name: "canceled before startup", launchErr: &agentexec.StartError{Err: context.Canceled}, wantErr: context.Canceled},
		{name: "unclassified failure before startup", launchErr: presentationErr, wantErr: presentationErr},
		{name: "missing launcher", noLauncher: true},
		{name: "launcher omitted startup notification"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var launches, captures int
			var launcher agentexec.Launcher = executionLauncherFunc(func(context.Context, agentexec.Command, agentexec.LaunchOptions) error {
				launches++
				return tt.launchErr
			})
			if tt.noLauncher {
				launcher = nil
			}

			result := agent.Execute(context.Background(), launcher, agent.ExecutionOptions{
				BeforeStart: func() error { return tt.beforeErr },
				CaptureUsage: func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
					captures++
					return executionCapturedUsage()
				},
			})

			assert.False(t, result.Started)
			assert.Nil(t, result.Exit, "start failures cannot manufacture a process exit")
			require.Error(t, result.Err)
			if tt.wantErr != nil {
				assert.ErrorIs(t, result.Err, tt.wantErr)
			}
			assert.Nil(t, result.Usage.Session)
			assert.Nil(t, result.Usage.Usage)
			assert.Nil(t, result.Usage.UsageCost)
			assert.Equal(t, taskstate.UsageCaptureUnknown, result.Usage.UsageCapture.Status)
			assert.NotEmpty(t, result.Usage.UsageCapture.Reason)
			assert.Zero(t, captures)
			if tt.beforeErr != nil || tt.noLauncher {
				assert.Zero(t, launches)
			} else {
				assert.Equal(t, 1, launches)
			}
		})
	}
}

func TestExecuteRetainsExplicitUncertaintyForUnsupportedHarnesses(t *testing.T) {
	for _, tt := range []struct{ harness, reason string }{
		{reason: "unsupported_harness:unknown"},
		{harness: " local ", reason: "unsupported_harness:local"},
	} {
		t.Run(tt.reason, func(t *testing.T) {
			launcher := executionLauncherFunc(func(_ context.Context, _ agentexec.Command, opts agentexec.LaunchOptions) error {
				if err := opts.OnStart(4321); err != nil {
					return err
				}
				opts.OnExit(agentexec.ProcessExit{PID: 4321, Code: 0, At: time.Now().UTC()})
				return nil
			})
			result := agent.Execute(context.Background(), launcher, agent.ExecutionOptions{
				Command: agentexec.Command{Harness: tt.harness},
				Launch:  agentexec.LaunchOptions{Dir: "/fixture/worktree"},
			})
			require.NoError(t, result.Err)
			assert.True(t, result.Started)
			assert.Nil(t, result.Usage.Session)
			assert.Nil(t, result.Usage.Usage)
			assert.Nil(t, result.Usage.UsageCost)
			assert.Equal(t, taskstate.UsageCaptureUnknown, result.Usage.UsageCapture.Status)
			assert.Equal(t, tt.reason, result.Usage.UsageCapture.Reason)
		})
	}
}

func TestExecuteRequiresAnObservedExitFromItsOwnChild(t *testing.T) {
	for _, scenario := range []string{"missing exit", "wrong child", "missing time", "exit before start", "invalid code", "duplicate exit", "nonzero without error", "signal without error"} {
		t.Run(scenario, func(t *testing.T) {
			captures, forwarded := 0, 0
			launcher := executionLauncherFunc(func(_ context.Context, _ agentexec.Command, opts agentexec.LaunchOptions) error {
				require.NoError(t, opts.OnStart(4321))
				exit := agentexec.ProcessExit{PID: 4321, Code: 0, At: time.Now().UTC()}
				switch scenario {
				case "missing exit":
					return nil
				case "wrong child":
					exit.PID = 9999
				case "missing time":
					exit.At = time.Time{}
				case "exit before start":
					exit.At = time.Unix(1, 0).UTC()
				case "invalid code":
					exit.Code = -2
				case "nonzero without error":
					exit.Code = 7
				case "signal without error":
					exit.Code = -1
				}
				opts.OnExit(exit)
				if scenario == "duplicate exit" {
					opts.OnExit(exit)
				}
				return nil
			})

			result := agent.Execute(context.Background(), launcher, agent.ExecutionOptions{
				Launch: agentexec.LaunchOptions{OnExit: func(agentexec.ProcessExit) { forwarded++ }},
				CaptureUsage: func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
					captures++
					return executionCapturedUsage()
				},
			})

			require.Error(t, result.Err, "nil launcher error alone must never imply successful process exit")
			assert.True(t, result.Started)
			if scenario == "nonzero without error" || scenario == "signal without error" {
				require.NotNil(t, result.Exit, "an actual unsuccessful exit is still retained evidence")
				assert.NotZero(t, result.Exit.Code)
				assert.Equal(t, 1, forwarded)
			} else {
				assert.Nil(t, result.Exit, "unverified exit must not become capture authority")
				assert.Zero(t, forwarded, "a preliminary event must not escape before conflicting observations are reconciled")
			}
			assert.Equal(t, 1, captures, "uncertain process outcomes do not erase usage")
			assert.Equal(t, 18, result.Usage.Usage.TotalTokens)
		})
	}
}

func executionCapturedUsage() taskstate.RecordRunUsageOptions {
	return taskstate.RecordRunUsageOptions{
		Session:      &taskstate.AgentSession{ID: "session-1"},
		Usage:        &taskstate.AgentUsage{InputTokens: 11, OutputTokens: 7, TotalTokens: 18},
		UsageCapture: taskstate.AgentUsageCapture{Status: taskstate.UsageCaptureCaptured, Reason: "matched session", CandidateCount: 1},
	}
}

type executionLauncherFunc func(context.Context, agentexec.Command, agentexec.LaunchOptions) error

func (f executionLauncherFunc) Run(ctx context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
	return f(ctx, command, opts)
}
