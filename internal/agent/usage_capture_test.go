package agent_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCaptureUsageDiagnosticsSanitizeDiscoveryErrors(t *testing.T) {
	var diagnostics bytes.Buffer

	result := agent.CaptureUsage(agent.UsageCaptureOptions{
		Harness: "codex",
		Env: map[string]string{
			"CODEX_HOME": "relative-secret-home",
		},
		Logger: logging.New(&diagnostics, logging.Config{Verbose: true}),
	})

	require.Contains(t, result.UsageCapture.Reason, "relative-secret-home")
	output := diagnostics.String()
	require.Contains(t, output, `component=agent operation=usage_capture`)
	require.Contains(t, output, `reason=codex_home_unavailable`)
	require.NotContains(t, output, "relative-secret-home")
	require.NotContains(t, output, "CODEX_HOME must be absolute")
}

func TestUsageCaptureDoesNotReadOperatorHomeWithoutExplicitFixture(t *testing.T) {
	operatorHome := testutil.CanonicalTempDir(t)
	workdir := filepath.Join(testutil.CanonicalTempDir(t), "worktree")
	require.NoError(t, os.MkdirAll(workdir, 0o755))
	startedAt := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)

	codexSessionDir := filepath.Join(operatorHome, ".codex", "sessions", "2026", "07", "07")
	require.NoError(t, os.MkdirAll(codexSessionDir, 0o755))
	writeCodexSessionLogAt(t, filepath.Join(codexSessionDir, "sentinel.jsonl"), workdir, "codex-sentinel", startedAt)
	writePiSessionLog(t, filepath.Join(operatorHome, ".pi", "agent", "sessions", "project", "sentinel.jsonl"), piSessionLogFixture{
		cwd:       workdir,
		sessionID: "pi-sentinel",
		startedAt: startedAt,
	})
	t.Setenv("HOME", operatorHome)

	for _, env := range []map[string]string{nil, {}} {
		t.Run("capture usage", func(t *testing.T) {
			for _, harness := range []string{"codex", "pi"} {
				t.Run(harness, func(t *testing.T) {
					got := agent.CaptureUsage(agent.UsageCaptureOptions{
						Harness:      harness,
						ExecutionDir: workdir,
						StartedAt:    startedAt,
						Env:          env,
					})
					require.Nil(t, got.Session, "CaptureUsage(%s) discovered operator session", harness)
				})
			}
		})
		t.Run("capture codex usage", func(t *testing.T) {
			got := agent.CaptureCodexUsage(agent.CodexUsageCaptureOptions{
				ExecutionDir: workdir,
				StartedAt:    startedAt,
				Env:          env,
			})
			require.Nil(t, got.Session, "CaptureCodexUsage discovered operator session")
		})
		t.Run("capture pi usage", func(t *testing.T) {
			got := agent.CapturePiUsage(agent.PiUsageCaptureOptions{
				ExecutionDir: workdir,
				StartedAt:    startedAt,
				Env:          env,
			})
			require.Nil(t, got.Session, "CapturePiUsage discovered operator session")
		})
	}
}

func TestUsageCaptureEnvironmentUsesIsolatedRootsUnlessTestProvidesFixtures(t *testing.T) {
	env := agent.UsageCaptureEnvironment()
	for _, key := range []string{"CODEX_HOME", "HOME", "PI_CODING_AGENT_DIR", "PI_CODING_AGENT_SESSION_DIR"} {
		value := env[key]
		if !filepath.IsAbs(value) {
			t.Fatalf("%s = %q, want absolute isolated root", key, value)
		}
	}
	if filepath.Dir(env["CODEX_HOME"]) != filepath.Dir(env["PI_CODING_AGENT_SESSION_DIR"]) {
		t.Fatalf("test roots do not share an isolated parent: %#v", env)
	}

	fixture := testutil.CanonicalTempDir(t)
	t.Setenv("CODEX_HOME", fixture)
	if got := agent.UsageCaptureEnvironment()["CODEX_HOME"]; got != fixture {
		t.Fatalf("CODEX_HOME = %q, want explicit fixture %q", got, fixture)
	}
}

func TestCaptureUsageDiagnosticsSanitizeSessionReadErrors(t *testing.T) {
	var diagnostics bytes.Buffer
	secretRoot := filepath.Join(testutil.CanonicalTempDir(t), "secret-session-root") + "\x00"

	result := agent.CaptureUsage(agent.UsageCaptureOptions{
		Harness: "codex",
		Env: map[string]string{
			"CODEX_HOME": secretRoot,
		},
		Logger: logging.New(&diagnostics, logging.Config{Verbose: true}),
	})

	require.Contains(t, result.UsageCapture.Reason, "secret-session-root")
	output := diagnostics.String()
	require.Contains(t, output, `component=agent operation=usage_capture`)
	require.Contains(t, output, `reason=read_codex_sessions_failed`)
	require.NotContains(t, output, "secret-session-root")
	require.NotContains(t, output, "invalid argument")
}
