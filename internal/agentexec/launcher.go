// Package agentexec runs resolved agent commands through a shared process boundary.
package agentexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/testguard"
)

// Command is a resolved direct process invocation.
type Command struct {
	Name    string
	Harness string
	Command string
	Args    []string
}

// LaunchOptions controls one attached agent process invocation.
type LaunchOptions struct {
	Dir    string
	Env    []string
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// OnStart receives the direct child PID immediately after it starts. Returning
	// an error stops the child before Run returns, so callers never knowingly
	// continue an untracked attached execution.
	OnStart func(pid int) error
}

// Launcher runs a resolved agent command.
type Launcher interface {
	Run(ctx context.Context, command Command, opts LaunchOptions) error
}

// StartError wraps a failure that happened before the agent process started.
type StartError struct {
	Name string
	Err  error
}

// Error returns a human-readable start failure.
func (e *StartError) Error() string {
	if e == nil {
		return "run agent: start process"
	}
	return fmt.Sprintf("run agent %q: start process: %v", e.Name, e.Err)
}

// Unwrap returns the underlying process-start error.
func (e *StartError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsStartError reports whether err indicates the agent process did not start.
func IsStartError(err error) bool {
	var startErr *StartError
	return errors.As(err, &startErr)
}

// AttachedLauncher runs an agent as a direct child process attached to the supplied stdio.
type AttachedLauncher struct {
	// Environment is the complete inherited environment for launched agents.
	// Empty uses the current process environment.
	Environment []string
}

// Run executes command directly with no implicit shell parsing.
func (l AttachedLauncher) Run(ctx context.Context, command Command, opts LaunchOptions) error {
	if strings.TrimSpace(command.Command) == "" {
		return &StartError{Name: command.Name, Err: errors.New("command is required")}
	}
	if strings.TrimSpace(opts.Dir) == "" {
		return &StartError{Name: command.Name, Err: errors.New("execution directory is required")}
	}
	if err := ctx.Err(); err != nil {
		return &StartError{Name: command.Name, Err: err}
	}
	environment := l.Environment
	if environment == nil {
		environment = os.Environ()
	}
	if err := requireTestFakeAgent(command, environment); err != nil {
		return &StartError{Name: command.Name, Err: err}
	}

	process := exec.CommandContext(ctx, resolveExecutable(environment, command.Command), command.Args...)
	process.Dir = opts.Dir
	process.Env = mergeEnvironment(environment, opts.Env)
	process.Stdin = opts.Stdin
	process.Stdout = opts.Stdout
	process.Stderr = opts.Stderr

	if err := process.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return &StartError{Name: command.Name, Err: fmt.Errorf(
				"executable %q not found; check the agent profile command or PATH: %w",
				command.Command,
				err,
			)}
		}
		return &StartError{Name: command.Name, Err: err}
	}
	if opts.OnStart != nil {
		if err := opts.OnStart(process.Process.Pid); err != nil {
			_ = process.Process.Kill()
			_ = process.Wait()
			return fmt.Errorf("record started agent %q: %w", command.Name, err)
		}
	}
	if err := process.Wait(); err != nil {
		return fmt.Errorf("run agent %q: %w", command.Name, err)
	}
	return nil
}

func mergeEnvironment(base []string, overrides []string) []string {
	merged := append([]string{}, base...)
	indexes := make(map[string]int, len(merged))
	for index, entry := range merged {
		if key, _, ok := strings.Cut(entry, "="); ok {
			indexes[key] = index
		}
	}
	for _, entry := range overrides {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			merged = append(merged, entry)
			continue
		}
		if index, ok := indexes[key]; ok {
			merged[index] = entry
			continue
		}
		indexes[key] = len(merged)
		merged = append(merged, entry)
	}
	return merged
}

func resolveExecutable(environment []string, name string) string {
	if filepath.IsAbs(name) || strings.ContainsRune(name, os.PathSeparator) {
		return name
	}
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || key != "PATH" {
			continue
		}
		for _, directory := range filepath.SplitList(value) {
			candidate := filepath.Join(directory, name)
			info, err := os.Stat(candidate)
			if err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
				absolute, absErr := filepath.Abs(candidate)
				if absErr == nil {
					return absolute
				}
			}
		}
	}
	return filepath.Join(os.TempDir(), "orpheus-missing-executable", filepath.Base(name))
}

func requireTestFakeAgent(command Command, environment []string) error {
	if !testguard.IsTestProcess() || !supportedHarnessCommand(command) {
		return nil
	}

	expected := environmentValue(environment, testguard.FakeAgentEnvKey(command.Command))
	if expected == "" {
		expected = testguard.FakeAgentPath(command.Command)
	}
	if expected == "" {
		return fmt.Errorf(
			"test safety gate blocked supported agent executable %q; register an explicit fake",
			command.Command,
		)
	}
	resolved := resolveExecutable(environment, command.Command)
	if _, err := os.Stat(resolved); err != nil {
		return fmt.Errorf("test safety gate resolve registered fake %q: %w", command.Command, err)
	}
	if !sameExecutable(expected, resolved) {
		return fmt.Errorf(
			"test safety gate blocked supported agent executable %q; resolved %q instead of registered fake %q",
			command.Command,
			resolved,
			expected,
		)
	}
	return nil
}

func environmentValue(environment []string, name string) string {
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if ok && key == name {
			return value
		}
	}
	return ""
}

func supportedHarnessCommand(command Command) bool {
	harness := strings.TrimSpace(command.Harness)
	if harness == "codex" || harness == "pi" {
		return true
	}
	base := filepath.Base(strings.TrimSpace(command.Command))
	return base == "codex" || base == "pi"
}

func sameExecutable(first string, second string) bool {
	first, err := filepath.EvalSymlinks(first)
	if err != nil {
		return false
	}
	second, err = filepath.EvalSymlinks(second)
	if err != nil {
		return false
	}
	return filepath.Clean(first) == filepath.Clean(second)
}
