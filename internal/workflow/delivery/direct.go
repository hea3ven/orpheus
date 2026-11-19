package delivery

import (
	"context"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/work"
	"time"
)

// DirectGit provides local synchronization and split integration
// effects. It deliberately has no PR or task-branch delivery operation.
type DirectGit interface {
	InspectDirectIntegrationCheckpoints(context.Context, gitmeta.DirectIntegrationRequest) (gitmeta.DirectIntegrationCheckpoints, error)
	DescribePublicChanges(context.Context, string, string, string) (string, error)
	ResumeDirectTaskCheckout(context.Context, string, string) error
	BeginLocalTaskBranchSync(context.Context, gitmeta.TaskBranchSyncOptions, string) (gitmeta.TaskBranchSyncResult, error)
	CommitTaskBranchConflictResolution(context.Context, gitmeta.TaskBranchSyncOptions, []string) (gitmeta.TaskBranchSyncResult, error)
	PrepareDirectIntegration(context.Context, gitmeta.DirectIntegrationRequest) (gitmeta.Revision, error)
	PushDirectIntegration(context.Context, gitmeta.DirectIntegrationRequest, gitmeta.Revision) (gitmeta.Revision, error)
}

func (LocalGit) BeginLocalTaskBranchSync(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, destination string) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.BeginLocalTaskBranchSync(ctx, opts, destination)
}
func (LocalGit) CommitTaskBranchConflictResolution(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, files []string) (gitmeta.TaskBranchSyncResult, error) {
	return gitmeta.CommitTaskBranchConflictResolution(ctx, opts, files)
}
func (LocalGit) PrepareDirectIntegration(ctx context.Context, req gitmeta.DirectIntegrationRequest) (gitmeta.Revision, error) {
	return gitmeta.PrepareDirectIntegration(ctx, req)
}
func (LocalGit) PushDirectIntegration(ctx context.Context, req gitmeta.DirectIntegrationRequest, result gitmeta.Revision) (gitmeta.Revision, error) {
	return gitmeta.PushDirectIntegration(ctx, req, result)
}

func directSyncOptions(repo task.Repository, selected committedPublication) gitmeta.TaskBranchSyncOptions {
	return gitmeta.TaskBranchSyncOptions{RepoPath: repo.Path, Worktree: selected.prepared.Directory, Branch: selected.prepared.TaskBranch, DefaultBranch: selected.destination.Branch}
}

func directIntegrationRequest(repo task.Repository, w *work.Workflow, p *work.PublicationPhase) gitmeta.DirectIntegrationRequest {
	prepared := w.Context()
	source := w.PublicSync(p).Result
	return gitmeta.DirectIntegrationRequest{
		Repository: repo, Directory: prepared.Directory, Branch: prepared.TaskBranch,
		Destination: p.Destination.Branch, DestinationCommit: p.Destination.Commit,
		Source: gitmeta.Revision{Commit: source.Commit, Tree: source.Tree},
	}
}

func (s *Service) renewDirectPublication(ctx context.Context, selected committedPublication, input work.CodeIdentity, destination work.Destination, disclosure string, keepSync bool) error {
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
		diff, err := s.DirectGit.DescribePublicChanges(ctx, selected.prepared.Directory, prepared.Operation.Public.Commit, input.Commit)
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
	return nil
}

func (s *Service) refreshDirectPublication(ctx context.Context, repo task.Repository, snapshot *work.State, w *work.Workflow, p *work.PublicationPhase) (bool, error) {
	if p.NeedsPublicationRecovery() {
		return false, nil
	}
	prepared := w.PublicPreparation(p)
	if prepared.PrivateRefsReleasedAt == nil {
		return false, nil
	}
	destination, err := s.Git.ObserveDestination(ctx, w.Context().Directory, w.FixedDestination().Branch)
	if err != nil {
		return false, err
	}
	if p.Integration != nil {
		return s.refreshDirectIntegration(ctx, repo, w, p, destination)
	}
	current, err := s.Git.InspectPublicationCheckout(ctx, w.Context().Directory, w.Context().TaskBranch)
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
	err = s.renewDirectPublication(ctx, selected, work.CodeIdentity{Commit: current.Commit, Tree: current.Tree}, destination, "Actual public checkout or destination changed; no additional task review was performed", keep)
	return true, err
}

func (LocalGit) ResumeDirectTaskCheckout(ctx context.Context, dir, branch string) error {
	return gitmeta.ResumeDirectTaskCheckout(ctx, dir, branch)
}

func (LocalGit) DescribePublicChanges(ctx context.Context, dir, from, to string) (string, error) {
	return gitmeta.DescribePublicChanges(ctx, dir, from, to)
}

func (s *Service) refreshDirectIntegration(ctx context.Context, repo task.Repository, w *work.Workflow, p *work.PublicationPhase, destination work.Destination) (bool, error) {
	integration := p.Integration
	// Independently observed delivery wins over later local edits. It is not a
	// grant to push changed input, and the original exact result is reconciled.
	if destination.Commit == integration.Result.Commit {
		return false, nil
	}
	checkpoints, err := s.DirectGit.InspectDirectIntegrationCheckpoints(ctx, directIntegrationRequest(repo, w, p))
	if err != nil {
		return false, err
	}
	source := work.CodeIdentity{Commit: checkpoints.Source.Commit, Tree: checkpoints.Source.Tree}
	localDestination := work.CodeIdentity{Commit: checkpoints.Destination.Commit, Tree: checkpoints.Destination.Tree}
	if destination == p.Destination && source == *w.PublicSync(p).Result && localDestination == *integration.Result {
		return false, nil
	}
	at := time.Now().UTC()
	integration.InvalidatedAt = &at
	return true, nil
}

func (LocalGit) InspectDirectIntegrationCheckpoints(ctx context.Context, req gitmeta.DirectIntegrationRequest) (gitmeta.DirectIntegrationCheckpoints, error) {
	return gitmeta.InspectDirectIntegrationCheckpoints(ctx, req)
}
