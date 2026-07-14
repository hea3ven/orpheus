// Package agentexec runs resolved agent commands through a shared process boundary.
package agentexec

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Command is a resolved direct process invocation.
type Command struct {
	Name    string
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
type AttachedLauncher struct{}

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

	process := exec.CommandContext(ctx, command.Command, command.Args...)
	process.Dir = opts.Dir
	process.Env = append(os.Environ(), opts.Env...)
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
	if err := process.Wait(); err != nil {
		return fmt.Errorf("run agent %q: %w", command.Name, err)
	}
	return nil
}
