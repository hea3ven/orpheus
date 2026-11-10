package workflow

import (
	"context"
	"errors"
	"fmt"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/work"
)

// DirectPublicationGit provides local synchronization and split integration
// effects. It deliberately has no PR or task-branch delivery operation.
type DirectPublicationGit interface {
	InspectDirectIntegrationCheckpoints(context.Context, gitmeta.DirectIntegrationRequest) (gitmeta.DirectIntegrationCheckpoints, error)
	DescribePublicChanges(context.Context, string, string, string) (string, error)
	ResumeDirectTaskCheckout(context.Context, string, string) error
	BeginLocalTaskBranchSync(context.Context, gitmeta.TaskBranchSyncOptions, string) (gitmeta.TaskBranchSyncResult, error)
	CommitTaskBranchConflictResolution(context.Context, gitmeta.TaskBranchSyncOptions, []string) (gitmeta.TaskBranchSyncResult, error)
	PrepareDirectIntegration(context.Context, gitmeta.DirectIntegrationRequest) (gitmeta.Revision, error)
	PushDirectIntegration(context.Context, gitmeta.DirectIntegrationRequest, gitmeta.Revision) (gitmeta.Revision, error)
}

func (LocalFinalizationGit) BeginLocalTaskBranchSync(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, destination string) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.BeginLocalTaskBranchSync(ctx, opts, destination)
}
func (LocalFinalizationGit) CommitTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, files []string) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.CommitTaskBranchConflictResolution(ctx, opts, files)
}
func (LocalFinalizationGit) PrepareDirectIntegration(ctx context.Context, req gitmeta.DirectIntegrationRequest) (gitmeta.Revision, error) {
	return gitmeta.PrepareDirectIntegration(ctx, req)
}
func (LocalFinalizationGit) PushDirectIntegration(ctx context.Context, req gitmeta.DirectIntegrationRequest, result gitmeta.Revision) (gitmeta.Revision, error) {
	return gitmeta.PushDirectIntegration(ctx, req, result)
}

func (s FinalizationService) directBoundary(boundary FinalizationGit) DirectPublicationGit {
	if s.DirectGit != nil {
		return s.DirectGit
	}
	if direct, ok := boundary.(DirectPublicationGit); ok {
		return direct
	}
	return LocalFinalizationGit{}
}

func directSyncOptions(repo task.Repository, selected committedPublication) gitmeta.TaskBranchSyncOptions {
	return gitmeta.TaskBranchSyncOptions{RepoPath: repo.Path, Worktree: selected.prepared.Directory, Branch: selected.prepared.TaskBranch, DefaultBranch: selected.destination.Branch}
}

// directResolverPending escapes mutation protection before attached agent work.
type directResolverPending struct {
	repository task.Repository
	item       task.Task
	owner      work.AttemptRef
}

func (directResolverPending) Error() string { return "direct publication conflict resolver is ready" }

func (s FinalizationService) recordDirectDelivery(ctx context.Context, store WorkStore, boundary FinalizationGit, repo task.Repository, item task.Task, snapshot *work.State, selected committedPublication) error {
	p, w := selected.publication, selected.workflow
	direct := s.directBoundary(boundary)
	prepared := w.PublicPreparation(p)
	if err := s.prepareDirectPublicHistory(ctx, store, boundary, snapshot, selected); err != nil {
		return err
	}
	if err := s.beginDirectSync(ctx, store, boundary, repo, item, snapshot, selected); err != nil {
		return err
	}
	sync := w.PublicSync(p)
	if sync.Result == nil {
		return errors.New("direct sync requires explicit operator recovery; continue task run")
	}
	if sync.Result.Commit != prepared.Operation.Public.Commit && p.Input.Code.CodeIdentity != *sync.Result {
		return s.renewDirectPublication(ctx, store, boundary, snapshot, selected, *sync.Result, sync.Destination, "Local destination synchronization; result has not been separately task reviewed", true)
	}
	if p.Integration == nil {
		p.Integration = &work.DirectIntegration{StartedAt: time.Now().UTC(), Source: *sync.Result, Destination: sync.Destination}
		if err := savePublicationOperation(store, snapshot); err != nil {
			return err
		}
		merged, err := direct.PrepareDirectIntegration(ctx, directIntegrationRequest(repo, selected))
		if err != nil {
			return err
		}
		p.Integration.Result = &work.CodeIdentity{Commit: merged.Commit, Tree: merged.Tree}
		if err := savePublicationOperation(store, snapshot); err != nil {
			return fmt.Errorf("integration commit was not recorded; continue task run for explicit recovery: %w", err)
		}
	}
	if p.Integration.Result == nil {
		return errors.New("unrecorded integration needs explicit operator restoration or manual repair")
	}
	result := gitmeta.Revision{Commit: p.Integration.Result.Commit, Tree: p.Integration.Result.Tree}
	delivered, err := direct.PushDirectIntegration(ctx, directIntegrationRequest(repo, selected), result)
	if err != nil {
		return err
	}
	if delivered.Commit != result.Commit || delivered.Tree != result.Tree {
		return errors.New("observed destination differs from recorded integration")
	}
	p.Delivery = &work.DirectDelivery{PublicCommit: prepared.Operation.Public.Commit, Destination: work.CodeIdentity{Commit: delivered.Commit, Tree: delivered.Tree}, ObservedAt: time.Now().UTC(), Evidence: "Exact destination integration independently observed; synchronized public source is recorded separately and was not separately task reviewed"}
	return nil
}

