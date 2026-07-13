package taskstate_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

//nolint:funlen // The persisted YAML shape is the behavior under test.
func TestStoreRecordsWorktreeAndRunAttempts(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 2, 0, 0, time.UTC),
	)

	if _, err := store.RecordSetupEvent("alpha", "op-1", taskstate.EventWorktreeCreated, taskstate.SetupEventOptions{
		Branch:   "orpheus/op-1",
		Worktree: "/tmp/op-1",
	}); err != nil {
		t.Fatalf("record worktree event: %v", err)
	}

	attempt, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:       "recorder",
		SessionName: "(op-1) Implement task",
		Branch:      "orpheus/op-1",
		Worktree:    "/tmp/op-1",
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if attempt.Attempt != 1 || attempt.Status != taskstate.RunStatusRunning {
		t.Fatalf("attempt = %#v, want running attempt 1", attempt)
	}

	finished, err := store.FinishRun("alpha", "op-1", attempt.Attempt, taskstate.RunStatusSucceeded)
	if err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if finished.Status != taskstate.RunStatusSucceeded || finished.Execution.FinishedAt == nil {
		t.Fatalf("finished attempt = %#v, want succeeded with finished_at", finished)
	}

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if loaded.RepoID != "alpha" || loaded.TaskID != "op-1" || len(loaded.Runs) != 1 || len(loaded.Events) != 3 {
		t.Fatalf("loaded state = %#v", loaded)
	}
	if loaded.Events[0].Type != taskstate.EventWorktreeCreated || loaded.Events[1].Type != taskstate.EventRunStarted || loaded.Events[2].Type != taskstate.EventRunFinished {
		t.Fatalf("event types = %#v", loaded.Events)
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"version: 3",
		"repo_id: alpha",
		"task_id: op-1",
		"target:",
		"branch: orpheus/op-1",
		"worktree: /tmp/op-1",
		"attempt: 1",
		"status: succeeded",
		"execution:",
		"purpose: implementation",
		"agent: recorder",
		"profile: recorder",
		"session_name: (op-1) Implement task",
		"worktree_created",
		"run_started",
		"run_finished",
	)
	assertStoreYAMLNotContains(t, store, "alpha", "op-1",
		"  branch: orpheus/op-1\n  worktree: /tmp/op-1\n  started_at",
	)
}

func TestStoreRejectsOldRunLevelTargetSchema(t *testing.T) {
	store := newTestStore(t)
	path, err := store.Path("alpha", "op-1")
	if err != nil {
		t.Fatalf("state path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	oldYAML := strings.Join([]string{
		"version: 1",
		"repo_id: alpha",
		"task_id: op-1",
		"runs:",
		"- attempt: 1",
		"  status: succeeded",
		"  branch: orpheus/op-1",
		"  worktree: /tmp/op-1",
		"  started_at: 2026-06-03T10:00:00Z",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(oldYAML), 0o644); err != nil {
		t.Fatalf("write old state: %v", err)
	}

	_, err = store.Load("alpha", "op-1")
	if err == nil {
		t.Fatal("load old run-level target state succeeded, want error")
	}
	if !strings.Contains(err.Error(), "unsupported task state version 1") {
		t.Fatalf("error = %v, want old schema rejection", err)
	}
}

func TestStoreRejectsVersionTwoStateWithMigrationGuidance(t *testing.T) {
	store := newTestStore(t)
	path, err := store.Path("alpha", "op-1")
	if err != nil {
		t.Fatalf("state path: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	oldYAML := strings.Join([]string{
		"version: 2",
		"repo_id: alpha",
		"task_id: op-1",
		"runs:",
		"- attempt: 1",
		"  status: succeeded",
		"  agent: recorder",
		"  started_at: 2026-06-03T10:00:00Z",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(oldYAML), 0o644); err != nil {
		t.Fatalf("write old state: %v", err)
	}

	_, err = store.Load("alpha", "op-1")
	if err == nil {
		t.Fatal("load v2 state succeeded, want error")
	}
	if !strings.Contains(err.Error(), "unsupported task state version 2") ||
		!strings.Contains(err.Error(), "/tmp/orpheus_migrate_taskstate_agent_executions.py") {
		t.Fatalf("error = %v, want migration guidance", err)
	}
}

func TestStoreRecordsFeatureBranchPREventsIdempotently(t *testing.T) {
	tests := []struct {
		name         string
		wasRecovered bool
		wantType     taskstate.EventType
	}{
		{
			name:     "created PR",
			wantType: taskstate.EventPRCreated,
		},
		{
			name:         "recovered PR",
			wasRecovered: true,
			wantType:     taskstate.EventPRRecovered,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 6, 11, 10, 0, 0, 0, time.UTC)
			store := newTestStore(t, now, now.Add(time.Minute))
			opts := taskstate.FeatureBranchPROptions{
				PRURL:        " https://github.test/org/repo/pull/42 ",
				Branch:       "orpheus/op-1",
				WasRecovered: tt.wasRecovered,
			}

			event, err := store.RecordFeatureBranchPR("alpha", "op-1", opts)
			if err != nil {
				t.Fatalf("record PR event: %v", err)
			}
			if event.Type != tt.wantType || event.PRURL != "https://github.test/org/repo/pull/42" || event.Branch != "orpheus/op-1" {
				t.Fatalf("event = %#v, want %q with structured PR facts", event, tt.wantType)
			}

			again, err := store.RecordFeatureBranchPR("alpha", "op-1", opts)
			if err != nil {
				t.Fatalf("record same PR event: %v", err)
			}
			if !again.At.Equal(event.At) {
				t.Fatalf("idempotent event time = %s, want %s", again.At, event.At)
			}

			loaded, err := store.Load("alpha", "op-1")
			if err != nil {
				t.Fatalf("load state: %v", err)
			}
			if len(loaded.Events) != 1 {
				t.Fatalf("events = %#v, want one idempotent PR event", loaded.Events)
			}
		})
	}
}

