package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workstate"
	"path/filepath"
)

const finalizationLockOperation = "task finalization"

// ErrPublicationConsentRequired is a deliberate boundary until explicit
// publication authorization is available. Review approval is not consent.
var ErrPublicationConsentRequired = errors.New("publication awaits explicit exact-plan authorization; review approval and retained commits do not grant consent")

type FinalizationBackend interface {
	task.Getter
	task.Lister
	task.PRURLMutator
	task.CloseMutator
}
type FinalizationBackendFactory func(task.RepositorySource) (FinalizationBackend, error)

// FinalizationGit consumes committed input. There is deliberately no staging or
// implementation-commit operation at the publication boundary.
type FinalizationGit interface {
	VerifyRevision(context.Context, string, gitmeta.CheckoutIdentity, gitmeta.Revision) error
	PushTaskBranch(context.Context, string, string) error
	PublishedRevision(context.Context, string, string) (gitmeta.Revision, error)
	DirectRevisionDelivery(context.Context, task.Repository, string, string, string, string, string, gitmeta.Revision) (gitmeta.Revision, error)
}

type LocalFinalizationGit struct{}

func (LocalFinalizationGit) VerifyRevision(ctx context.Context, dir string, identity gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	return gitmeta.VerifyRevision(ctx, dir, identity, revision)
}
func (LocalFinalizationGit) PushTaskBranch(ctx context.Context, dir, branch string) error {
	return gitmeta.PushTaskBranch(ctx, dir, branch)
}
func (LocalFinalizationGit) PublishedRevision(ctx context.Context, dir, branch string) (gitmeta.Revision, error) {
	return gitmeta.PublishedRevision(ctx, dir, branch)
}
func (LocalFinalizationGit) DirectRevisionDelivery(ctx context.Context, repo task.Repository, dir, branch, destination, commit, retention string, output gitmeta.Revision) (gitmeta.Revision, error) {
	return gitmeta.DirectRevisionDelivery(ctx, repo, dir, branch, destination, commit, retention, output)
}

type FinalizationService struct {
	Paths          state.Paths
	Sources        []task.RepositorySource
	BackendFactory FinalizationBackendFactory
	Store          WorkStore
	Git            FinalizationGit
	CleanupGit     ClosedTaskWorktreeGit
	PRProvider     pullrequest.Provider
	Logger         *slog.Logger
}

type FinalizeOptions struct {
	TaskID string
	CWD    string
}

type FinalizationResult struct {
	Repository      task.Repository
	Task            task.Task
	Finalization    taskstate.Finalization
	Branch          string
	PRURL           string
	PRRecovered     bool
	WorktreeCleanup *WorktreeCleanupResult
}

// Finalize delivers only an existing, explicitly authorized immutable plan.
// There is no implicit review/capture/authorization path here.
func (s FinalizationService) Finalize(ctx context.Context, opts FinalizeOptions) (FinalizationResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.BackendFactory == nil {
		return FinalizationResult{}, errors.New("task finalization backend factory is required")
	}

	store := s.Store
	if store == nil {
		store = workstate.NewStore(s.Paths)
	}
	boundary := s.Git
	if boundary == nil {
		boundary = LocalFinalizationGit{}
	}
	var result FinalizationResult
	err := state.WithGlobalMutationLockLogger(ctx, s.Paths, finalizationLockOperation, s.Logger, func() error {
		source, item, backend, err := s.resolvePublicationTask(ctx, opts, store)
		if err != nil {
			return err
		}
		snapshot, err := store.Load(source.Repository.ID, item.ID)
		if err != nil {
			return err
		}
		result, err = s.publishCommitted(ctx, store, boundary, source, item, backend, &snapshot)
		return err
	})
	return result, err
}

