package agent

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testguard"
)

// UsageCaptureOptions describes a launched harness-backed process.
type UsageCaptureOptions struct {
	Harness        string
	ExecutionDir   string
	ExecutionDirs  []string
	SessionName    string
	StartedAt      time.Time
	Env            map[string]string
	Logger         *slog.Logger
	Context        context.Context
	RepoID         string
	TaskID         string
	Attempt        int
	Launch         *taskstate.AgentLaunch
	ResumeBoundary *ResumedUsageBoundary
}

// ResumedUsageBoundary is a later launch's immutable pre-launch snapshot of
// the same session. Delayed recovery uses it as the end of an earlier resumed
// execution after that session has subsequently been reused.
type ResumedUsageBoundary struct {
	Session      *taskstate.AgentSession
	Usage        *taskstate.AgentUsage
	CostMicroUSD *int64
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
		if value, ok := usageCaptureEnvironmentValue(key); ok {
			env[key] = value
		}
	}
	return env
}

func usageCaptureEnvironmentValue(key string) (string, bool) {
	if !testguard.IsTestProcess() || testguard.EnvironmentChanged(key) {
		return os.LookupEnv(key)
	}
	roots := testguard.IsolatedUsageRoots()
	switch key {
	case "CODEX_HOME":
		return roots.CodexHome, true
	case "HOME":
		return roots.Home, true
	case "PI_CODING_AGENT_DIR":
		return roots.PiDir, true
	case "PI_CODING_AGENT_SESSION_DIR":
		return roots.PiSessionDir, true
	default:
		return "", false
	}
}

// CaptureUsage correlates an Orpheus execution with harness-specific usage logs.
func CaptureUsage(opts UsageCaptureOptions) taskstate.RecordRunUsageOptions {
	ctx := opts.Context
	if ctx == nil {
		ctx = context.Background()
	}
	span := logging.Start(ctx, opts.Logger, "agent usage capture",
		usageCaptureStartAttrs(opts)...,
	)

	var result taskstate.RecordRunUsageOptions
	if opts.Launch != nil && opts.Launch.Mode == taskstate.AgentLaunchResumed {
		result = captureResumedUsage(opts)
		result = withCodexUsageCost(opts.Harness, opts.StartedAt, result)
		span.Finish(ctx, usageCaptureDiagnosticStatus(result), usageCaptureDiagnosticAttrs(result)...)
		return result
	}
	switch strings.TrimSpace(opts.Harness) {
	case codexHarness:
		result = CaptureCodexUsage(CodexUsageCaptureOptions{
			ExecutionDir:  opts.ExecutionDir,
			ExecutionDirs: opts.ExecutionDirs,
			StartedAt:     opts.StartedAt,
			Env:           opts.Env,
		})
	case piHarness:
		result = CapturePiUsage(PiUsageCaptureOptions{
			ExecutionDir:  opts.ExecutionDir,
			ExecutionDirs: opts.ExecutionDirs,
			SessionName:   opts.SessionName,
			StartedAt:     opts.StartedAt,
			Env:           opts.Env,
		})
	default:
		result = taskstate.RecordRunUsageOptions{
			UsageCapture: taskstate.AgentUsageCapture{
				Status: taskstate.UsageCaptureUnknown,
				Reason: "unsupported_harness:" + formatUsageCaptureHarness(opts.Harness),
			},
		}
	}

	result = withCodexUsageCost(opts.Harness, opts.StartedAt, result)
	span.Finish(ctx, usageCaptureDiagnosticStatus(result), usageCaptureDiagnosticAttrs(result)...)
	return result
}

func withCodexUsageCost(
	harness string,
	startedAt time.Time,
	result taskstate.RecordRunUsageOptions,
) taskstate.RecordRunUsageOptions {
	if strings.TrimSpace(harness) != codexHarness || result.Usage == nil || strings.TrimSpace(result.Model) == "" {
		return result
	}
	if cost, ok := EstimateCodexUsageCostAt(result.Model, *result.Usage, startedAt); ok {
		result.UsageCost = cost
	}
	return result
}

func usageCaptureStartAttrs(opts UsageCaptureOptions) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("component", "agent"),
		slog.String("operation", "usage_capture"),
		slog.String("harness", formatUsageCaptureHarness(opts.Harness)),
		slog.Int("execution_dir_count", usageExecutionDirCount(opts)),
	}
	if repoID := strings.TrimSpace(opts.RepoID); repoID != "" {
		attrs = append(attrs, slog.String("repo_id", repoID))
	}
	if taskID := strings.TrimSpace(opts.TaskID); taskID != "" {
		attrs = append(attrs, slog.String("task_id", taskID))
	}
	if opts.Attempt > 0 {
		attrs = append(attrs, slog.Int("attempt", opts.Attempt))
	}
	return attrs
}

func usageExecutionDirCount(opts UsageCaptureOptions) int {
	if len(opts.ExecutionDirs) > 0 {
		return len(opts.ExecutionDirs)
	}
	if strings.TrimSpace(opts.ExecutionDir) != "" {
		return 1
	}
	return 0
}

func usageCaptureDiagnosticStatus(result taskstate.RecordRunUsageOptions) string {
	switch result.UsageCapture.Status {
	case taskstate.UsageCaptureCaptured:
		return logging.StatusSuccess
	case taskstate.UsageCaptureAmbiguous:
		return "ambiguous"
	default:
		return logging.StatusFailure
	}
}

func usageCaptureDiagnosticAttrs(result taskstate.RecordRunUsageOptions) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("capture_status", string(result.UsageCapture.Status)),
		slog.Int("candidate_count", result.UsageCapture.CandidateCount),
		slog.Bool("matched_session", result.Session != nil),
		slog.Bool("captured_usage", result.Usage != nil),
	}
	if reason := usageCaptureDiagnosticReason(result.UsageCapture.Reason); reason != "" {
		attrs = append(attrs, slog.String("reason", reason))
	}
	return attrs
}

func usageCaptureDiagnosticReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	code, detail, ok := strings.Cut(reason, ":")
	if !ok || strings.TrimSpace(detail) == "" || !isUsageCaptureReasonCode(code) {
		return reason
	}
	if code == "unsupported_harness" {
		return reason
	}
	return code
}

func isUsageCaptureReasonCode(code string) bool {
	if code == "" {
		return false
	}
	for _, r := range code {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return true
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
