package agent_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/logging"
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

func TestCaptureUsageDiagnosticsSanitizeSessionReadErrors(t *testing.T) {
	var diagnostics bytes.Buffer
	secretRoot := filepath.Join(t.TempDir(), "secret-session-root") + "\x00"

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
