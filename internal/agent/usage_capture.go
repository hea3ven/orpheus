package agent

import (
	"os"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

// UsageCaptureOptions describes a launched harness-backed process.
type UsageCaptureOptions struct {
	Harness       string
	ExecutionDir  string
	ExecutionDirs []string
	SessionName   string
	StartedAt     time.Time
	Env           map[string]string
}

// UsageCaptureEnvironment returns environment values needed to find supported harness session logs.
func UsageCaptureEnvironment() map[string]string {
	env := map[string]string{}
	for _, key := range []string{
		"CODEX_HOME",
		"HOME",
		"PI_CODING_AGENT_DIR",
		"PI_CODING_AGENT_SESSION_DIR",
	} {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	return env
}

// CaptureUsage correlates an Orpheus execution with harness-specific usage logs.
func CaptureUsage(opts UsageCaptureOptions) taskstate.RecordRunUsageOptions {
	switch strings.TrimSpace(opts.Harness) {
	case codexHarness:
		return CaptureCodexUsage(CodexUsageCaptureOptions{
			ExecutionDir:  opts.ExecutionDir,
			ExecutionDirs: opts.ExecutionDirs,
			StartedAt:     opts.StartedAt,
			Env:           opts.Env,
		})
	case piHarness:
		return CapturePiUsage(PiUsageCaptureOptions{
			ExecutionDir:  opts.ExecutionDir,
			ExecutionDirs: opts.ExecutionDirs,
			SessionName:   opts.SessionName,
			StartedAt:     opts.StartedAt,
			Env:           opts.Env,
		})
	default:
		return taskstate.RecordRunUsageOptions{
			UsageCapture: taskstate.AgentUsageCapture{
				Status: taskstate.UsageCaptureUnknown,
				Reason: "unsupported_harness:" + formatUsageCaptureHarness(opts.Harness),
			},
		}
	}
}

func formatUsageCaptureHarness(harness string) string {
	harness = strings.TrimSpace(harness)
	if harness == "" {
		return "unknown"
	}
	return harness
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
