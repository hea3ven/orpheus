package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

const (
	piUsageCorrelationSlack       = 2 * time.Minute
	piUsageClosestSessionMaxDelay = 15 * time.Second
	piUsageClosestSessionMinGap   = 30 * time.Second
	piUsageClosestSessionRatio    = 5
)

// PiUsageCaptureOptions describes the launched Pi-backed process.
type PiUsageCaptureOptions struct {
	ExecutionDir  string
	ExecutionDirs []string
	SessionName   string
	StartedAt     time.Time
	Env           map[string]string
}

// CapturePiUsage correlates an Orpheus run with one Pi session log.
func CapturePiUsage(opts PiUsageCaptureOptions) taskstate.RecordRunUsageOptions {
	root, err := piSessionRoot(opts.Env)
	if err != nil {
		return unknownUsage("pi_session_dir_unavailable: "+err.Error(), 0)
	}

	candidates, err := matchingPiSessions(root, opts)
	if err != nil {
		return unknownUsage("read_pi_sessions_failed: "+err.Error(), 0)
	}
	switch len(candidates) {
	case 0:
		return unknownUsage("no_matching_pi_session", 0)
	case 1:
		return usageFromPiSession(candidates[0])
	default:
		descriptors := piUsageCaptureCandidates(candidates, opts.StartedAt)
		if candidate, ok := closestPiSession(candidates, opts.StartedAt); ok {
			result := usageFromPiSession(candidate)
			if result.Usage != nil {
				result.UsageCapture.Reason = "matched_closest_pi_session"
			}
			result.UsageCapture.CandidateCount = len(candidates)
			result.Candidates = descriptors
			return result
		}
		return taskstate.RecordRunUsageOptions{
			Candidates: descriptors,
			UsageCapture: taskstate.AgentUsageCapture{
				Status:         taskstate.UsageCaptureAmbiguous,
				Reason:         "multiple_matching_pi_sessions",
				CandidateCount: len(candidates),
			},
		}
	}
}

func piUsageCaptureCandidates(
	candidates []piSessionCandidate,
	startedAt time.Time,
) []taskstate.UsageCaptureCandidate {
	descriptors := make([]taskstate.UsageCaptureCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		descriptors = append(descriptors, taskstate.UsageCaptureCandidate{
			SessionID:         candidate.id,
			SessionName:       candidate.name,
			LogPath:           candidate.path,
			CWD:               candidate.cwd,
			Model:             candidate.model,
			StartedAt:         candidate.startedAt,
			StartOffsetMillis: piSessionStartOffset(candidate, startedAt).Milliseconds(),
		})
	}
	return descriptors
}

func closestPiSession(candidates []piSessionCandidate, startedAt time.Time) (piSessionCandidate, bool) {
	sort.Slice(candidates, func(i int, j int) bool {
		return piSessionStartOffset(candidates[i], startedAt) <
			piSessionStartOffset(candidates[j], startedAt)
	})
	closestOffset := piSessionStartOffset(candidates[0], startedAt)
	secondOffset := piSessionStartOffset(candidates[1], startedAt)
	closeEnough := closestOffset <= piUsageClosestSessionMaxDelay
	clearlyCloser := secondOffset >= closestOffset*piUsageClosestSessionRatio
	largeEnoughGap := secondOffset-closestOffset >= piUsageClosestSessionMinGap
	return candidates[0], closeEnough && clearlyCloser && largeEnoughGap
}

func piSessionStartOffset(candidate piSessionCandidate, startedAt time.Time) time.Duration {
	offset := candidate.startedAt.Sub(startedAt)
	if offset < 0 {
		return -offset
	}
	return offset
}

func piSessionRoot(env map[string]string) (string, error) {
	if env == nil {
		env = map[string]string{}
	}
	if value := strings.TrimSpace(env["PI_CODING_AGENT_SESSION_DIR"]); value != "" {
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("PI_CODING_AGENT_SESSION_DIR must be absolute, got %q", value)
		}
		return filepath.Clean(value), nil
	}
	if value := strings.TrimSpace(env["PI_CODING_AGENT_DIR"]); value != "" {
		if !filepath.IsAbs(value) {
			return "", fmt.Errorf("PI_CODING_AGENT_DIR must be absolute, got %q", value)
		}
		return filepath.Join(filepath.Clean(value), "sessions"), nil
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
	return filepath.Join(filepath.Clean(home), ".pi", "agent", "sessions"), nil
}

