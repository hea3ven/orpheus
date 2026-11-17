package phaseexec_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAttachedAgentBindsMandatoryPIDCheckpointToReservedOwner(t *testing.T) {
	checkpointFailure := errors.New("PID checkpoint unavailable")
	for _, scenario := range []string{"checkpoint succeeds", "checkpoint fails", "missing checkpoint", "wrong owner"} {
		t.Run(scenario, func(t *testing.T) {
			prepared, err := agent.PrepareImplementation(agent.Config{
				Defaults: agent.AgentDefaults{Implementer: "implementer"},
				Agents:   map[string]agent.Profile{"implementer": {Command: "semantic-agent"}},
			}, agent.ImplementationPreparation{SessionName: "Feature"})
			require.NoError(t, err)
			owner := work.AttemptRef{RepositoryID: "alpha", TaskID: "op-1", Workflow: 2, Phase: 3}
			execution := prepared.Execution()
			execution.Address = owner
			var launches, checkpoints, captures int
			attached := phaseexec.AttachedAgents{RecordPID: func(ref work.AttemptRef, pid int) error {
				checkpoints++
				assert.Equal(t, owner, ref)
				assert.Equal(t, 4321, pid)
				if scenario == "checkpoint fails" {
					return checkpointFailure
				}
				return nil
			}}
			attached.Launcher = attachedLauncherFunc(func(_ context.Context, _ agentexec.Command, opts agentexec.LaunchOptions) error {
				launches++
				require.NotNil(t, opts.OnStart)
				err := opts.OnStart(4321)
				// The concrete launcher owns kill/reap on checkpoint failure;
				// this collaborator supplies its independent observed exit.
				opts.OnExit(agentexec.ProcessExit{PID: 4321, Code: 0, At: time.Now().UTC()})
				return err
			})
			if scenario == "missing checkpoint" {
				attached.RecordPID = nil
			}
			if scenario == "wrong owner" {
				execution.Address.Phase++
			}
			result := attached.Run(context.Background(), owner, phaseexec.AgentRun{
				Prepared: prepared, Execution: execution, Workdir: "/fixture/task", Branch: "orpheus/op-1",
				CaptureUsage: func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
					captures++
					if scenario != "missing checkpoint" {
						assert.Equal(t, 1, checkpoints)
					}
					return taskstate.RecordRunUsageOptions{}
				},
			})
			if scenario == "wrong owner" {
				require.Error(t, result.Err)
				assert.False(t, result.Started)
				assert.Zero(t, launches)
				assert.Zero(t, checkpoints)
				assert.Zero(t, captures)
				return
			}
			assert.Equal(t, 1, launches)
			assert.True(t, result.Started)
			require.NotNil(t, result.Exit, "a checkpoint failure must not erase actual exit evidence")
			assert.Equal(t, 4321, result.Exit.PID)
			assert.Equal(t, 1, captures, "usage remains independent of a PID recording error")
			switch scenario {
			case "checkpoint succeeds":
				require.NoError(t, result.Err)
			case "checkpoint fails":
				require.ErrorIs(t, result.Err, checkpointFailure)
			case "missing checkpoint":
				require.ErrorContains(t, result.Err, "child PID recorder is required")
			}
		})
	}
}

type attachedLauncherFunc func(context.Context, agentexec.Command, agentexec.LaunchOptions) error

func (f attachedLauncherFunc) Run(ctx context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
	return f(ctx, command, opts)
}