func (s FinalizationService) resolvePublicationTask(ctx context.Context, opts FinalizeOptions, store WorkStore) (task.RepositorySource, task.Task, FinalizationBackend, error) {
	if strings.TrimSpace(opts.TaskID) != "" {
		resolved, err := task.ResolveTaskSource(s.Sources, opts.TaskID)
		if err != nil {
			return task.RepositorySource{}, task.Task{}, nil, err
		}
		backend, err := s.BackendFactory(resolved.Source)
		if err != nil {
			return task.RepositorySource{}, task.Task{}, nil, err
		}
		item, err := backend.Get(ctx, resolved.TaskID)
		if err == nil && item.ID != resolved.TaskID {
			return task.RepositorySource{}, task.Task{}, nil, errors.New("task source returned a different task identity")
		}
		return resolved.Source, item, backend, err
	}
	cwd, err := currentDirectory(opts.CWD)
	if err != nil {
		return task.RepositorySource{}, task.Task{}, nil, err
	}
	var sources []task.RepositorySource
	var items []task.Task
	var backends []FinalizationBackend
	for _, source := range s.Sources {
		backend, err := s.BackendFactory(source)
		if err != nil {
			return task.RepositorySource{}, task.Task{}, nil, err
		}
		candidates, err := backend.List(ctx)
		if err != nil {
			return task.RepositorySource{}, task.Task{}, nil, err
		}
		for _, item := range candidates {
			snapshot, err := store.Load(source.Repository.ID, item.ID)
			if err != nil {
				return task.RepositorySource{}, task.Task{}, nil, err
			}
			inside, err := publicationDirectoryContains(snapshot, cwd)
			if err != nil {
				return task.RepositorySource{}, task.Task{}, nil, err
			}

			if inside {
				sources = append(sources, source)
				items = append(items, item)
				backends = append(backends, backend)
			}
		}
	}
	if len(items) != 1 {
		return task.RepositorySource{}, task.Task{}, nil, fmt.Errorf("cannot infer one workflow-owned publication from this directory; specify a task ID (%d candidates)", len(items))
	}
	return sources[0], items[0], backends[0], nil
}

func publicationDirectoryContains(snapshot work.State, cwd string) (bool, error) {
	if len(snapshot.Workflows) == 0 {
		return false, nil
	}
	prepared := snapshot.Workflows[len(snapshot.Workflows)-1].Context()
	if prepared == nil {
		return false, nil
	}
	relative, err := filepath.Rel(prepared.Directory, cwd)
	if err != nil {
		return false, err
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) && !filepath.IsAbs(relative)), nil
}

type committedPublication struct {
	workflowIndex, phaseIndex int
	workflow                  *work.Workflow
	publication               *work.PublicationPhase
	prepared                  *work.WorkContext
	output                    *work.CodeOutput
	destination               *work.Destination
}

func selectCommittedPublication(snapshot *work.State, item task.Task) (committedPublication, error) {
	if err := snapshot.Validate(); err != nil {
		return committedPublication{}, err
	}
	if len(snapshot.Workflows) == 0 {
		return committedPublication{}, ErrPublicationConsentRequired
	}
	wi := len(snapshot.Workflows) - 1
	w := &snapshot.Workflows[wi]
	pi := -1
	for i := len(w.Phases) - 1; i >= 0; i-- {
		if _, ok := w.Phases[i].(*work.PublicationPhase); ok {
			pi = i
			break
		}
	}
	if pi < 0 {
		return committedPublication{}, ErrPublicationConsentRequired
	}
	p := w.Phases[pi].(*work.PublicationPhase)
	if p.Consent == nil {
		return committedPublication{}, ErrPublicationConsentRequired
	}
	review := w.Phases[p.Plan.Review.Phase].(*work.ReviewPhase)
	latest, exists := w.LatestOutput()
	if !exists || latest != review.Scope.Output {
		return committedPublication{}, errors.New("newer local output requires its own review and publication authorization")
	}
	switch w.LastPhase().(type) {
	case *work.PublicationPhase, *work.ExternalReviewPhase, *work.FinalizationPhase:
	default:
		return committedPublication{}, errors.New("active local work prevents publication")
	}
	prepared := w.Context()
	metadata := item.OrpheusMetadata()
	if !metadata.HasBranch || !metadata.HasWorktree || metadata.Branch != prepared.TaskBranch || metadata.Worktree != prepared.Directory {
		return committedPublication{}, errors.New("task-source execution target differs from the authorized workflow")
	}
	if item.Status != task.StatusInProgress && item.Status != task.StatusClosed {
		return committedPublication{}, fmt.Errorf("task %s is %s, expected in_progress or an observed closure retry", item.ID, item.Status)
	}
	output, err := w.Output(review.Scope.Output)
	if err != nil {
		return committedPublication{}, err
	}
	destination, err := w.ReviewDestination(p.Plan.Review)
	if err != nil {
		return committedPublication{}, err
	}
	if destination.Branch == prepared.TaskBranch {
		return committedPublication{}, errors.New("publication destination cannot be the task branch")
	}

	return committedPublication{workflowIndex: wi, phaseIndex: pi, workflow: w, publication: p, prepared: prepared, output: output, destination: destination}, nil
}

