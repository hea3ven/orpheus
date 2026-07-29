package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

const resumeSessionsEnvironment = "ORPHEUS_RESUME_SESSIONS"

// FollowUpResumeOptions describes a resolved review-follow-up command and the
// completed implementation history that may supply a compatible session.
type FollowUpResumeOptions struct {
	Command      CommandSnapshot
	State        taskstate.TaskState
	ExecutionDir string
	Env          map[string]string
	Enabled      bool
}

// ResumeSessionsEnabled reports whether follow-up session resumption is
// strictly opted in for this process.
func ResumeSessionsEnabled() bool {
	return os.Getenv(resumeSessionsEnvironment) == "1"
}

// PrepareFollowUpResume returns either an exact-session resume command or the
// original fresh command with persisted provenance explaining the fallback.
func PrepareFollowUpResume(opts FollowUpResumeOptions) (CommandSnapshot, *taskstate.AgentLaunch) {
	fresh := &taskstate.AgentLaunch{Mode: taskstate.AgentLaunchFresh}
	if !opts.Enabled {
		return opts.Command, fresh
	}

	harness := strings.ToLower(strings.TrimSpace(opts.Command.Harness))
	if harness != piHarness && harness != codexHarness {
		fresh.FallbackReason = "selected profile is a raw command; only structured Pi and Codex profiles support session resumption"
		return opts.Command, fresh
	}

	latestFailure := ""
	for index := len(opts.State.Runs) - 1; index >= 0; index-- {
		run := opts.State.Runs[index]
		if run.Status != taskstate.RunStatusSucceeded || run.Completion == nil ||
			run.Execution.Purpose != taskstate.AgentExecutionPurposeImplementation {
			continue
		}
		if strings.TrimSpace(run.Execution.Profile) != strings.TrimSpace(opts.Command.AgentName) ||
			strings.ToLower(strings.TrimSpace(run.Execution.Harness)) != harness {
			continue
		}
		baseline, err := inspectResumeSession(harness, run.Execution.Session, opts.ExecutionDir, opts.Env)
		if err != nil {
			if latestFailure == "" {
				latestFailure = fmt.Sprintf("run attempt %d cannot be resumed: %v", run.Attempt, err)
			}
			continue
		}
		resumed, err := resumedCommand(opts.Command, baseline.session)
		if err != nil {
			fresh.FallbackReason = fmt.Sprintf("run attempt %d cannot be resumed safely: %v", run.Attempt, err)
			return opts.Command, fresh
		}
		return resumed, &taskstate.AgentLaunch{
			Mode:             taskstate.AgentLaunchResumed,
			SourceRunAttempt: run.Attempt,
			SourceSession:    cloneAgentSession(&baseline.session),
			UsageBaseline:    cloneAgentUsage(baseline.usage),
			CostBaseline:     cloneInt64(baseline.costMicroUSD),
		}
	}

	switch {
	case latestFailure != "":
		fresh.FallbackReason = latestFailure
	default:
		fresh.FallbackReason = fmt.Sprintf(
			"no successful completed implementation session matches profile %q and harness %q",
			strings.TrimSpace(opts.Command.AgentName),
			harness,
		)
	}
	return opts.Command, fresh
}

type resumeSessionSnapshot struct {
	session      taskstate.AgentSession
	usage        *taskstate.AgentUsage
	costMicroUSD *int64
	model        string
}

func inspectResumeSession(
	harness string,
	recorded *taskstate.AgentSession,
	executionDir string,
	env map[string]string,
) (resumeSessionSnapshot, error) {
	if recorded == nil {
		return resumeSessionSnapshot{}, errors.New("session telemetry is missing or ambiguous")
	}
	session := taskstate.AgentSession{
		ID:      strings.TrimSpace(recorded.ID),
		LogPath: strings.TrimSpace(recorded.LogPath),
	}
	if session.ID == "" || session.LogPath == "" {
		return resumeSessionSnapshot{}, errors.New("session telemetry must include both id and log path")
	}
	if !filepath.IsAbs(session.LogPath) {
		return resumeSessionSnapshot{}, fmt.Errorf("session log path %q is not absolute", session.LogPath)
	}
	info, err := os.Stat(session.LogPath)
	if err != nil {
		return resumeSessionSnapshot{}, fmt.Errorf("session log %q is unavailable: %w", session.LogPath, err)
	}
	if !info.Mode().IsRegular() {
		return resumeSessionSnapshot{}, fmt.Errorf("session log %q is not a regular file", session.LogPath)
	}

	var snapshot resumeSessionSnapshot
	snapshot.session = session
	switch harness {
	case piHarness:
		parsed, parseErr := parsePiSession(session.LogPath)
		if parseErr != nil {
			return resumeSessionSnapshot{}, fmt.Errorf("parse Pi session log: %w", parseErr)
		}
		if err := validateResumeSessionIdentity(session, parsed.id, parsed.cwd, executionDir); err != nil {
			return resumeSessionSnapshot{}, err
		}
		snapshot.usage = cloneAgentUsage(parsed.usage)
		if parsed.cost != nil {
			cost := parsed.cost.AmountMicroUSD
			snapshot.costMicroUSD = &cost
		}
		snapshot.model = parsed.model
	case codexHarness:
		return inspectCodexResumeSession(session, executionDir, env)
	default:
		return resumeSessionSnapshot{}, fmt.Errorf("unsupported structured harness %q", harness)
	}
	return snapshot, nil
}

