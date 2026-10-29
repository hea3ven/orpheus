package workflow

import (
	"context"
	"encoding/hex"
	"fmt"
	"log/slog"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/work"
)

// WorkStore is the validated, expected-version workflow persistence boundary.
// Implementations must independently reload prior state when saving a successor.
type WorkStore interface {
	Load(repositoryID, taskID string) (work.State, error)
	Save(work.State, work.StateVersion) (work.StateVersion, error)
}

// OutputGit owns ordinary commits, retention, recovery and exact checkout checks.
// It never authenticates agent completions or chooses workflow progression.
type OutputGit interface {
	CaptureRevision(context.Context, gitmeta.RevisionCapture) (gitmeta.Revision, error)
	VerifyRevision(context.Context, string, gitmeta.CheckoutIdentity, gitmeta.Revision) error
}

// LocalOutputGit is the local Git output boundary.
type LocalOutputGit struct{}

// CaptureRevision captures an effort's ordinary retained Git output.
func (LocalOutputGit) CaptureRevision(ctx context.Context, opts gitmeta.RevisionCapture) (gitmeta.Revision, error) {
	return gitmeta.CaptureRevision(ctx, opts)
}

// VerifyRevision checks a recorded output without creating another commit.
func (LocalOutputGit) VerifyRevision(ctx context.Context, dir string, expected gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	return gitmeta.VerifyRevision(ctx, dir, expected, revision)
}

// ImplementationService captures output after independently recorded handoff and
// process evidence under application mutation protection. It does not
// advance phases, launch agents, record review results or grant publication consent.
type ImplementationService struct {
	Paths  state.Paths
	Store  WorkStore
	Git    OutputGit
	Logger *slog.Logger
}

// Capture records output only for the current effort after its safe successful
// process boundary. If Git succeeds but persistence fails, the owned ref remains
// recoverable; an unchanged retry reconciles it rather than committing again.
func (s ImplementationService) Capture(ctx context.Context, ref work.AttemptRef) (work.CodeOutput, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.Store == nil || s.Git == nil {
		return work.CodeOutput{}, fmt.Errorf("output capture requires workflow storage and Git operations")
	}
	var result work.CodeOutput
	err := state.WithGlobalMutationLockLogger(ctx, s.Paths, "capture implementation output", s.Logger, func() error {
		captured, err := s.captureLocked(ctx, ref)
		result = captured
		return err
	})
	if err != nil {
		return work.CodeOutput{}, err
	}
	return result, nil
}

func (s ImplementationService) captureLocked(ctx context.Context, ref work.AttemptRef) (work.CodeOutput, error) {
	snapshot, err := s.Store.Load(ref.RepositoryID, ref.TaskID)
	if err != nil {
		return work.CodeOutput{}, err
	}
	if err := snapshot.CheckOutputCapture(ref); err != nil {
		return work.CodeOutput{}, err
	}
	w := &snapshot.Workflows[ref.Workflow]
	p := w.Phases[ref.Phase].(*work.ImplementationPhase) // CheckOutputCapture establishes ownership and kind.
	prepared := w.Context()
	if p.Output != nil {
		return s.verifyCaptured(ctx, w, prepared, ref.Phase)
	}
	input, err := implementationInput(w, prepared, p)
	if err != nil {
		return work.CodeOutput{}, err
	}
	revision, err := s.Git.CaptureRevision(ctx, gitmeta.RevisionCapture{
		Directory: prepared.Directory,
		Input:     gitmeta.CheckoutIdentity{Commit: input.Commit, Ref: "refs/heads/" + prepared.TaskBranch},
		Ref:       outputRefNamespace(ref),
		Message:   p.Completion.Summary + "\n\n" + p.Completion.Description,
	})
	if err != nil {
		return work.CodeOutput{}, err
	}
	if revision.Reused && (revision.Commit != input.Commit || revision.Tree != input.Tree) {
		return work.CodeOutput{}, fmt.Errorf("unchanged output does not match this effort's exact input revision")
	}
	p.Output = &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: revision.Commit, Tree: revision.Tree}, RetainedRef: revision.RetainedRef}
	if revision.Reused && p.Input != nil {
		p.Output = &work.ReusedOutput{Input: *p.Input}
	}
	// Reverify the selected reference as well: reuse inherits the earlier owner's
	// retention, not the new adapter checkpoint or another effort's handoff.
	result, err := s.verifyCaptured(ctx, w, prepared, ref.Phase)
	if err != nil {
		return work.CodeOutput{}, err
	}
	if _, err := s.Store.Save(snapshot, snapshot.Version); err != nil {
		return work.CodeOutput{}, fmt.Errorf("retained Git output could not be saved; retry this effort without changing its input: %w", err)
	}
	return result, nil
}

func (s ImplementationService) verifyCaptured(ctx context.Context, w *work.Workflow, prepared *work.WorkContext, phase int) (work.CodeOutput, error) {
	output, err := w.Output(work.OutputRef{Phase: phase})
	if err != nil {
		return work.CodeOutput{}, err
	}
	err = s.Git.VerifyRevision(ctx, prepared.Directory, gitmeta.CheckoutIdentity{Commit: output.Commit, Ref: "refs/heads/" + prepared.TaskBranch}, gitmeta.Revision{Commit: output.Commit, Tree: output.Tree, RetainedRef: output.RetainedRef})
	if err != nil {
		return work.CodeOutput{}, err
	}
	return *output, nil
}

func implementationInput(w *work.Workflow, prepared *work.WorkContext, p *work.ImplementationPhase) (work.CodeIdentity, error) {
	if p.Input == nil {
		return prepared.Baseline, nil
	}
	output, err := w.Output(*p.Input)
	if err != nil {
		return work.CodeIdentity{}, err
	}
	return output.CodeIdentity, nil
}

func outputRefNamespace(ref work.AttemptRef) string {
	// Hex encodes external repository/task components without inventing another
	// identity. Positions remain the stable parent/index workflow addresses.
	return fmt.Sprintf("refs/orpheus/revisions/%s/%s/%d/%d", hex.EncodeToString([]byte(ref.RepositoryID)), hex.EncodeToString([]byte(ref.TaskID)), ref.Workflow, ref.Phase)
}