func TestStoreCompleteRunRecordsCompletionFacts(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
	)
	attempt := startAlphaRun(t, store, taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: "/tmp/alpha",
	})

	completed := completeAlphaRun(t, store, attempt.Attempt, "complete run", taskstate.CompleteRunOptions{
		Summary:             "Implemented completion",
		Description:         "Recorded local review data.",
		DetailedDescription: "Detailed PR body.",
	})
	assertInitialCompletionRecorded(t, completed)

	withCommit := completeAlphaRun(t, store, attempt.Attempt, "record completion commit", taskstate.CompleteRunOptions{
		Summary:             "Implemented completion",
		Description:         "Recorded local review data.",
		DetailedDescription: "Detailed PR body.",
		Commit:              "abc123",
	})
	if withCommit.Completion.Commit != "abc123" {
		t.Fatalf("completion commit = %q, want abc123", withCommit.Completion.Commit)
	}

	finishAlphaRunSucceeded(t, store, attempt.Attempt)

	again := completeAlphaRun(t, store, attempt.Attempt, "complete same run again", taskstate.CompleteRunOptions{
		Summary:             "Implemented completion",
		Description:         "Recorded local review data.",
		DetailedDescription: "Detailed PR body.",
		Commit:              "abc123",
	})
	if !again.Completion.CompletedAt.Equal(completed.Completion.CompletedAt) {
		t.Fatalf("idempotent completed_at = %s, want %s", again.Completion.CompletedAt, completed.Completion.CompletedAt)
	}

	assertConflictingCompletionRejected(t, store, attempt.Attempt)
	assertCompletionStateLoaded(t, store)
	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"status: succeeded",
		"completion:",
		"summary: Implemented completion",
		"description: Recorded local review data.",
		"detailed_description: Detailed PR body.",
		"completed_at: 2026-06-03T10:01:00Z",
		"commit: abc123",
	)
}

func TestStoreRecordsRepeatedCompletionDiagnostic(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 2, 0, 0, time.UTC),
	)
	attempt := startAlphaRun(t, store, taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: "/tmp/alpha",
	})
	completeAlphaRun(t, store, attempt.Attempt, "complete run", taskstate.CompleteRunOptions{
		Summary:             "First summary",
		Description:         "First details.",
		DetailedDescription: "Detailed PR body.",
	})

	event, err := store.RecordRepeatedCompletion("alpha", "op-1", attempt.Attempt, taskstate.RepeatedCompletionOptions{
		Summary:             "Second summary",
		Description:         "Second details.",
		DetailedDescription: "Detailed PR body.",
	})
	if err != nil {
		t.Fatalf("record repeated completion: %v", err)
	}
	if event.Type != taskstate.EventCompletionRepeated || event.Attempt != attempt.Attempt || event.Status != taskstate.RunStatusRunning {
		t.Fatalf("event = %#v, want completion_repeated for running attempt", event)
	}
	if event.RequestedSummary != "Second summary" ||
		event.RequestedDescription != "Second details." ||
		event.RequestedDetailedDescription != "Detailed PR body." {
		t.Fatalf("event requested payload = %#v", event)
	}
	if !strings.Contains(event.Message, "preserved first completion") {
		t.Fatalf("event message = %q, want preservation diagnostic", event.Message)
	}

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if len(loaded.Events) != 3 || loaded.Events[2].Type != taskstate.EventCompletionRepeated {
		t.Fatalf("events = %#v, want repeated completion diagnostic", loaded.Events)
	}
	if loaded.Runs[0].Completion.Summary != "First summary" ||
		loaded.Runs[0].Completion.Description != "First details." ||
		loaded.Runs[0].Completion.DetailedDescription != "Detailed PR body." {
		t.Fatalf("completion = %#v, want first payload preserved", loaded.Runs[0].Completion)
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"completion_repeated",
		"requested_summary: Second summary",
		"requested_description: Second details.",
		"requested_detailed_description: Detailed PR body.",
		"preserved first completion",
	)
}

