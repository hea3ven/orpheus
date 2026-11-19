package delivery

import (
	"context"
	"errors"
	"strings"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

type publicPreflight struct {
	owner   work.AttemptRef
	content PullRequestContent
}

type prObservation struct {
	url       string
	recovered bool
}

func (s *Service) nextPR(current work.State, selected committedPublication) (phaseexec.Selection[Request, Outcome], error) {
	owner, p := selected.owner(current), selected.publication
	if s.preflight == nil || !s.preflight.owner.Equal(owner) {
		return phaseexec.Run[Request, Outcome](s.publicPreflight(owner, true)), nil
	}
	if p.Operation == nil {
		return phaseexec.Run[Request, Outcome](s.packagePublic(owner)), nil
	}
	if p.Operation.Public == nil {
		return phaseexec.Selection[Request, Outcome]{}, errors.New("publication packaging needs an interactive restore-or-manual-repair decision; continue task run")
	}
	if p.Operation.Public.Tree != p.Input.Code.Tree {
		return phaseexec.Selection[Request, Outcome]{}, packageTreeError(p.Input.Code.CodeIdentity, *p.Operation.Public)
	}
	if s.prObservation == nil {
		return phaseexec.Run[Request, Outcome](s.pushAndFindPR(owner)), nil
	}
	if s.prObservation.url == "" {
		return phaseexec.Run[Request, Outcome](s.createPR(owner)), nil
	}
	return phaseexec.Run[Request, Outcome](s.observePRDelivery(owner)), nil
}

func publicOperation(owner work.AttemptRef) *work.PublicationOperation {
	return &work.PublicationOperation{StartedAt: time.Now().UTC(), Ref: phaseexec.RevisionNamespace(owner) + "/public"}
}

func publicationRequest(w *work.Workflow, p *work.PublicationPhase) gitmeta.PublicCommitRequest {
	input := p.Input.Code
	return gitmeta.PublicCommitRequest{Directory: w.Context().Directory, Branch: w.Context().TaskBranch, Parent: w.Context().Baseline.Commit, Ref: p.Operation.Ref, Message: w.Publication.CommitMessage(), Input: gitmeta.Revision{Commit: input.Commit, Tree: input.Tree, RetainedRef: input.RetainedRef}}
}

func (s *Service) publicPreflight(owner work.AttemptRef, pr bool) phaseexec.Operation {
	return operation("inspect public history context", owner, func(current *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		selected, err := selectCommittedPublication(current, s.Task)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		plan := *p
		if plan.Operation == nil {
			plan.Operation = publicOperation(owner)
		}
		req := publicationRequest(w, &plan)
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			var content PullRequestContent
			if pr {
				var err error
				content, err = s.publicationPRContent(s.Source.Repository, s.Task, current, p)
				if err != nil {
					return phaseexec.Result{Err: err}
				}
				if err := s.verifyPublicContext(ctx, s.Source.Repository, s.Task, selected, s.diagnostics(w)); err != nil {
					return phaseexec.Result{Err: err}
				}
			}
			if p.Operation == nil {
				if err := s.Git.RetainPublicationInput(ctx, req); err != nil {
					return phaseexec.Result{Err: err}
				}
			}
			s.preflight = &publicPreflight{owner: owner, content: content}
			return phaseexec.Result{}
		}}, nil
	})
}

func (s *Service) packagePublic(owner work.AttemptRef) phaseexec.Operation {
	return operation("package public history", owner, func(current *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		if p.Consent == nil {
			return phaseexec.Prepared{}, ErrConsentRequired
		}
		if p.Operation != nil {
			return phaseexec.Prepared{}, errors.New("publication packaging requires confirmed recovery")
		}
		p.Operation = publicOperation(owner)
		req := publicationRequest(w, p)
		_, pr := p.Target.(*work.PullRequestTarget)
		return phaseexec.Prepared{Changed: true, Run: func(ctx context.Context) phaseexec.Result {
			public, err := s.Git.PackagePublicCommit(ctx, req)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			actual := work.CodeIdentity{Commit: public.Commit, Tree: public.Tree}
			var treeErr error
			if pr && public.Tree != req.Input.Tree {
				treeErr = packageTreeError(work.CodeIdentity{Commit: req.Input.Commit, Tree: req.Input.Tree}, actual)
			}
			return phaseexec.Result{Err: treeErr, Update: change(owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (bool, error) {
				p.Operation.Public = &actual
				return true, nil
			})}
		}}, nil
	})
}

