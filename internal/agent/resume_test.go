package agent_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareFollowUpResumePreservesFreshCommandWhenDisabled(t *testing.T) {
	command := resumeTestCommand("pi", false)
	got, launch := agent.PrepareFollowUpResume(agent.FollowUpResumeOptions{
		Command: command,
		Enabled: false,
	})

	assert.Equal(t, command.Args, got.Args)
	require.NotNil(t, launch)
	assert.Equal(t, taskstate.AgentLaunchFresh, launch.Mode)
	assert.Empty(t, launch.FallbackReason)
}

func TestPrepareFollowUpResumeBuildsStructuredCommandsInBothLaunchModes(t *testing.T) {
	tests := []struct {
		name           string
		harness        string
		nonInteractive bool
		wantPrefix     []string
	}{
		{name: "Pi interactive", harness: "pi", wantPrefix: []string{"--session"}},
		{name: "Pi non-interactive", harness: "pi", nonInteractive: true, wantPrefix: []string{"--print", "--session"}},
		{name: "Codex interactive", harness: "codex", wantPrefix: []string{"resume"}},
		{name: "Codex non-interactive", harness: "codex", nonInteractive: true, wantPrefix: []string{"exec", "resume"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workdir := filepath.Join(t.TempDir(), "worktree")
			require.NoError(t, os.MkdirAll(workdir, 0o755))
			session := writeResumeTestSession(t, tt.harness, workdir, "session-1")
			state := resumeTestState(1, "implementer", tt.harness, session)

			got, launch := agent.PrepareFollowUpResume(agent.FollowUpResumeOptions{
				Command:      resumeTestCommand(tt.harness, tt.nonInteractive),
				State:        state,
				ExecutionDir: workdir,
				Env:          resumeTestEnvironment(tt.harness, session),
				Enabled:      true,
			})

			require.NotNil(t, launch)
			assert.Equal(t, taskstate.AgentLaunchResumed, launch.Mode)
			assert.Equal(t, 1, launch.SourceRunAttempt)
			require.NotNil(t, launch.SourceSession)
			assert.Equal(t, "session-1", launch.SourceSession.ID)
			assert.Equal(t, tt.wantPrefix, got.Args[:len(tt.wantPrefix)])
			assert.Equal(t, "Run orpheus agent context now.", got.Args[len(got.Args)-1])
			assert.NotContains(t, got.Args, "Follow-up session")
			if tt.harness == "pi" {
				assert.Contains(t, got.Args, session.LogPath)
				assert.NotContains(t, got.Args, "--name")
			} else {
				assert.Contains(t, got.Args, session.ID)
			}
		})
	}
}

func TestPrepareFollowUpResumeSelectsLatestUsableCompatibleRun(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, os.MkdirAll(workdir, 0o755))
	older := writeResumeTestSession(t, "pi", workdir, "older")
	latest := writeResumeTestSession(t, "pi", workdir, "latest")
	state := resumeTestState(1, "implementer", "pi", older)
	state.Runs = append(state.Runs,
		resumeTestRun(2, "other-profile", "pi", latest),
		resumeTestRun(3, "implementer", "pi", latest),
	)

	_, launch := agent.PrepareFollowUpResume(agent.FollowUpResumeOptions{
		Command:      resumeTestCommand("pi", false),
		State:        state,
		ExecutionDir: workdir,
		Enabled:      true,
	})

	require.NotNil(t, launch)
	assert.Equal(t, taskstate.AgentLaunchResumed, launch.Mode)
	assert.Equal(t, 3, launch.SourceRunAttempt)
	assert.Equal(t, "latest", launch.SourceSession.ID)
}