func TestStoreRecordsReviewAttemptsAndFindings(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 26, 10, 1, 0, 0, time.UTC),
	)

	review, err := store.StartReview("alpha", "op-1")
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	if review.Attempt != 1 || review.Status != taskstate.ReviewStatusRunning ||
		review.Pipeline != "default" || review.Step != "local-review" {
		t.Fatalf("review = %#v, want running default local-review attempt", review)
	}

	review, err = store.RecordReviewFinding("alpha", "op-1", review.Attempt, taskstate.ReviewFinding{
		Type:            taskstate.FindingTypeSeparateTask,
		Title:           "Follow-up",
		Description:     "Track a later cleanup.",
		SuggestedAction: "Plan separately.",
		TaskProposal: taskstate.ReviewTaskProposal{
			Title:              "Create a cleanup task",
			Description:        "Clean up the implementation later.",
			AcceptanceCriteria: "Cleanup is complete.",
		},
	})
	if err != nil {
		t.Fatalf("record finding: %v", err)
	}
	if len(review.Findings) != 1 || review.Findings[0].Type != taskstate.FindingTypeSeparateTask {
		t.Fatalf("findings = %#v, want separate-task finding", review.Findings)
	}

	passed, err := store.FinishReview("alpha", "op-1", review.Attempt, taskstate.ReviewStatusPassed)
	if err != nil {
		t.Fatalf("finish review: %v", err)
	}
	if passed.Status != taskstate.ReviewStatusPassed || passed.FinishedAt == nil {
		t.Fatalf("passed review = %#v, want passed with finished_at", passed)
	}

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	latest, ok := taskstate.LatestReview(loaded)
	if !ok || latest.Status != taskstate.ReviewStatusPassed {
		t.Fatalf("latest review = %#v ok=%v, want passed", latest, ok)
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"reviews:",
		"status: passed",
		"pipeline: default",
		"step: local-review",
		"type: separate_task",
		"task_proposal:",
		"title: Create a cleanup task",
		"description: Clean up the implementation later.",
		"acceptance_criteria: Cleanup is complete.",
	)
}

func TestStoreRejectsUnsafeReviewStepArgsAndPreservesState(t *testing.T) {
	store := newTestStore(t)
	review, err := store.StartReview("alpha", "op-1")
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	statePath, err := store.Path("alpha", "op-1")
	if err != nil {
		t.Fatalf("state path: %v", err)
	}
	before, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state before: %v", err)
	}

	_, err = store.RecordReviewStep("alpha", "op-1", review.Attempt, taskstate.RecordReviewStepOptions{
		Kind: "agent_review",
		Name: "ai-review",
		Execution: &taskstate.AgentExecution{
			Purpose:   taskstate.AgentExecutionPurposeReview,
			Status:    taskstate.RunStatusRunning,
			Command:   "review-agent",
			Args:      []string{" - You are an agent dispatched by Orpheus.\n\nRun `orpheus agent context` now.\n"},
			StartedAt: time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		},
	})
	if err == nil {
		t.Fatal("record review step with unsafe args succeeded, want error")
	}
	if !strings.Contains(err.Error(), `review attempt 1 step "ai-review" has invalid args`) {
		t.Fatalf("error = %v, want review step args context", err)
	}
	after, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read state after: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("state changed after rejected write:\nbefore:%s\nafter:%s", before, after)
	}
}

func TestStoreTargetsReviewFindingsByRunAttempt(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 26, 10, 1, 0, 0, time.UTC),
		time.Date(2026, 6, 26, 10, 2, 0, 0, time.UTC),
		time.Date(2026, 6, 26, 10, 3, 0, 0, time.UTC),
	)

	review, err := store.StartReview("alpha", "op-1")
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	review, err = store.RecordReviewFinding("alpha", "op-1", review.Attempt, taskstate.ReviewFinding{
		Type:        taskstate.FindingTypeBlocking,
		Title:       "Bug",
		Description: "Fix it.",
	})
	if err != nil {
		t.Fatalf("record blocking finding: %v", err)
	}
	if _, err := store.FinishReview("alpha", "op-1", review.Attempt, taskstate.ReviewStatusBlocked); err != nil {
		t.Fatalf("finish review: %v", err)
	}

	run, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{
		Agent:    "recorder",
		Branch:   "main",
		Worktree: "/tmp/alpha",
		ReviewFollowUp: &taskstate.ReviewFollowUp{
			ReviewAttempt:  review.Attempt,
			FindingIndexes: []int{0},
		},
	})
	if err != nil {
		t.Fatalf("start follow-up run: %v", err)
	}
	targeted, err := store.TargetReviewFindings("alpha", "op-1", review.Attempt, []int{0}, run.Attempt)
	if err != nil {
		t.Fatalf("target review findings: %v", err)
	}
	if targeted.Findings[0].TargetedByRunAttempt != run.Attempt {
		t.Fatalf("targeted finding = %#v, want run attempt %d", targeted.Findings[0], run.Attempt)
	}

	again, err := store.TargetReviewFindings("alpha", "op-1", review.Attempt, []int{0}, run.Attempt)
	if err != nil {
		t.Fatalf("target same review finding: %v", err)
	}
	if again.Findings[0].TargetedByRunAttempt != run.Attempt {
		t.Fatalf("retargeted finding = %#v, want run attempt %d", again.Findings[0], run.Attempt)
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"review_follow_up:",
		"review_attempt: 1",
		"finding_indexes:",
		"- 0",
		"targeted_by_run_attempt: 1",
	)
}

func TestStoreRecordsReviewFindingCreatedTaskTimestamp(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 26, 10, 1, 0, 0, time.UTC),
	)

	review, err := store.StartReview("alpha", "op-1")
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	review, err = store.RecordReviewFinding("alpha", "op-1", review.Attempt, taskstate.ReviewFinding{
		Type:        taskstate.FindingTypeSeparateTask,
		Title:       "Follow-up",
		Description: "Track separately.",
		TaskProposal: taskstate.ReviewTaskProposal{
			Title:              "Clean up",
			Description:        "Clean up separately.",
			AcceptanceCriteria: "Cleanup has tests.",
		},
	})
	if err != nil {
		t.Fatalf("record separate-task finding: %v", err)
	}

	review, err = store.RecordReviewFindingCreatedTask("alpha", "op-1", review.Attempt, 0, "op-2")
	if err != nil {
		t.Fatalf("record created task: %v", err)
	}

	if review.Findings[0].CreatedTaskAt == nil ||
		!review.Findings[0].CreatedTaskAt.Equal(time.Date(2026, 6, 26, 10, 1, 0, 0, time.UTC)) {
		t.Fatalf("created task timestamp = %#v, want 2026-06-26T10:01:00Z", review.Findings[0].CreatedTaskAt)
	}
	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"created_task_id: op-2",
		"created_task_at: 2026-06-26T10:01:00Z",
	)
}