func (s FinalizationService) publishCommitted(ctx context.Context, store WorkStore, boundary FinalizationGit, source task.RepositorySource, item task.Task, backend FinalizationBackend, snapshot *work.State) (FinalizationResult, error) {
	selected, err := selectCommittedPublication(snapshot, item)
	if err != nil {
		return FinalizationResult{}, err
	}
	result := FinalizationResult{Repository: source.Repository, Task: item, Branch: selected.prepared.TaskBranch}
	if err := s.recordCommittedDelivery(ctx, store, boundary, source.Repository, item, snapshot, selected, &result); err != nil {
		return result, err
	}
	if err := s.reconcileCommittedDelivery(ctx, store, source.Repository, item, backend, snapshot, selected, &result); err != nil {
		return result, err
	}
	view, err := taskstate.Project(*snapshot)
	if err != nil {
		return result, err
	}
	result.Finalization = taskstate.FinalizationFacts(view)
	return result, nil
}

func (s FinalizationService) recordCommittedDelivery(ctx context.Context, store WorkStore, boundary FinalizationGit, repo task.Repository, item task.Task, snapshot *work.State, selected committedPublication, result *FinalizationResult) error {
	p, prepared, output, destination := selected.publication, selected.prepared, selected.output, selected.destination
	if p.Delivery != nil {
		return nil
	}
	if item.Status == task.StatusClosed {
		return errors.New("task source is closed without recorded delivery; reconcile before publishing")
	}
	revision := gitmeta.Revision{Commit: output.Commit, Tree: output.Tree, RetainedRef: output.RetainedRef}
	switch target := p.Plan.Target.(type) {
	case *work.PullRequestTarget:
		if err := boundary.VerifyRevision(ctx, prepared.Directory, gitmeta.CheckoutIdentity{Commit: output.Commit, Ref: "refs/heads/" + prepared.TaskBranch}, revision); err != nil {
			return err
		}
		url, recovered, err := s.deliverPullRequest(ctx, boundary, repo, item, prepared, destination, target, p.Plan.Content, revision)
		if err != nil {
			return err
		}
		result.PRRecovered = recovered
		p.Delivery = &work.PullRequestDelivery{PullRequest: work.PullRequestRef{URL: url}, PublicCommit: output.Commit, Head: output.Commit, Tree: output.Tree, ObservedAt: time.Now().UTC(), Evidence: "Origin branch independently verified at the reviewed commit and tree; native PR observed for the exact branch pair"}
	case *work.DirectMergeTarget:
		retention := outputRefNamespace(work.AttemptRef{RepositoryID: snapshot.RepositoryID, TaskID: snapshot.TaskID, Workflow: selected.workflowIndex, Phase: selected.phaseIndex}) + "/delivery"
		delivered, err := boundary.DirectRevisionDelivery(ctx, repo, prepared.Directory, prepared.TaskBranch, destination.Branch, destination.Commit, retention, revision)
		if err != nil {
			return err
		}
		if delivered.Commit == "" || delivered.Tree != output.Tree {
			return errors.New("observed destination does not preserve reviewed code")
		}
		p.Delivery = &work.DirectDelivery{PublicCommit: output.Commit, Destination: work.CodeIdentity{Commit: delivered.Commit, Tree: delivered.Tree}, ObservedAt: time.Now().UTC(), Evidence: "Independently observed exact destination push preserving the reviewed tree"}
	default:
		return errors.New("unsupported publication target")
	}
	version, err := store.Save(*snapshot, snapshot.Version)
	if err != nil {
		return fmt.Errorf("delivery observed but could not be recorded; retry unchanged intent: %w", err)
	}
	snapshot.Version = version
	return nil
}

