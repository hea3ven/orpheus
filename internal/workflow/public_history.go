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
	"github.com/hea3ven/orpheus/internal/work"
)

func savePublicationOperation(store WorkStore, snapshot *work.State) error {
	version, err := store.Save(*snapshot, snapshot.Version)
	if err == nil {
		snapshot.Version = version
	}
	return err
}

func (s FinalizationService) recordPublicDelivery(ctx context.Context, store WorkStore, boundary FinalizationGit, repo task.Repository, item task.Task, snapshot *work.State, selected committedPublication, target *work.PullRequestTarget, input gitmeta.Revision, result *FinalizationResult) error {
	if s.PRProvider == nil {
		return errors.New("PR provider is required")
	}
	p, prepared := selected.publication, selected.prepared
	if err := reservePublicOperation(store, snapshot, selected, target); err != nil {
		return err
	}
	op := p.Operation
	req := gitmeta.PublicCommitRequest{Directory: prepared.Directory, Branch: prepared.TaskBranch, Parent: op.Parent, Ref: op.Ref, Message: p.Plan.Content, Input: input}
	diagnostics := pullrequest.DiagnosticContext{RepoID: repo.ID, TaskID: item.ID, Branch: prepared.TaskBranch}
	remote, err := s.verifyPublicContext(ctx, boundary, repo, item, selected, target, diagnostics)
	if err != nil {
		return err
	}
	recovering := op.Public != nil && remote == op.Public.Commit
	public, err := packagePublicOperation(ctx, store, boundary, snapshot, p, req)
	if err != nil {
		return err
	}
	if target.Existing != nil {
		head := target.ExpectedHead
		if recovering {
			head = public.Commit
		}
		if err := s.verifyPublicationPR(ctx, target.Existing.URL, prepared.TaskBranch, p.Plan.Destination.Branch, head, diagnostics); err != nil {
			return err
		}
	}
	pushErr := boundary.PushPublicCommit(ctx, req, public, target.ExpectedHead)
	delivered, recovered, err := s.observePublicDelivery(ctx, boundary, repo, prepared, p, public, diagnostics)
	if err != nil {
		return errors.Join(pushErr, err)
	}
	result.PRRecovered = recovered
	p.Delivery = delivered
	return nil
}

// observePublicDelivery reconciles only an already published commit. It never
// pushes or requires the checkout to still name that operation's selected input.
func (s FinalizationService) observePublicDelivery(ctx context.Context, boundary FinalizationGit, repo task.Repository, prepared *work.WorkContext, p *work.PublicationPhase, public gitmeta.Revision, diagnostics pullrequest.DiagnosticContext) (*work.PullRequestDelivery, bool, error) {
	if s.PRProvider == nil {
		return nil, false, errors.New("PR provider is required")
	}

	target := p.Plan.Target.(*work.PullRequestTarget)
	if public.Tree != p.Plan.Input.Code.Tree {
		return nil, false, errors.New("published operation does not preserve its authorized input tree")
	}
	if err := verifyPublishedOutput(ctx, boundary, prepared.Directory, prepared.TaskBranch, public); err != nil {
		return nil, false, err
	}
	url, recovered, err := s.observeOrCreatePublicationPR(ctx, repo, prepared, p, target, public, diagnostics)
	if err != nil {
		return nil, false, err
	}
	if err := s.ensurePublicationContent(ctx, repo.Path, p.Plan.Content, pullrequest.PullRequestStatus{URL: url, State: pullrequest.StateOpen, HeadBranch: prepared.TaskBranch, BaseBranch: p.Plan.Destination.Branch, HeadCommit: public.Commit}, diagnostics); err != nil {
		return nil, false, err
	}
	if err := s.verifyPublicationPR(ctx, url, prepared.TaskBranch, p.Plan.Destination.Branch, public.Commit, diagnostics); err != nil {
		return nil, false, err
	}
	if err := verifyPublishedOutput(ctx, boundary, prepared.Directory, prepared.TaskBranch, public); err != nil {
		return nil, false, err
	}
	return &work.PullRequestDelivery{PullRequest: work.PullRequestRef{URL: url}, PublicCommit: public.Commit, Head: public.Commit, Tree: public.Tree, PreviousHead: target.ExpectedHead, ObservedAt: time.Now().UTC(), Evidence: "Origin and open PR head independently verified at the retained public commit, preserving authorized input; reviewed revision and selected input remain separate"}, recovered, nil
}