func TestStorePromotesReviewAdvisoryFinding(t *testing.T) {
	store := newTestStore(t)
	review, err := store.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "ai-review",
	})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	review, err = store.RecordReviewFinding("alpha", "op-1", review.Attempt, taskstate.ReviewFinding{
		Type:            taskstate.FindingTypeAdvisory,
		Title:           "Generated advisory",
		Description:     "The review agent found a risk.",
		Step:            "ai-review",
		SuggestedAction: "Make it block publication.",
	})
	if err != nil {
		t.Fatalf("record advisory finding: %v", err)
	}

	promoted, err := store.PromoteReviewAdvisoryFinding("alpha", "op-1", review.Attempt, 0)
	if err != nil {
		t.Fatalf("promote advisory finding: %v", err)
	}
	if promoted.Findings[0].Type != taskstate.FindingTypeBlocking {
		t.Fatalf("promoted finding type = %q, want blocking", promoted.Findings[0].Type)
	}
	if promoted.Findings[0].Title != "Generated advisory" ||
		promoted.Findings[0].SuggestedAction != "Make it block publication." {
		t.Fatalf("promoted finding = %#v, want content preserved", promoted.Findings[0])
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"type: blocking",
		"title: Generated advisory",
		"suggested_action: Make it block publication.",
	)
}

func TestStoreDowngradesReviewBlockingFinding(t *testing.T) {
	store := newTestStore(t)
	review, err := store.StartReviewWithOptions("alpha", "op-1", taskstate.StartReviewOptions{
		Pipeline: "standard",
		Step:     "unit",
	})
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	review, err = store.RecordReviewFinding("alpha", "op-1", review.Attempt, taskstate.ReviewFinding{
		Type:            taskstate.FindingTypeBlocking,
		Title:           "Generated blocker",
		Description:     "The automated step found a blocker.",
		Step:            "unit",
		SuggestedAction: "Fix the blocker.",
	})
	if err != nil {
		t.Fatalf("record blocking finding: %v", err)
	}

	downgraded, err := store.DowngradeReviewBlockingFinding(
		"alpha",
		"op-1",
		review.Attempt,
		0,
		"False positive for this task.",
	)
	if err != nil {
		t.Fatalf("downgrade blocking finding: %v", err)
	}
	finding := downgraded.Findings[0]
	if finding.Type != taskstate.FindingTypeAdvisory {
		t.Fatalf("downgraded finding type = %q, want advisory", finding.Type)
	}
	if finding.Title != "Generated blocker" ||
		finding.Description != "The automated step found a blocker." ||
		finding.Step != "unit" ||
		finding.SuggestedAction != "Fix the blocker." {
		t.Fatalf("downgraded finding = %#v, want content preserved", finding)
	}
	if finding.DowngradeReason != "False positive for this task." {
		t.Fatalf("downgrade reason = %q", finding.DowngradeReason)
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"type: advisory",
		"title: Generated blocker",
		"downgrade_reason: False positive for this task.",
	)
}

func TestStoreWaivesReviewBlockingFinding(t *testing.T) {
	store := newTestStore(t)
	review, err := store.StartReview("alpha", "op-1")
	if err != nil {
		t.Fatalf("start review: %v", err)
	}
	review, err = store.RecordReviewFinding("alpha", "op-1", review.Attempt, taskstate.ReviewFinding{
		Type:        taskstate.FindingTypeBlocking,
		Title:       "Check failed",
		Description: "The check failed.",
	})
	if err != nil {
		t.Fatalf("record blocking finding: %v", err)
	}

	waived, err := store.WaiveReviewBlockingFinding("alpha", "op-1", review.Attempt, 0, "Known flaky check.")
	if err != nil {
		t.Fatalf("waive blocking finding: %v", err)
	}
	finding := waived.Findings[0]
	if finding.Type != taskstate.FindingTypeBlocking {
		t.Fatalf("waived finding type = %q, want blocking audit type preserved", finding.Type)
	}
	if finding.Waiver != "Known flaky check." {
		t.Fatalf("waiver = %q", finding.Waiver)
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"type: blocking",
		"waiver: Known flaky check.",
	)
}

