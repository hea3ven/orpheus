// Package beads provides a narrow adapter for inspecting and initializing Beads state.
package beads

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/logging"
)

var (
	// ErrNoLocal indicates the inspected directory does not have repo-local Beads state.
	ErrNoLocal = errors.New("no local beads state")
)

// Result is the output of one Beads CLI invocation.
type Result struct {
	Stdout string
	Stderr string
}

// Runner executes the Beads CLI in a specified working directory.
type Runner interface {
	Run(dir string, args ...string) (Result, error)
}

type diagnosticStatusClassifier func(operation string, result Result, err error) string

// CommandRunner executes the real bd binary.
type CommandRunner struct {
	// Binary is the executable name or path. Empty uses "bd".
	Binary string
	// Logger receives safe command diagnostics. Raw argv, output, and environment are not logged.
	Logger *slog.Logger
	// DiagnosticAttrs are safe semantic fields included on every command diagnostic.
	DiagnosticAttrs  []slog.Attr
	diagnosticStatus diagnosticStatusClassifier
}

// NewInspectLocalRunner returns a bd runner with diagnostics classified for local discovery.
func NewInspectLocalRunner(logger *slog.Logger, attrs ...slog.Attr) Runner {
	return CommandRunner{
		Logger:           logger,
		DiagnosticAttrs:  append([]slog.Attr{}, attrs...),
		diagnosticStatus: inspectLocalDiagnosticStatus,
	}
}

// WithDiagnosticAttrs returns a runner copy that includes additional safe diagnostic fields.
func (r CommandRunner) WithDiagnosticAttrs(attrs ...slog.Attr) Runner {
	copied := r
	copied.DiagnosticAttrs = append(append([]slog.Attr{}, r.DiagnosticAttrs...), attrs...)
	return copied
}

// Run executes bd in dir and captures stdout and stderr separately.
func (r CommandRunner) Run(dir string, args ...string) (Result, error) {
	binary := r.Binary
	if strings.TrimSpace(binary) == "" {
		binary = "bd"
	}

	operation := bdSemanticOperation(args)
	attrs := []slog.Attr{
		slog.String("component", "beads"),
		slog.String("operation", operation),
		slog.String("cwd", dir),
	}
	attrs = append(attrs, r.DiagnosticAttrs...)
	span := logging.Start(context.Background(), r.Logger, "beads command", attrs...)
	command := exec.Command(binary, args...)
	command.Dir = dir
	command.Env = sanitizedEnvironment()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	result := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	finishAttrs := subprocessExitAttrs(command, err)
	span.Finish(context.Background(), r.finishStatus(operation, result, err), finishAttrs...)
	return result, err
}

func (r CommandRunner) finishStatus(operation string, result Result, err error) string {
	if r.diagnosticStatus != nil {
		if status := r.diagnosticStatus(operation, result, err); status != "" {
			return status
		}
	}
	if err != nil {
		return logging.StatusFailure
	}
	return logging.StatusSuccess
}

func inspectLocalDiagnosticStatus(operation string, result Result, _ error) string {
	if operation != "context" {
		return ""
	}
	response, parseErr := parseContextResponse(result.Stdout)
	if parseErr == nil && response.Error == "no_beads_directory" {
		return logging.StatusExpectedAbsence
	}
	return ""
}

func subprocessExitAttrs(command *exec.Cmd, err error) []slog.Attr {
	if command != nil && command.ProcessState != nil {
		return []slog.Attr{slog.Int("exit_code", command.ProcessState.ExitCode())}
	}
	if exitCode, ok := logging.ExitCode(err); ok {
		return []slog.Attr{slog.Int("exit_code", exitCode)}
	}
	return nil
}

func bdSemanticOperation(args []string) string {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		switch arg {
		case "context":
			return "context"
		case "config":
			return "config"
		case "init":
			return "init"
		case "list":
			return "list"
		case "show":
			return "show"
		case "get":
			return "get"
		case "create":
			return "create"
		case "update":
			return "update"
		case "close":
			return "close"
		}
	}
	return "bd"
}

// LocalInspection describes repo-local Beads state discovered for a repository root.
type LocalInspection struct {
	Root     string
	BeadsDir string
	Prefix   string
}

