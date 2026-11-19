// Package externalreview owns PR observation and post-publication branch maintenance.
package externalreview

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/logging"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// Service is scoped to one task and one sync invocation. Native observations are
// transient; a fresh invocation reconciles the durable checkpoint before polling.
type Service struct {
	Paths            state.Paths
	Repository       task.Repository
	Task             task.Task
	Git              Git
	PRProvider       pullrequest.Provider
	ConflictResolver phaseexec.ConflictResolver
	ProcessProbe     phaseexec.ProcessProbe
	Checkpoint       func(context.Context, phaseexec.Change) (work.State, error)
	Observed         func(pullrequest.State) error
	Logger           *slog.Logger

	reconciled   bool
	status       *pullrequest.PullRequestStatus
	presented    bool
	checkpointed bool
	branchResult *gitmeta.TaskBranchSyncResult
	resolver     *phaseexec.PreparedConflictResolution
	outcome      *Open
}

type Intent struct {
	UpdatePolicy gitmeta.TaskBranchUpdatePolicy
	RecoveryOnly bool
}
type Request struct{}
type Outcome interface{ externalReviewOutcome() }
type Open struct {
	Updated bool
	Reason  string
	PRURL   string
}
type Merged struct{ PRURL string }
type ClosedUnmerged struct{ PRURL string }
type Reconciled struct{}

func (Open) externalReviewOutcome()           {}
func (Merged) externalReviewOutcome()         {}
func (ClosedUnmerged) externalReviewOutcome() {}
func (Reconciled) externalReviewOutcome()     {}

// Next never enters Finalization or selects a new publication or task review.
func (s *Service) Next(current work.State, intent Intent) (phaseexec.Selection[Request, Outcome], error) {
	if current.RepositoryID != s.Repository.ID || current.TaskID != s.Task.ID {
		return phaseexec.Selection[Request, Outcome]{}, errors.New("external review state belongs to a different task")
	}
	if !s.reconciled {
		if ref, sync := activeSync(current); sync != nil {
			return phaseexec.Run[Request, Outcome](s.recoveryOperation(ref)), nil
		}
		s.reconciled = true
	}
	if intent.RecoveryOnly {
		return phaseexec.Return[Request, Outcome](Reconciled{}), nil
	}
	_, external, publication, err := externalAt(&current)
	url := strings.TrimSpace(s.Task.OrpheusMetadata().PRURL)
	if err != nil && len(current.Workflows) > 0 {
		return phaseexec.Selection[Request, Outcome]{}, err
	}
	if publication != nil {
		url = publication.Delivery.(*work.PullRequestDelivery).PullRequest.URL
	}
	if external != nil && external.Outcome != nil {
		if external.Outcome.State == work.PRClosedUnmerged {
			return phaseexec.Return[Request, Outcome](ClosedUnmerged{PRURL: url}), nil
		}
		return phaseexec.Return[Request, Outcome](Merged{PRURL: url}), nil
	}
	if s.status == nil {
		return phaseexec.Run[Request, Outcome](s.pollOperation(url)), nil
	}
	if !s.presented {
		return phaseexec.Run[Request, Outcome](s.presentationOperation()), nil
	}
	if s.outcome != nil {
		outcome := *s.outcome
		outcome.PRURL = s.status.URL
		return phaseexec.Return[Request, Outcome](outcome), nil
	}
	return s.nextBranch(current, intent, url)
}

func (s *Service) nextBranch(current work.State, intent Intent, url string) (phaseexec.Selection[Request, Outcome], error) {
	opts, skipped, err := s.branchOptions(current, intent)
	if err != nil {
		return phaseexec.Selection[Request, Outcome]{}, err
	}
	if skipped != "" {
		return phaseexec.Return[Request, Outcome](Open{PRURL: s.status.URL, Reason: "branch update skipped: " + skipped}), nil
	}
	if !s.checkpointed {
		return phaseexec.Run[Request, Outcome](s.checkpointOperation(opts)), nil
	}
	ref, sync := activeSync(current)
	if sync == nil {
		return phaseexec.Selection[Request, Outcome]{}, errors.New("branch synchronization lost its unsettled checkpoint")
	}
	if s.branchResult == nil {
		return phaseexec.Run[Request, Outcome](s.mergeOperation(ref, opts)), nil
	}
	if s.branchResult.Status == gitmeta.TaskBranchSyncConflicted {
		if sync.ConflictResolution == nil {
			if s.resolver == nil {
				return phaseexec.Run[Request, Outcome](s.resolverSelectionOperation(ref, opts, url)), nil
			}
			return phaseexec.Run[Request, Outcome](s.resolverOperation(ref, opts)), nil
		}
		if sync.LocalCommit == nil {
			return phaseexec.Run[Request, Outcome](s.commitOperation(ref, opts)), nil
		}
	}
	return phaseexec.Run[Request, Outcome](s.pushOperation(ref, opts)), nil
}