func TestStoreRejectsReclassifyingResolvedOrNonBlockingFindings(t *testing.T) {
	for _, test := range reviewBlockingReclassificationRejectionCases() {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			review, err := store.StartReview("alpha", "op-1")
			if err != nil {
				t.Fatalf("start review: %v", err)
			}
			if _, err := store.RecordReviewFinding("alpha", "op-1", review.Attempt, test.finding); err != nil {
				t.Fatalf("record finding: %v", err)
			}

			err = test.run(store, review)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("reclassify error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestReviewFindingResolutionContract(t *testing.T) {
	for _, test := range reviewFindingResolutionContractCases {
		t.Run(test.name, func(t *testing.T) {
			if got := taskstate.ResolveReviewFinding(test.finding); got != test.resolution {
				t.Fatalf("ResolveReviewFinding() = %q, want %q", got, test.resolution)
			}
			if got := taskstate.ReviewFindingResolved(test.finding); got != test.resolved {
				t.Fatalf("ReviewFindingResolved() = %v, want %v", got, test.resolved)
			}
			if got := taskstate.IsOpenBlockingReviewFinding(test.finding); got != test.openBlocking {
				t.Fatalf("IsOpenBlockingReviewFinding() = %v, want %v", got, test.openBlocking)
			}
			if got := taskstate.IsOpenAdvisoryReviewFinding(test.finding); got != test.openAdvisory {
				t.Fatalf("IsOpenAdvisoryReviewFinding() = %v, want %v", got, test.openAdvisory)
			}
		})
	}
}

type reviewFindingResolutionContractCase struct {
	name         string
	finding      taskstate.ReviewFinding
	resolution   taskstate.ReviewFindingResolution
	resolved     bool
	openBlocking bool
	openAdvisory bool
}

var reviewFindingResolutionContractCases = []reviewFindingResolutionContractCase{
	{
		name: "open blocking",
		finding: taskstate.ReviewFinding{
			Type:        taskstate.FindingTypeBlocking,
			Title:       "Blocker",
			Description: "Still open.",
		},
		resolution:   taskstate.ReviewFindingResolutionOpen,
		openBlocking: true,
	},
	{
		name: "waived blocking",
		finding: taskstate.ReviewFinding{
			Type:        taskstate.FindingTypeBlocking,
			Title:       "Waived",
			Description: "Accepted.",
			Waiver:      "Accepted risk.",
		},
		resolution: taskstate.ReviewFindingResolutionWaived,
		resolved:   true,
	},
	{
		name: "downgraded advisory",
		finding: taskstate.ReviewFinding{
			Type:            taskstate.FindingTypeAdvisory,
			Title:           "Downgraded",
			Description:     "No longer blocking.",
			DowngradeReason: "False positive.",
		},
		resolution: taskstate.ReviewFindingResolutionDowngraded,
		resolved:   true,
	},
	{
		name: "created follow-up task",
		finding: taskstate.ReviewFinding{
			Type:          taskstate.FindingTypeSeparateTask,
			Title:         "Follow-up",
			Description:   "Track later.",
			CreatedTaskID: "op-2",
		},
		resolution: taskstate.ReviewFindingResolutionCreatedTask,
		resolved:   true,
	},
	{
		name: "targeted blocker",
		finding: taskstate.ReviewFinding{
			Type:                 taskstate.FindingTypeBlocking,
			Title:                "Targeted",
			Description:          "Follow-up in progress.",
			TargetedByRunAttempt: 2,
		},
		resolution: taskstate.ReviewFindingResolutionTargetedByRun,
		resolved:   true,
	},
	{
		name: "open advisory",
		finding: taskstate.ReviewFinding{
			Type:        taskstate.FindingTypeAdvisory,
			Title:       "Advisory",
			Description: "Could be promoted.",
		},
		resolution:   taskstate.ReviewFindingResolutionNonBlocking,
		openAdvisory: true,
	},
}

func TestUntargetedBlockingFindingIndexes(t *testing.T) {
	review := taskstate.ReviewAttempt{
		Findings: []taskstate.ReviewFinding{
			{
				Type:        taskstate.FindingTypeBlocking,
				Title:       "Open",
				Description: "Needs follow-up.",
			},
			{
				Type:        taskstate.FindingTypeBlocking,
				Title:       "Waived",
				Description: "Accepted.",
				Waiver:      "Not relevant.",
			},
			{
				Type:            taskstate.FindingTypeAdvisory,
				Title:           "Downgraded",
				Description:     "Not blocking.",
				DowngradeReason: "False positive.",
			},
			{
				Type:                 taskstate.FindingTypeBlocking,
				Title:                "Targeted",
				Description:          "Already has follow-up.",
				TargetedByRunAttempt: 2,
			},
			{
				Type:        taskstate.FindingTypeBlocking,
				Title:       "Second open",
				Description: "Also needs follow-up.",
			},
		},
	}

	indexes := taskstate.UntargetedBlockingFindingIndexes(review)
	if len(indexes) != 2 || indexes[0] != 0 || indexes[1] != 4 {
		t.Fatalf("UntargetedBlockingFindingIndexes() = %#v, want []int{0, 4}", indexes)
	}
	if !taskstate.ReviewHasOpenBlockers(review) {
		t.Fatal("ReviewHasOpenBlockers() = false, want true")
	}
}

type reviewBlockingReclassificationRejectionCase struct {
	name    string
	finding taskstate.ReviewFinding
	run     func(taskstate.Store, taskstate.ReviewAttempt) error
	wantErr string
}

func reviewBlockingReclassificationRejectionCases() []reviewBlockingReclassificationRejectionCase {
	return []reviewBlockingReclassificationRejectionCase{
		{
			name: "advisory downgrade",
			finding: taskstate.ReviewFinding{
				Type:        taskstate.FindingTypeAdvisory,
				Title:       "Advisory",
				Description: "Already advisory.",
			},
			run: func(store taskstate.Store, review taskstate.ReviewAttempt) error {
				_, err := store.DowngradeReviewBlockingFinding("alpha", "op-1", review.Attempt, 0, "Not blocking.")
				return err
			},
			wantErr: "is \"advisory\", expected \"blocking\"",
		},
		{
			name: "waive without reason",
			finding: taskstate.ReviewFinding{
				Type:        taskstate.FindingTypeBlocking,
				Title:       "Blocker",
				Description: "Needs a reason.",
			},
			run: func(store taskstate.Store, review taskstate.ReviewAttempt) error {
				_, err := store.WaiveReviewBlockingFinding("alpha", "op-1", review.Attempt, 0, " ")
				return err
			},
			wantErr: "reason is required",
		},
		{
			name: "already waived",
			finding: taskstate.ReviewFinding{
				Type:        taskstate.FindingTypeBlocking,
				Title:       "Waived",
				Description: "Already waived.",
				Waiver:      "Existing reason.",
			},
			run: func(store taskstate.Store, review taskstate.ReviewAttempt) error {
				_, err := store.DowngradeReviewBlockingFinding("alpha", "op-1", review.Attempt, 0, "Now advisory.")
				return err
			},
			wantErr: "is already resolved",
		},
	}
}

func TestStoreRejectsPromotingResolvedOrNonAdvisoryFindings(t *testing.T) {
	tests := []struct {
		name    string
		finding taskstate.ReviewFinding
		wantErr string
	}{
		{
			name: "blocking",
			finding: taskstate.ReviewFinding{
				Type:        taskstate.FindingTypeBlocking,
				Title:       "Already blocking",
				Description: "This is already blocking.",
			},
			wantErr: "is \"blocking\", expected \"advisory\"",
		},
		{
			name: "targeted advisory",
			finding: taskstate.ReviewFinding{
				Type:                 taskstate.FindingTypeAdvisory,
				Title:                "Targeted",
				Description:          "This was already targeted.",
				TargetedByRunAttempt: 2,
			},
			wantErr: "is already resolved",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestStore(t)
			review, err := store.StartReview("alpha", "op-1")
			if err != nil {
				t.Fatalf("start review: %v", err)
			}
			if _, err := store.RecordReviewFinding("alpha", "op-1", review.Attempt, test.finding); err != nil {
				t.Fatalf("record finding: %v", err)
			}

			_, err = store.PromoteReviewAdvisoryFinding("alpha", "op-1", review.Attempt, 0)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("promote advisory error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

//nolint:funlen // The durable boundary sequence is the behavior under test.
func TestStoreRecordsFinalizationFactsIdempotently(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 4, 10, 1, 0, 0, time.UTC),
		time.Date(2026, 6, 4, 10, 2, 0, 0, time.UTC),
	)

	commitFacts, err := store.RecordFinalizationCommit("alpha", "op-1", " abc123 ")
	if err != nil {
		t.Fatalf("record commit: %v", err)
	}
	if commitFacts.Commit != "abc123" || commitFacts.CommittedAt == nil ||
		!commitFacts.CommittedAt.Equal(time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("commit facts = %#v, want commit and committed_at", commitFacts)
	}

	pushFacts, err := store.RecordFinalizationPush("alpha", "op-1", taskstate.FinalizationPushOptions{
		Branch:     "main",
		PushTarget: taskstate.PushTargetMain,
	})
	if err != nil {
		t.Fatalf("record push: %v", err)
	}
	if pushFacts.PushedAt == nil || !pushFacts.PushedAt.Equal(time.Date(2026, 6, 4, 10, 1, 0, 0, time.UTC)) {
		t.Fatalf("push facts = %#v, want pushed_at", pushFacts)
	}

	closeFacts, err := store.RecordFinalizationClose("alpha", "op-1", taskstate.FinalizationCloseOptions{
		Reason: taskstate.CloseReasonDefaultBranchPublished,
	})
	if err != nil {
		t.Fatalf("record close: %v", err)
	}
	if closeFacts.ClosedAt == nil || !closeFacts.ClosedAt.Equal(time.Date(2026, 6, 4, 10, 2, 0, 0, time.UTC)) {
		t.Fatalf("close facts = %#v, want closed_at", closeFacts)
	}

	again, err := store.RecordFinalizationCommit("alpha", "op-1", "abc123")
	if err != nil {
		t.Fatalf("record same commit again: %v", err)
	}
	if !again.CommittedAt.Equal(*commitFacts.CommittedAt) || !again.PushedAt.Equal(*pushFacts.PushedAt) || !again.ClosedAt.Equal(*closeFacts.ClosedAt) {
		t.Fatalf("idempotent facts = %#v, want original timestamps preserved", again)
	}
	repeatedPush, err := store.RecordFinalizationPush("alpha", "op-1", taskstate.FinalizationPushOptions{
		Branch:     "main",
		PushTarget: taskstate.PushTargetMain,
	})
	if err != nil {
		t.Fatalf("record repeated push: %v", err)
	}
	if !repeatedPush.PushedAt.Equal(*pushFacts.PushedAt) {
		t.Fatalf("repeated push facts = %#v, want original push timestamp", repeatedPush)
	}
	repeatedClose, err := store.RecordFinalizationClose("alpha", "op-1", taskstate.FinalizationCloseOptions{
		Reason: taskstate.CloseReasonDefaultBranchPublished,
	})
	if err != nil {
		t.Fatalf("record repeated close: %v", err)
	}
	if !repeatedClose.ClosedAt.Equal(*closeFacts.ClosedAt) {
		t.Fatalf("repeated close facts = %#v, want original close timestamp", repeatedClose)
	}

	_, err = store.RecordFinalizationCommit("alpha", "op-1", "def456")
	if !errors.Is(err, taskstate.ErrFinalizationConflict) {
		t.Fatalf("conflicting commit error = %v, want ErrFinalizationConflict", err)
	}

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	facts := taskstate.FinalizationFacts(loaded)
	if facts.Commit != "abc123" || facts.CommittedAt == nil || facts.PushedAt == nil || facts.ClosedAt == nil {
		t.Fatalf("loaded finalization = %#v, want all facts", facts)
	}
	if len(loaded.Events) != 2 || loaded.Events[0].Type != taskstate.EventChangesPushed || loaded.Events[1].Type != taskstate.EventTaskClosed {
		t.Fatalf("events = %#v, want one push and one close event", loaded.Events)
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"finalization:",
		"committed_at: 2026-06-04T10:00:00Z",
		"commit: abc123",
		"pushed_at: 2026-06-04T10:01:00Z",
		"closed_at: 2026-06-04T10:02:00Z",
		"changes_pushed",
		"push_target: main",
		"task_closed",
		"close_reason: default_branch_published",
	)
}

func TestStoreRecordsPRMergedTaskClosedEventIdempotently(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 9, 10, 1, 0, 0, time.UTC),
	)

	event, err := store.RecordTaskClosed("alpha", "op-1", taskstate.TaskClosedOptions{
		Reason:          taskstate.CloseReasonPRMerged,
		PRURL:           " https://github.test/org/repo/pull/42 ",
		ObservedPRState: "merged",
	})
	if err != nil {
		t.Fatalf("record merged PR close event: %v", err)
	}
	if event.Type != taskstate.EventTaskClosed ||
		event.CloseReason != taskstate.CloseReasonPRMerged ||
		event.PRURL != "https://github.test/org/repo/pull/42" ||
		event.ObservedPRState != "merged" ||
		!event.At.Equal(time.Date(2026, 6, 9, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("event = %#v, want merged PR close facts", event)
	}
	again, err := store.RecordTaskClosed("alpha", "op-1", taskstate.TaskClosedOptions{
		Reason:          taskstate.CloseReasonPRMerged,
		PRURL:           "https://github.test/org/repo/pull/42",
		ObservedPRState: "merged",
	})
	if err != nil {
		t.Fatalf("record same merged PR close event again: %v", err)
	}
	if !again.At.Equal(event.At) {
		t.Fatalf("idempotent event time = %s, want %s", again.At, event.At)
	}

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Events) != 1 {
		t.Fatalf("events = %#v, want one idempotent merged PR close event", loaded.Events)
	}

	assertStoreYAMLContains(t, store, "alpha", "op-1",
		"task_closed",
		"at: 2026-06-09T10:00:00Z",
		"close_reason: pr_merged",
		"pr_url: https://github.test/org/repo/pull/42",
		"observed_pr_state: merged",
	)
}

func TestStartRunRefusesLatestRunningAttempt(t *testing.T) {
	store := newTestStore(t, time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC))
	if _, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "recorder"}); err != nil {
		t.Fatalf("start run: %v", err)
	}

	_, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "recorder"})
	if !errors.Is(err, taskstate.ErrActiveRun) {
		t.Fatalf("second start error = %v, want ErrActiveRun", err)
	}

	active, ok, err := store.ActiveRun("alpha", "op-1")
	if err != nil || !ok || active.Attempt != 1 {
		t.Fatalf("active = %#v ok=%v err=%v, want attempt 1", active, ok, err)
	}
}