func (s FinalizationService) reconcileCommittedDelivery(ctx context.Context, store WorkStore, repo task.Repository, item task.Task, backend FinalizationBackend, snapshot *work.State, selected committedPublication, result *FinalizationResult) error {
	p, w, prepared := selected.publication, selected.workflow, selected.prepared
	metadata := item.OrpheusMetadata()
	switch delivery := p.Delivery.(type) {
	case *work.PullRequestDelivery:
		result.PRURL = delivery.PullRequest.URL
		if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" && metadata.PRURL != result.PRURL {
			return errors.New("task source names a different PR; refusing to overwrite it")
		}
		if !metadata.HasPRURL || metadata.PRURL != result.PRURL {
			if err := backend.SetPRURL(ctx, item.ID, result.PRURL); err != nil {
				return err
			}
			observed, err := backend.Get(ctx, item.ID)
			if err != nil {
				return err
			}
			if observed.OrpheusMetadata().PRURL != result.PRURL {
				return errors.New("task-source PR update was not independently observed")
			}
			result.Task = observed
		}
		if _, ok := w.LastPhase().(*work.PublicationPhase); ok {
			w.Phases = append(w.Phases, &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: selected.phaseIndex}})
			version, err := store.Save(*snapshot, snapshot.Version)
			if err != nil {
				return err
			}
			snapshot.Version = version
		}
	case *work.DirectDelivery:
		if err := s.closeDeliveredTask(ctx, store, snapshot, w, selected.phaseIndex, backend, item); err != nil {
			return err
		}
		observed, err := backend.Get(ctx, item.ID)
		if err != nil {
			return err
		}
		result.Task = observed
		result.WorktreeCleanup = s.cleanupDeliveredWorktree(ctx, store, repo, observed, backend, prepared.Directory)
	}
	return nil
}