func externalAt(current *work.State) (*work.Workflow, *work.ExternalReviewPhase, *work.PublicationPhase, error) {
	w := current.ActiveWorkflow()
	if w == nil {
		return nil, nil, nil, errors.New("branch sync requires its workflow-owned delivered PR")
	}
	external, ok := w.LastPhase().(*work.ExternalReviewPhase)
	if !ok {
		return nil, nil, nil, errors.New("local implementation, review or publication boundary prevents branch sync")
	}
	publication := w.Phases[external.Publication.Phase].(*work.PublicationPhase)
	if _, ok := publication.Delivery.(*work.PullRequestDelivery); !ok {
		return nil, nil, nil, errors.New("branch sync requires workflow-owned PR delivery")
	}
	return w, external, publication, nil
}

func activeSync(current work.State) (work.AttemptRef, *work.BranchSync) {
	w := current.ActiveWorkflow()
	if w == nil {
		return work.AttemptRef{}, nil
	}
	p, ok := w.LastPhase().(*work.ExternalReviewPhase)
	if !ok || len(p.BranchSyncs) == 0 {
		return work.AttemptRef{}, nil
	}
	i := len(p.BranchSyncs) - 1
	sync := &p.BranchSyncs[i]
	if sync.Push != nil || sync.Rollback != nil {
		return work.AttemptRef{}, nil
	}
	return work.AttemptRef{RepositoryID: current.RepositoryID, TaskID: current.TaskID, Workflow: len(current.Workflows) - 1, Phase: len(w.Phases) - 1, BranchSync: &i}, sync
}

func syncAt(current *work.State, ref work.AttemptRef) (*work.BranchSync, error) {
	actual, sync := activeSync(*current)
	if sync == nil || !actual.Equal(ref) {
		return nil, errors.New("branch sync no longer owns the current effort")
	}
	return sync, nil
}

func (s *Service) branchOptions(current work.State, intent Intent) (gitmeta.TaskBranchSyncOptions, string, error) {
	w, _, publication, err := externalAt(&current)
	if err != nil {
		return gitmeta.TaskBranchSyncOptions{}, "", err
	}
	destination, err := w.ReviewDestination(publication.Review)
	if err != nil {
		return gitmeta.TaskBranchSyncOptions{}, "", err
	}
	metadata := s.Task.OrpheusMetadata()
	targets, err := tasktarget.ExpectedTargetsForTaskOrRecordedBranch(s.Repository, s.Task, metadata.Branch, s.Paths)
	if err != nil {
		return gitmeta.TaskBranchSyncOptions{}, "", fmt.Errorf("resolve sync targets for task %s: %w", s.Task.ID, err)
	}
	target, err := tasktarget.ClassifyMetadataTarget(metadata, targets)
	if err != nil {
		return gitmeta.TaskBranchSyncOptions{}, fmt.Sprintf("task metadata target is incomplete or unsupported: %v", err), nil
	}
	if target.Kind != tasktarget.TargetWorktreeTeam && target.Kind != tasktarget.TargetRepoRootTeam {
		return gitmeta.TaskBranchSyncOptions{}, fmt.Sprintf("task target %s is not an Orpheus-managed PR branch", target.Kind.DisplayName()), nil
	}
	return gitmeta.TaskBranchSyncOptions{RepoPath: s.Repository.Path, DefaultBranch: destination.Branch, Branch: target.Branch, Worktree: target.Worktree, UpdatePolicy: intent.UpdatePolicy}, "", nil
}

