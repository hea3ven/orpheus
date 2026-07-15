package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

const (
	codexUsageCorrelationSlack       = 2 * time.Minute
	codexUsageClosestSessionMaxDelay = 15 * time.Second
	codexUsageClosestSessionMinGap   = 30 * time.Second
	codexUsageClosestSessionRatio    = 5
)

// CodexUsageCaptureOptions describes the launched Codex-backed process.
type CodexUsageCaptureOptions struct {
	ExecutionDir  string
	ExecutionDirs []string
	StartedAt     time.Time
	Env           map[string]string
}

// CaptureCodexUsage correlates an Orpheus run with one Codex session log.
func CaptureCodexUsage(opts CodexUsageCaptureOptions) taskstate.RecordRunUsageOptions {
	root, err := codexRoot(opts.Env)
	if err != nil {
		return unknownUsage("codex_home_unavailable: "+err.Error(), 0)
	}

	candidates, err := matchingCodexSessions(filepath.Join(root, "sessions"), opts)
	if err != nil {
		return unknownUsage("read_codex_sessions_failed: "+err.Error(), 0)
	}
	switch len(candidates) {
	case 0:
		return unknownUsage("no_matching_codex_session", 0)
	case 1:
		return usageFromCodexSession(candidates[0])
	default:
		descriptors := codexUsageCaptureCandidates(candidates, opts.StartedAt)
		if candidate, ok := closestCodexSession(candidates, opts.StartedAt); ok {
			result := usageFromCodexSession(candidate)
			result.UsageCapture.Reason = "matched_closest_codex_session"
			result.UsageCapture.CandidateCount = len(candidates)
			result.Candidates = descriptors
			return result
		}
		return taskstate.RecordRunUsageOptions{
			Candidates: descriptors,
			UsageCapture: taskstate.AgentUsageCapture{
				Status:         taskstate.UsageCaptureAmbiguous,
				Reason:         "multiple_matching_codex_sessions",
				CandidateCount: len(candidates),
			},
		}
	}
}

func codexUsageCaptureCandidates(
	candidates []codexSessionCandidate,
	startedAt time.Time,
) []taskstate.UsageCaptureCandidate {
	descriptors := make([]taskstate.UsageCaptureCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		descriptors = append(descriptors, taskstate.UsageCaptureCandidate{
			SessionID:         candidate.id,
			LogPath:           candidate.path,
			CWD:               candidate.cwd,
			Model:             candidate.model,
			StartedAt:         candidate.startedAt,
			StartOffsetMillis: codexSessionStartOffset(candidate, startedAt).Milliseconds(),
		})
	}
	return descriptors
}

func closestCodexSession(
	candidates []codexSessionCandidate,
	startedAt time.Time,
) (codexSessionCandidate, bool) {
	sort.Slice(candidates, func(i int, j int) bool {
		return codexSessionStartOffset(candidates[i], startedAt) <
			codexSessionStartOffset(candidates[j], startedAt)
	})
	closestOffset := codexSessionStartOffset(candidates[0], startedAt)
	secondOffset := codexSessionStartOffset(candidates[1], startedAt)
	closeEnough := closestOffset <= codexUsageClosestSessionMaxDelay
	clearlyCloser := secondOffset >= closestOffset*codexUsageClosestSessionRatio
	largeEnoughGap := secondOffset-closestOffset >= codexUsageClosestSessionMinGap
	return candidates[0], closeEnough && clearlyCloser && largeEnoughGap
}

func codexSessionStartOffset(candidate codexSessionCandidate, startedAt time.Time) time.Duration {
	offset := candidate.startedAt.Sub(startedAt)
	if offset < 0 {
		return -offset
	}
	return offset
}

func codexRoot(env map[string]string) (string, error) {
	if env == nil {
		env = map[string]string{}
	}
	if value := strings.TrimSpace(env["CODEX_HOME"]); value != "" {
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("CODEX_HOME must be absolute, got %q", value)
		}
		return filepath.Clean(value), nil
	}
	home := strings.TrimSpace(env["HOME"])
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", err
		}
	}
	if !filepath.IsAbs(home) {
		return "", fmt.Errorf("HOME must be absolute, got %q", home)
	}
	return filepath.Join(filepath.Clean(home), ".codex"), nil
}

type codexSessionCandidate struct {
	path      string
	id        string
	cwd       string
	model     string
	startedAt time.Time
	usage     *taskstate.AgentUsage
}