// InspectLocal inspects root using the bd binary.
func InspectLocal(root string) (LocalInspection, error) {
	return InspectLocalWithLogger(root, nil)
}

// InspectLocalWithLogger inspects root using the bd binary and emits diagnostics.
func InspectLocalWithLogger(root string, logger *slog.Logger) (LocalInspection, error) {
	return InspectLocalWithRunner(root, NewInspectLocalRunner(logger))
}

// InitializeManaged initializes Beads state in an Orpheus-managed workspace using the bd binary.
func InitializeManaged(dir string, prefix string) error {
	return InitializeManagedWithLogger(dir, prefix, nil)
}

// InitializeManagedWithLogger initializes Beads state and emits diagnostics.
func InitializeManagedWithLogger(dir string, prefix string, logger *slog.Logger) error {
	return InitializeManagedWithRunner(dir, prefix, CommandRunner{Logger: logger})
}

// InitializeManagedWithRunner initializes a Beads database rooted at dir with prefix.
//
// The directory is created on demand, but existing non-empty directories are rejected
// so Orpheus does not accidentally reuse or overwrite unrelated state.
func InitializeManagedWithRunner(dir string, prefix string, runner Runner) error {
	if runner == nil {
		return errors.New("initialize managed Beads: runner is required")
	}

	normalizedDir, err := normalizeManagedDir(dir)
	if err != nil {
		return err
	}

	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return errors.New("initialize managed Beads: prefix is required")
	}

	if err := ensureManagedDirIsEmpty(normalizedDir); err != nil {
		return err
	}

	result, err := runner.Run(
		normalizedDir,
		"init",
		"--non-interactive",
		"--prefix",
		prefix,
		"--skip-agents",
		"--skip-hooks",
		"--quiet",
	)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("initialize managed Beads at %q: bd executable not found; install Beads or ensure bd is on PATH: %w", normalizedDir, err)
		}
		return fmt.Errorf("initialize managed Beads at %q: run bd init: %w%s", normalizedDir, err, formattedOutput(result))
	}
	return nil
}

// InspectLocalWithRunner detects repo-local Beads state and extracts its issue prefix.
//
// ErrNoLocal is returned when root has no repo-local .beads directory or when bd
// reports that no active Beads workspace exists. Other errors are actionable
// command, parsing, or configuration failures.
func InspectLocalWithRunner(root string, runner Runner) (LocalInspection, error) {
	if runner == nil {
		return LocalInspection{}, errors.New("inspect local Beads: runner is required")
	}

	normalizedRoot, err := normalizeRoot(root)
	if err != nil {
		return LocalInspection{}, err
	}

	expectedBeadsDir := filepath.Join(normalizedRoot, ".beads")
	if err := requireLocalBeadsDir(expectedBeadsDir); err != nil {
		return LocalInspection{}, err
	}

	context, err := inspectContext(normalizedRoot, runner)
	if err != nil {
		return LocalInspection{}, err
	}

	beadsDir, err := normalizePath(context.BeadsDir)
	if err != nil {
		return LocalInspection{}, fmt.Errorf("inspect local Beads at %q: normalize bd context beads_dir %q: %w", normalizedRoot, context.BeadsDir, err)
	}
	if beadsDir != expectedBeadsDir {
		return LocalInspection{}, fmt.Errorf(
			"inspect local Beads at %q: bd resolved beads_dir %q, expected repo-local directory %q; unset BEADS_DIR or repair the local Beads setup",
			normalizedRoot,
			beadsDir,
			expectedBeadsDir,
		)
	}

	prefix, err := inspectIssuePrefix(normalizedRoot, runner)
	if err != nil {
		return LocalInspection{}, err
	}

	return LocalInspection{Root: normalizedRoot, BeadsDir: beadsDir, Prefix: prefix}, nil
}

func normalizeRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("inspect local Beads: %w: repository root is required", ErrNoLocal)
	}
	return normalizePath(root)
}

func normalizeManagedDir(dir string) (string, error) {
	if strings.TrimSpace(dir) == "" {
		return "", errors.New("initialize managed Beads: directory is required")
	}
	return normalizePath(dir)
}

