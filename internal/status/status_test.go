package status_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/status"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/workflow"
)

func TestProjectGroupsItemsByLocalM4Policy(t *testing.T) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"},
		Tasks: []task.Task{
			{ID: "a-ready", Title: "ready", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
			{ID: "a-dep", Title: "dependency", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
			{
				ID:        "a-blocked",
				Title:     "blocked",
				Status:    task.StatusOpen,
				IssueType: task.IssueTypeTask,
				Relations: task.RelationSummary{DependencyIDs: []string{"a-dep"}},
			},
			{ID: "a-epic-ready", Title: "epic ready", Status: task.StatusOpen, IssueType: task.IssueTypeEpic},
			{
				ID:        "a-epic-blocked",
				Title:     "epic blocked",
				Status:    task.StatusOpen,
				IssueType: task.IssueTypeEpic,
				Relations: task.RelationSummary{DependencyIDs: []string{"a-dep"}},
			},
			{
				ID:        "a-review",
				Title:     "review",
				Status:    task.StatusOpen,
				IssueType: task.IssueTypeTask,
				Metadata:  task.Metadata{task.MetadataPRURL: "https://example.test/pr/1"},
			},
			{ID: "a-idle", Title: "idle", Status: task.StatusInProgress, IssueType: task.IssueTypeTask},
			{ID: "a-epic-idle", Title: "epic idle", Status: task.StatusInProgress, IssueType: task.IssueTypeEpic},
			{ID: "a-done", Title: "done", Status: task.StatusClosed, IssueType: task.IssueTypeTask},
			{ID: "a-epic-done", Title: "epic done", Status: task.StatusClosed, IssueType: task.IssueTypeEpic},
			{ID: "a-unknown", Title: "unknown", Status: task.StatusUnknown, IssueType: task.IssueTypeTask},
		},
	}}}

	got := status.Project(snapshot)

	assertProjectionGroupOrder(t, got, []status.GroupID{
		status.GroupNeedsAttention,
		status.GroupInReview,
		status.GroupWorking,
		status.GroupIdle,
		status.GroupReadyToRun,
		status.GroupBlocked,
		status.GroupDoneClosed,
	})
	assertGroupTaskIDs(t, got, status.GroupReadyToRun, []string{"a-ready", "a-dep", "a-epic-ready"})
	assertGroupTaskIDs(t, got, status.GroupWorking, nil)
	assertGroupTaskIDs(t, got, status.GroupIdle, []string{"a-idle", "a-epic-idle"})
	assertGroupTaskIDs(t, got, status.GroupBlocked, []string{"a-blocked", "a-epic-blocked"})
	assertGroupTaskIDs(t, got, status.GroupInReview, []string{"a-review"})
	assertGroupTaskIDs(t, got, status.GroupDoneClosed, []string{"a-done", "a-epic-done"})
	assertGroupTaskIDs(t, got, status.GroupNeedsAttention, []string{"a-unknown"})

	assertLocalM4PolicyProjectionDetails(t, got)
}

func assertLocalM4PolicyProjectionDetails(t *testing.T, got status.Projection) {
	t.Helper()

	reviewEntry := groupEntries(t, got, status.GroupInReview)[0]
	if reviewEntry.Detail != "https://example.test/pr/1" {
		t.Fatalf("review detail = %q, want PR URL", reviewEntry.Detail)
	}
	blockedEntry := groupEntries(t, got, status.GroupBlocked)[0]
	if blockedEntry.Detail != "blocked by a-dep" {
		t.Fatalf("blocked detail = %q, want dependency detail", blockedEntry.Detail)
	}
	idleEntry := groupEntries(t, got, status.GroupIdle)[0]
	if idleEntry.Detail != "no attached run recorded" {
		t.Fatalf("idle detail = %q, want no-run detail", idleEntry.Detail)
	}
}

func TestProjectRequiresExternalReferenceBeforePrePRWorkflowStates(t *testing.T) {
	const missingRefDetail = "missing required external reference; set it with `bd update gated-open --external-ref <reference>`"

	snapshot, runStates := externalRefGateFixture()
	projection := status.ProjectWithRunStates(snapshot, runStates)
	attention := groupEntries(t, projection, status.GroupNeedsAttention)
	if len(attention) != 3 {
		t.Fatalf("needs-attention entries = %#v, want three missing-reference tasks", attention)
	}
	for _, entry := range attention {
		if !strings.HasPrefix(entry.Detail, "missing required external reference; set it with `bd update ") {
			t.Fatalf("needs-attention detail = %q, want external-reference guidance", entry.Detail)
		}
	}
	if attention[0].Detail != missingRefDetail {
		t.Fatalf("open task detail = %q, want %q", attention[0].Detail, missingRefDetail)
	}
	assertGroupTaskIDs(t, projection, status.GroupInReview, []string{"gated-pr"})
	assertGroupTaskIDs(t, projection, status.GroupReadyToRun, []string{"gated-fixed", "default-open"})

	rows := status.ReadyRowsWithRunStates(snapshot, runStates)
	if len(rows) != 2 || rows[0].Task.ID != "gated-fixed" || rows[1].Task.ID != "default-open" {
		t.Fatalf("ready rows = %#v, want fixed-reference and default-repo tasks", rows)
	}
}

