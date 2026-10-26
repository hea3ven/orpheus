package work_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/require"
)

func TestBranchSyncHistoryOwnsCleanMergesAndOptionalConflictResolution(t *testing.T) {
	before := branchSyncState(t)
	w := &before.Workflows[0]
	maintenance := branchMaintenance(&before)
	require.NoError(t, before.Validate())
	require.Len(t, maintenance.BranchSyncs, 2)
	require.Nil(t, maintenance.BranchSyncs[0].ConflictResolution)
	require.Equal(t, "S1", maintenance.BranchSyncs[0].Push.Head)
	purpose, err := w.ExecutionPurpose(work.ExecutionRef{Phase: 10, BranchSync: indexPointer(1)})
	require.NoError(t, err)
	require.Equal(t, work.PurposeConflictResolution, purpose)
	purpose, err = w.ExecutionPurpose(work.ExecutionRef{Phase: 8, Step: indexPointer(2)})
	require.NoError(t, err)
	require.Equal(t, work.PurposeReview, purpose)
	purpose, err = w.ExecutionPurpose(work.ExecutionRef{Phase: 7})
	require.NoError(t, err)
	require.Equal(t, work.PurposeImplementation, purpose)
	require.Equal(t, "B", w.Context().Baseline.Commit)
	require.Equal(t, "D3", maintenance.BranchSyncs[1].Checkpoint.MergeSource)

	token := attempt(t, 0, 10, nil)
	token.BranchSync = indexPointer(1)
	require.NoError(t, before.CheckActiveAttempt(token))
	for _, position := range []*int{nil, indexPointer(-1), indexPointer(0), indexPointer(2)} {
		stale := token
		stale.BranchSync = position
		require.Error(t, before.CheckActiveAttempt(stale), "only the current sync's running resolver owns this callback")
		_, resolveErr := w.Execution(work.ExecutionRef{Phase: 10, BranchSync: position})
		require.Error(t, resolveErr)
	}
	ambiguous := token
	ambiguous.Step = indexPointer(0)
	require.Error(t, before.CheckActiveAttempt(ambiguous))
	_, err = w.Execution(work.ExecutionRef{Phase: 10, Step: indexPointer(0), BranchSync: indexPointer(1)})
	require.Error(t, err)
	first := branchSyncState(t)
	branchMaintenance(&first).BranchSyncs = branchMaintenance(&first).BranchSyncs[1:]
	branchMaintenance(&first).BranchSyncs[0].Checkpoint.LocalHead = "P2"
	branchMaintenance(&first).BranchSyncs[0].Checkpoint.RemoteHead = "P2"
	firstToken := attempt(t, 0, 10, nil)
	firstToken.BranchSync = indexPointer(0)
	require.NoError(t, first.CheckActiveAttempt(firstToken), "zero selects a real first sync, not an absent index")

	local := localBranchSyncState(t)
	require.NoError(t, local.ValidateSuccessor(&before))
	require.Nil(t, maintenance.BranchSyncs[1].LocalCommit, "the independent prior snapshot retains the running attempt")
	require.Zero(t, maintenance.BranchSyncs[1].ConflictResolution.Execution.ChildPID)
	require.Equal(t, 321, branchMaintenance(&local).BranchSyncs[1].ConflictResolution.Execution.ChildPID)
	require.Equal(t, "S2", branchMaintenance(&local).BranchSyncs[1].LocalCommit.Head)
	require.Nil(t, branchMaintenance(&local).BranchSyncs[1].Push, "a local merge is not remote publication")
	require.Error(t, local.CheckActiveAttempt(token), "process exit ends child ownership even while push is pending")

	pushed := pushedBranchSyncState(t)
	require.NoError(t, pushed.ValidateSuccessor(&local), "push retry completes the same sync entry")
	require.Len(t, branchMaintenance(&pushed).BranchSyncs, 2)
	require.Equal(t, "S2", branchMaintenance(&pushed).BranchSyncs[1].Push.Head)
	require.Nil(t, branchMaintenance(&local).BranchSyncs[1].Push)

	next := pushedBranchSyncState(t)
	branchMaintenance(&next).BranchSyncs = append(branchMaintenance(&next).BranchSyncs, cleanBranchSync("S2", "D4", "S3", 130))
	require.NoError(t, next.ValidateSuccessor(&pushed), "another merge appends rather than replacing the previous synchronization")
	require.Len(t, branchMaintenance(&next).BranchSyncs, 3)
	require.Equal(t, "S1", branchMaintenance(&next).BranchSyncs[0].Push.Head)
	require.Equal(t, "S2", branchMaintenance(&next).BranchSyncs[1].Push.Head)
	require.Equal(t, "S3", branchMaintenance(&next).BranchSyncs[2].Push.Head)
	require.Len(t, next.Workflows[0].Phases, 11, "branch sync must not fabricate implementation, review, or publication phases")
	output, ok := next.Workflows[0].LatestOutput()
	require.True(t, ok)
	require.Equal(t, 7, output.Phase, "branch maintenance is not another feature implementation")
	require.Equal(t, "feat: original feature", next.Workflows[0].FeatureCompletion().Summary)
	require.Equal(t, "P2", next.Workflows[0].Phases[9].(*work.PublicationPhase).Delivery.(*work.PullRequestDelivery).Head)
	next.Workflows[0].Phases = append(next.Workflows[0].Phases, &work.FinalizationPhase{Acceptance: &work.PRAcceptance{ExternalReview: 10, ObservedAt: at(133), Evidence: "Provider confirmed merge after synchronization"}})
	require.NoError(t, next.Validate())
	require.NotNil(t, next.ActiveWorkflow(), "confirmed PR merge permits finalization but does not fabricate task-source closure")
}