func (s FinalizationService) deliverPullRequest(ctx context.Context, boundary FinalizationGit, repo task.Repository, item task.Task, prepared *work.WorkContext, destination *work.Destination, target *work.PullRequestTarget, content string, output gitmeta.Revision) (string, bool, error) {
	if s.PRProvider == nil {
		return "", false, errors.New("PR provider is required")
	}
	if target.Existing != nil {
		return "", false, errors.New("public update packaging is not implemented; existing PR intent cannot be treated as create consent")
	}
	pushErr := boundary.PushTaskBranch(ctx, prepared.Directory, prepared.TaskBranch)
	observed, err := boundary.PublishedRevision(ctx, prepared.Directory, prepared.TaskBranch)
	if err != nil {
		return "", false, errors.Join(pushErr, err)
	}
	if observed.Commit != output.Commit || observed.Tree != output.Tree {
		return "", false, errors.Join(pushErr, errors.New("origin branch differs from the reviewed output"))
	}
	diagnostics := pullrequest.DiagnosticContext{RepoID: repo.ID, TaskID: item.ID, Branch: prepared.TaskBranch}
	found, exists, err := s.PRProvider.FindOpenByBranch(ctx, pullrequest.FindOpenByBranchRequest{RepositoryPath: repo.Path, HeadBranch: prepared.TaskBranch, BaseBranch: destination.Branch, Diagnostics: diagnostics})
	if err != nil {
		return "", false, err
	}
	if exists {
		if strings.TrimSpace(found.URL) == "" {
			return "", false, errors.New("PR provider reported a match without native identity")
		}
		if err := verifyPublishedOutput(ctx, boundary, prepared.Directory, prepared.TaskBranch, output); err != nil {
			return "", false, err
		}
		return found.URL, true, nil
	}
	title, body, ok := strings.Cut(content, "\n\n")
	if !ok || strings.TrimSpace(title) == "" || strings.TrimSpace(body) == "" {
		return "", false, errors.New("publication plan lacks exact title and body")
	}
	created, err := s.PRProvider.Create(ctx, pullrequest.CreateRequest{RepositoryPath: repo.Path, HeadBranch: prepared.TaskBranch, BaseBranch: destination.Branch, Title: title, Body: body, Diagnostics: diagnostics})
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(created.URL) == "" {
		return "", false, errors.New("PR creation returned no native identity; retry find/import")
	}
	status, err := s.PRProvider.StatusByURL(ctx, pullrequest.StatusByURLRequest{URL: created.URL, Diagnostics: diagnostics})
	if err != nil {
		return "", false, err
	}
	if status.URL != created.URL || status.State != pullrequest.StateOpen {
		return "", false, errors.New("created PR was not independently observed open")
	}
	if err := verifyPublishedOutput(ctx, boundary, prepared.Directory, prepared.TaskBranch, output); err != nil {
		return "", false, err
	}
	return created.URL, false, nil
}

func (s FinalizationService) closeDeliveredTask(ctx context.Context, store WorkStore, snapshot *work.State, w *work.Workflow, pi int, backend FinalizationBackend, item task.Task) error {
	final, ok := w.LastPhase().(*work.FinalizationPhase)
	if !ok {
		final = &work.FinalizationPhase{Acceptance: &work.DirectAcceptance{Publication: work.PublicationRef{Phase: pi}}}
		w.Phases = append(w.Phases, final)
		version, err := store.Save(*snapshot, snapshot.Version)
		if err != nil {
			return err
		}
		snapshot.Version = version
	}
	if final.Closure != nil {
		return nil
	}
	if item.Status != task.StatusClosed {
		if err := backend.Close(ctx, item.ID); err != nil {
			return err
		}
	}
	observed, err := backend.Get(ctx, item.ID)
	if err != nil {
		return err
	}
	if observed.Status != task.StatusClosed {
		return errors.New("task-source closure was not independently observed")
	}
	final.Closure = &work.TaskClosure{At: time.Now().UTC(), Reason: "Reviewed direct delivery completed", Evidence: "Task source independently reports closed after observed destination delivery"}
	version, err := store.Save(*snapshot, snapshot.Version)
	if err != nil {
		return err
	}
	snapshot.Version = version
	return nil
}

func isFeatureBranchTarget(kind tasktarget.TargetKind) bool {
	return kind == tasktarget.TargetWorktreeTeam || kind == tasktarget.TargetRepoRootTeam
}

func integrationDestination(repo task.Repository, finalization taskstate.Finalization) (string, error) {
	destination := strings.TrimSpace(finalization.DestinationBranch)
	if destination == "" {
		destination = strings.TrimSpace(repo.DefaultBranch)
	}
	if destination == "" {
		return "", fmt.Errorf("repo %q has no registered default branch or selected integration destination", repo.ID)
	}
	return destination, nil
}

func currentDirectory(cwd string) (string, error) {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolve current directory: %w", err)
		}
	}
	return cleanAbsPath("current directory", cwd)
}

func verifyPublishedOutput(ctx context.Context, boundary FinalizationGit, dir, branch string, output gitmeta.Revision) error {
	actual, err := boundary.PublishedRevision(ctx, dir, branch)
	if err != nil {
		return err
	}
	if actual.Commit != output.Commit || actual.Tree != output.Tree {
		return errors.New("origin branch changed while observing the reviewed publication")
	}
	return nil
}