func TestProjectGatesChildReadinessOnImmediateParentEpic(t *testing.T) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"},
		Tasks: []task.Task{
			{ID: "a-paused", Status: task.StatusOpen, IssueType: task.IssueTypeEpic},
			{ID: "a-active", Status: task.StatusInProgress, IssueType: task.IssueTypeEpic, Relations: task.RelationSummary{ParentID: "a-paused"}},
			{ID: "a-child-task", Status: task.StatusOpen, IssueType: task.IssueTypeTask, Relations: task.RelationSummary{ParentID: "a-active"}},
			{ID: "a-child-epic", Status: task.StatusOpen, IssueType: task.IssueTypeEpic, Relations: task.RelationSummary{ParentID: "a-active"}},
			{ID: "a-blocked-child", Status: task.StatusOpen, IssueType: task.IssueTypeBug, Relations: task.RelationSummary{ParentID: "a-paused"}},
			{ID: "a-missing-parent", Status: task.StatusOpen, IssueType: task.IssueTypeTask, Relations: task.RelationSummary{ParentID: "a-gone"}},
			{ID: "a-non-epic-parent", Status: task.StatusOpen, IssueType: task.IssueTypeTask, Relations: task.RelationSummary{ParentID: "a-child-task"}},
			{ID: "a-closed-parent", Status: task.StatusClosed, IssueType: task.IssueTypeEpic},
			{ID: "a-closed-parent-child", Status: task.StatusOpen, IssueType: task.IssueTypeTask, Relations: task.RelationSummary{ParentID: "a-closed-parent"}},
			{ID: "a-unknown-parent", Status: task.Status("paused"), IssueType: task.IssueTypeEpic},
			{ID: "a-unknown-parent-child", Status: task.StatusOpen, IssueType: task.IssueTypeTask, Relations: task.RelationSummary{ParentID: "a-unknown-parent"}},
			{ID: "a-running", Status: task.StatusInProgress, IssueType: task.IssueTypeTask, Relations: task.RelationSummary{ParentID: "a-paused"}},
			{ID: "a-reviewing", Status: task.StatusOpen, IssueType: task.IssueTypeTask, Metadata: task.Metadata{task.MetadataPRURL: "https://example.test/pr/1"}, Relations: task.RelationSummary{ParentID: "a-paused"}},
			{ID: "a-done", Status: task.StatusClosed, IssueType: task.IssueTypeTask, Relations: task.RelationSummary{ParentID: "a-paused"}},
		},
	}}}
	runStates := status.RunStateIndex{
		status.RunStateKey("alpha", "a-running"): {Attempt: 1, Status: taskstate.RunStatusRunning},
	}

	projection := status.ProjectWithRunStates(snapshot, runStates)
	assertGroupTaskIDs(t, projection, status.GroupReadyToRun, []string{"a-paused", "a-child-task", "a-child-epic"})
	assertGroupTaskIDs(t, projection, status.GroupBlocked, []string{"a-active", "a-blocked-child"})
	assertGroupTaskIDs(t, projection, status.GroupNeedsAttention, []string{"a-missing-parent", "a-non-epic-parent", "a-closed-parent-child", "a-unknown-parent", "a-unknown-parent-child"})
	assertGroupTaskIDs(t, projection, status.GroupWorking, []string{"a-running"})
	assertGroupTaskIDs(t, projection, status.GroupInReview, []string{"a-reviewing"})
	assertGroupTaskIDs(t, projection, status.GroupDoneClosed, []string{"a-closed-parent", "a-done"})

	blocked := groupEntries(t, projection, status.GroupBlocked)[1]
	if blocked.Detail != "immediate parent epic a-paused is open; immediate parent epic must be in_progress" {
		t.Fatalf("blocked detail = %q", blocked.Detail)
	}
	for _, entry := range groupEntries(t, projection, status.GroupNeedsAttention) {
		if entry.Task.Relations.ParentID == "" {
			continue
		}
		if !strings.Contains(entry.Detail, "immediate parent epic must be in_progress") {
			t.Fatalf("attention detail = %q, want parent epic guidance", entry.Detail)
		}
	}

	rows := status.ReadyRowsWithRunStates(snapshot, runStates)
	if len(rows) != 3 || rows[0].Task.ID != "a-paused" || rows[1].Task.ID != "a-child-task" || rows[2].Task.ID != "a-child-epic" {
		t.Fatalf("ready rows = %#v, want only children of the active immediate epic", rows)
	}
}