func ensureManagedDirIsEmpty(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("initialize managed Beads at %q: create directory: %w", dir, err)
			}
			return nil
		}
		return fmt.Errorf("initialize managed Beads at %q: inspect directory: %w", dir, err)
	}
	if len(entries) != 0 {
		return fmt.Errorf("initialize managed Beads at %q: directory already exists and is not empty; remove it or choose a different repo id", dir)
	}
	return nil
}

func normalizePath(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolutePath), nil
}

func requireLocalBeadsDir(path string) error {
	stat, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s does not exist", ErrNoLocal, path)
		}
		return fmt.Errorf("inspect local Beads at %q: %w", path, err)
	}
	if !stat.IsDir() {
		return fmt.Errorf("inspect local Beads at %q: .beads exists but is not a directory", path)
	}
	return nil
}

type contextResponse struct {
	BeadsDir string `json:"beads_dir"`
	Error    string `json:"error"`
	Message  string `json:"message"`
	Hint     string `json:"hint"`
}

func inspectContext(root string, runner Runner) (contextResponse, error) {
	result, err := runner.Run(root, "--json", "--readonly", "context")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return contextResponse{}, fmt.Errorf("inspect local Beads at %q: bd executable not found; install Beads or ensure bd is on PATH: %w", root, err)
		}

		response, parseErr := parseContextResponse(result.Stdout)
		if parseErr == nil && response.Error == "no_beads_directory" {
			return contextResponse{}, fmt.Errorf("%w: bd reported no active Beads workspace at %q", ErrNoLocal, root)
		}

		return contextResponse{}, fmt.Errorf("inspect local Beads at %q: run bd --json --readonly context: %w%s", root, err, formattedOutput(result))
	}

	response, err := parseContextResponse(result.Stdout)
	if err != nil {
		return contextResponse{}, fmt.Errorf("inspect local Beads at %q: parse bd context JSON: %w%s", root, err, formattedOutput(result))
	}
	if response.Error != "" {
		if response.Error == "no_beads_directory" {
			return contextResponse{}, fmt.Errorf("%w: bd reported no active Beads workspace at %q", ErrNoLocal, root)
		}
		return contextResponse{}, fmt.Errorf("inspect local Beads at %q: bd context error %q: %s%s", root, response.Error, response.Message, hintSuffix(response.Hint))
	}
	if strings.TrimSpace(response.BeadsDir) == "" {
		return contextResponse{}, fmt.Errorf("inspect local Beads at %q: bd context JSON missing beads_dir", root)
	}
	return response, nil
}

func parseContextResponse(output string) (contextResponse, error) {
	var response contextResponse
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		return contextResponse{}, err
	}
	return response, nil
}

type configGetResponse struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Error   string `json:"error"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

func inspectIssuePrefix(root string, runner Runner) (string, error) {
	result, err := runner.Run(root, "--json", "--readonly", "config", "get", "issue_prefix")
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("inspect local Beads prefix at %q: bd executable not found; install Beads or ensure bd is on PATH: %w", root, err)
		}
		return "", fmt.Errorf("inspect local Beads prefix at %q: run bd --json --readonly config get issue_prefix: %w%s", root, err, formattedOutput(result))
	}

	var response configGetResponse
	if err := json.Unmarshal([]byte(result.Stdout), &response); err != nil {
		return "", fmt.Errorf("inspect local Beads prefix at %q: parse bd config JSON: %w%s", root, err, formattedOutput(result))
	}
	if response.Error != "" {
		return "", fmt.Errorf("inspect local Beads prefix at %q: bd config error %q: %s%s", root, response.Error, response.Message, hintSuffix(response.Hint))
	}

	prefix := strings.TrimSpace(response.Value)
	if prefix == "" {
		return "", fmt.Errorf("inspect local Beads prefix at %q: issue_prefix is empty; set a Beads issue prefix before registering the repo", root)
	}
	return prefix, nil
}

func sanitizedEnvironment() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		key, _, ok := strings.Cut(entry, "=")
		if ok && (strings.HasPrefix(key, "BEADS_") || key == "BD_NON_INTERACTIVE") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "BD_NON_INTERACTIVE=1")
}

func formattedOutput(result Result) string {
	output := strings.TrimSpace(strings.Join([]string{result.Stdout, result.Stderr}, "\n"))
	if output == "" {
		return ""
	}
	return ": " + truncate(output, 1000)
}

func hintSuffix(hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return ""
	}
	return "; hint: " + hint
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