type piSessionCandidate struct {
	path      string
	id        string
	name      string
	cwd       string
	model     string
	startedAt time.Time
	usage     *taskstate.AgentUsage
	cost      *taskstate.AgentUsageCost
}

func matchingPiSessions(root string, opts PiUsageCaptureOptions) ([]piSessionCandidate, error) {
	var candidates []piSessionCandidate
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		session, parseErr := parsePiSession(path)
		if parseErr == nil && piSessionMatches(session, opts) {
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

func piSessionMatches(session piSessionCandidate, opts PiUsageCaptureOptions) bool {
	if !piSessionCWDMatches(session.cwd, opts) {
		return false
	}
	expectedName := strings.TrimSpace(opts.SessionName)
	if expectedName != "" && session.name != "" && session.name != expectedName {
		return false
	}
	started := opts.StartedAt.Add(-piUsageCorrelationSlack)
	finished := opts.StartedAt.Add(piUsageCorrelationSlack)
	return !session.startedAt.Before(started) && !session.startedAt.After(finished)
}

func piSessionCWDMatches(cwd string, opts PiUsageCaptureOptions) bool {
	cleanCWD := cleanPiExecutionDir(cwd)
	if cleanCWD == "" {
		return false
	}
	for _, dir := range piExecutionDirs(opts) {
		if cleanCWD == dir {
			return true
		}
	}
	return false
}

func piExecutionDirs(opts PiUsageCaptureOptions) []string {
	dirs := make([]string, 0, len(opts.ExecutionDirs)+1)
	dirs = appendCleanPiExecutionDir(dirs, opts.ExecutionDir)
	for _, dir := range opts.ExecutionDirs {
		dirs = appendCleanPiExecutionDir(dirs, dir)
	}
	return dirs
}

func appendCleanPiExecutionDir(dirs []string, dir string) []string {
	cleaned := cleanPiExecutionDir(dir)
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

func cleanPiExecutionDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	return filepath.Clean(dir)
}

type piLogRecord struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Timestamp string          `json:"timestamp"`
	CWD       string          `json:"cwd"`
	Name      string          `json:"name"`
	Title     string          `json:"title"`
	Provider  string          `json:"provider"`
	ModelID   string          `json:"modelId"`
	Message   piMessageRecord `json:"message"`
	Usage     piUsageRecord   `json:"usage"`
}

type piMessageRecord struct {
	Role string `json:"role"`
}

type piUsageRecord struct {
	Input       json.Number       `json:"input"`
	Output      json.Number       `json:"output"`
	CacheRead   json.Number       `json:"cacheRead"`
	CacheWrite  json.Number       `json:"cacheWrite"`
	Reasoning   json.Number       `json:"reasoning"`
	TotalTokens json.Number       `json:"totalTokens"`
	Cost        piUsageCostRecord `json:"cost"`
}

type piUsageCostRecord struct {
	Total json.Number `json:"total"`
}

func parsePiSession(path string) (piSessionCandidate, error) {
	file, err := os.Open(path)
	if err != nil {
		return piSessionCandidate{}, err
	}
	defer func() {
		_ = file.Close()
	}()

	session := piSessionCandidate{path: path}
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 0, 64*1024)
	scanner.Buffer(buffer, 4*1024*1024)
	for scanner.Scan() {
		var record piLogRecord
		decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
		decoder.UseNumber()
		if err := decoder.Decode(&record); err != nil {
			continue
		}
		recordedAt, _ := time.Parse(time.RFC3339Nano, record.Timestamp)
		switch record.Type {
		case "session":
			applyPiSessionMeta(&session, record, recordedAt)
		case "model_change":
			applyPiModelChange(&session, record)
		case "message":
			applyPiAssistantUsage(&session, record)
		}
	}
	if err := scanner.Err(); err != nil {
		return piSessionCandidate{}, err
	}
	if session.startedAt.IsZero() {
		session.startedAt = piTimestampFromFilename(path)
	}
	if session.startedAt.IsZero() || strings.TrimSpace(session.cwd) == "" {
		return piSessionCandidate{}, errors.New("missing session metadata")
	}
	return session, nil
}

func applyPiSessionMeta(session *piSessionCandidate, record piLogRecord, fallback time.Time) {
	session.id = strings.TrimSpace(record.ID)
	session.cwd = strings.TrimSpace(record.CWD)
	session.name = firstNonEmptyString(record.Name, record.Title)
	if !fallback.IsZero() {
		session.startedAt = fallback.UTC()
	}
}