func externalRefGateFixture() (task.SnapshotResult, status.RunStateIndex) {
	const titleTemplate = "[{{external_ref}}] {{summary}}"

	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{
		{
			Repository: task.Repository{
				ID:            "gated",
				Name:          "Gated",
				TaskIDPrefix:  "gated",
				TitleTemplate: titleTemplate,
			},
			Tasks: []task.Task{
				{ID: "gated-open", Title: "missing ref open", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
				{ID: "gated-progress", Title: "missing ref in progress", Status: task.StatusInProgress, IssueType: task.IssueTypeTask},
				{
					ID:        "gated-local-review",
					Title:     "missing ref local review",
					Status:    task.StatusInProgress,
					IssueType: task.IssueTypeTask,
					Metadata: task.Metadata{
						task.MetadataBranch:   "main",
						task.MetadataWorktree: "/tmp/gated",
					},
				},
				{ID: "gated-fixed", Title: "external ref set", ExternalRef: "TREX-1234", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
				{
					ID:        "gated-pr",
					Title:     "external ref not needed after PR",
					Status:    task.StatusInProgress,
					IssueType: task.IssueTypeTask,
					Metadata:  task.Metadata{task.MetadataPRURL: "https://example.test/pr/1"},
				},
			},
		},
		{
			Repository: task.Repository{ID: "default", Name: "Default", TaskIDPrefix: "default"},
			Tasks: []task.Task{
				{ID: "default-open", Title: "default repo", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
			},
		},
	}}
	runStates := status.RunStateIndex{
		status.RunStateKey("gated", "gated-local-review"): {
			Attempt: 1,
			Status:  taskstate.RunStatusSucceeded,
			Completion: &taskstate.Completion{
				Summary:             "Ready for review",
				DetailedDescription: "Review details.",
			},
		},
	}

	return snapshot, runStates
}

func TestProjectWithRunStatesShowsSuccessfulMainCompletionInReview(t *testing.T) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a", Path: "/tmp/alpha", DefaultBranch: "main"},
		Tasks: []task.Task{{
			ID:        "a-main",
			Title:     "local main review",
			Status:    task.StatusInProgress,
			IssueType: task.IssueTypeTask,
			Metadata: task.Metadata{
				task.MetadataBranch:   "main",
				task.MetadataWorktree: "/tmp/alpha",
			},
		}},
	}}}
	latestRun := taskstate.RunAttempt{
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary:             "Done",
			Description:         "Ready for review.",
			DetailedDescription: "Detailed PR body.",
			CompletedAt:         time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
		},
	}
	localStates := status.LocalTaskStateIndex{
		status.RunStateKey("alpha", "a-main"): {
			LatestRun:       &latestRun,
			Target:          testTaskTarget("main", "/tmp/alpha"),
			ExpectedTargets: testExpectedTargets("main", "/tmp/alpha", "orpheus/a-main", "/tmp/orpheus/worktrees/a-main"),
		},
	}

	got := status.ProjectWithLocalTaskStates(snapshot, localStates)

	assertGroupTaskIDs(t, got, status.GroupInReview, []string{"a-main"})
	reviewEntry := groupEntries(t, got, status.GroupInReview)[0]
	if reviewEntry.Detail != "local review; run task review" {
		t.Fatalf("review detail = %q, want local review detail", reviewEntry.Detail)
	}
	assertGroupTaskIDs(t, got, status.GroupWorking, nil)
}

func TestProjectWithRunStatesShowsWorktreeCompletionReadyForTaskDone(t *testing.T) {
	got := projectWorktreeCompletion(taskstate.Completion{
		Summary:             "Done",
		Description:         "Ready for PR.",
		DetailedDescription: "Detailed PR body.",
		CompletedAt:         time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
		Commit:              "abc123",
	})

	assertGroupTaskIDs(t, got, status.GroupInReview, []string{"a-worktree"})
	entry := groupEntries(t, got, status.GroupInReview)[0]
	if entry.Detail != "local review; run task review" {
		t.Fatalf("review detail = %q, want task review detail", entry.Detail)
	}
}

