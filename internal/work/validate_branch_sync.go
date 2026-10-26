package work

import (
	"strings"
	"time"
)

func (v *validator) externalReview(p *ExternalReviewPhase) {
	if p == nil {
		v.check(false, "external review is required")
		return
	}
	if !v.prior(p.Publication.Phase, "external publication") {
		return
	}
	delivery, err := v.w.externalDelivery(v.phase)
	v.check(err == nil, "external review requires delivered PR: %v", err)
	if err != nil {
		return
	}
	v.unmergedPR(delivery.PullRequest)
	previous := delivery.ObservedAt
	for i := range p.BranchSyncs {
		v.step, v.sync = -1, i
		sync := &p.BranchSyncs[i]
		v.check(!sync.StartedAt.Before(previous), "branch sync predates delivery or the previous settled sync")
		v.branchSync(sync, v.live && v.phase == len(v.w.Phases)-1 && i == len(p.BranchSyncs)-1)
		previous = sync.settledAt()
	}
}

func (v *validator) branchSync(sync *BranchSync, active bool) {
	v.check(!sync.StartedAt.IsZero(), "branch sync requires observed start time")
	checkpoint := sync.Checkpoint
	v.check(strings.TrimSpace(checkpoint.LocalHead) != "" && strings.TrimSpace(checkpoint.RemoteHead) != "" && strings.TrimSpace(checkpoint.MergeSource) != "", "branch sync requires local/remote heads and merge source checkpoint")
	boundary := sync.StartedAt
	if resolution := sync.ConflictResolution; resolution != nil {
		v.conflictResolution(resolution, active)
		v.check(!resolution.Execution.StartedAt.Before(sync.StartedAt), "conflict resolver predates its branch sync")
		boundary = executionFinishTime(&resolution.Execution)
	}
	if commit := sync.LocalCommit; commit != nil {
		v.check(strings.TrimSpace(commit.Head) != "" && !commit.ObservedAt.IsZero() && !commit.ObservedAt.Before(sync.StartedAt), "branch sync requires observed local commit after start")
		if resolution := sync.ConflictResolution; resolution != nil {
			v.check(successful(&resolution.Execution) && !commit.ObservedAt.Before(boundary), "conflict merge requires observed successful resolver exit before local commit")
		}
		boundary = commit.ObservedAt
	}
	if push := sync.Push; push != nil {
		v.check(sync.LocalCommit != nil && push.Head != "" && push.Head == sync.LocalCommit.Head, "branch sync push must independently confirm the local commit")
		v.check(!push.ObservedAt.IsZero() && !push.ObservedAt.Before(boundary), "branch sync push observation predates local commit")
		v.text(push.Evidence, "branch sync push evidence")
	}
	if rollback := sync.Rollback; rollback != nil {
		v.check(sync.Push == nil, "pushed branch sync cannot be rolled back")
		v.check(sync.ConflictResolution == nil || !running(&sync.ConflictResolution.Execution), "running conflict resolver cannot be rolled back")
		v.check(rollback.LocalHead == checkpoint.LocalHead && rollback.RemoteHead == checkpoint.RemoteHead, "branch sync rollback must verify checkpoint local/remote heads")
		v.check(!rollback.ObservedAt.IsZero() && !rollback.ObservedAt.Before(sync.StartedAt) && !rollback.ObservedAt.Before(boundary), "branch sync rollback predates work")
		v.text(rollback.Reason, "branch sync rollback reason")
		v.text(rollback.Evidence, "branch sync rollback evidence")
	}
	v.check(sync.settled() || active, "unfinished branch sync must belong to the active last effort")
}

func (v *validator) conflictResolution(resolution *ConflictResolution, active bool) {
	v.check(len(resolution.Files) > 0, "conflict resolution requires the actual conflicted files")
	seen := make(map[string]bool)
	for _, file := range resolution.Files {
		v.check(strings.TrimSpace(file) != "" && !seen[file], "conflict files must be nonblank and unique")
		seen[file] = true
	}
	v.execution(&resolution.Execution, active)
}

func executionFinishTime(execution *AgentExecution) time.Time {
	switch result := execution.Process.(type) {
	case *ExitedProcess:
		if result != nil {
			return result.At
		}
	case *InterruptedProcess:
		if result != nil {
			return result.At
		}
	case *StartFailedProcess:
		if result != nil {
			return result.At
		}
	}
	return time.Time{}
}