func TestRetriesCreateNewAttemptsWithoutOverwritingOldAttempts(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 2, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 3, 0, 0, time.UTC),
	)

	first, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "recorder"})
	if err != nil {
		t.Fatalf("start first: %v", err)
	}
	if _, err := store.FinishRun("alpha", "op-1", first.Attempt, taskstate.RunStatusFailed); err != nil {
		t.Fatalf("fail first: %v", err)
	}
	second, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "recorder"})
	if err != nil {
		t.Fatalf("start second: %v", err)
	}
	if _, err := store.FinishRun("alpha", "op-1", second.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish second: %v", err)
	}

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded.Runs) != 2 {
		t.Fatalf("runs = %#v, want two attempts", loaded.Runs)
	}
	if loaded.Runs[0].Attempt != 1 || loaded.Runs[0].Status != taskstate.RunStatusFailed {
		t.Fatalf("first run = %#v, want failed attempt 1", loaded.Runs[0])
	}
	if loaded.Runs[1].Attempt != 2 || loaded.Runs[1].Status != taskstate.RunStatusSucceeded {
		t.Fatalf("second run = %#v, want succeeded attempt 2", loaded.Runs[1])
	}
	latest, ok := taskstate.LatestRun(loaded)
	if !ok || latest.Attempt != 2 || latest.Status != taskstate.RunStatusSucceeded {
		t.Fatalf("latest = %#v ok=%v, want succeeded attempt 2", latest, ok)
	}
}