func inspectCodexResumeSession(
	session taskstate.AgentSession,
	executionDir string,
	env map[string]string,
) (resumeSessionSnapshot, error) {
	if err := validateCodexResumeLogRoot(session.LogPath, env); err != nil {
		return resumeSessionSnapshot{}, err
	}
	parsed, err := parseCodexSession(session.LogPath)
	if err != nil {
		return resumeSessionSnapshot{}, fmt.Errorf("parse Codex session log: %w", err)
	}
	if err := validateResumeSessionIdentity(session, parsed.id, parsed.cwd, executionDir); err != nil {
		return resumeSessionSnapshot{}, err
	}
	if err := validateUniqueCodexResumeSessionID(session, env); err != nil {
		return resumeSessionSnapshot{}, err
	}
	return resumeSessionSnapshot{
		session: session,
		usage:   cloneAgentUsage(parsed.usage),
		model:   parsed.model,
	}, nil
}

func validateCodexResumeLogRoot(logPath string, env map[string]string) error {
	_, _, err := canonicalCodexResumePaths(logPath, env)
	return err
}

func canonicalCodexResumePaths(logPath string, env map[string]string) (string, string, error) {
	root, err := codexRoot(env)
	if err != nil {
		return "", "", fmt.Errorf("resolve active Codex home: %w", err)
	}
	sessionsRoot := filepath.Join(root, "sessions")
	canonicalRoot, err := filepath.EvalSymlinks(sessionsRoot)
	if err != nil {
		return "", "", fmt.Errorf("active Codex sessions root %q is unavailable: %w", sessionsRoot, err)
	}
	rootInfo, err := os.Stat(canonicalRoot)
	if err != nil {
		return "", "", fmt.Errorf("inspect active Codex sessions root %q: %w", sessionsRoot, err)
	}
	if !rootInfo.IsDir() {
		return "", "", fmt.Errorf("active Codex sessions root %q is not a directory", sessionsRoot)
	}
	canonicalLog, err := filepath.EvalSymlinks(logPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve canonical Codex session log %q: %w", logPath, err)
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalLog)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", "", fmt.Errorf(
			"session log %q is not discoverable under active Codex sessions root %q; check CODEX_HOME",
			logPath,
			sessionsRoot,
		)
	}
	return canonicalRoot, canonicalLog, nil
}

func validateUniqueCodexResumeSessionID(recorded taskstate.AgentSession, env map[string]string) error {
	canonicalRoot, canonicalRecorded, err := canonicalCodexResumePaths(recorded.LogPath, env)
	if err != nil {
		return err
	}

	matchingLogs := make(map[string]string)
	err = filepath.WalkDir(canonicalRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		candidateID := ""
		if candidate, parseErr := parseCodexSession(path); parseErr == nil {
			candidateID = strings.TrimSpace(candidate.id)
		}
		if candidateID != recorded.ID {
			return nil
		}
		canonicalCandidate, canonicalErr := filepath.EvalSymlinks(path)
		if canonicalErr != nil {
			return canonicalErr
		}
		matchingLogs[canonicalCandidate] = path
		return nil
	})
	if err != nil {
		return fmt.Errorf("inspect Codex session id %q under %q: %w", recorded.ID, canonicalRoot, err)
	}
	if _, ok := matchingLogs[canonicalRecorded]; !ok {
		return fmt.Errorf(
			"codex session id %q does not resolve to recorded log %q under active sessions root %q",
			recorded.ID,
			recorded.LogPath,
			canonicalRoot,
		)
	}
	if len(matchingLogs) == 1 {
		return nil
	}
	for canonicalPath, path := range matchingLogs {
		if canonicalPath != canonicalRecorded {
			return fmt.Errorf(
				"codex session id is ambiguous: id %q resolves to recorded log %q and conflicting log %q under active sessions root %q",
				recorded.ID,
				recorded.LogPath,
				path,
				canonicalRoot,
			)
		}
	}
	return fmt.Errorf("codex session id %q is ambiguous under active sessions root %q", recorded.ID, canonicalRoot)
}