// reconcilePublicOperation finishes observation of an authorized remote effect
// before its phase becomes history. A newly selected checkout is not pushed or
// substituted for the earlier operation's input, and cannot inherit its consent.
func (s FinalizationService) reconcilePublicOperation(ctx context.Context, boundary FinalizationGit, repo task.Repository, item task.Task, w *work.Workflow, p *work.PublicationPhase) (bool, error) {
	if p.Operation == nil || p.Operation.Public == nil || p.Delivery != nil {
		return false, nil
	}
	if item.Status != task.StatusInProgress {
		return false, errors.New("task source is not in progress; reconcile lifecycle before publication")
	}

	target, ok := p.Plan.Target.(*work.PullRequestTarget)
	if !ok {
		return false, nil
	}
	prepared := w.Context()
	remote, err := boundary.ObservePublicHead(ctx, prepared.Directory, prepared.TaskBranch)
	if err != nil {
		return false, err
	}
	if remote == target.ExpectedHead {
		return false, nil
	}
	public := p.Operation.Public
	if remote != public.Commit {
		return false, errors.New("unexpected public head; reconcile remote changes before replacing publication intent")
	}
	diagnostics := pullrequest.DiagnosticContext{RepoID: repo.ID, TaskID: item.ID, Branch: prepared.TaskBranch}
	delivered, _, err := s.observePublicDelivery(ctx, boundary, repo, prepared, p, gitmeta.Revision{Commit: public.Commit, Tree: public.Tree, RetainedRef: public.RetainedRef}, diagnostics)
	if err != nil {
		return false, err
	}
	p.Delivery = delivered
	return true, nil
}

func packagePublicOperation(ctx context.Context, store WorkStore, boundary FinalizationGit, snapshot *work.State, p *work.PublicationPhase, req gitmeta.PublicCommitRequest) (gitmeta.Revision, error) {
	op := p.Operation
	if op.Public == nil {
		public, err := boundary.PackagePublicCommit(ctx, req)
		if err != nil {
			return gitmeta.Revision{}, err
		}
		op.Public = &work.CodeOutput{CodeIdentity: work.CodeIdentity{Commit: public.Commit, Tree: public.Tree}, RetainedRef: public.RetainedRef}
		if err := savePublicationOperation(store, snapshot); err != nil {
			return gitmeta.Revision{}, fmt.Errorf("public commit retained; retry unchanged operation to reconcile: %w", err)
		}
	}
	public := gitmeta.Revision{Commit: op.Public.Commit, Tree: op.Public.Tree, RetainedRef: op.Public.RetainedRef}
	if public.Tree != req.Input.Tree {
		return gitmeta.Revision{}, fmt.Errorf("packaging changed authorized tree %s to actual committed tree %s at %s (retained by %s); nothing was pushed. Select that committed input on the owned task branch and continue task run for a new proceed-or-abort decision", req.Input.Tree, public.Tree, public.Commit, public.RetainedRef)
	}
	return public, nil
}

func reservePublicOperation(store WorkStore, snapshot *work.State, selected committedPublication, target *work.PullRequestTarget) error {
	p, prepared := selected.publication, selected.prepared
	if p.Operation != nil {
		return nil
	}
	parent := prepared.Baseline.Commit
	if target.Existing != nil {
		parent = target.ExpectedHead
	}
	p.Operation = &work.PublicationOperation{StartedAt: time.Now().UTC(), Ref: outputRefNamespace(work.AttemptRef{RepositoryID: snapshot.RepositoryID, TaskID: snapshot.TaskID, Workflow: selected.workflowIndex, Phase: selected.phaseIndex}) + "/public", Parent: parent}
	return savePublicationOperation(store, snapshot)
}

func (s FinalizationService) verifyPublicContext(ctx context.Context, boundary FinalizationGit, repo task.Repository, item task.Task, selected committedPublication, target *work.PullRequestTarget, diagnostics pullrequest.DiagnosticContext) (string, error) {
	p, prepared := selected.publication, selected.prepared
	remote, err := boundary.ObservePublicHead(ctx, prepared.Directory, prepared.TaskBranch)
	if err != nil {
		return "", err
	}
	recovering := p.Operation.Public != nil && remote == p.Operation.Public.Commit
	if remote != target.ExpectedHead && !recovering {
		return "", errors.New("unexpected public head; reconcile remote changes before publication")
	}
	if target.Existing != nil {
		if err := s.verifyPublicationPR(ctx, target.Existing.URL, prepared.TaskBranch, p.Plan.Destination.Branch, remote, diagnostics); err != nil {
			return "", err
		}
	} else if err := s.verifyInitialPublicPR(ctx, repo, prepared, p, remote, recovering, diagnostics); err != nil {
		return "", err
	}
	metadata := item.OrpheusMetadata()
	if metadata.HasPRURL && metadata.PRURL != "" && (target.Existing == nil || target.Existing.URL != metadata.PRURL) && !recovering {
		return "", errors.New("task source names an unrelated PR; reconcile before publication")
	}
	return remote, nil
}

