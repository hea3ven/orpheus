package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

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
	Run(ctx context.Context, command CommandSnapshot, opts LaunchOptions) error
}

// AttachedLauncher runs an agent as a direct child process attached to the supplied stdio.
type AttachedLauncher struct{}

// Run executes command directly with no implicit shell parsing.
func (l AttachedLauncher) Run(ctx context.Context, command CommandSnapshot, opts LaunchOptions) error {
	if strings.TrimSpace(command.Command) == "" {
		return fmt.Errorf("run agent %q: command is required", command.AgentName)
	}
	if strings.TrimSpace(opts.Dir) == "" {
		return fmt.Errorf("run agent %q: execution directory is required", command.AgentName)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("run agent %q: %w", command.AgentName, err)
	}

	process := exec.CommandContext(ctx, command.Command, command.Args...)
	process.Dir = opts.Dir
	process.Env = append(os.Environ(), opts.Env...)
	process.Stdin = opts.Stdin
	process.Stdout = opts.Stdout
	process.Stderr = opts.Stderr

	if err := process.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf(
				"run agent %q: executable %q not found; check the agent profile command or PATH: %w",
				command.AgentName,
				command.Command,
				err,
			)
		}
		return fmt.Errorf("run agent %q: %w", command.AgentName, err)
	}
	return nil
}