func directIntegrationRequest(repo task.Repository, selected committedPublication) gitmeta.DirectIntegrationRequest {
	integration := selected.publication.Integration
	return gitmeta.DirectIntegrationRequest{Repository: repo, Directory: selected.prepared.Directory, Branch: selected.prepared.TaskBranch, Destination: integration.Destination.Branch, DestinationCommit: integration.Destination.Commit, Source: gitmeta.Revision{Commit: integration.Source.Commit, Tree: integration.Source.Tree}}
}

func recordDirectSyncResult(ctx context.Context, store WorkStore, boundary FinalizationGit, snapshot *work.State, selected committedPublication) error {
	current, err := boundary.InspectPublicationCheckout(ctx, selected.prepared.Directory, selected.prepared.TaskBranch)
	if err != nil {
		return err
	}
	selected.publication.DirectSync.Result = &work.CodeIdentity{Commit: current.Commit, Tree: current.Tree}
	return savePublicationOperation(store, snapshot)
}

func (s FinalizationService) renewDirectPublication(ctx context.Context, store WorkStore, boundary FinalizationGit, snapshot *work.State, selected committedPublication, input work.CodeIdentity, destination work.Destination, disclosure string, keepSync bool) error {
	p := selected.publication
	plan := publicationContinuation(p)
	provenance := work.InputOperator
	if keepSync {
		provenance = work.InputSynchronized
	}
	plan.Input = work.PublicationInput{Code: work.CodeOutput{CodeIdentity: input, RetainedRef: "refs/heads/" + selected.prepared.TaskBranch}, Provenance: provenance}
	prepared := selected.workflow.PublicPreparation(p)
	changes := prepared.Comparison.Changes + prepared.Operation.InputChanges
	if input.Commit != prepared.Operation.Public.Commit {
		diff, err := s.directBoundary(boundary).DescribePublicChanges(ctx, selected.prepared.Directory, prepared.Operation.Public.Commit, input.Commit)
		if err != nil {
			return err
		}
		changes += diff
	}
	disclosure += "\n" + changes
	plan.Comparison = work.PublicationComparison{Relationship: "public history continuation (not separately reviewed)", Changes: disclosure}
	plan.Destination = destination
	preparation := p.PreparedFrom
	if preparation == nil {
		preparation = &work.PublicationRef{Phase: selected.phaseIndex}
	}
	next := &plan
	next.PreparedFrom = preparation
	if keepSync {
		next.SyncFrom = p.SyncFrom
		if next.SyncFrom == nil {
			next.SyncFrom = &work.PublicationRef{Phase: selected.phaseIndex}
		}
	}
	selected.workflow.Phases = append(selected.workflow.Phases, next)
	if store == nil {
		return nil
	}
	if err := savePublicationOperation(store, snapshot); err != nil {
		return err
	}
	return ErrPublicationConsentRequired
}

func (s Controller) refreshDirectPublication(ctx context.Context, repo task.Repository, snapshot *work.State, w *work.Workflow, p *work.PublicationPhase, item task.Task) (bool, error) {
	if !publicationRequirements(w, p, item) {
		return false, errors.New("task requirements changed; reconcile prepared public history before fresh review")
	}
	if p.NeedsPublicationRecovery() {
		return false, nil
	}
	prepared := w.PublicPreparation(p)
	if prepared.PrivateRefsReleasedAt == nil {
		return false, nil
	}
	boundary := s.publicationBoundary()
	destination, err := boundary.ObserveDestination(ctx, w.Context().Directory, w.FixedDestination().Branch)
	if err != nil {
		return false, err
	}
	if p.Integration != nil {
		return s.refreshDirectIntegration(ctx, repo, boundary, w, p, destination)
	}
	current, err := boundary.InspectPublicationCheckout(ctx, w.Context().Directory, w.Context().TaskBranch)
	if err != nil {
		return false, err
	}
	expected := *prepared.Operation.Public
	if p.PreparedFrom != nil {
		expected = p.Input.Code.CodeIdentity
	}
	sync := w.PublicSync(p)
	if sync != nil && sync.Result != nil {
		expected = *sync.Result
	}
	if current.Commit == expected.Commit && current.Tree == expected.Tree && destination == p.Destination && current.Tree == prepared.Operation.Public.Tree && current.Tree == prepared.Input.Code.Tree {
		return false, nil
	}
	if current.Commit == expected.Commit && current.Tree == expected.Tree && destination == p.Destination && p.Input.Code.CodeIdentity == expected {
		return false, nil
	}
	selected := committedPublication{workflowIndex: len(snapshot.Workflows) - 1, phaseIndex: len(w.Phases) - 1, workflow: w, publication: p, prepared: w.Context()}
	keep := sync != nil && sync.Result != nil && *sync.Result == (work.CodeIdentity{Commit: current.Commit, Tree: current.Tree}) && sync.Destination == destination
	err = s.Delivery.renewDirectPublication(ctx, nil, boundary, snapshot, selected, work.CodeIdentity{Commit: current.Commit, Tree: current.Tree}, destination, "Actual public checkout or destination changed; no additional task review was performed", keep)
	return true, err
}