func TestPrepareFollowUpResumeSelectsSuccessfulResumedRunAfterCaptureReadFailure(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, os.MkdirAll(workdir, 0o755))
	session := writeResumeTestSession(t, "pi", workdir, "session-1")
	state := resumeTestState(1, "implementer", "pi", session)

	_, secondLaunch := agent.PrepareFollowUpResume(agent.FollowUpResumeOptions{
		Command:      resumeTestCommand("pi", false),
		State:        state,
		ExecutionDir: workdir,
		Enabled:      true,
	})
	require.Equal(t, taskstate.AgentLaunchResumed, secondLaunch.Mode)

	sessionLog, err := os.ReadFile(session.LogPath)
	require.NoError(t, err)
	require.NoError(t, os.Remove(session.LogPath))
	capture := agent.CaptureUsage(agent.UsageCaptureOptions{
		Harness:      "pi",
		ExecutionDir: workdir,
		Launch:       secondLaunch,
	})
	require.Equal(t, taskstate.UsageCaptureUnknown, capture.UsageCapture.Status)
	assert.Contains(t, capture.UsageCapture.Reason, "read_resumed_session_failed")
	require.Equal(t, &session, capture.Session)

	require.NoError(t, os.WriteFile(session.LogPath, sessionLog, 0o644))
	state.Runs = append(state.Runs, taskstate.RunAttempt{
		Attempt: 2,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary: "resumed repair complete",
		},
		Execution: taskstate.AgentExecution{
			Purpose:      taskstate.AgentExecutionPurposeImplementation,
			Profile:      "implementer",
			Harness:      "pi",
			Launch:       secondLaunch,
			Session:      capture.Session,
			UsageCapture: capture.UsageCapture,
		},
	})

	_, thirdLaunch := agent.PrepareFollowUpResume(agent.FollowUpResumeOptions{
		Command:      resumeTestCommand("pi", false),
		State:        state,
		ExecutionDir: workdir,
		Enabled:      true,
	})

	require.Equal(t, taskstate.AgentLaunchResumed, thirdLaunch.Mode)
	assert.Equal(t, 2, thirdLaunch.SourceRunAttempt)
	require.NotNil(t, thirdLaunch.SourceSession)
	assert.Equal(t, session.ID, thirdLaunch.SourceSession.ID)
}

func TestPrepareFollowUpResumeFallsBackForRawIncompatibleAndUnsafeState(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, os.MkdirAll(workdir, 0o755))
	deleted := taskstate.AgentSession{ID: "gone", LogPath: filepath.Join(t.TempDir(), "gone.jsonl")}
	tests := []struct {
		name    string
		command agent.CommandSnapshot
		state   taskstate.TaskState
		want    string
	}{
		{
			name:    "raw profile",
			command: agent.CommandSnapshot{AgentName: "raw", Command: "agent", Args: []string{"prompt"}},
			want:    "raw command",
		},
		{
			name:    "incompatible profile",
			command: resumeTestCommand("pi", false),
			state:   resumeTestState(1, "other", "pi", deleted),
			want:    "matches profile",
		},
		{
			name:    "deleted session",
			command: resumeTestCommand("pi", false),
			state:   resumeTestState(1, "implementer", "pi", deleted),
			want:    "unavailable",
		},
		{
			name:    "ambiguous telemetry",
			command: resumeTestCommand("pi", false),
			state: taskstate.TaskState{Runs: []taskstate.RunAttempt{{
				Attempt: 1, Status: taskstate.RunStatusSucceeded, Completion: &taskstate.Completion{Summary: "done"},
				Execution: taskstate.AgentExecution{Purpose: taskstate.AgentExecutionPurposeImplementation, Profile: "implementer", Harness: "pi"},
			}}},
			want: "missing or ambiguous",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := append([]string{}, tt.command.Args...)
			got, launch := agent.PrepareFollowUpResume(agent.FollowUpResumeOptions{
				Command:      tt.command,
				State:        tt.state,
				ExecutionDir: workdir,
				Enabled:      true,
			})
			assert.Equal(t, original, got.Args)
			require.NotNil(t, launch)
			assert.Equal(t, taskstate.AgentLaunchFresh, launch.Mode)
			assert.Contains(t, launch.FallbackReason, tt.want)
		})
	}
}

func TestPrepareFollowUpResumeFallsBackWhenCodexHomeChanged(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, os.MkdirAll(workdir, 0o755))
	session := writeResumeTestSession(t, "codex", workdir, "codex-session")
	state := resumeTestState(1, "implementer", "codex", session)
	activeHome := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(activeHome, "sessions"), 0o755))

	for _, nonInteractive := range []bool{false, true} {
		name := "interactive"
		if nonInteractive {
			name = "exec"
		}
		t.Run(name, func(t *testing.T) {
			command := resumeTestCommand("codex", nonInteractive)
			got, launch := agent.PrepareFollowUpResume(agent.FollowUpResumeOptions{
				Command: command, State: state, ExecutionDir: workdir,
				Env: map[string]string{"CODEX_HOME": activeHome}, Enabled: true,
			})

			assert.Equal(t, command.Args, got.Args)
			require.NotNil(t, launch)
			assert.Equal(t, taskstate.AgentLaunchFresh, launch.Mode)
			assert.Contains(t, launch.FallbackReason, "active Codex sessions root")
		})
	}
}

