// Package implementation owns execution and retained output for an implementation effort.
package implementation

import (
	"context"
	"fmt"
	"log/slog"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// OutputGit owns ordinary commits, retention and interrupted capture recovery.
type OutputGit interface {
	CaptureRevision(context.Context, gitmeta.RevisionCapture) (gitmeta.Revision, error)
	VerifyRetainedRevision(context.Context, string, gitmeta.Revision) error
}

type LocalOutputGit struct{}

func (LocalOutputGit) CaptureRevision(ctx context.Context, opts gitmeta.RevisionCapture) (gitmeta.Revision, error) {
	return gitmeta.CaptureRevision(ctx, opts)
}
func (LocalOutputGit) VerifyRetainedRevision(ctx context.Context, dir string, revision gitmeta.Revision) error {
	return gitmeta.VerifyRetainedRevision(ctx, dir, revision)
}

type Service struct {
	Paths  state.Paths
	Store  phaseexec.Store
	Git    OutputGit
	Logger *slog.Logger
}

type Intent struct{ Owner work.AttemptRef }
type Request struct{}
type OutputCaptured struct{ Output work.CodeOutput }

// Next selects capture or verification from committed facts. Handoff and an
// observed successful process remain independent prerequisites for either.
func (s Service) Next(current work.State, intent Intent) (phaseexec.Selection[Request, OutputCaptured], error) {
	ref := intent.Owner
	if err := current.CheckOutputCapture(ref); err != nil {
		return phaseexec.Selection[Request, OutputCaptured]{}, err
	}
	w := &current.Workflows[ref.Workflow]
	p := w.Phases[ref.Phase].(*work.ImplementationPhase)
	if p.Output != nil {
		output, err := w.Output(work.OutputRef{Phase: ref.Phase})
		if err != nil {
			return phaseexec.Selection[Request, OutputCaptured]{}, err
		}
		return phaseexec.Return[Request](OutputCaptured{Output: *output}), nil
	}
	return phaseexec.Run[Request, OutputCaptured](s.captureOperation(ref)), nil
}

func (s Service) captureOperation(ref work.AttemptRef) phaseexec.Operation {
	return phaseexec.Operation{Name: "capture implementation output", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		if err := current.CheckOutputCapture(ref); err != nil {
			return phaseexec.Prepared{}, err
		}
		w := &current.Workflows[ref.Workflow]
		p := w.Phases[ref.Phase].(*work.ImplementationPhase)
		prepared := w.Context()
		input, err := implementationInput(w, prepared, p)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		request := gitmeta.RevisionCapture{Directory: prepared.Directory, Input: gitmeta.CheckoutIdentity{Commit: input.Commit, Ref: "refs/heads/" + prepared.TaskBranch}, Ref: phaseexec.RevisionNamespace(ref), Message: p.Completion.CommitSubject + "\n\n" + p.Completion.CommitBody}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			revision, err := s.Git.CaptureRevision(ctx, request)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			if revision.Reused && (revision.Commit != input.Commit || revision.Tree != input.Tree) {
				return phaseexec.Result{Err: fmt.Errorf("unchanged output does not match this effort's exact input revision")}
			}
			// Reuse inherits the earlier owner's retained ref, not a new checkpoint.
			retained := revision
			if revision.Reused && p.Input != nil {
				output, err := w.Output(*p.Input)
				if err != nil {
					return phaseexec.Result{Err: err}
				}
				retained = gitmeta.Revision{Commit: output.Commit, Tree: output.Tree, RetainedRef: output.RetainedRef}
			}
			if err := s.Git.VerifyRetainedRevision(ctx, request.Directory, retained); err != nil {
				return phaseexec.Result{Err: err}
			}
			return phaseexec.Result{Update: capturedOutput(ref, revision)}
		}}, nil
	}}
}

func capturedOutput(ref work.AttemptRef, revision gitmeta.Revision) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		if err := current.CheckOutputCapture(ref); err != nil {
			return false, err
		}
		effort := current.Workflows[ref.Workflow].Phases[ref.Phase].(*work.ImplementationPhase)
		if effort.Output != nil {
			return false, nil
		}
		effort.Output = &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: revision.Commit, Tree: revision.Tree}, RetainedRef: revision.RetainedRef}
		if revision.Reused && effort.Input != nil {
			effort.Output = &work.ReusedOutput{Input: *effort.Input}
		}
		return true, nil
	}
}

// Capture drives the phase boundary for callers that only need retained output.
// Existing output is still verified on retry; no checkout admission is imposed.
func (s Service) Capture(ctx context.Context, ref work.AttemptRef) (work.CodeOutput, error) {
	runner := phaseexec.Runner{Store: s.Store, Paths: s.Paths, RepositoryID: ref.RepositoryID, TaskID: ref.TaskID, Logger: s.Logger}
	current, err := runner.Load(ctx)
	if err != nil {
		return work.CodeOutput{}, err
	}
	for {
		selected, err := s.Next(current, Intent{Owner: ref})
		if err != nil {
			return work.CodeOutput{}, err
		}
		if op, ok := selected.Operation(); ok {
			current, err = runner.Execute(ctx, current, op)
			if err != nil {
				return work.CodeOutput{}, fmt.Errorf("retained Git output could not be saved; retry this effort without changing its input: %w", err)
			}
			continue
		}
		outcome, _ := selected.Outcome()
		err = s.Git.VerifyRetainedRevision(ctx, current.Workflows[ref.Workflow].Context().Directory, gitmeta.Revision{Commit: outcome.Output.Commit, Tree: outcome.Output.Tree, RetainedRef: outcome.Output.RetainedRef})
		return outcome.Output, err
	}
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