func (LocalFinalizationGit) ResumeDirectTaskCheckout(ctx context.Context, dir, branch string) error {
	return gitmeta.ResumeDirectTaskCheckout(ctx, dir, branch)
}

func (LocalFinalizationGit) DescribePublicChanges(ctx context.Context, dir, from, to string) (string, error) {
	return gitmeta.DescribePublicChanges(ctx, dir, from, to)
}

func (s FinalizationService) prepareDirectPublicHistory(ctx context.Context, store WorkStore, boundary FinalizationGit, snapshot *work.State, selected committedPublication) error {
	p, w := selected.publication, selected.workflow
	direct := s.directBoundary(boundary)
	if selected.workflow.PublicPreparation(p) != p {
		return nil
	}
	if err := reservePublicOperation(ctx, store, boundary, snapshot, selected); err != nil {
		return err
	}
	_, packageErr := packagePublicOperation(ctx, store, boundary, snapshot, p, publicationRequest(w, p))
	if p.Operation.Public != nil && p.Operation.Public.Tree != p.Input.Code.Tree && p.Operation.InputChanges == "" {
		changes, err := direct.DescribePublicChanges(ctx, selected.prepared.Directory, p.Input.Code.Commit, p.Operation.Public.Commit)
		if err != nil {
			return err
		}
		p.Operation.InputChanges = changes
		if err := savePublicationOperation(store, snapshot); err != nil {
			return err
		}
	}
	if p.Operation.Public != nil {
		if err := releasePublicationHistory(ctx, store, boundary, snapshot, selected); err != nil {
			return err
		}
	}
	if packageErr != nil {
		return packageErr
	}

	return nil
}

func (s FinalizationService) beginDirectSync(ctx context.Context, store WorkStore, boundary FinalizationGit, repo task.Repository, item task.Task, snapshot *work.State, selected committedPublication) error {
	p := selected.publication
	direct := s.directBoundary(boundary)
	if selected.workflow.PublicSync(p) != nil {
		return nil
	}
	current, err := boundary.InspectPublicationCheckout(ctx, selected.prepared.Directory, selected.prepared.TaskBranch)
	if err != nil {
		return err
	}
	p.DirectSync = &work.DirectPublicationSync{StartedAt: time.Now().UTC(), Source: work.CodeIdentity{Commit: current.Commit, Tree: current.Tree}, Destination: p.Destination}
	if err := savePublicationOperation(store, snapshot); err != nil {
		return err
	}
	synced, err := direct.BeginLocalTaskBranchSync(ctx, directSyncOptions(repo, selected), p.Destination.Commit)
	if err != nil {
		return err
	}
	if synced.Status == gitmeta.TaskBranchSyncConflicted {
		p.DirectSync.ConflictFiles = append([]string(nil), synced.ConflictFiles...)
		if err := savePublicationOperation(store, snapshot); err != nil {
			return err
		}
		if s.ConflictResolver == nil {
			return errors.New("direct sync conflicts require a configured resolver; continue task run for restoration or manual repair")
		}
		return directResolverPending{repository: repo, item: item, owner: work.AttemptRef{RepositoryID: snapshot.RepositoryID, TaskID: snapshot.TaskID, Workflow: selected.workflowIndex, Phase: selected.phaseIndex}}
	}
	return recordDirectSyncResult(ctx, store, boundary, snapshot, selected)
}

func (s Controller) refreshDirectIntegration(ctx context.Context, repo task.Repository, boundary FinalizationGit, w *work.Workflow, p *work.PublicationPhase, destination work.Destination) (bool, error) {
	integration := p.Integration
	// Independently observed delivery wins over later local edits. It is not a
	// grant to push changed input, and the original exact result is reconciled.
	if destination.Commit == integration.Result.Commit {
		return false, nil
	}
	selected := committedPublication{publication: p, prepared: w.Context()}
	checkpoints, err := s.Delivery.directBoundary(boundary).InspectDirectIntegrationCheckpoints(ctx, directIntegrationRequest(repo, selected))
	if err != nil {
		return false, err
	}
	source := work.CodeIdentity{Commit: checkpoints.Source.Commit, Tree: checkpoints.Source.Tree}
	localDestination := work.CodeIdentity{Commit: checkpoints.Destination.Commit, Tree: checkpoints.Destination.Tree}
	if destination == integration.Destination && source == integration.Source && localDestination == *integration.Result {
		return false, nil
	}
	at := time.Now().UTC()
	integration.InvalidatedAt = &at
	return true, nil
}

func (LocalFinalizationGit) InspectDirectIntegrationCheckpoints(ctx context.Context, req gitmeta.DirectIntegrationRequest) (gitmeta.DirectIntegrationCheckpoints, error) {
	return gitmeta.InspectDirectIntegrationCheckpoints(ctx, req)
}
