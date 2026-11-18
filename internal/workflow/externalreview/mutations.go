package externalreview

import (
	"errors"
	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pullrequest"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"strings"
	"time"
)

func prOutcomeChange(owner work.AttemptRef, status pullrequest.PullRequestStatus) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		if len(current.Workflows) == 0 {
			return false, errors.New("PR observation requires workflow-owned delivery")
		}
		w := current.ActiveWorkflow()
		if w == nil || owner.Workflow != len(current.Workflows)-1 || owner.Phase != len(w.Phases)-1 {
			return false, errors.New("PR observation no longer owns external review")
		}
		_, external, publication, err := externalAt(current)
		if err != nil {
			return false, err
		}
		if publication.Delivery.(*work.PullRequestDelivery).PullRequest.URL != status.URL {
			return false, errors.New("observed PR differs from workflow publication")
		}
		outcome := work.PRMerged
		if status.State == pullrequest.StateClosed {
			outcome = work.PRClosedUnmerged
		}
		external.Outcome = &work.PROutcome{State: outcome, ObservedAt: time.Now().UTC(), Evidence: "Provider independently reported the recorded PR " + string(outcome)}
		return true, nil
	}
}

func branchChange(ref work.AttemptRef, update func(*work.BranchSync) error) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		sync, err := syncAt(current, ref)
		if err != nil {
			return false, err
		}
		return true, update(sync)
	}
}

// CheckpointChange retains the exact native checkpoint before any merge effect.
func CheckpointChange(opts gitmeta.TaskBranchSyncOptions, checkpoint gitmeta.TaskBranchConflictCheckpoint) phaseexec.Change {
	return func(current *work.State) (bool, error) {
		w, external, publication, err := externalAt(current)
		if err != nil {
			return false, err
		}
		destination, err := w.ReviewDestination(publication.Review)
		if err != nil {
			return false, err
		}
		prepared := w.Context()
		if opts.Branch != prepared.TaskBranch || opts.Worktree != prepared.Directory || opts.DefaultBranch != destination.Branch {
			return false, errors.New("branch-sync observations differ from their owning workflow")
		}
		if _, active := activeSync(*current); active != nil {
			return false, errors.New("branch sync already has an unsettled effort")
		}
		external.BranchSyncs = append(external.BranchSyncs, work.BranchSync{StartedAt: time.Now().UTC(), Checkpoint: work.BranchSyncCheckpoint(checkpoint)})
		return true, nil
	}
}

// LocalCommitChange records observed local completion, not remote delivery.
func LocalCommitChange(ref work.AttemptRef, head string) phaseexec.Change {
	return branchChange(ref, func(sync *work.BranchSync) error {
		if sync.LocalCommit == nil {
			sync.LocalCommit = &work.BranchSyncCommit{Head: head, ObservedAt: time.Now().UTC()}
		}
		return nil
	})
}

// PushChange records an independently verified push of the retained local head.
func PushChange(ref work.AttemptRef, head string) phaseexec.Change { return pushed(ref, head, false) }

func pushed(ref work.AttemptRef, head string, local bool) phaseexec.Change {
	return branchChange(ref, func(sync *work.BranchSync) error {
		if local && sync.LocalCommit == nil {
			sync.LocalCommit = &work.BranchSyncCommit{Head: head, ObservedAt: time.Now().UTC()}
		}
		if sync.LocalCommit == nil || head != sync.LocalCommit.Head {
			return errors.New("push requires independently observed exact local merge head")
		}
		sync.Push = &work.BranchSyncPush{Head: head, ObservedAt: time.Now().UTC(), Evidence: "Origin task branch independently observed at the recorded local sync result"}
		return nil
	})
}

// RollbackChange consumes a verified native rollback without erasing history.
func RollbackChange(ref work.AttemptRef, reason string) phaseexec.Change {
	return branchChange(ref, func(sync *work.BranchSync) error {
		if strings.TrimSpace(reason) == "" {
			return errors.New("verified rollback requires a reason")
		}
		now := time.Now().UTC()
		if sync.ConflictResolution != nil {
			e := &sync.ConflictResolution.Execution
			if _, running := e.Process.(*work.RunningProcess); running {
				e.Process = &work.InterruptedProcess{At: now, Reason: reason, Trigger: "sync_recovery"}
			}
		}
		sync.Rollback = &work.BranchSyncRollback{LocalHead: sync.Checkpoint.LocalHead, RemoteHead: sync.Checkpoint.RemoteHead, ObservedAt: now, Reason: reason, Evidence: "Caller independently verified native checkout and local/remote checkpoint restoration"}
		return nil
	})
}
