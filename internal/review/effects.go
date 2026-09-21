package review

import (
	"context"
	"io"
	"log/slog"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// Effects supplies the external candidate and command operations of a pipeline.
// Nil fields retain the local Git and child-process adapters. Pipeline decisions,
// findings, prompts, and persistence always remain in RunPipeline.
type Effects struct {
	CaptureCandidate func(context.Context, string, *slog.Logger, ...slog.Attr) (CandidateCheck, error)
	RunCommand       func(CommandOptions) (*int, error)
	RunHunkCommand   func(HunkCommandOptions) (*int, []HunkNote, error)
	CaptureUsage     func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions
}

// CandidateCheck checks for reviewer mutations and restores the captured candidate.
type CandidateCheck func() error

// CommandOptions describes a review check or a confirmed manual command.
// RunCommand returns a nil exit code for start failures. Exit errors implement
// ExitCode() int, as os/exec.ExitError does.
type CommandOptions struct {
	Context     context.Context
	Step        Step
	Workdir     string
	Environment []string
	Stdout      io.Writer
	Stderr      io.Writer
}

// HunkCommandOptions describes a confirmed Hunk-backed manual command.
// RunHunkCommand returns captured Hunk notes after the command exits.
type HunkCommandOptions struct {
	Context     context.Context
	Step        Step
	Workdir     string
	Environment []string
	Stdout      io.Writer
	Stderr      io.Writer
}

func captureLocalCandidate(ctx context.Context, workdir string, logger *slog.Logger, attrs ...slog.Attr) (CandidateCheck, error) {
	snapshot, err := captureCandidateSnapshot(ctx, workdir, logger, attrs...)
	if err != nil {
		return nil, err
	}
	return func() error { return restoreCandidateIfMutated(ctx, snapshot, logger, attrs...) }, nil
}