func validateResumeSessionIdentity(
	recorded taskstate.AgentSession,
	parsedID string,
	parsedCWD string,
	executionDir string,
) error {
	if strings.TrimSpace(parsedID) == "" || strings.TrimSpace(parsedID) != recorded.ID {
		return fmt.Errorf("session id mismatch: recorded %q, log contains %q", recorded.ID, strings.TrimSpace(parsedID))
	}
	expected := cleanPiExecutionDir(executionDir)
	actual := cleanPiExecutionDir(parsedCWD)
	if expected == "" || actual == "" || actual != expected {
		return fmt.Errorf("session working directory mismatch: expected %q, log contains %q", expected, actual)
	}
	return nil
}

func resumedCommand(command CommandSnapshot, session taskstate.AgentSession) (CommandSnapshot, error) {
	result := command
	var args []string
	var err error
	switch strings.ToLower(strings.TrimSpace(command.Harness)) {
	case piHarness:
		args, err = resumedPiArgs(command.Args, session.LogPath)
	case codexHarness:
		args, err = resumedCodexArgs(command.Args, session.ID)
	default:
		err = fmt.Errorf("unsupported structured harness %q", command.Harness)
	}
	if err != nil {
		return CommandSnapshot{}, err
	}
	result.Args = args
	return result, nil
}

func resumedPiArgs(fresh []string, sessionPath string) ([]string, error) {
	if len(fresh) == 0 {
		return nil, errors.New("Pi command has no bootstrap prompt") //nolint:staticcheck // Pi is a product name.
	}
	args := make([]string, 0, len(fresh)+2)
	index := 0
	if fresh[0] == "--print" {
		args = append(args, fresh[0])
		index++
	}
	args = append(args, "--session", sessionPath)
	for index < len(fresh) {
		if fresh[index] == "--name" {
			if index+1 >= len(fresh) {
				return nil, errors.New("Pi command has an incomplete --name option") //nolint:staticcheck // Pi is a product name.
			}
			index += 2
			continue
		}
		args = append(args, fresh[index])
		index++
	}
	return args, nil
}

func resumedCodexArgs(fresh []string, sessionID string) ([]string, error) {
	if len(fresh) == 0 {
		return nil, errors.New("Codex command has no bootstrap prompt") //nolint:staticcheck // Codex is a product name.
	}
	promptIndex := len(fresh) - 1
	nonInteractive := fresh[0] == "exec"
	optionStart := 0
	args := make([]string, 0, len(fresh)+2)
	if nonInteractive {
		args = append(args, "exec", "resume")
		optionStart = 1
	} else {
		args = append(args, "resume")
	}
	args = append(args, fresh[optionStart:promptIndex]...)
	args = append(args, sessionID, fresh[promptIndex])
	return args, nil
}

func captureResumedUsage(opts UsageCaptureOptions) taskstate.RecordRunUsageOptions {
	launch := opts.Launch
	if launch == nil || launch.Mode != taskstate.AgentLaunchResumed || launch.SourceSession == nil {
		return unknownUsage("invalid_resumed_launch_provenance", 0)
	}
	if launch.UsageBaseline == nil {
		result := unknownUsage("resumed_session_usage_baseline_unavailable", 1)
		result.Session = cloneAgentSession(launch.SourceSession)
		return result
	}
	snapshot, err := inspectResumedUsageSession(opts, launch.SourceSession)
	if err != nil {
		result := unknownUsage("read_resumed_session_failed: "+err.Error(), 0)
		result.Session = cloneAgentSession(launch.SourceSession)
		return result
	}
	result := taskstate.RecordRunUsageOptions{
		Session: cloneAgentSession(&snapshot.session),
		Model:   snapshot.model,
	}
	afterUsage, afterCost, boundaryErr := resumedUsageEndSnapshot(launch.SourceSession, snapshot, opts.ResumeBoundary)
	if boundaryErr != nil {
		return unknownResumedCapture(result, "resumed_session_upper_boundary_invalid: "+boundaryErr.Error())
	}
	if afterUsage == nil {
		reason := "resumed_session_usage_unavailable"
		if opts.ResumeBoundary != nil {
			reason = "resumed_session_usage_upper_boundary_unavailable"
		}
		return unknownResumedCapture(result, reason)
	}
	delta, ok := subtractAgentUsage(*afterUsage, *launch.UsageBaseline)
	if !ok {
		return unknownResumedCapture(result, "resumed_session_usage_regressed")
	}
	if resumeUsageIsZero(delta) {
		return unknownResumedCapture(result, "resumed_session_has_no_incremental_usage")
	}
	result.Usage = &delta
	result.UsageCapture = taskstate.AgentUsageCapture{
		Status:         taskstate.UsageCaptureCaptured,
		Reason:         "matched_resumed_" + strings.ToLower(strings.TrimSpace(opts.Harness)) + "_session",
		CandidateCount: 1,
	}
	if launch.CostBaseline != nil && afterCost != nil && *afterCost >= *launch.CostBaseline {
		if costDelta := *afterCost - *launch.CostBaseline; costDelta > 0 {
			cost := PiReportedUsageCost(costDelta)
			result.UsageCost = &cost
		}
	}
	return result
}