func TestProjectWithRunStatesShowsWorktreeCompletionWithoutCommitReadyForTaskDone(t *testing.T) {
	got := projectWorktreeCompletion(taskstate.Completion{
		Summary:             "Done",
		Description:         "Commit failed.",
		DetailedDescription: "Detailed PR body.",
		CompletedAt:         time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
		CommitError:         "commit failed",
	})

	assertGroupTaskIDs(t, got, status.GroupInReview, []string{"a-worktree"})
	entry := groupEntries(t, got, status.GroupInReview)[0]
	if entry.Detail != "local review; run task review" {
		t.Fatalf("review detail = %q, want task review detail", entry.Detail)
	}
}

func TestProjectWithRunStatesDoesNotInferRepoRootReviewWithoutCompletion(t *testing.T) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a", Path: "/tmp/alpha", DefaultBranch: "main"},
		Tasks: []task.Task{{
			ID:        "a-main",
			Title:     "local main review",
			Status:    task.StatusInProgress,
			IssueType: task.IssueTypeTask,
			Metadata: task.Metadata{
				task.MetadataBranch:   "main",
				task.MetadataWorktree: "/tmp/alpha",
			},
		}},
	}}}
	runStates := status.RunStateIndex{
		status.RunStateKey("alpha", "a-main"): {
			Attempt: 1,
			Status:  taskstate.RunStatusSucceeded,
		},
	}

	got := status.ProjectWithRunStates(snapshot, runStates)

	assertGroupTaskIDs(t, got, status.GroupInReview, nil)
	assertGroupTaskIDs(t, got, status.GroupIdle, []string{"a-main"})
	idleEntry := groupEntries(t, got, status.GroupIdle)[0]
	if !strings.Contains(idleEntry.Detail, "agent exited without completion") {
		t.Fatalf("idle detail = %q, want missing-completion detail", idleEntry.Detail)
	}
}

func TestProjectWithLocalTaskStatesDoesNotShowClosedFinalizationAsLocalReview(t *testing.T) {
	closedAt := time.Date(2026, 6, 3, 11, 1, 0, 0, time.UTC)
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a", Path: "/tmp/alpha", DefaultBranch: "main"},
		Tasks: []task.Task{{
			ID:        "a-main",
			Title:     "local main finalized",
			Status:    task.StatusInProgress,
			IssueType: task.IssueTypeTask,
			Metadata: task.Metadata{
				task.MetadataBranch:   "main",
				task.MetadataWorktree: "/tmp/alpha",
			},
		}},
	}}}
	latestRun := taskstate.RunAttempt{
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary:             "Done",
			Description:         "Ready for local review.",
			DetailedDescription: "Detailed PR body.",
			CompletedAt:         time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
		},
	}
	localStates := status.LocalTaskStateIndex{
		status.RunStateKey("alpha", "a-main"): {
			LatestRun: &latestRun,
			Target:    testTaskTarget("main", "/tmp/alpha"),
			Finalization: taskstate.Finalization{
				Commit:   "abc123",
				ClosedAt: &closedAt,
			},
			ExpectedTargets: testExpectedTargets("main", "/tmp/alpha", "orpheus/a-main", "/tmp/orpheus/worktrees/a-main"),
		},
	}

	got := status.ProjectWithLocalTaskStates(snapshot, localStates)

	assertGroupTaskIDs(t, got, status.GroupInReview, nil)
	assertGroupTaskIDs(t, got, status.GroupNeedsAttention, []string{"a-main"})
	entry := groupEntries(t, got, status.GroupNeedsAttention)[0]
	if entry.Detail != "finalization recorded but backend task is not closed" {
		t.Fatalf("needs-attention detail = %q, want stale finalization detail", entry.Detail)
	}
}

