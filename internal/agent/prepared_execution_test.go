package agent_test

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreparedExecutionFreezesSelectionAndAssemblesPurposeSpecificLaunch(t *testing.T) {
	for _, tc := range []struct {
		name, profile, session, purpose, stdin string
		prepare                                func(agent.Config) (agent.PreparedExecution, error)
		step, sync                             *int
	}{
		{name: "implementation default keeps attached stdin", profile: "implementer", session: "Feature", stdin: "operator input",
			prepare: func(c agent.Config) (agent.PreparedExecution, error) {
				return agent.PrepareImplementation(c, agent.ImplementationPreparation{SessionName: "Feature"})
			}},
		{name: "implementation override", profile: "selected", session: "Feature", stdin: "operator input",
			prepare: func(c agent.Config) (agent.PreparedExecution, error) {
				return agent.PrepareImplementation(c, agent.ImplementationPreparation{Profile: "selected", SessionName: "Feature"})
			}},
		{name: "noninteractive reviewer has no stdin", profile: "reviewer", session: "Review", purpose: "review", step: preparedIndex(0),
			prepare: func(c agent.Config) (agent.PreparedExecution, error) {
				return agent.PrepareReview(c, agent.ReviewPreparation{SessionName: "Review", Attempt: 3, Step: "inspect"})
			}},
		{name: "interactive reviewer override", profile: "selected", session: "Review", purpose: "review", stdin: "operator input", step: preparedIndex(0),
			prepare: func(c agent.Config) (agent.PreparedExecution, error) {
				return agent.PrepareReview(c, agent.ReviewPreparation{Profile: "selected", SessionName: "Review", Attempt: 3, Step: "inspect"})
			}},
		{name: "conflict resolver default", profile: "resolver", session: "sync-conflict-op-1", purpose: "conflict_resolution", sync: preparedIndex(1),
			prepare: func(c agent.Config) (agent.PreparedExecution, error) {
				return agent.PrepareConflictResolution(c, "op-1", []string{"a.go", "b.go"})
			}},
		{name: "direct conflict resolver falls back to implementer", profile: "implementer", session: "sync-conflict-op-1", purpose: "conflict_resolution",
			prepare: func(c agent.Config) (agent.PreparedExecution, error) {
				c.Defaults.SyncConflictResolver = ""
				return agent.PrepareConflictResolution(c, "op-1", []string{"a.go", "b.go"})
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := agent.Config{
				Defaults: agent.AgentDefaults{Implementer: "implementer", Reviewer: "reviewer", SyncConflictResolver: "resolver"},
				Agents:   map[string]agent.Profile{},
			}
			for _, name := range []string{"implementer", "reviewer", "resolver", "selected"} {
				config.Agents[name] = agent.Profile{Command: name + "-agent", Args: []string{"{{session_name}}", "{{prompt}}"}, Interactive: name == "selected"}
			}
			var launches, captures, childPID int
			var stdout, stderr bytes.Buffer
			prepared, err := tc.prepare(config)
			require.NoError(t, err)
			execution := prepared.Execution()
			assert.Equal(t, tc.profile, execution.Profile)
			assert.Equal(t, tc.session, execution.SessionName)
			assert.Equal(t, tc.profile+"-agent", execution.Command)
			assert.Nil(t, execution.Launch, "these purposes must not acquire an implicit resume")
			// Reservation owns the address and time, but cannot mutate the selected command.
			execution.Address = work.AttemptRef{RepositoryID: "alpha", TaskID: "op-1", Workflow: 2, Phase: 7, Step: tc.step, BranchSync: tc.sync}
			execution.StartedAt = time.Now().UTC()
			execution.Args[0] = "changed recorded copy"
			config.Defaults.Implementer = "selected"
			config.Agents[tc.profile] = agent.Profile{Command: "changed-after-preparation"}
			launcher := executionLauncherFunc(func(_ context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
				launches++
				assert.Equal(t, tc.profile+"-agent", command.Command)
				assert.Equal(t, tc.session, command.Args[0])
				assert.Equal(t, "/fixture/task", opts.Dir)
				env := preparedEnvironment(opts.Env)
				assert.Equal(t, "alpha", env["ORPHEUS_REPO_ID"])
				assert.Equal(t, "op-1", env["ORPHEUS_TASK_ID"])
				assert.Equal(t, "/fixture/task", env["ORPHEUS_WORKTREE"])
				assert.Equal(t, "orpheus/op-1", env["ORPHEUS_BRANCH"])
				assert.Equal(t, "2", env["ORPHEUS_WORKFLOW"])
				assert.Equal(t, "7", env["ORPHEUS_PHASE"])
				assert.Equal(t, "/fixture/config", env["XDG_CONFIG_HOME"])
				assert.Equal(t, tc.purpose, env["ORPHEUS_AGENT_PURPOSE"])
				assert.Contains(t, env["ORPHEUS_AGENT_PROMPT"], "Run `orpheus agent context` now")
				assert.Equal(t, command.Args[1], env["ORPHEUS_AGENT_PROMPT"])
				if tc.purpose == "review" {
					assert.Equal(t, "3", env["ORPHEUS_REVIEW_ATTEMPT"])
					assert.Equal(t, "inspect", env["ORPHEUS_REVIEW_STEP"])
					assert.Equal(t, "0", env["ORPHEUS_STEP_INDEX"])
				} else {
					assert.NotContains(t, env, "ORPHEUS_REVIEW_STEP")
					assert.NotContains(t, env, "ORPHEUS_STEP_INDEX")
				}
				if tc.purpose == "conflict_resolution" {
					assert.Equal(t, "a.go\nb.go", env["ORPHEUS_CONFLICT_FILES"])
					require.NotNil(t, opts.Stdin, "resolver must receive explicit EOF, not attached input")
				} else {
					assert.NotContains(t, env, "ORPHEUS_CONFLICT_FILES")
				}
				if tc.sync != nil {
					assert.Equal(t, "1", env["ORPHEUS_BRANCH_SYNC_INDEX"])
				} else {
					assert.NotContains(t, env, "ORPHEUS_BRANCH_SYNC_INDEX")
				}
				if opts.Stdin == nil {
					assert.Empty(t, tc.stdin)
				} else {
					input, err := io.ReadAll(opts.Stdin)
					require.NoError(t, err)
					assert.Equal(t, tc.stdin, string(input))
				}
				_, err := io.WriteString(opts.Stdout, "agent output")
				require.NoError(t, err)
				_, err = io.WriteString(opts.Stderr, "agent diagnostic")
				require.NoError(t, err)
				require.NoError(t, opts.OnStart(4321))
				opts.OnExit(agentexec.ProcessExit{PID: 4321, Code: 0, At: time.Now().UTC()})
				return nil
			})

			result := prepared.Run(context.Background(), launcher, agent.PreparedRunOptions{
				Execution: execution, Workdir: "/fixture/task", Branch: "orpheus/op-1",
				Environment: []string{"XDG_CONFIG_HOME=/fixture/config"},
				Stdin:       strings.NewReader("operator input"), Stdout: &stdout, Stderr: &stderr,
				RecordChildPID:   func(pid int) error { childPID = pid; return nil },
				UsageEnvironment: map[string]string{"HOME": "/fixture/home"},
				CaptureUsage: func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
					captures++
					assert.Equal(t, 4321, childPID)
					assert.Equal(t, tc.session, opts.SessionName)
					assert.Equal(t, execution.StartedAt, opts.StartedAt)
					assert.Equal(t, "/fixture/task", opts.ExecutionDir)
					assert.Equal(t, map[string]string{"HOME": "/fixture/home"}, opts.Env)
					return executionCapturedUsage()
				},
			})

			require.NoError(t, result.Err)
			require.NotNil(t, result.Exit)
			assert.Equal(t, 4321, result.Exit.PID)
			assert.Equal(t, 18, result.Usage.Usage.TotalTokens)
			assert.Equal(t, 1, launches)
			assert.Equal(t, 1, captures)
			assert.Equal(t, "agent output", stdout.String())
			assert.Equal(t, "agent diagnostic", stderr.String())
		})
	}
}