func TestBranchSyncRollbackPreservesFailedResolverAndPermitsNewEffort(t *testing.T) {
	before, rolledBack := branchSyncState(t), rolledBackBranchSyncState(t)
	require.NoError(t, rolledBack.ValidateSuccessor(&before))
	failed := &branchMaintenance(&rolledBack).BranchSyncs[1]
	require.Equal(t, 1, *failed.ConflictResolution.Execution.Process.(*work.ExitedProcess).Code)
	require.Nil(t, failed.Push)
	require.Equal(t, "L1", failed.Rollback.LocalHead)
	require.Equal(t, "S1", failed.Rollback.RemoteHead)

	next := rolledBackBranchSyncState(t)
	retry := cleanBranchSync("L1", "D4", "S3", 130)
	retry.Checkpoint.RemoteHead = "S1"
	branchMaintenance(&next).BranchSyncs = append(branchMaintenance(&next).BranchSyncs, retry)
	require.NoError(t, next.ValidateSuccessor(&rolledBack))
	require.Len(t, branchMaintenance(&next).BranchSyncs, 3)
	require.Equal(t, "S3", branchMaintenance(&next).BranchSyncs[2].Push.Head)
	require.Equal(t, 1, *branchMaintenance(&next).BranchSyncs[1].ConflictResolution.Execution.Process.(*work.ExitedProcess).Code)
	require.Len(t, branchMaintenance(&rolledBack).BranchSyncs, 2)
}

func TestBranchSyncRejectsUnsafeProgressionAndUnconfirmedResults(t *testing.T) {
	cases := []struct {
		name   string
		build  func(*testing.T) work.State
		change func(*work.State)
		want   string
	}{
		{"sync before publication", pushedBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[0].StartedAt = at(100)
		}, "predates delivery"},
		{"missing remote checkpoint", branchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[1].Checkpoint.RemoteHead = ""
		}, "local/remote heads"},
		{"another sync before push", localBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs = append(branchMaintenance(s).BranchSyncs, cleanBranchSync("S2", "D4", "S3", 130))
		}, "unfinished branch sync"},
		{"finalization before push", localBranchSyncState, func(s *work.State) {
			s.Workflows[0].Phases = append(s.Workflows[0].Phases, &work.FinalizationPhase{Acceptance: &work.PRAcceptance{ExternalReview: 10, ObservedAt: at(130), Evidence: "Merged"}})
		}, "unfinished branch sync"},
		{"failed resolver claimed a local merge", localBranchSyncState, func(s *work.State) {
			*branchMaintenance(s).BranchSyncs[1].ConflictResolution.Execution.Process.(*work.ExitedProcess).Code = 1
		}, "successful resolver exit"},
		{"merge predates resolver exit", localBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[1].LocalCommit.ObservedAt = at(121)
		}, "successful resolver exit"},
		{"remote differs from local result", pushedBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[1].Push.Head = "foreign-head"
		}, "independently confirm the local commit"},
		{"old observation cannot establish new push", pushedBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[1].Push.ObservedAt = at(122)
		}, "push observation predates local commit"},
		{"rollback remote differs from checkpoint", rolledBackBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[1].Rollback.RemoteHead = "foreign-head"
		}, "verify checkpoint local/remote heads"},
		{"published result cannot be rolled back", pushedBranchSyncState, func(s *work.State) {
			branchMaintenance(s).BranchSyncs[1].Rollback = &work.BranchSyncRollback{LocalHead: "L1", RemoteHead: "S1", ObservedAt: at(125), Reason: "Not a safe local rollback", Evidence: "Claimed restoration"}
		}, "pushed branch sync cannot be rolled back"},
		{"maintenance after confirmed merge", finalizedBranchSyncState, func(s *work.State) {
			s.Workflows[0].Phases = append(s.Workflows[0].Phases, &work.ExternalReviewPhase{Publication: work.PublicationRef{Phase: 9}, BranchSyncs: []work.BranchSync{cleanBranchSync("S2", "D4", "S3", 150)}})
		}, "merged PR requires reconciliation"},
		{"merge confirmation predates synchronization", pushedBranchSyncState, func(s *work.State) {
			s.Workflows[0].Phases = append(s.Workflows[0].Phases, &work.FinalizationPhase{Acceptance: &work.PRAcceptance{ExternalReview: 10, ObservedAt: at(123), Evidence: "Merged"}})
		}, "merge confirmation predates delivery or branch synchronization"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.build(t)
			require.NoError(t, s.Validate())
			tc.change(&s)
			require.ErrorContains(t, s.Validate(), tc.want)
		})
	}
}