//nolint:funlen // The review-state table is clearer kept together.
func TestProjectWithLocalTaskStatesClassifiesLatestReviewAttempts(t *testing.T) {
	tests := []struct {
		name           string
		review         taskstate.ReviewAttempt
		failure        *taskstate.Event
		wantGroup      status.GroupID
		wantDetail     string
		wantDetailPart string
	}{
		{
			name: "blocked review is idle follow-up work",
			review: reviewAttempt(1, taskstate.ReviewStatusBlocked, []taskstate.ReviewFinding{
				{Type: taskstate.FindingTypeBlocking, Title: "Bug", Description: "Fix it"},
				{Type: taskstate.FindingTypeAdvisory, Title: "Note", Description: "Consider it"},
			}),
			wantGroup:  status.GroupIdle,
			wantDetail: "review blocked by 1 finding(s); run task run",
		},
		{
			name: "blocked review targeted by follow-up returns to review",
			review: reviewAttempt(1, taskstate.ReviewStatusBlocked, []taskstate.ReviewFinding{
				{Type: taskstate.FindingTypeBlocking, Title: "Bug", Description: "Fix it", TargetedByRunAttempt: 2},
			}),
			wantGroup:  status.GroupInReview,
			wantDetail: "review blockers targeted; run task review",
		},
		{
			name:       "aborted review is reviewing retry",
			review:     reviewAttempt(1, taskstate.ReviewStatusAborted, nil),
			wantGroup:  status.GroupInReview,
			wantDetail: "review aborted; run task review",
		},
		{
			name:       "failed review needs operator attention",
			review:     reviewAttempt(1, taskstate.ReviewStatusFailed, nil),
			wantGroup:  status.GroupNeedsAttention,
			wantDetail: "review failed operationally; run task review",
		},
		{
			name:   "passed review with publication failure needs task done retry",
			review: reviewAttempt(1, taskstate.ReviewStatusPassed, nil),
			failure: &taskstate.Event{
				Type:  taskstate.EventFinalizationFailed,
				At:    time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
				Error: "push failed",
			},
			wantGroup:      status.GroupNeedsAttention,
			wantDetailPart: "review passed; publication failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := localReviewSnapshot("a-main", "/tmp/alpha")
			latestRun := localReviewRun("/tmp/alpha")
			localStates := status.LocalTaskStateIndex{
				status.RunStateKey("alpha", "a-main"): {
					LatestRun:                 &latestRun,
					Target:                    testTaskTarget("main", "/tmp/alpha"),
					LatestReview:              &tt.review,
					LatestFinalizationFailure: tt.failure,
					ExpectedTargets:           testExpectedTargets("main", "/tmp/alpha", "orpheus/a-main", "/tmp/orpheus/worktrees/a-main"),
				},
			}

			got := status.ProjectWithLocalTaskStates(snapshot, localStates)
			assertGroupTaskIDs(t, got, tt.wantGroup, []string{"a-main"})
			entry := groupEntries(t, got, tt.wantGroup)[0]
			if tt.wantDetail != "" && entry.Detail != tt.wantDetail {
				t.Fatalf("detail = %q, want %q", entry.Detail, tt.wantDetail)
			}
			if tt.wantDetailPart != "" && !strings.Contains(entry.Detail, tt.wantDetailPart) {
				t.Fatalf("detail = %q, want to contain %q", entry.Detail, tt.wantDetailPart)
			}
		})
	}
}

func TestProjectWithRunStatesClassifiesLatestAttachedAttempts(t *testing.T) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"},
		Tasks: []task.Task{
			{ID: "a-running", Title: "running", Status: task.StatusInProgress, IssueType: task.IssueTypeTask},
			{ID: "a-failed", Title: "failed", Status: task.StatusInProgress, IssueType: task.IssueTypeTask},
			{ID: "a-idle-succeeded", Title: "succeeded", Status: task.StatusInProgress, IssueType: task.IssueTypeTask},
			{ID: "a-idle-no-run", Title: "no run", Status: task.StatusInProgress, IssueType: task.IssueTypeTask},
			{ID: "a-open-history", Title: "open history", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
			{ID: "a-ready", Title: "ready", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
		},
	}}}
	runStates := status.RunStateIndex{
		status.RunStateKey("alpha", "a-running"):        {Attempt: 2, Status: taskstate.RunStatusRunning},
		status.RunStateKey("alpha", "a-failed"):         {Attempt: 3, Status: taskstate.RunStatusFailed},
		status.RunStateKey("alpha", "a-idle-succeeded"): {Attempt: 4, Status: taskstate.RunStatusSucceeded},
		status.RunStateKey("alpha", "a-open-history"):   {Attempt: 1, Status: taskstate.RunStatusFailed},
	}

	got := status.ProjectWithRunStates(snapshot, runStates)

	assertLatestAttachedAttemptProjection(t, got)
	assertGroupTaskIDs(t, got, status.GroupReadyToRun, []string{"a-ready"})

	readyRows := status.ReadyRowsWithRunStates(snapshot, runStates)
	if len(readyRows) != 1 || readyRows[0].Task.ID != "a-ready" {
		t.Fatalf("ready rows = %#v, want only a-ready", readyRows)
	}
}

func localReviewSnapshot(taskID string, repoPath string) task.SnapshotResult {
	return task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a", Path: repoPath, DefaultBranch: "main"},
		Tasks: []task.Task{{
			ID:        taskID,
			Title:     "local main review",
			Status:    task.StatusInProgress,
			IssueType: task.IssueTypeTask,
			Metadata: task.Metadata{
				task.MetadataBranch:   "main",
				task.MetadataWorktree: repoPath,
			},
		}},
	}}}
}