func TestPrepareFollowUpResumeRejectsDuplicateCodexSessionIDs(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, os.MkdirAll(workdir, 0o755))
	session := writeResumeTestSession(t, "codex", workdir, "codex-session")
	duplicatePath := filepath.Join(filepath.Dir(session.LogPath), "restored-session.jsonl")
	writeCodexSessionLog(t, duplicatePath, workdir, session.ID)
	state := resumeTestState(1, "implementer", "codex", session)
	env := resumeTestEnvironment("codex", session)

	for _, nonInteractive := range []bool{false, true} {
		name := "interactive"
		if nonInteractive {
			name = "exec"
		}
		t.Run(name, func(t *testing.T) {
			command := resumeTestCommand("codex", nonInteractive)
			got, launch := agent.PrepareFollowUpResume(agent.FollowUpResumeOptions{
				Command: command, State: state, ExecutionDir: workdir,
				Env: env, Enabled: true,
			})

			assert.Equal(t, command.Args, got.Args)
			require.NotNil(t, launch)
			assert.Equal(t, taskstate.AgentLaunchFresh, launch.Mode)
			assert.Contains(t, launch.FallbackReason, "session id is ambiguous")
			assert.Contains(t, launch.FallbackReason, duplicatePath)
		})
	}
}

func TestCaptureUsageReportsUnknownWhenResumedBaselineIsUnavailable(t *testing.T) {
	tests := []struct {
		name        string
		harness     string
		usageRecord string
	}{
		{
			name:        "Pi",
			harness:     "pi",
			usageRecord: `{"type":"message","id":"follow-up","timestamp":"2026-07-07T10:02:00Z","message":{"role":"assistant","usage":{"input":11,"output":7,"totalTokens":18,"cost":{"total":0.000321}}}}` + "\n",
		},
		{
			name:        "Codex",
			harness:     "codex",
			usageRecord: `{"timestamp":"2026-07-07T10:02:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":17,"output_tokens":13,"total_tokens":30}}}}` + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workdir := filepath.Join(t.TempDir(), "worktree")
			require.NoError(t, os.MkdirAll(workdir, 0o755))
			session := writeResumeTestSessionWithoutUsage(t, tt.harness, workdir, tt.harness+"-session")
			env := resumeTestEnvironment(tt.harness, session)
			_, launch := agent.PrepareFollowUpResume(agent.FollowUpResumeOptions{
				Command: resumeTestCommand(tt.harness, false), State: resumeTestState(1, "implementer", tt.harness, session),
				ExecutionDir: workdir, Env: env, Enabled: true,
			})
			require.Equal(t, taskstate.AgentLaunchResumed, launch.Mode)
			assert.Nil(t, launch.UsageBaseline)

			file, err := os.OpenFile(session.LogPath, os.O_APPEND|os.O_WRONLY, 0o644)
			require.NoError(t, err)
			_, err = file.WriteString(tt.usageRecord)
			require.NoError(t, err)
			require.NoError(t, file.Close())

			got := agent.CaptureUsage(agent.UsageCaptureOptions{
				Harness: tt.harness, ExecutionDir: workdir, Env: env, Launch: launch,
			})
			assert.Nil(t, got.Usage)
			assert.Nil(t, got.UsageCost)
			assert.Equal(t, taskstate.UsageCaptureUnknown, got.UsageCapture.Status)
			assert.Equal(t, "resumed_session_usage_baseline_unavailable", got.UsageCapture.Reason)
		})
	}
}