func TestFailRunStartRecordsFailedAttemptAndStartFailureEvent(t *testing.T) {
	store := newTestStore(t,
		time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC),
	)

	attempt, err := store.StartRun("alpha", "op-1", taskstate.StartRunOptions{Agent: "missing"})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	failed, err := store.FailRunStart("alpha", "op-1", attempt.Attempt, errors.New("command not found"))
	if err != nil {
		t.Fatalf("fail run start: %v", err)
	}
	if failed.Status != taskstate.RunStatusFailed {
		t.Fatalf("failed status = %q, want failed", failed.Status)
	}

	events, err := store.Events("alpha", "op-1")
	if err != nil {
		t.Fatalf("events: %v", err)
	}
	last := events[len(events)-1]
	if last.Type != taskstate.EventRunStartFailed || last.Status != taskstate.RunStatusFailed || !strings.Contains(last.Error, "command not found") {
		t.Fatalf("last event = %#v, want run_start_failed with error", last)
	}
}

func TestStoreValidatesTaskStatePathComponents(t *testing.T) {
	store := newTestStore(t, time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC))

	_, err := store.Path("alpha", "../op-1")
	if err == nil || !strings.Contains(err.Error(), "task id") {
		t.Fatalf("path error = %v, want task id validation", err)
	}
	_, err = store.Load("alpha/other", "op-1")
	if err == nil || !strings.Contains(err.Error(), "repo id") {
		t.Fatalf("load error = %v, want repo id validation", err)
	}
}