func localReviewRun(repoPath string) taskstate.RunAttempt {
	return taskstate.RunAttempt{
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary:             "Done",
			Description:         "Ready for local review.",
			DetailedDescription: "Detailed PR body.",
			CompletedAt:         time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
		},
	}
}

func reviewAttempt(
	attempt int,
	reviewStatus taskstate.ReviewStatus,
	findings []taskstate.ReviewFinding,
) taskstate.ReviewAttempt {
	finishedAt := time.Date(2026, 6, 3, 11, 0, 0, 0, time.UTC)
	return taskstate.ReviewAttempt{
		Attempt:    attempt,
		Status:     reviewStatus,
		Pipeline:   "default",
		Step:       "local-review",
		StartedAt:  time.Date(2026, 6, 3, 10, 30, 0, 0, time.UTC),
		FinishedAt: &finishedAt,
		Findings:   findings,
	}
}

func assertLatestAttachedAttemptProjection(t *testing.T, got status.Projection) {
	t.Helper()

	working := groupEntries(t, got, status.GroupWorking)
	if len(working) != 1 || working[0].Task.ID != "a-running" || working[0].Detail != "run attempt 2 is running" {
		t.Fatalf("working entries = %#v, want running attempt detail", working)
	}
	idle := groupEntries(t, got, status.GroupIdle)
	if len(idle) != 2 || idle[0].Task.ID != "a-idle-succeeded" || idle[1].Task.ID != "a-idle-no-run" {
		t.Fatalf("idle entries = %#v, want succeeded and no-run tasks", idle)
	}
	hasSucceededAttempt := strings.Contains(idle[0].Detail, "run attempt 4 succeeded")
	hasNonInferenceDetail := strings.Contains(
		idle[0].Detail,
		"agent exited without completion",
	)
	if !hasSucceededAttempt || !hasNonInferenceDetail {
		t.Fatalf("succeeded idle detail = %q, want missing-completion detail", idle[0].Detail)
	}
	if idle[1].Detail != "no attached run recorded" {
		t.Fatalf("no-run idle detail = %q, want no-run detail", idle[1].Detail)
	}
	attention := groupEntries(t, got, status.GroupNeedsAttention)
	if len(attention) != 2 {
		t.Fatalf("needs-attention entries = %#v, want failed and open-history tasks", attention)
	}
	if attention[0].Task.ID != "a-failed" || attention[0].Detail != "run attempt 3 failed" {
		t.Fatalf("needs-attention entries = %#v, want failed attempt detail first", attention)
	}
	hasOpenStatusDetail := strings.Contains(
		attention[1].Detail,
		"backend status is open",
	)
	hasFailedRunDetail := strings.Contains(
		attention[1].Detail,
		"run attempt 1 failed",
	)
	if attention[1].Task.ID != "a-open-history" || !hasOpenStatusDetail || !hasFailedRunDetail {
		t.Fatalf("needs-attention entries = %#v, want open task run-history detail", attention)
	}
}

func TestProjectTreatsSameRepoClosedDependenciesAsReady(t *testing.T) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"},
		Tasks: []task.Task{
			{ID: "a-dep", Title: "done dependency", Status: task.StatusClosed, IssueType: task.IssueTypeTask},
			{
				ID:        "a-ready",
				Title:     "ready",
				Status:    task.StatusOpen,
				IssueType: task.IssueTypeTask,
				Relations: task.RelationSummary{DependencyIDs: []string{"a-dep"}},
			},
		},
	}}}

	got := status.Project(snapshot)

	assertGroupTaskIDs(t, got, status.GroupReadyToRun, []string{"a-ready"})
	assertGroupTaskIDs(t, got, status.GroupDoneClosed, []string{"a-dep"})
}

func TestProjectTreatsMissingDependenciesAsUnknown(t *testing.T) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"},
		Tasks: []task.Task{{
			ID:        "a-task",
			Title:     "missing dependency",
			Status:    task.StatusOpen,
			IssueType: task.IssueTypeTask,
			Relations: task.RelationSummary{DependencyIDs: []string{"a-missing"}},
		}},
	}}}

	got := status.Project(snapshot)
	entries := groupEntries(t, got, status.GroupNeedsAttention)

	if len(entries) != 1 || entries[0].Task.ID != "a-task" || entries[0].Detail != "missing dependency a-missing" {
		t.Fatalf("unknown entries = %#v, want missing dependency detail", entries)
	}
	assertGroupTaskIDs(t, got, status.GroupReadyToRun, nil)
}