func branchSyncState(t *testing.T) work.State {
	t.Helper()
	s := deliveredHistory(t)
	resolver := execution(121, 122, 0)
	resolver.Process = &work.RunningProcess{}
	s.Workflows[0].Phases = append(s.Workflows[0].Phases, &work.ExternalReviewPhase{
		Publication: work.PublicationRef{Phase: 9},
		BranchSyncs: []work.BranchSync{
			cleanBranchSync("P2", "D2", "S1", 110),
			{StartedAt: at(120), Checkpoint: work.BranchSyncCheckpoint{LocalHead: "L1", RemoteHead: "S1", MergeSource: "D3"}, ConflictResolution: &work.ConflictResolution{Files: []string{"internal/work/model.go"}, Execution: resolver}},
		},
	})
	return s
}
func localBranchSyncState(t *testing.T) work.State {
	t.Helper()
	s := branchSyncState(t)
	sync := &branchMaintenance(&s).BranchSyncs[1]
	sync.ConflictResolution.Execution.Process = &work.ExitedProcess{At: at(122), Code: exitValue(0)}
	sync.ConflictResolution.Execution.ChildPID = 321
	sync.ConflictResolution.Execution.SupervisorPID = 123
	sync.LocalCommit = &work.BranchSyncCommit{Head: "S2", ObservedAt: at(123)}
	return s
}
func pushedBranchSyncState(t *testing.T) work.State {
	t.Helper()
	s := localBranchSyncState(t)
	branchMaintenance(&s).BranchSyncs[1].Push = &work.BranchSyncPush{Head: "S2", ObservedAt: at(124), Evidence: "Remote branch independently confirmed"}
	return s
}
func finalizedBranchSyncState(t *testing.T) work.State {
	t.Helper()
	s := pushedBranchSyncState(t)
	s.Workflows[0].Phases = append(s.Workflows[0].Phases, &work.FinalizationPhase{Acceptance: &work.PRAcceptance{ExternalReview: 10, ObservedAt: at(140), Evidence: "Provider confirmed merge"}})
	return s
}
func rolledBackBranchSyncState(t *testing.T) work.State {
	t.Helper()
	s := branchSyncState(t)
	sync := &branchMaintenance(&s).BranchSyncs[1]
	sync.ConflictResolution.Execution.Process = &work.ExitedProcess{At: at(122), Code: exitValue(1)}
	sync.Rollback = &work.BranchSyncRollback{LocalHead: "L1", RemoteHead: "S1", ObservedAt: at(125), Reason: "Resolver failed", Evidence: "Checkpoint restoration verified"}
	return s
}
func branchMaintenance(s *work.State) *work.ExternalReviewPhase {
	return s.Workflows[0].Phases[10].(*work.ExternalReviewPhase)
}
func cleanBranchSync(before, incoming, result string, minute int) work.BranchSync {
	return work.BranchSync{StartedAt: at(minute), Checkpoint: work.BranchSyncCheckpoint{LocalHead: before, RemoteHead: before, MergeSource: incoming}, LocalCommit: &work.BranchSyncCommit{Head: result, ObservedAt: at(minute + 1)}, Push: &work.BranchSyncPush{Head: result, ObservedAt: at(minute + 2), Evidence: "Remote branch independently confirmed"}}
}