func newTestStore(t *testing.T, times ...time.Time) taskstate.Store {
	t.Helper()

	root := t.TempDir()
	paths, err := state.NewPaths(filepath.Join(root, "config"), filepath.Join(root, "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	index := 0
	clock := func() time.Time {
		if len(times) == 0 {
			return time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
		}
		if index >= len(times) {
			return times[len(times)-1]
		}
		value := times[index]
		index++
		return value
	}
	return taskstate.NewStoreWithClock(paths, clock)
}

func startAlphaRun(t *testing.T, store taskstate.Store, opts taskstate.StartRunOptions) taskstate.RunAttempt {
	t.Helper()

	attempt, err := store.StartRun("alpha", "op-1", opts)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	return attempt
}

func completeAlphaRun(
	t *testing.T,
	store taskstate.Store,
	attempt int,
	action string,
	opts taskstate.CompleteRunOptions,
) taskstate.RunAttempt {
	t.Helper()

	completed, err := store.CompleteRun("alpha", "op-1", attempt, opts)
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	return completed
}

func assertInitialCompletionRecorded(t *testing.T, completed taskstate.RunAttempt) {
	t.Helper()

	if completed.Status != taskstate.RunStatusRunning || completed.Execution.FinishedAt != nil {
		t.Fatalf("completed run = %#v, want still-running completion without finished_at", completed)
	}
	if completed.Completion == nil {
		t.Fatalf("completed run missing completion: %#v", completed)
	}
	if completed.Completion.Summary != "Implemented completion" ||
		completed.Completion.Description != "Recorded local review data." ||
		completed.Completion.DetailedDescription != "Detailed PR body." ||
		!completed.Completion.CompletedAt.Equal(time.Date(2026, 6, 3, 10, 1, 0, 0, time.UTC)) {
		t.Fatalf("completion = %#v, want recorded summary/description/detailed_description/completed_at", completed.Completion)
	}
}

func finishAlphaRunSucceeded(t *testing.T, store taskstate.Store, attempt int) {
	t.Helper()

	finished, err := store.FinishRun("alpha", "op-1", attempt, taskstate.RunStatusSucceeded)
	if err != nil {
		t.Fatalf("finish run: %v", err)
	}
	if finished.Status != taskstate.RunStatusSucceeded || finished.Execution.FinishedAt == nil {
		t.Fatalf("finished run = %#v, want succeeded with finished_at", finished)
	}
}

func assertConflictingCompletionRejected(t *testing.T, store taskstate.Store, attempt int) {
	t.Helper()

	_, err := store.CompleteRun("alpha", "op-1", attempt, taskstate.CompleteRunOptions{
		Summary:             "Different",
		Description:         "Recorded local review data.",
		DetailedDescription: "Detailed PR body.",
	})
	if !errors.Is(err, taskstate.ErrCompletionConflict) {
		t.Fatalf("conflicting completion error = %v, want ErrCompletionConflict", err)
	}
}

func assertCompletionStateLoaded(t *testing.T, store taskstate.Store) {
	t.Helper()

	loaded, err := store.Load("alpha", "op-1")
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if len(loaded.Runs) != 1 || loaded.Runs[0].Completion == nil || len(loaded.Events) != 3 {
		t.Fatalf("loaded state = %#v, want one completed run and three events", loaded)
	}
}

func assertStoreYAMLContains(t *testing.T, store taskstate.Store, repoID, taskID string, wants ...string) {
	t.Helper()

	statePath, err := store.Path(repoID, taskID)
	if err != nil {
		t.Fatalf("state path: %v", err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read YAML: %v", err)
	}
	text := string(data)
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("YAML missing %q:\n%s", want, text)
		}
	}
}

func assertStoreYAMLNotContains(t *testing.T, store taskstate.Store, repoID, taskID string, values ...string) {
	t.Helper()

	statePath, err := store.Path(repoID, taskID)
	if err != nil {
		t.Fatalf("state path: %v", err)
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read YAML: %v", err)
	}
	text := string(data)
	for _, value := range values {
		if strings.Contains(text, value) {
			t.Fatalf("YAML contains %q:\n%s", value, text)
		}
	}
}