func (s *Service) pushAndFindPR(owner work.AttemptRef) phaseexec.Operation {
	return operation("push and reconcile public PR", owner, func(_ *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		req := publicationRequest(w, p)
		public := gitmeta.Revision{Commit: p.Operation.Public.Commit, Tree: p.Operation.Public.Tree}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			if err := s.Git.PushPublicCommit(ctx, req, public); err != nil {
				return phaseexec.Result{Err: err}
			}
			if err := verifyPublishedOutput(ctx, s.Git, req.Directory, req.Branch, public); err != nil {
				return phaseexec.Result{Err: err}
			}
			found, exists, err := s.PRProvider.FindByBranch(ctx, pullrequest.FindByBranchRequest{RepositoryPath: s.Source.Repository.Path, HeadBranch: req.Branch, BaseBranch: p.Destination.Branch, Diagnostics: s.diagnostics(w)})
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			observed := &prObservation{}
			if exists {
				if p.Operation.PRCreationStartedAt == nil {
					return phaseexec.Result{Err: errors.New("PR exists without this operation's creation intent; reconcile instead of importing")}
				}
				if err := s.verifyPublicationPR(ctx, found.URL, req.Branch, p.Destination.Branch, public.Commit, s.diagnostics(w)); err != nil {
					return phaseexec.Result{Err: err}
				}
				observed.url, observed.recovered = found.URL, true
			}
			s.prObservation = observed
			return phaseexec.Result{}
		}}, nil
	})
}

func (s *Service) createPR(owner work.AttemptRef) phaseexec.Operation {
	return operation("create publication PR", owner, func(_ *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		changed := p.Operation.PRCreationStartedAt == nil
		if changed {
			at := time.Now().UTC()
			p.Operation.PRCreationStartedAt = &at
		}
		target := p.Target.(*work.PullRequestTarget)
		content := s.preflight.content
		return phaseexec.Prepared{Changed: changed, Run: func(ctx context.Context) phaseexec.Result {
			created, err := s.PRProvider.Create(ctx, pullrequest.CreateRequest{Draft: target.Draft, RepositoryPath: s.Source.Repository.Path, HeadBranch: w.Context().TaskBranch, BaseBranch: p.Destination.Branch, Title: content.Title, Body: content.Body, Diagnostics: s.diagnostics(w)})
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			if strings.TrimSpace(created.URL) == "" {
				return phaseexec.Result{Err: errors.New("PR creation returned no native identity; retry find/import")}
			}
			if err := s.verifyPublicationPR(ctx, created.URL, w.Context().TaskBranch, p.Destination.Branch, p.Operation.Public.Commit, s.diagnostics(w)); err != nil {
				return phaseexec.Result{Err: err}
			}
			s.prObservation = &prObservation{url: created.URL}
			return s.prDeliveryResult(ctx, owner, w, p)
		}}, nil
	})
}

func (s *Service) observePRDelivery(owner work.AttemptRef) phaseexec.Operation {
	return operation("observe publication PR delivery", owner, func(_ *work.State, w *work.Workflow, p *work.PublicationPhase) (phaseexec.Prepared, error) {
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result { return s.prDeliveryResult(ctx, owner, w, p) }}, nil
	})
}

func (s *Service) prDeliveryResult(ctx context.Context, owner work.AttemptRef, w *work.Workflow, p *work.PublicationPhase) phaseexec.Result {
	public := gitmeta.Revision{Commit: p.Operation.Public.Commit, Tree: p.Operation.Public.Tree}
	expected := pullrequest.PullRequestStatus{URL: s.prObservation.url, State: pullrequest.StateOpen, HeadBranch: w.Context().TaskBranch, BaseBranch: p.Destination.Branch, HeadCommit: public.Commit}
	if err := s.ensurePublicationContent(ctx, s.Source.Repository.Path, s.preflight.content, expected, s.diagnostics(w)); err != nil {
		return phaseexec.Result{Err: err}
	}
	if err := s.verifyPublicationPR(ctx, expected.URL, expected.HeadBranch, expected.BaseBranch, public.Commit, s.diagnostics(w)); err != nil {
		return phaseexec.Result{Err: err}
	}
	if err := verifyPublishedOutput(ctx, s.Git, w.Context().Directory, w.Context().TaskBranch, public); err != nil {
		return phaseexec.Result{Err: err}
	}
	delivered := &work.PullRequestDelivery{PullRequest: work.PullRequestRef{URL: expected.URL}, ObservedAt: time.Now().UTC()}
	return phaseexec.Result{Update: change(owner, func(_ *work.State, _ *work.Workflow, p *work.PublicationPhase) (bool, error) {
		p.Delivery = delivered
		return true, nil
	})}
}

func (s *Service) diagnostics(w *work.Workflow) pullrequest.DiagnosticContext {
	return pullrequest.DiagnosticContext{RepoID: s.Source.Repository.ID, TaskID: s.Task.ID, Branch: w.Context().TaskBranch}
}

func verifyPublishedOutput(ctx context.Context, boundary Git, dir, branch string, output gitmeta.Revision) error {
	actual, err := boundary.PublishedRevision(ctx, dir, branch)
	if err != nil {
		return err
	}
	if actual.Commit != output.Commit || actual.Tree != output.Tree {
		return errors.New("origin branch changed while observing the authorized input")
	}
	return nil
}