func unknownResumedCapture(
	result taskstate.RecordRunUsageOptions,
	reason string,
) taskstate.RecordRunUsageOptions {
	result.UsageCapture = taskstate.AgentUsageCapture{
		Status:         taskstate.UsageCaptureUnknown,
		Reason:         reason,
		CandidateCount: 1,
	}
	return result
}

func resumedUsageEndSnapshot(
	sourceSession *taskstate.AgentSession,
	current resumeSessionSnapshot,
	boundary *ResumedUsageBoundary,
) (*taskstate.AgentUsage, *int64, error) {
	if boundary == nil {
		return current.usage, current.costMicroUSD, nil
	}
	matching, err := SameCanonicalSession(sourceSession, boundary.Session)
	if err != nil {
		return nil, nil, err
	}
	if !matching {
		return nil, nil, errors.New("boundary does not identify the resumed source session")
	}
	return boundary.Usage, boundary.CostMicroUSD, nil
}

// SameCanonicalSession reports whether two persisted session records identify
// the same session ID and filesystem object.
func SameCanonicalSession(left, right *taskstate.AgentSession) (bool, error) {
	if left == nil || right == nil {
		return false, nil
	}
	leftID := strings.TrimSpace(left.ID)
	rightID := strings.TrimSpace(right.ID)
	if leftID == "" || rightID == "" {
		return false, errors.New("session id is missing")
	}
	if leftID != rightID {
		return false, nil
	}
	leftPath := strings.TrimSpace(left.LogPath)
	rightPath := strings.TrimSpace(right.LogPath)
	if leftPath == "" || rightPath == "" {
		return false, errors.New("session log path is missing")
	}
	canonicalLeft, err := filepath.EvalSymlinks(leftPath)
	if err != nil {
		return false, fmt.Errorf("resolve source session log %q: %w", leftPath, err)
	}
	canonicalRight, err := filepath.EvalSymlinks(rightPath)
	if err != nil {
		return false, fmt.Errorf("resolve boundary session log %q: %w", rightPath, err)
	}
	return filepath.Clean(canonicalLeft) == filepath.Clean(canonicalRight), nil
}

func inspectResumedUsageSession(
	opts UsageCaptureOptions,
	session *taskstate.AgentSession,
) (resumeSessionSnapshot, error) {
	dirs := make([]string, 0, len(opts.ExecutionDirs)+1)
	if strings.TrimSpace(opts.ExecutionDir) != "" {
		dirs = append(dirs, opts.ExecutionDir)
	}
	dirs = append(dirs, opts.ExecutionDirs...)
	if len(dirs) == 0 {
		return resumeSessionSnapshot{}, errors.New("execution directory is missing")
	}
	var latestErr error
	for _, dir := range dirs {
		snapshot, err := inspectResumeSession(strings.ToLower(strings.TrimSpace(opts.Harness)), session, dir, opts.Env)
		if err == nil {
			return snapshot, nil
		}
		latestErr = err
	}
	return resumeSessionSnapshot{}, latestErr
}

func resumeUsageIsZero(usage taskstate.AgentUsage) bool {
	return usage.InputTokens == 0 && usage.CachedInputTokens == 0 &&
		usage.OutputTokens == 0 && usage.ReasoningOutputTokens == 0 && usage.TotalTokens == 0
}

func subtractAgentUsage(after taskstate.AgentUsage, before taskstate.AgentUsage) (taskstate.AgentUsage, bool) {
	if after.InputTokens < before.InputTokens ||
		after.CachedInputTokens < before.CachedInputTokens ||
		after.OutputTokens < before.OutputTokens ||
		after.ReasoningOutputTokens < before.ReasoningOutputTokens ||
		after.TotalTokens < before.TotalTokens {
		return taskstate.AgentUsage{}, false
	}
	return taskstate.AgentUsage{
		InputTokens:           after.InputTokens - before.InputTokens,
		CachedInputTokens:     after.CachedInputTokens - before.CachedInputTokens,
		OutputTokens:          after.OutputTokens - before.OutputTokens,
		ReasoningOutputTokens: after.ReasoningOutputTokens - before.ReasoningOutputTokens,
		TotalTokens:           after.TotalTokens - before.TotalTokens,
	}, true
}

func cloneAgentSession(session *taskstate.AgentSession) *taskstate.AgentSession {
	if session == nil {
		return nil
	}
	cloned := *session
	return &cloned
}

func cloneAgentUsage(usage *taskstate.AgentUsage) *taskstate.AgentUsage {
	if usage == nil {
		return nil
	}
	cloned := *usage
	return &cloned
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