func TestReadyRowsUsesCanonicalReadinessPolicyForEligibleIssueTypes(t *testing.T) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a"},
		Tasks: []task.Task{
			{ID: "a-task", Title: "task", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
			{ID: "a-bug", Title: "bug", Status: task.StatusOpen, IssueType: task.IssueTypeBug},
			{ID: "a-chore", Title: "chore", Status: task.StatusOpen, IssueType: task.IssueTypeChore},
			{ID: "a-unknown-type", Title: "unknown type", Status: task.StatusOpen, IssueType: task.IssueTypeUnknown},
			{ID: "a-epic", Title: "epic", Status: task.StatusOpen, IssueType: task.IssueTypeEpic},
			{ID: "a-epic-idle", Title: "epic idle", Status: task.StatusInProgress, IssueType: task.IssueTypeEpic},
			{ID: "a-epic-done", Title: "epic done", Status: task.StatusClosed, IssueType: task.IssueTypeEpic},
			{
				ID:        "a-review",
				Title:     "review",
				Status:    task.StatusOpen,
				IssueType: task.IssueTypeTask,
				Metadata:  task.Metadata{task.MetadataPRURL: "https://example.test/pr/2"},
			},
			{ID: "a-started", Title: "started", Status: task.StatusInProgress, IssueType: task.IssueTypeTask},
			{ID: "a-closed", Title: "closed", Status: task.StatusClosed, IssueType: task.IssueTypeTask},
		},
	}}}

	got := status.ReadyRows(snapshot)
	ids := make([]string, 0, len(got))
	for _, row := range got {
		ids = append(ids, row.Task.ID)
	}

	expected := []string{"a-task", "a-bug", "a-chore", "a-unknown-type", "a-epic"}
	if len(ids) != len(expected) {
		t.Fatalf("ready ids = %v, want %v", ids, expected)
	}
	for i := range ids {
		if ids[i] != expected[i] {
			t.Fatalf("ready ids = %v, want %v", ids, expected)
		}
	}
}

func TestReadyRowsWithRunStatesExcludesCompletionAndAttentionStates(t *testing.T) {
	snapshot, runStates := readyRowsCompletionAndAttentionFixture()

	got := status.ReadyRowsWithRunStates(snapshot, runStates)

	if len(got) != 1 || got[0].Task.ID != "a-ready" {
		t.Fatalf("ready rows = %#v, want only a-ready", got)
	}
}

func readyRowsCompletionAndAttentionFixture() (task.SnapshotResult, status.RunStateIndex) {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a", Path: "/tmp/alpha", DefaultBranch: "main"},
		Tasks: []task.Task{
			{ID: "a-ready", Title: "ready", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
			reviewTask("a-main", "main local review", "main", "/tmp/alpha"),
			reviewTask(
				"a-worktree",
				"worktree needs PR",
				"orpheus/a-worktree",
				"/tmp/orpheus/worktrees/a-worktree",
			),
			{ID: "a-failed", Title: "failed", Status: task.StatusInProgress, IssueType: task.IssueTypeTask},
			{ID: "a-open-history", Title: "open history", Status: task.StatusOpen, IssueType: task.IssueTypeTask},
		},
	}}}
	runStates := status.RunStateIndex{
		status.RunStateKey("alpha", "a-main"): completedRun("main", "/tmp/alpha", ""),
		status.RunStateKey("alpha", "a-worktree"): completedRun(
			"orpheus/a-worktree",
			"/tmp/orpheus/worktrees/a-worktree",
			"abc123",
		),
		status.RunStateKey("alpha", "a-failed"):       {Attempt: 1, Status: taskstate.RunStatusFailed},
		status.RunStateKey("alpha", "a-open-history"): {Attempt: 1, Status: taskstate.RunStatusSucceeded},
	}
	return snapshot, runStates
}

func reviewTask(id string, title string, branch string, worktree string) task.Task {
	return task.Task{
		ID:        id,
		Title:     title,
		Status:    task.StatusInProgress,
		IssueType: task.IssueTypeTask,
		Metadata: task.Metadata{
			task.MetadataBranch:   branch,
			task.MetadataWorktree: worktree,
		},
	}
}

func completedRun(branch string, worktree string, commit string) taskstate.RunAttempt {
	description := "Ready for local review."
	if commit != "" {
		description = "Ready for PR."
	}
	return taskstate.RunAttempt{
		Attempt: 1,
		Status:  taskstate.RunStatusSucceeded,
		Completion: &taskstate.Completion{
			Summary:             "Done",
			Description:         description,
			DetailedDescription: "Detailed PR body.",
			CompletedAt:         time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
			Commit:              commit,
		},
	}
}

