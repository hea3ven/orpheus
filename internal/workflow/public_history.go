package workflow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
)

func savePublicationOperation(store WorkStore, snapshot *work.State) error {
	version, err := store.Save(*snapshot, snapshot.Version)
	if err == nil {
		snapshot.Version = version
	}
	return err
}

func (s FinalizationService) recordPublicDelivery(ctx context.Context, store WorkStore, boundary FinalizationGit, repo task.Repository, item task.Task, snapshot *work.State, selected committedPublication, result *FinalizationResult) error {
	if s.PRProvider == nil {
		return errors.New("PR provider is required")
	}
	p, prepared := selected.publication, selected.prepared
	diagnostics := pullrequest.DiagnosticContext{RepoID: repo.ID, TaskID: item.ID, Branch: prepared.TaskBranch}
	content, err := s.publicationPRContent(repo, item, snapshot, p)
	if err != nil {
		return err
	}
	if err := s.verifyPublicContext(ctx, boundary, repo, item, selected, diagnostics); err != nil {
		return err
	}
	if err := reservePublicOperation(ctx, store, boundary, snapshot, selected); err != nil {
		return err
	}
	req := publicationRequest(selected.workflow, p)
	public, err := packagePublicOperation(ctx, store, boundary, snapshot, p, req)
	if err != nil {
		return err
	}
	if err := boundary.PushPublicCommit(ctx, req, public); err != nil {
		return err
	}
	delivered, recovered, err := s.observePublicDelivery(ctx, store, snapshot, boundary, repo, prepared, p, public, content, diagnostics)
	if err != nil {
		return err
	}
	result.PRRecovered = recovered
	p.Delivery = delivered
	return nil
}

// observePublicDelivery reconciles only an already published commit. It never
// pushes or requires the checkout to still name that operation's selected input.
func (s FinalizationService) observePublicDelivery(ctx context.Context, store WorkStore, snapshot *work.State, boundary FinalizationGit, repo task.Repository, prepared *work.WorkContext, p *work.PublicationPhase, public gitmeta.Revision, content PullRequestContent, diagnostics pullrequest.DiagnosticContext) (*work.PullRequestDelivery, bool, error) {
	if s.PRProvider == nil {
		return nil, false, errors.New("PR provider is required")
	}

	if public.Tree != p.Input.Code.Tree {
		return nil, false, errors.New("published operation does not preserve its authorized input tree")
	}
	if err := verifyPublishedOutput(ctx, boundary, prepared.Directory, prepared.TaskBranch, public); err != nil {
		return nil, false, err
	}
	url, recovered, err := s.observeOrCreatePublicationPR(ctx, store, snapshot, repo, prepared, p, public, content, diagnostics)
	if err != nil {
		return nil, false, err
	}
	if err := s.ensurePublicationContent(ctx, repo.Path, content, pullrequest.PullRequestStatus{URL: url, State: pullrequest.StateOpen, HeadBranch: prepared.TaskBranch, BaseBranch: p.Destination.Branch, HeadCommit: public.Commit}, diagnostics); err != nil {
		return nil, false, err
	}
	if err := s.verifyPublicationPR(ctx, url, prepared.TaskBranch, p.Destination.Branch, public.Commit, diagnostics); err != nil {
		return nil, false, err
	}
	if err := verifyPublishedOutput(ctx, boundary, prepared.Directory, prepared.TaskBranch, public); err != nil {
		return nil, false, err
	}
	return &work.PullRequestDelivery{PullRequest: work.PullRequestRef{URL: url}, PublicCommit: public.Commit, Head: public.Commit, Tree: public.Tree, ObservedAt: time.Now().UTC(), Evidence: "Origin and open PR head independently verified at the retained public commit, preserving authorized input; reviewed revision and selected input remain separate"}, recovered, nil
}

func packagePublicOperation(ctx context.Context, store WorkStore, boundary FinalizationGit, snapshot *work.State, p *work.PublicationPhase, req gitmeta.PublicCommitRequest) (gitmeta.Revision, error) {
	op := p.Operation
	if op.Public == nil {
		public, err := boundary.PackagePublicCommit(ctx, req)
		if err != nil {
			return gitmeta.Revision{}, err
		}
		op.Public = &work.CodeIdentity{Commit: public.Commit, Tree: public.Tree}
		if err := savePublicationOperation(store, snapshot); err != nil {
			op.Public = nil
			return gitmeta.Revision{}, fmt.Errorf("public commit could not be recorded; continue task run for restore-or-manual-repair recovery: %w", err)
		}
	}
	public := gitmeta.Revision{Commit: op.Public.Commit, Tree: op.Public.Tree}
	if public.Tree != req.Input.Tree {
		return gitmeta.Revision{}, fmt.Errorf("packaging changed authorized tree %s to actual committed tree %s at %s on the owned task branch; nothing was pushed. Continue task run for a new proceed-or-abort decision on the actual committed input", req.Input.Tree, public.Tree, public.Commit)
	}
	return public, nil
}

