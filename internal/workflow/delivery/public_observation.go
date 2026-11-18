package delivery

import (
	"context"
	"errors"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

func (s *Service) verifyPublicContext(ctx context.Context, boundary Git, repo task.Repository, item task.Task, selected committedPublication, diagnostics pullrequest.DiagnosticContext) error {
	p, prepared := selected.publication, selected.prepared
	remote, err := boundary.ObservePublicHead(ctx, prepared.Directory, prepared.TaskBranch)
	if err != nil {
		return err
	}
	recovering := p.Operation != nil && p.Operation.Public != nil && remote == p.Operation.Public.Commit
	if remote != "" && !recovering {
		return errors.New("unexpected public head; reconcile remote changes before publication")
	}
	if err := s.verifyInitialPublicPR(ctx, repo, prepared, p, remote, recovering, diagnostics); err != nil {
		return err
	}

	metadata := item.OrpheusMetadata()
	if metadata.HasPRURL && metadata.PRURL != "" && !recovering {
		return errors.New("task source names an unrelated PR; reconcile before publication")
	}
	return nil
}

func (s *Service) verifyInitialPublicPR(ctx context.Context, repo task.Repository, prepared *work.WorkContext, p *work.PublicationPhase, remote string, recovering bool, diagnostics pullrequest.DiagnosticContext) error {
	found, exists, err := s.PRProvider.FindByBranch(ctx, pullrequest.FindByBranchRequest{RepositoryPath: repo.Path, HeadBranch: prepared.TaskBranch, BaseBranch: p.Destination.Branch, Diagnostics: diagnostics})
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if !recovering || p.Operation == nil || p.Operation.PRCreationStartedAt == nil {
		return errors.New("existing PR requires reconciliation; initial publication cannot import unrelated remote work")
	}
	return s.verifyPublicationPR(ctx, found.URL, prepared.TaskBranch, p.Destination.Branch, remote, diagnostics)
}

func (s *Service) verifyPublicationPR(ctx context.Context, url, branch, destination, head string, diagnostics pullrequest.DiagnosticContext) error {
	status, err := s.PRProvider.StatusByURL(ctx, pullrequest.StatusByURLRequest{URL: url, Diagnostics: diagnostics})
	if err != nil {
		return err
	}
	return validatePublicationPR(status, pullrequest.PullRequestStatus{URL: url, State: pullrequest.StateOpen, HeadBranch: branch, BaseBranch: destination, HeadCommit: head})
}

func validatePublicationPR(status, expected pullrequest.PullRequestStatus) error {
	if status.URL != expected.URL || status.State != pullrequest.StateOpen {
		return errors.New("publication PR is merged or closed; reconcile lifecycle before delivery")
	}
	if status.CrossRepository || status.HeadBranch != expected.HeadBranch || status.BaseBranch != expected.BaseBranch || (expected.HeadCommit != "" && status.HeadCommit != expected.HeadCommit) {
		return errors.New("publication PR repository, destination, branch or public head changed; reconcile before delivery")
	}
	return nil
}

func (s *Service) ensurePublicationContent(ctx context.Context, dir string, content PullRequestContent, expected pullrequest.PullRequestStatus, diagnostics pullrequest.DiagnosticContext) error {
	title, body := content.Title, content.Body
	req := pullrequest.StatusByURLRequest{URL: expected.URL, Diagnostics: diagnostics}
	status, err := s.PRProvider.StatusByURL(ctx, req)
	if err != nil {
		return err
	}
	if err := validatePublicationPR(status, expected); err != nil {
		return err
	}
	if status.Title == title && status.Body == body {
		return nil
	}
	updateErr := s.PRProvider.Update(ctx, pullrequest.UpdateRequest{RepositoryPath: dir, URL: expected.URL, Title: content.Title, Body: content.Body, Diagnostics: diagnostics})
	status, err = s.PRProvider.StatusByURL(ctx, req)
	if err != nil {
		return errors.Join(updateErr, err)
	}
	if err := validatePublicationPR(status, expected); err != nil {
		return errors.Join(updateErr, err)
	}
	if status.Title != title || status.Body != body {
		return errors.Join(updateErr, errors.New("PR narrative was not independently observed at exact authorized content"))
	}
	return nil
}

// publicationPRContent reads policy at use time; authored content alone is locked
// by consent. Generated process and review-gap sections are PR-only.
func (s *Service) publicationPRContent(repo task.Repository, item task.Task, snapshot *work.State, p *work.PublicationPhase) (PullRequestContent, error) {
	options, err := ResolvePublicationOptions(s.Paths, repo)
	if err != nil {
		return PullRequestContent{}, err
	}
	view, err := taskstate.Project(*snapshot)
	if err != nil {
		return PullRequestContent{}, err
	}
	content, err := BuildPublicationPullRequestContentFromStateWithOptions(options, item, view)
	if err != nil {
		return PullRequestContent{}, err
	}
	w := snapshot.ActiveWorkflow()
	review := w.Phases[p.Review.Phase].(*work.ReviewPhase)
	reviewed, err := w.Output(review.Scope.RevisionRef(p.Review.Phase))
	if err != nil {
		return PullRequestContent{}, err
	}
	content.Body += publicationReviewGap(p.Input, reviewed.Commit)
	return content, nil
}