func TestProjectAddsStructuredRepoFailuresToNeedsAttention(t *testing.T) {
	failureErr := errors.New("bd list failed")
	snapshot := task.SnapshotResult{Failures: []task.RepoFailure{{
		Repository: task.Repository{ID: "broken", Name: "Broken", TaskIDPrefix: "br"},
		Source:     "task_backend",
		Operation:  "snapshot",
		Err:        failureErr,
	}}}

	got := status.Project(snapshot)
	entries := groupEntries(t, got, status.GroupNeedsAttention)

	if len(entries) != 1 {
		t.Fatalf("unknown entries = %#v, want one repo failure", entries)
	}
	entry := entries[0]
	if entry.Kind != status.EntryRepoFailure ||
		entry.Repository.ID != "broken" ||
		entry.Source != "task_backend" ||
		entry.Operation != "snapshot" ||
		!errors.Is(entry.Failure, failureErr) {
		t.Fatalf("unknown entry = %#v, want structured broken repo failure", entry)
	}
}

func assertProjectionGroupOrder(t *testing.T, projection status.Projection, expected []status.GroupID) {
	t.Helper()

	if len(projection.Groups) != len(expected) {
		t.Fatalf("group count = %d, want %d", len(projection.Groups), len(expected))
	}
	for i, groupID := range expected {
		if projection.Groups[i].ID != groupID {
			t.Fatalf("group order = %#v, want %s at position %d", projection.Groups, groupID, i)
		}
	}
}

func assertGroupTaskIDs(t *testing.T, projection status.Projection, groupID status.GroupID, expected []string) {
	t.Helper()

	entries := groupEntries(t, projection, groupID)
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Kind != status.EntryTask {
			continue
		}
		got = append(got, entry.Task.ID)
	}
	if len(got) != len(expected) {
		t.Fatalf("group %s task ids = %v, want %v", groupID, got, expected)
	}
	for i := range got {
		if got[i] != expected[i] {
			t.Fatalf("group %s task ids = %v, want %v", groupID, got, expected)
		}
	}
}

func groupEntries(t *testing.T, projection status.Projection, groupID status.GroupID) []status.Entry {
	t.Helper()

	for _, group := range projection.Groups {
		if group.ID == groupID {
			return group.Entries
		}
	}
	t.Fatalf("missing group %s", groupID)
	return nil
}

func projectWorktreeCompletion(completion taskstate.Completion) status.Projection {
	snapshot := task.SnapshotResult{Repositories: []task.RepositorySnapshot{{
		Repository: task.Repository{ID: "alpha", Name: "Alpha", TaskIDPrefix: "a", Path: "/tmp/alpha", DefaultBranch: "main"},
		Tasks: []task.Task{{
			ID:        "a-worktree",
			Title:     "worktree review",
			Status:    task.StatusInProgress,
			IssueType: task.IssueTypeTask,
			Metadata: task.Metadata{
				task.MetadataBranch:   "orpheus/a-worktree",
				task.MetadataWorktree: "/tmp/orpheus/worktrees/a-worktree",
			},
		}},
	}}}
	latestRun := taskstate.RunAttempt{
		Attempt:    1,
		Status:     taskstate.RunStatusSucceeded,
		Completion: &completion,
	}
	localStates := status.LocalTaskStateIndex{
		status.RunStateKey("alpha", "a-worktree"): {
			LatestRun:       &latestRun,
			Target:          testTaskTarget("orpheus/a-worktree", "/tmp/orpheus/worktrees/a-worktree"),
			ExpectedTargets: testExpectedTargets("main", "/tmp/alpha", "orpheus/a-worktree", "/tmp/orpheus/worktrees/a-worktree"),
		},
	}
	return status.ProjectWithLocalTaskStates(snapshot, localStates)
}

func testTaskTarget(branch string, worktree string) *taskstate.TaskTarget {
	return &taskstate.TaskTarget{
		Branch:   branch,
		Worktree: worktree,
	}
}

func testExpectedTargets(
	mainBranch string,
	mainWorktree string,
	worktreeBranch string,
	worktreePath string,
) *workflow.ExpectedTargets {
	return &workflow.ExpectedTargets{
		MainSolo: workflow.Target{
			Kind:     workflow.TargetMainSolo,
			Branch:   mainBranch,
			Worktree: mainWorktree,
		},
		WorktreeTeam: workflow.Target{
			Kind:     workflow.TargetWorktreeTeam,
			Branch:   worktreeBranch,
			Worktree: worktreePath,
		},
	}
}
