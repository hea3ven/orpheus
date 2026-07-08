package agent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

const codexUsageCorrelationSlack = 2 * time.Minute

// CodexUsageCaptureOptions describes the launched Codex-backed process.
type CodexUsageCaptureOptions struct {
	ExecutionDir string
	StartedAt    time.Time
	FinishedAt   time.Time
	Env          map[string]string
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
		return taskstate.RecordRunUsageOptions{
			UsageCapture: taskstate.AgentUsageCapture{
				Status:         taskstate.UsageCaptureAmbiguous,
				Reason:         "multiple_matching_codex_sessions",
				CandidateCount: len(candidates),
			},
		}
	}
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
	if filepath.Clean(session.cwd) != filepath.Clean(opts.ExecutionDir) {
		return false
	}
	started := opts.StartedAt.Add(-codexUsageCorrelationSlack)
	finished := opts.FinishedAt.Add(codexUsageCorrelationSlack)
	if opts.FinishedAt.IsZero() {
		finished = time.Now().UTC().Add(codexUsageCorrelationSlack)
	}
	return !session.startedAt.Before(started) && !session.startedAt.After(finished)
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