func applyPiModelChange(session *piSessionCandidate, record piLogRecord) {
	provider := strings.TrimSpace(record.Provider)
	model := strings.TrimSpace(record.ModelID)
	switch {
	case provider != "" && model != "":
		session.model = provider + "/" + model
	case model != "":
		session.model = model
	}
}

func applyPiAssistantUsage(session *piSessionCandidate, record piLogRecord) {
	if record.Message.Role != "assistant" || piUsageRecordIsZero(record.Usage) {
		return
	}
	usage := taskstate.AgentUsage{}
	if session.usage != nil {
		usage = *session.usage
	}
	usage.InputTokens += piJSONInt(record.Usage.Input)
	usage.CachedInputTokens += piJSONInt(record.Usage.CacheRead) + piJSONInt(record.Usage.CacheWrite)
	usage.OutputTokens += piJSONInt(record.Usage.Output)
	usage.ReasoningOutputTokens += piJSONInt(record.Usage.Reasoning)
	usage.TotalTokens += piJSONInt(record.Usage.TotalTokens)
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	session.usage = &usage

	costMicroUSD := piCostMicroUSD(record.Usage.Cost.Total)
	if costMicroUSD <= 0 {
		return
	}
	cost := PiReportedUsageCost(costMicroUSD)
	if session.cost != nil {
		cost.AmountMicroUSD += session.cost.AmountMicroUSD
	}
	session.cost = &cost
}

func piUsageRecordIsZero(usage piUsageRecord) bool {
	return usage.Input == "" &&
		usage.Output == "" &&
		usage.CacheRead == "" &&
		usage.CacheWrite == "" &&
		usage.Reasoning == "" &&
		usage.TotalTokens == "" &&
		usage.Cost.Total == ""
}

func piJSONInt(value json.Number) int {
	text := strings.TrimSpace(value.String())
	if text == "" {
		return 0
	}
	integer, err := strconv.Atoi(text)
	if err == nil && integer > 0 {
		return integer
	}
	return 0
}

func piCostMicroUSD(value json.Number) int64 {
	text := strings.TrimSpace(value.String())
	if text == "" {
		return 0
	}
	return decimalUSDMicroUSD(text)
}

func decimalUSDMicroUSD(text string) int64 {
	text = strings.TrimSpace(text)
	if text == "" || strings.HasPrefix(text, "-") {
		return 0
	}
	whole, fraction, _ := strings.Cut(text, ".")
	wholeValue, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		amount, parseErr := strconv.ParseFloat(text, 64)
		if parseErr != nil || amount <= 0 {
			return 0
		}
		return int64(math.Round(amount * float64(microUSDPerUSD)))
	}
	fraction = strings.TrimRight(fraction, " \t\r\n")
	if len(fraction) > 6 {
		round := fraction[6] >= '5'
		fraction = fraction[:6]
		for len(fraction) < 6 {
			fraction += "0"
		}
		fractionValue, err := strconv.ParseInt(fraction, 10, 64)
		if err != nil {
			return 0
		}
		if round {
			fractionValue++
		}
		return wholeValue*microUSDPerUSD + fractionValue
	}
	for len(fraction) < 6 {
		fraction += "0"
	}
	fractionValue, err := strconv.ParseInt(fraction, 10, 64)
	if err != nil {
		return 0
	}
	return wholeValue*microUSDPerUSD + fractionValue
}

func piTimestampFromFilename(path string) time.Time {
	name := filepath.Base(path)
	prefix, _, ok := strings.Cut(name, "_")
	if !ok {
		return time.Time{}
	}
	if len(prefix) != len("2006-01-02T15-04-05-000Z") {
		return time.Time{}
	}
	normalized := prefix[:13] + ":" + prefix[14:16] + ":" + prefix[17:19] + "." + prefix[20:]
	parsed, err := time.Parse("2006-01-02T15:04:05.000Z", normalized)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func usageFromPiSession(session piSessionCandidate) taskstate.RecordRunUsageOptions {
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
			Reason:         "matching_pi_session_has_no_assistant_usage",
			CandidateCount: 1,
		}
		return result
	}
	result.Usage = session.usage
	result.UsageCost = session.cost
	result.UsageCapture = taskstate.AgentUsageCapture{
		Status:         taskstate.UsageCaptureCaptured,
		Reason:         "matched_pi_session",
		CandidateCount: 1,
	}
	return result
}