func (s *Service) pollOperation(url string) phaseexec.Operation {
	return phaseexec.Operation{Name: "observe pull request", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		owner := work.AttemptRef{RepositoryID: current.RepositoryID, TaskID: current.TaskID, Workflow: len(current.Workflows) - 1}
		if w := current.ActiveWorkflow(); w != nil {
			owner.Phase = len(w.Phases) - 1
		}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			span := logging.Start(ctx, s.Logger, "pull request poll", slog.String("component", "workflow"), slog.String("operation", "poll_pr"), slog.String("repo_id", s.Repository.ID), slog.String("task_id", s.Task.ID))
			status, err := s.PRProvider.StatusByURL(ctx, pullrequest.StatusByURLRequest{URL: url, Diagnostics: pullrequest.DiagnosticContext{RepoID: s.Repository.ID, TaskID: s.Task.ID, Branch: strings.TrimSpace(s.Task.OrpheusMetadata().Branch), HasPR: true}})
			if err != nil {
				span.FinishError(ctx, err)
				return phaseexec.Result{Err: err}
			}
			span.Finish(ctx, logging.StatusSuccess, slog.String("pr_state", string(status.State)))
			if strings.TrimSpace(status.URL) == "" {
				status.URL = url
			} else {
				status.URL = strings.TrimSpace(status.URL)
			}
			s.status = &status
			switch status.State {
			case pullrequest.StateOpen:
				return phaseexec.Result{}
			case pullrequest.StateMerged, pullrequest.StateClosed:
				return phaseexec.Result{Update: prOutcomeChange(owner, status)}
			default:
				return phaseexec.Result{Err: fmt.Errorf("task %s PR %s has unsupported provider state %q", s.Task.ID, status.URL, status.State)}
			}
		}}, nil
	}}
}

func (s *Service) presentationOperation() phaseexec.Operation {
	return phaseexec.Operation{Name: "present pull request observation", Prepare: func(*work.State) (phaseexec.Prepared, error) {
		return phaseexec.Prepared{Run: func(context.Context) phaseexec.Result {
			if s.Observed != nil {
				if err := s.Observed(s.status.State); err != nil {
					return phaseexec.Result{Err: err}
				}
			}
			s.presented = true
			return phaseexec.Result{}
		}}, nil
	}}
}

func (s *Service) checkpointOperation(opts gitmeta.TaskBranchSyncOptions) phaseexec.Operation {
	return phaseexec.Operation{Name: "checkpoint branch synchronization", Prepare: func(*work.State) (phaseexec.Prepared, error) {
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			checkpoint, err := s.Git.InspectTaskBranchConflictCheckpoint(ctx, opts)
			if err != nil {
				return phaseexec.Result{Err: fmt.Errorf("checkpoint conflict resolution for task %s: %w", s.Task.ID, err)}
			}
			s.checkpointed = true
			return phaseexec.Result{Update: CheckpointChange(opts, checkpoint)}
		}}, nil
	}}
}

func (s *Service) mergeOperation(ref work.AttemptRef, opts gitmeta.TaskBranchSyncOptions) phaseexec.Operation {
	return phaseexec.Operation{Name: "synchronize open PR branch", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		sync, err := syncAt(current, ref)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		checkpoint := sync.Checkpoint
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			observed, err := s.Git.BeginTaskBranchConflictResolution(ctx, opts)
			if err != nil {
				cause := fmt.Errorf("prepare conflict resolution for task %s: %w", s.Task.ID, err)
				return s.recoverFailure(ctx, ref, opts, checkpoint, nil, "", cause)
			}
			s.branchResult = &observed
			if observed.Status == gitmeta.TaskBranchSyncConflicted {
				return phaseexec.Result{}
			}
			needsPush, err := s.nonConflictNeedsPush(ctx, opts, observed)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			if needsPush {
				return phaseexec.Result{Update: LocalCommitChange(ref, observed.Head)}
			}
			remote, err := s.Git.InspectRemoteTaskBranchHead(ctx, opts)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			var update phaseexec.Change
			if remote == observed.Head {
				update = pushed(ref, observed.Head, true)
			} else {
				if err := s.Git.VerifyTaskBranchConflictRollback(ctx, opts, gitmeta.TaskBranchConflictCheckpoint(checkpoint)); err != nil {
					return phaseexec.Result{Err: err}
				}
				update = RollbackChange(ref, "Synchronization left the local and public checkpoint unchanged")
			}
			outcome, err := branchOutcome(opts, observed)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			s.outcome = &outcome
			return phaseexec.Result{Update: update}
		}}, nil
	}}
}