func matchingCodexSessions(root string, opts CodexUsageCaptureOptions) ([]codexSessionCandidate, error) {
	var candidates []codexSessionCandidate
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		if session, parseErr := parseCodexSession(path); parseErr == nil && codexSessionMatches(session, opts) {
			candidates = append(candidates, session)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return candidates, nil
}

func codexSessionMatches(session codexSessionCandidate, opts CodexUsageCaptureOptions) bool {
	if !codexSessionCWDMatches(session.cwd, opts) {
		return false
	}
	started := opts.StartedAt.Add(-codexUsageCorrelationSlack)
	finished := opts.StartedAt.Add(codexUsageCorrelationSlack)
	return !session.startedAt.Before(started) && !session.startedAt.After(finished)
}

func codexSessionCWDMatches(cwd string, opts CodexUsageCaptureOptions) bool {
	cleanCWD := cleanCodexExecutionDir(cwd)
	if cleanCWD == "" {
		return false
	}
	for _, dir := range codexExecutionDirs(opts) {
		if cleanCWD == dir {
			return true
		}
	}
	return false
}

func codexExecutionDirs(opts CodexUsageCaptureOptions) []string {
	dirs := make([]string, 0, len(opts.ExecutionDirs)+1)
	dirs = appendCleanCodexExecutionDir(dirs, opts.ExecutionDir)
	for _, dir := range opts.ExecutionDirs {
		dirs = appendCleanCodexExecutionDir(dirs, dir)
	}
	return dirs
}

func appendCleanCodexExecutionDir(dirs []string, dir string) []string {
	cleaned := cleanCodexExecutionDir(dir)
	if cleaned == "" {
		return dirs
	}
	for _, existing := range dirs {
		if existing == cleaned {
			return dirs
		}
	}
	return append(dirs, cleaned)
}

func cleanCodexExecutionDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	return filepath.Clean(dir)
}

type codexLogRecord struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

func parseCodexSession(path string) (codexSessionCandidate, error) {
	file, err := os.Open(path)
	if err != nil {
		return codexSessionCandidate{}, err
	}
	defer func() {
		_ = file.Close()
	}()

	session := codexSessionCandidate{path: path}
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	for scanner.Scan() {
		var record codexLogRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		recordedAt, _ := time.Parse(time.RFC3339Nano, record.Timestamp)
		switch record.Type {
		case "session_meta":
			applyCodexSessionMeta(&session, record.Payload, recordedAt)
		case "event_msg":
			applyCodexTokenCount(&session, record.Payload)
		}
	}
	if err := scanner.Err(); err != nil {
		return codexSessionCandidate{}, err
	}
	if session.startedAt.IsZero() || strings.TrimSpace(session.cwd) == "" {
		return codexSessionCandidate{}, errors.New("missing session metadata")
	}
	return session, nil
}

func applyCodexSessionMeta(session *codexSessionCandidate, payload json.RawMessage, fallback time.Time) {
	var meta map[string]any
	if err := json.Unmarshal(payload, &meta); err != nil {
		return
	}
	session.id = firstString(meta, "session_id", "id")
	session.cwd = firstString(meta, "cwd")
	session.model = firstString(meta, "model", "model_slug")
	if value := firstString(meta, "timestamp"); value != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			session.startedAt = parsed.UTC()
		}
	}
	if session.startedAt.IsZero() {
		session.startedAt = fallback.UTC()
	}
}

func applyCodexTokenCount(session *codexSessionCandidate, payload json.RawMessage) {
	var event struct {
		Type string `json:"type"`
		Info struct {
			TotalTokenUsage taskstate.AgentUsage `json:"total_token_usage"`
		} `json:"info"`
	}
	if err := json.Unmarshal(payload, &event); err != nil || event.Type != "token_count" {
		return
	}
	usage := event.Info.TotalTokenUsage
	if usage.TotalTokens == 0 &&
		(usage.InputTokens != 0 ||
			usage.CachedInputTokens != 0 ||
			usage.OutputTokens != 0 ||
			usage.ReasoningOutputTokens != 0) {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	session.usage = &usage
}

func usageFromCodexSession(session codexSessionCandidate) taskstate.RecordRunUsageOptions {
	result := taskstate.RecordRunUsageOptions{
		Session: &taskstate.AgentSession{
			ID:      session.id,
			LogPath: session.path,
		},
		Model: session.model,
	}
	if session.usage == nil {
		result.UsageCapture = taskstate.AgentUsageCapture{
			Status:         taskstate.UsageCaptureUnknown,
			Reason:         "matching_codex_session_has_no_token_count",
			CandidateCount: 1,
		}
		return result
	}
	result.Usage = session.usage
	result.UsageCapture = taskstate.AgentUsageCapture{
		Status:         taskstate.UsageCaptureCaptured,
		Reason:         "matched_codex_session",
		CandidateCount: 1,
	}
	return result
}

// CodexUsageCaptureEnvironment returns environment values needed to find Codex session logs.
func CodexUsageCaptureEnvironment() map[string]string {
	env := map[string]string{}
	for _, key := range []string{"CODEX_HOME", "HOME"} {
		if value, ok := os.LookupEnv(key); ok {
			env[key] = value
		}
	}
	return env
}

func unknownUsage(reason string, candidateCount int) taskstate.RecordRunUsageOptions {
	return taskstate.RecordRunUsageOptions{
		UsageCapture: taskstate.AgentUsageCapture{
			Status:         taskstate.UsageCaptureUnknown,
			Reason:         reason,
			CandidateCount: candidateCount,
		},
	}
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := values[key]
		if !ok {
			continue
		}
		if text, ok := value.(string); ok {
			return strings.TrimSpace(text)
		}
	}
	return ""
}
