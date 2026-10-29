package review

import (
	"context"
	"io"
	"log/slog"

	"github.com/hea3ven/orpheus/internal/agent"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

// Effects supplies the external candidate and command operations of a pipeline.
// Nil fields retain the local Git and child-process adapters. Pipeline decisions,
// findings, prompts, and persistence always remain in RunPipeline.
type Effects struct {
	CaptureCandidate func(context.Context, string, CommittedCandidate, *slog.Logger, ...slog.Attr) (CandidateCheck, error)
	RunCommand       func(CommandOptions) (*int, error)
	RunHunkCommand   func(HunkCommandOptions) (*int, []HunkNote, error)
	CaptureUsage     func(agent.UsageCaptureOptions) taskstate.RecordRunUsageOptions
}

// CommittedCandidate is the exact retained output and attached branch evaluated
// by this review; a clean checkout alone is not admission evidence.
type CommittedCandidate struct {
	Identity gitmeta.CheckoutIdentity
	Revision gitmeta.Revision
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

func captureLocalCandidate(ctx context.Context, workdir string, candidate CommittedCandidate, logger *slog.Logger, _ ...slog.Attr) (CandidateCheck, error) {
	return CaptureCommittedCandidate(ctx, workdir, candidate.Identity, candidate.Revision, logger)
}