func (s *Service) commitOperation(ref work.AttemptRef, opts gitmeta.TaskBranchSyncOptions) phaseexec.Operation {
	return phaseexec.Operation{Name: "record resolved local merge", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		sync, err := syncAt(current, ref)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		checkpoint := sync.Checkpoint
		execution := sync.ConflictResolution.Execution
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			observed, err := s.Git.CommitTaskBranchConflictResolution(ctx, opts, s.branchResult.ConflictFiles)
			if err != nil {
				return s.recoverFailure(ctx, ref, opts, checkpoint, &execution, "", fmt.Errorf("complete resolved merge for task %s: %w", s.Task.ID, err))
			}
			return phaseexec.Result{Update: LocalCommitChange(ref, observed.Head)}
		}}, nil
	}}
}

func (s *Service) pushOperation(ref work.AttemptRef, opts gitmeta.TaskBranchSyncOptions) phaseexec.Operation {
	return phaseexec.Operation{Name: "push synchronized PR branch", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		sync, err := syncAt(current, ref)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		if sync.LocalCommit == nil {
			return phaseexec.Prepared{}, errors.New("branch push requires its recorded local merge")
		}
		head, checkpoint := sync.LocalCommit.Head, sync.Checkpoint
		var execution *work.AgentExecution
		if sync.ConflictResolution != nil {
			observed := sync.ConflictResolution.Execution
			execution = &observed
		}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			observed, err := s.Git.PushCommittedTaskBranchConflictResolution(ctx, opts)
			if err != nil {
				if execution != nil {
					return s.recoverFailure(ctx, ref, opts, checkpoint, execution, head, fmt.Errorf("complete resolved merge for task %s: %w", s.Task.ID, err))
				}
				return phaseexec.Result{Err: err}
			}
			remote, err := s.Git.InspectRemoteTaskBranchHead(ctx, opts)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			expected := head
			if execution != nil {
				expected = observed.Head
			}
			if remote != expected {
				return phaseexec.Result{Err: fmt.Errorf("remote head %s does not match local head %s", remote, expected)}
			}
			outcome, outcomeErr := s.pushedOutcome(opts, observed, execution != nil)
			s.outcome = &outcome
			return phaseexec.Result{Update: pushed(ref, remote, false), Err: outcomeErr}
		}}, nil
	}}
}

func (s *Service) nonConflictNeedsPush(ctx context.Context, opts gitmeta.TaskBranchSyncOptions, result gitmeta.TaskBranchSyncResult) (bool, error) {
	if opts.UpdatePolicy == gitmeta.TaskBranchUpdateConflictsOnly {
		return false, nil
	}
	if result.Status == gitmeta.TaskBranchSyncUpdated {
		return true, nil
	}
	if result.Status != gitmeta.TaskBranchSyncAlreadyCurrent {
		return false, nil
	}
	remote, err := s.Git.InspectRemoteTaskBranchHead(ctx, opts)
	return remote != result.Head, err
}

func (s *Service) pushedOutcome(opts gitmeta.TaskBranchSyncOptions, observed gitmeta.TaskBranchSyncResult, resolved bool) (Open, error) {
	if !resolved {
		return branchOutcome(opts, *s.branchResult)
	}
	if observed.Status == gitmeta.TaskBranchSyncUpdated {
		return Open{Updated: true, Reason: fmt.Sprintf("resolved merge conflicts with the configured agent, merged %s into %s, and pushed the branch", opts.DefaultBranch, opts.Branch)}, nil
	}
	outcome, err := branchOutcome(opts, observed)
	if err == nil {
		err = fmt.Errorf("conflict resolution completed with branch sync status %q", observed.Status)
	}
	return outcome, err
}

func branchOutcome(opts gitmeta.TaskBranchSyncOptions, result gitmeta.TaskBranchSyncResult) (Open, error) {
	switch result.Status {
	case gitmeta.TaskBranchSyncAlreadyCurrent:
		return Open{Reason: fmt.Sprintf("branch %s already includes %s", opts.Branch, opts.DefaultBranch)}, nil
	case gitmeta.TaskBranchSyncPushed:
		return Open{Updated: true, Reason: fmt.Sprintf("pushed %s because the local branch already includes %s but origin was behind", opts.Branch, opts.DefaultBranch)}, nil
	case gitmeta.TaskBranchSyncUpdated:
		return Open{Updated: true, Reason: fmt.Sprintf("merged %s into %s and pushed the branch", opts.DefaultBranch, opts.Branch)}, nil
	case gitmeta.TaskBranchSyncConflictFree:
		return Open{Reason: fmt.Sprintf("branch %s would merge %s without conflicts; conflict-only batch sync left it unchanged", opts.Branch, opts.DefaultBranch)}, nil
	default:
		return Open{}, fmt.Errorf("unsupported branch sync status %q", result.Status)
	}
}
