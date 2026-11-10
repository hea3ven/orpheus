package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/work"
	"path/filepath"
)

const finalizationLockOperation = "task finalization"

// ErrPublicationConsentRequired marks missing exact-plan authorization.
// Review approval is not publication consent.
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
	ObserveDestination(context.Context, string, string) (work.Destination, error)
	ObservePublicationInput(context.Context, gitmeta.PublicationInputRequest) (gitmeta.PublicationInputObservation, error)
	VerifyRevision(context.Context, string, gitmeta.CheckoutIdentity, gitmeta.Revision) error
	ObservePublicHead(context.Context, string, string) (string, error)
	RetainPublicationInput(context.Context, gitmeta.PublicCommitRequest) error
	InspectPublicationCheckout(context.Context, string, string) (gitmeta.Revision, error)
	InspectPublicationRecovery(context.Context, gitmeta.PublicCommitRequest) (gitmeta.PublicationRecovery, error)
	RestorePublicationInput(context.Context, gitmeta.PublicCommitRequest, gitmeta.PublicationRecovery) error
	ReleasePrivatePublicationRefs(context.Context, string, []string) error
	PackagePublicCommit(context.Context, gitmeta.PublicCommitRequest) (gitmeta.Revision, error)
	PushPublicCommit(context.Context, gitmeta.PublicCommitRequest, gitmeta.Revision) error
	PublishedRevision(context.Context, string, string) (gitmeta.Revision, error)
}

type LocalFinalizationGit struct{}

func (LocalFinalizationGit) ObserveDestination(ctx context.Context, dir, branch string) (work.Destination, error) {
	return (LocalReviewCandidateGit{}).ObserveDestination(ctx, dir, branch)
}
func (LocalFinalizationGit) ObservePublicationInput(ctx context.Context, req gitmeta.PublicationInputRequest) (gitmeta.PublicationInputObservation, error) {
	return gitmeta.ObservePublicationInput(ctx, req)
}
func (LocalFinalizationGit) VerifyRevision(ctx context.Context, dir string, identity gitmeta.CheckoutIdentity, revision gitmeta.Revision) error {
	return gitmeta.VerifyRevision(ctx, dir, identity, revision)
}
func (LocalFinalizationGit) ObservePublicHead(ctx context.Context, dir, branch string) (string, error) {
	return gitmeta.ObservePublicHead(ctx, dir, branch)
}
func (LocalFinalizationGit) RetainPublicationInput(ctx context.Context, req gitmeta.PublicCommitRequest) error {
	return gitmeta.RetainPublicationInput(ctx, req)
}
func (LocalFinalizationGit) InspectPublicationCheckout(ctx context.Context, dir, branch string) (gitmeta.Revision, error) {
	return gitmeta.InspectPublicationCheckout(ctx, dir, branch)
}
func (LocalFinalizationGit) InspectPublicationRecovery(ctx context.Context, req gitmeta.PublicCommitRequest) (gitmeta.PublicationRecovery, error) {
	return gitmeta.InspectPublicationRecovery(ctx, req)
}
func (LocalFinalizationGit) RestorePublicationInput(ctx context.Context, req gitmeta.PublicCommitRequest, view gitmeta.PublicationRecovery) error {
	return gitmeta.RestorePublicationInput(ctx, req, view)
}
func (LocalFinalizationGit) ReleasePrivatePublicationRefs(ctx context.Context, dir string, refs []string) error {
	return gitmeta.ReleasePrivatePublicationRefs(ctx, dir, refs)
}
func (LocalFinalizationGit) PackagePublicCommit(ctx context.Context, req gitmeta.PublicCommitRequest) (gitmeta.Revision, error) {
	return gitmeta.PackagePublicCommit(ctx, req)
}
func (LocalFinalizationGit) PushPublicCommit(ctx context.Context, req gitmeta.PublicCommitRequest, public gitmeta.Revision) error {
	return gitmeta.PushPublicCommit(ctx, req, public)
}
func (LocalFinalizationGit) PublishedRevision(ctx context.Context, dir, branch string) (gitmeta.Revision, error) {
	return gitmeta.PublishedRevision(ctx, dir, branch)
}

type FinalizationService struct {
	Paths            state.Paths
	Sources          []task.RepositorySource
	BackendFactory   FinalizationBackendFactory
	Store            WorkStore
	DirectGit        DirectPublicationGit
	ConflictResolver SyncConflictResolver
	Git              FinalizationGit
	PRProvider       pullrequest.Provider
	Logger           *slog.Logger
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
	InputCommit     string
	PRURL           string
	PRRecovered     bool
	WorktreeCleanup *WorktreeCleanupResult
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
	pi, err := publicationOwner(w)
	if err != nil {
		return committedPublication{}, err
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
	prepared := w.Context()
	metadata := item.OrpheusMetadata()
	if !metadata.HasBranch || !metadata.HasWorktree || metadata.Branch != prepared.TaskBranch || metadata.Worktree != prepared.Directory {
		return committedPublication{}, errors.New("task-source execution target differs from the authorized workflow")
	}
	if item.Status != task.StatusInProgress && item.Status != task.StatusClosed {
		return committedPublication{}, fmt.Errorf("task %s is %s, expected in_progress or an observed closure retry", item.ID, item.Status)
	}
	output := &p.Plan.Input.Code
	destination := &p.Plan.Destination
	if destination.Branch == prepared.TaskBranch {
		return committedPublication{}, errors.New("publication destination cannot be the task branch")
	}

	return committedPublication{workflowIndex: wi, phaseIndex: pi, workflow: w, publication: p, prepared: prepared, output: output, destination: destination}, nil
}

func (s FinalizationService) recordCommittedDelivery(ctx context.Context, store WorkStore, boundary FinalizationGit, repo task.Repository, item task.Task, snapshot *work.State, selected committedPublication, result *FinalizationResult) error {
	p := selected.publication
	if p.Delivery != nil {
		return nil
	}
	if item.Status == task.StatusClosed {
		return errors.New("task source is closed without recorded delivery; reconcile before publishing")
	}
	switch p.Plan.Target.(type) {
	case *work.PullRequestTarget:
		if err := s.recordPublicDelivery(ctx, store, boundary, repo, item, snapshot, selected, result); err != nil {
			return err
		}

	case *work.DirectMergeTarget:
		if err := s.recordDirectDelivery(ctx, store, boundary, repo, item, snapshot, selected); err != nil {
			return err
		}
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
		return errors.New("origin branch changed while observing the authorized input")
	}
	return nil
}

// recordPublicationPR mirrors an observed delivery, not permission to start work.
func (s FinalizationService) recordPublicationPR(ctx context.Context, backend FinalizationBackend, item task.Task, url string) (task.Task, error) {
	metadata := item.OrpheusMetadata()
	if metadata.HasPRURL && strings.TrimSpace(metadata.PRURL) != "" && metadata.PRURL != url {
		return task.Task{}, errors.New("task source names a different PR; refusing to overwrite it")
	}
	if !metadata.HasPRURL || metadata.PRURL != url {
		if err := backend.SetPRURL(ctx, item.ID, url); err != nil {
			return task.Task{}, err
		}
		observed, err := backend.Get(ctx, item.ID)
		if err != nil {
			return task.Task{}, err
		}
		if observed.OrpheusMetadata().PRURL != url {
			return task.Task{}, errors.New("task-source PR update was not independently observed")
		}
		return observed, nil
	}

	return item, nil
}