func TestPreparedRepairKeepsSelectedSessionAndIndependentUsageBaseline(t *testing.T) {
	workdir := testutil.CanonicalTempDir(t)
	session := writeResumeTestSession(t, "pi", workdir, "original-session")
	history := resumeTestState(1, "implementer", "pi", session)
	config := agent.Config{Defaults: agent.AgentDefaults{Implementer: "implementer"}, Agents: map[string]agent.Profile{
		"implementer": {Harness: "pi", Model: "gpt-5", Interactive: true},
	}}
	prepared, err := agent.PrepareImplementation(config, agent.ImplementationPreparation{SessionName: "Repair", Workdir: workdir, RepairHistory: &history})
	require.NoError(t, err)
	execution := prepared.Execution()
	require.NotNil(t, execution.Launch)
	assert.Equal(t, taskstate.AgentLaunchResumed, execution.Launch.Mode)
	assert.Equal(t, "original-session", execution.Launch.SourceSession.ID)
	require.NotNil(t, execution.Launch.UsageBaseline)
	assert.Equal(t, 180, execution.Launch.UsageBaseline.TotalTokens)
	execution.Launch.SourceSession.ID = "changed recorded copy"
	execution.Launch.UsageBaseline.TotalTokens = 999
	history.Runs[0].Execution.Session.ID = "changed history"
	var captured bool
	launcher := executionLauncherFunc(func(_ context.Context, command agentexec.Command, opts agentexec.LaunchOptions) error {
		assert.Contains(t, command.Args, session.LogPath)
		require.NoError(t, opts.OnStart(4321))
		opts.OnExit(agentexec.ProcessExit{PID: 4321, Code: 0, At: time.Now().UTC()})
		return nil
	})

	result := prepared.Run(context.Background(), launcher, agent.PreparedRunOptions{
		Execution: execution, Workdir: workdir,
		CaptureUsage: func(opts agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions {
			captured = true
			assert.Equal(t, "original-session", opts.Launch.SourceSession.ID)
			assert.Equal(t, 180, opts.Launch.UsageBaseline.TotalTokens)
			assert.Equal(t, 1, opts.Launch.SourceRunAttempt)
			return executionCapturedUsage()
		},
	})

	require.NoError(t, result.Err)
	assert.True(t, captured)
	assert.Equal(t, "original-session", prepared.Execution().Launch.SourceSession.ID)
}

func preparedIndex(index int) *int { return &index }

func preparedEnvironment(entries []string) map[string]string {
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}