func TestCaptureUsageReportsOnlyIncrementalResumedPiUsageAndCost(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, os.MkdirAll(workdir, 0o755))
	session := writeResumeTestSession(t, "pi", workdir, "pi-session")
	state := resumeTestState(1, "implementer", "pi", session)
	_, launch := agent.PrepareFollowUpResume(agent.FollowUpResumeOptions{
		Command: resumeTestCommand("pi", false), State: state, ExecutionDir: workdir, Enabled: true,
	})
	require.Equal(t, taskstate.AgentLaunchResumed, launch.Mode)

	file, err := os.OpenFile(session.LogPath, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = file.WriteString(`{"type":"message","id":"follow-up","timestamp":"2026-07-07T10:02:00Z","message":{"role":"assistant","usage":{"input":11,"output":7,"cacheRead":3,"reasoning":2,"totalTokens":18,"cost":{"total":0.000321}}}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())

	got := agent.CaptureUsage(agent.UsageCaptureOptions{Harness: "pi", ExecutionDir: workdir, Launch: launch})
	require.NotNil(t, got.Usage)
	assert.Equal(t, taskstate.AgentUsage{InputTokens: 11, CachedInputTokens: 3, OutputTokens: 7, ReasoningOutputTokens: 2, TotalTokens: 18}, *got.Usage)
	require.NotNil(t, got.UsageCost)
	assert.Equal(t, int64(321), got.UsageCost.AmountMicroUSD)
	assert.Equal(t, taskstate.UsageCaptureCaptured, got.UsageCapture.Status)
}

func TestCaptureUsageReportsOnlyIncrementalResumedCodexUsage(t *testing.T) {
	workdir := filepath.Join(t.TempDir(), "worktree")
	require.NoError(t, os.MkdirAll(workdir, 0o755))
	session := writeResumeTestSession(t, "codex", workdir, "codex-session")
	state := resumeTestState(1, "implementer", "codex", session)
	env := resumeTestEnvironment("codex", session)
	_, launch := agent.PrepareFollowUpResume(agent.FollowUpResumeOptions{
		Command: resumeTestCommand("codex", true), State: state, ExecutionDir: workdir, Env: env, Enabled: true,
	})
	require.Equal(t, taskstate.AgentLaunchResumed, launch.Mode)

	file, err := os.OpenFile(session.LogPath, os.O_APPEND|os.O_WRONLY, 0o644)
	require.NoError(t, err)
	_, err = file.WriteString(`{"timestamp":"2026-07-07T10:02:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":140,"cached_input_tokens":50,"output_tokens":80,"reasoning_output_tokens":10,"total_tokens":220}}}}` + "\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())

	got := agent.CaptureUsage(agent.UsageCaptureOptions{Harness: "codex", ExecutionDir: workdir, Env: env, Launch: launch})
	require.NotNil(t, got.Usage)
	assert.Equal(t, taskstate.AgentUsage{InputTokens: 17, CachedInputTokens: 5, OutputTokens: 13, ReasoningOutputTokens: 2, TotalTokens: 30}, *got.Usage)
	assert.Nil(t, got.UsageCost)
	assert.Equal(t, taskstate.UsageCaptureCaptured, got.UsageCapture.Status)
}

func resumeTestCommand(harness string, nonInteractive bool) agent.CommandSnapshot {
	if harness == "pi" {
		args := []string{"--model", "gpt-5", "--name", "Follow-up session", "Run orpheus agent context now."}
		if nonInteractive {
			args = append([]string{"--print"}, args...)
		}
		return agent.CommandSnapshot{AgentName: "implementer", Command: "pi", Args: args, Harness: "pi", Model: "gpt-5"}
	}
	args := []string{"--model", "gpt-5", "--dangerously-bypass-approvals-and-sandbox", "Run orpheus agent context now."}
	if nonInteractive {
		args = append([]string{"exec"}, args...)
	}
	return agent.CommandSnapshot{AgentName: "implementer", Command: "codex", Args: args, Harness: "codex", Model: "gpt-5"}
}

func resumeTestState(attempt int, profile string, harness string, session taskstate.AgentSession) taskstate.TaskState {
	return taskstate.TaskState{Runs: []taskstate.RunAttempt{resumeTestRun(attempt, profile, harness, session)}}
}

func resumeTestRun(attempt int, profile string, harness string, session taskstate.AgentSession) taskstate.RunAttempt {
	return taskstate.RunAttempt{
		Attempt: attempt,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary: "done",
		},
		Execution: taskstate.AgentExecution{
			Purpose: taskstate.AgentExecutionPurposeImplementation,
			Profile: profile,
			Harness: harness,
			Session: &session,
		},
	}
}

func writeResumeTestSession(t *testing.T, harness string, workdir string, sessionID string) taskstate.AgentSession {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, harness+"-session.jsonl")
	if harness == "pi" {
		writePiSessionLog(t, path, piSessionLogFixture{
			cwd: workdir, sessionID: sessionID, sessionName: "Implementation", startedAt: time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC),
		})
	} else {
		path = filepath.Join(root, "sessions", harness+"-session.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		writeCodexSessionLog(t, path, workdir, sessionID)
	}
	return taskstate.AgentSession{ID: sessionID, LogPath: path}
}

func writeResumeTestSessionWithoutUsage(t *testing.T, harness string, workdir string, sessionID string) taskstate.AgentSession {
	t.Helper()
	session := writeResumeTestSession(t, harness, workdir, sessionID)
	timestamp := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC).Format(time.RFC3339Nano)
	var content string
	if harness == "pi" {
		content = `{"type":"session","version":3,"id":"` + sessionID + `","timestamp":"` + timestamp + `","cwd":"` + workdir + `"}` + "\n"
	} else {
		content = `{"timestamp":"` + timestamp + `","type":"session_meta","payload":{"session_id":"` + sessionID + `","id":"` + sessionID + `","timestamp":"` + timestamp + `","cwd":"` + workdir + `","model":"gpt-5"}}` + "\n"
	}
	require.NoError(t, os.WriteFile(session.LogPath, []byte(content), 0o644))
	return session
}

func resumeTestEnvironment(harness string, session taskstate.AgentSession) map[string]string {
	if harness != "codex" {
		return nil
	}
	return map[string]string{"CODEX_HOME": filepath.Dir(filepath.Dir(session.LogPath))}
}
