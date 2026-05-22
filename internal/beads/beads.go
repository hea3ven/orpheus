// Package beads provides a narrow adapter for inspecting local Beads state.
package beads

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// CommandRunner executes the real bd binary.
type CommandRunner struct {
	// Binary is the executable name or path. Empty uses "bd".
	Binary string
}

// Run executes bd in dir and captures stdout and stderr separately.
func (r CommandRunner) Run(dir string, args ...string) (Result, error) {
	binary := r.Binary
	if strings.TrimSpace(binary) == "" {
		binary = "bd"
	}

	command := exec.Command(binary, args...)
	command.Dir = dir

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	err := command.Run()
	return Result{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

// LocalInspection describes repo-local Beads state discovered for a repository root.
type LocalInspection struct {
	Root     string
	BeadsDir string
	Prefix   string
}

// InspectLocal inspects root using the bd binary.
func InspectLocal(root string) (LocalInspection, error) {
	return InspectLocalWithRunner(root, CommandRunner{})
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

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	return value[:max] + "…"
}