func (s FinalizationService) verifyInitialPublicPR(ctx context.Context, repo task.Repository, prepared *work.WorkContext, p *work.PublicationPhase, remote string, recovering bool, diagnostics pullrequest.DiagnosticContext) error {
	found, exists, err := s.PRProvider.FindByBranch(ctx, pullrequest.FindByBranchRequest{RepositoryPath: repo.Path, HeadBranch: prepared.TaskBranch, BaseBranch: p.Plan.Destination.Branch, Diagnostics: diagnostics})
	if err != nil {
		return err
	}
	if !exists {
		return nil
	}
	if !recovering {
		return errors.New("existing PR requires reconciliation; initial publication cannot import unrelated remote work")
	}
	return s.verifyPublicationPR(ctx, found.URL, prepared.TaskBranch, p.Plan.Destination.Branch, remote, diagnostics)
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
	if status.HeadBranch != expected.HeadBranch || status.BaseBranch != expected.BaseBranch || (expected.HeadCommit != "" && status.HeadCommit != expected.HeadCommit) {
		return errors.New("publication PR destination, branch or public head changed; reconcile before delivery")
	}
	return nil
}

func (s FinalizationService) observeOrCreatePublicationPR(ctx context.Context, repo task.Repository, prepared *work.WorkContext, p *work.PublicationPhase, target *work.PullRequestTarget, public gitmeta.Revision, diagnostics pullrequest.DiagnosticContext) (string, bool, error) {
	if target.Existing != nil {
		return target.Existing.URL, false, s.verifyPublicationPR(ctx, target.Existing.URL, prepared.TaskBranch, p.Plan.Destination.Branch, public.Commit, diagnostics)
	}
	found, exists, err := s.PRProvider.FindByBranch(ctx, pullrequest.FindByBranchRequest{RepositoryPath: repo.Path, HeadBranch: prepared.TaskBranch, BaseBranch: p.Plan.Destination.Branch, Diagnostics: diagnostics})
	if err != nil {
		return "", false, err
	}
	if exists {
		return found.URL, true, s.verifyPublicationPR(ctx, found.URL, prepared.TaskBranch, p.Plan.Destination.Branch, public.Commit, diagnostics)
	}
	title, body, ok := strings.Cut(p.Plan.Content, "\n\n")
	if !ok || strings.TrimSpace(title) == "" || strings.TrimSpace(body) == "" {
		return "", false, errors.New("publication plan lacks exact title and body")
	}
	created, err := s.PRProvider.Create(ctx, pullrequest.CreateRequest{RepositoryPath: repo.Path, HeadBranch: prepared.TaskBranch, BaseBranch: p.Plan.Destination.Branch, Title: title, Body: body, Diagnostics: diagnostics})
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(created.URL) == "" {
		return "", false, errors.New("PR creation returned no native identity; retry find/import")
	}
	return created.URL, false, s.verifyPublicationPR(ctx, created.URL, prepared.TaskBranch, p.Plan.Destination.Branch, public.Commit, diagnostics)
}

// priorPublicTarget follows retained delivered occurrences, including verified
// sync pushes, rather than mutable task-source PR metadata or a task-wide flag.
func priorPublicTarget(w *work.Workflow) *work.PullRequestTarget {
	for i := len(w.Phases) - 1; i >= 0; i-- {
		switch p := w.Phases[i].(type) {
		case *work.ExternalReviewPhase:
			publication := w.Phases[p.Publication.Phase].(*work.PublicationPhase)
			delivery := publication.Delivery.(*work.PullRequestDelivery)
			head := delivery.Head
			for _, sync := range p.BranchSyncs {
				if sync.Push != nil {
					head = sync.Push.Head
				}
			}
			pr := delivery.PullRequest
			return &work.PullRequestTarget{Existing: &pr, ExpectedHead: head}
		case *work.PublicationPhase:
			if delivery, ok := p.Delivery.(*work.PullRequestDelivery); ok {
				pr := delivery.PullRequest
				return &work.PullRequestTarget{Existing: &pr, ExpectedHead: delivery.Head}
			}
		}
	}
	return &work.PullRequestTarget{}
}

func (s FinalizationService) ensurePublicationContent(ctx context.Context, dir, content string, expected pullrequest.PullRequestStatus, diagnostics pullrequest.DiagnosticContext) error {
	title, body, ok := strings.Cut(content, "\n\n")
	if !ok {
		return errors.New("publication plan lacks title and body")
	}
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
	updateErr := s.PRProvider.Update(ctx, pullrequest.UpdateRequest{RepositoryPath: dir, URL: expected.URL, Title: title, Body: body, Diagnostics: diagnostics})
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