func reservePublicOperation(ctx context.Context, store WorkStore, boundary FinalizationGit, snapshot *work.State, selected committedPublication) error {
	p := selected.publication
	if p.Operation != nil {
		return nil
	}
	op := &work.PublicationOperation{StartedAt: time.Now().UTC(), Ref: outputRefNamespace(work.AttemptRef{RepositoryID: snapshot.RepositoryID, TaskID: snapshot.TaskID, Workflow: selected.workflowIndex, Phase: selected.phaseIndex}) + "/public", Parent: selected.prepared.Baseline.Commit}
	req := gitmeta.PublicCommitRequest{Directory: selected.prepared.Directory, Branch: selected.prepared.TaskBranch, Parent: op.Parent, Ref: op.Ref, Message: selected.workflow.Publication.CommitMessage(), Input: gitmeta.Revision{Commit: p.Input.Code.Commit, Tree: p.Input.Code.Tree, RetainedRef: p.Input.Code.RetainedRef}}
	if err := boundary.RetainPublicationInput(ctx, req); err != nil {
		return err
	}
	p.Operation = op
	return savePublicationOperation(store, snapshot)
}

func (s FinalizationService) verifyPublicContext(ctx context.Context, boundary FinalizationGit, repo task.Repository, item task.Task, selected committedPublication, diagnostics pullrequest.DiagnosticContext) error {
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

func (s FinalizationService) verifyInitialPublicPR(ctx context.Context, repo task.Repository, prepared *work.WorkContext, p *work.PublicationPhase, remote string, recovering bool, diagnostics pullrequest.DiagnosticContext) error {
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

func (s FinalizationService) verifyPublicationPR(ctx context.Context, url, branch, destination, head string, diagnostics pullrequest.DiagnosticContext) error {
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

func (s FinalizationService) observeOrCreatePublicationPR(ctx context.Context, store WorkStore, snapshot *work.State, repo task.Repository, prepared *work.WorkContext, p *work.PublicationPhase, public gitmeta.Revision, content PullRequestContent, diagnostics pullrequest.DiagnosticContext) (string, bool, error) {
	found, exists, err := s.PRProvider.FindByBranch(ctx, pullrequest.FindByBranchRequest{RepositoryPath: repo.Path, HeadBranch: prepared.TaskBranch, BaseBranch: p.Destination.Branch, Diagnostics: diagnostics})
	if err != nil {
		return "", false, err
	}
	if exists {
		if p.Operation.PRCreationStartedAt == nil {
			return "", false, errors.New("PR exists without this operation's creation intent; reconcile instead of importing")
		}
		return found.URL, true, s.verifyPublicationPR(ctx, found.URL, prepared.TaskBranch, p.Destination.Branch, public.Commit, diagnostics)
	}
	if p.Operation.PRCreationStartedAt == nil {
		at := time.Now().UTC()
		p.Operation.PRCreationStartedAt = &at
		if err := savePublicationOperation(store, snapshot); err != nil {
			return "", false, err
		}
	}

	target := p.Target.(*work.PullRequestTarget)
	created, err := s.PRProvider.Create(ctx, pullrequest.CreateRequest{Draft: target.Draft, RepositoryPath: repo.Path, HeadBranch: prepared.TaskBranch, BaseBranch: p.Destination.Branch, Title: content.Title, Body: content.Body, Diagnostics: diagnostics})
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(created.URL) == "" {
		return "", false, errors.New("PR creation returned no native identity; retry find/import")
	}
	return created.URL, false, s.verifyPublicationPR(ctx, created.URL, prepared.TaskBranch, p.Destination.Branch, public.Commit, diagnostics)
}

func (s FinalizationService) ensurePublicationContent(ctx context.Context, dir string, content PullRequestContent, expected pullrequest.PullRequestStatus, diagnostics pullrequest.DiagnosticContext) error {
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
func (s FinalizationService) publicationPRContent(repo task.Repository, item task.Task, snapshot *work.State, p *work.PublicationPhase) (PullRequestContent, error) {
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
	reviewed, err := w.Output(review.Scope.Output)
	if err != nil {
		return PullRequestContent{}, err
	}
	content.Body += publicationReviewGap(p.Input, reviewed.Commit)
	return content, nil
}
