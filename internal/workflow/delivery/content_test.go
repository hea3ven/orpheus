package delivery_test

import (
	"fmt"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/delivery"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestBuildPublicationPullRequestContentUsesWorkflowContentAndValidatesTitlePolicy(t *testing.T) {
	for _, tt := range []struct{ name, template, externalRef, title, wantError string }{
		{name: "default title", title: "Ready for review"},
		{name: "custom title", template: "[{{external_ref}}] {{summary}}", externalRef: "\n TREX-1234\t", title: "[TREX-1234] Ready for review"},
		{name: "missing required reference", template: "[{{external_ref}}] {{summary}}", wantError: "requires a task external reference"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			item := task.Task{ID: "op-1", Title: "Create PR", Description: "Implement publication.", AcceptanceCriteria: "No duplicate PRs.", ExternalRef: tt.externalRef}
			authored := &work.PublicationContent{CommitSubject: "Ready for review", CommitBody: "Public commit rationale", PRDescription: "Detailed PR body."}

			content, err := delivery.BuildPublicationPullRequestContent(tt.template, item, authored)

			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("build error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if content.Title != tt.title || content.Body != "Detailed PR body." {
				t.Fatalf("content = %#v, want title %q and exact completion body", content, tt.title)
			}
		})
	}
}

func TestBuildPublicationPullRequestContentFromStateFormatsReviewProcess(t *testing.T) {
	finishedAt := time.Date(2026, 6, 10, 11, 0, 0, 0, time.UTC)
	snapshot := taskstate.TaskState{
		Runs: []taskstate.RunAttempt{
			{
				Attempt: 1,
				Completion: &taskstate.Completion{
					CommitSubject:        "Original summary",
					TechnicalExplanation: "Technical explanation.",
				},
			},
			{
				Attempt: 2,
				Completion: &taskstate.Completion{
					CommitSubject:        "Fix review findings",
					CommitBody:           "Addressed review feedback.",
					TechnicalExplanation: "Technical explanation.",
				},
				ReviewFollowUp: &taskstate.ReviewFollowUp{ReviewAttempt: 1},
			},
		},
		Reviews: []taskstate.ReviewAttempt{
			{
				Attempt:    1,
				Status:     taskstate.ReviewStatusPassed,
				Pipeline:   "default",
				Step:       "manual-review",
				StartedAt:  time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
				FinishedAt: &finishedAt,
				Steps: []taskstate.ReviewStep{
					{Kind: "manual", Name: "manual-review"},
				},
				Findings: []taskstate.ReviewFinding{
					{
						Type:                 taskstate.FindingTypeBlocking,
						Title:                "Fixed blocker",
						Description:          "Finding details must be omitted.",
						Step:                 "manual-review",
						TargetedByRunAttempt: 2,
					},
					{
						Type:   taskstate.FindingTypeBlocking,
						Title:  "Waived blocker",
						Step:   "manual-review",
						Waiver: "Accepted for this task.",
					},
					{
						Type:              taskstate.FindingTypeBlocking,
						Title:             "Manually addressed blocker",
						Step:              "manual-review",
						AddressedManually: "Verified direct repair.",
					},
					{
						Type:  taskstate.FindingTypeBlocking,
						Title: "Unfixed blocker",
						Step:  "manual-review",
					},
					{
						Type:  taskstate.FindingTypeAdvisory,
						Title: "Advisory note",
						Step:  "manual-review",
					},
					{
						Type:          taskstate.FindingTypeSeparateTask,
						Title:         "Future cleanup",
						Step:          "manual-review",
						CreatedTaskID: "op-2",
					},
				},
			},
			{
				Attempt:    2,
				Status:     taskstate.ReviewStatusFailed,
				Pipeline:   "default",
				Step:       "lint",
				StartedAt:  time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC),
				FinishedAt: &finishedAt,
				Steps: []taskstate.ReviewStep{
					{Kind: "check", Name: "lint"},
				},
			},
			{
				Attempt:    3,
				Status:     taskstate.ReviewStatusPassed,
				Pipeline:   "default",
				Step:       "agent-review",
				StartedAt:  time.Date(2026, 6, 10, 13, 0, 0, 0, time.UTC),
				FinishedAt: &finishedAt,
				Steps: []taskstate.ReviewStep{
					{Kind: "agent_review", Name: "agent-review"},
				},
				Findings: []taskstate.ReviewFinding{
					{
						Type:                 taskstate.FindingTypeBlocking,
						Title:                "Resolved blocker",
						Step:                 "agent-review",
						TargetedByRunAttempt: 2,
					},
					{
						Type:            taskstate.FindingTypeAdvisory,
						Title:           "Downgraded blocker",
						Step:            "agent-review",
						DowngradeReason: "False positive for this task.",
					},
					{
						Type:   taskstate.FindingTypeBlocking,
						Title:  "Waived automated blocker",
						Step:   "agent-review",
						Waiver: "Accepted for this task.",
					},
				},
			},
		},
	}
	history := testutil.ReviewableWork("alpha", "op-1", "/fixture/repo", "orpheus/op-1")
	history.Workflows[0].Publication = &work.PublicationContent{CommitSubject: "Original summary", CommitBody: "Original public rationale", PRDescription: "Original PR body."}
	snapshot.Work = &history
	snapshot.CurrentWorkflow = 0
	snapshot.Runs[0].Address = work.AttemptRef{RepositoryID: "alpha", TaskID: "op-1", Workflow: 0, Phase: 1}
	snapshot.Runs[1].Address = work.AttemptRef{RepositoryID: "alpha", TaskID: "op-1", Workflow: 0, Phase: 3}

	for _, include := range []bool{true, false} {
		t.Run(fmt.Sprintf("include review=%t", include), func(t *testing.T) {
			content, err := delivery.BuildPublicationPullRequestContentFromStateWithOptions(delivery.PublicationOptions{IncludeReviewProcess: include}, task.Task{ID: "op-1"}, snapshot)

			require.NoError(t, err)
			assert.Equal(t, "Original summary", content.Title)
			if !include {
				assert.Equal(t, "Original PR body.", content.Body, "omitting review process must preserve the exact completion body")
				return
			}
			assert.True(t, strings.HasPrefix(content.Body, "Original PR body."), "completion must appear before the review history")
			assertPublicationReviewProcess(t, content.Body)
		})
	}
}

func assertPublicationReviewProcess(t *testing.T, body string) {
	t.Helper()
	for _, want := range []string{
		"### Review attempt 1 — passed",
		"- ❌ `manual-review`",
		"  - **Blocking:** Fixed blocker",
		"    - Fixed by run attempt 2",
		"  - **Blocking (waived):** Waived blocker",
		"    - Waived.",
		"  - **Blocking (addressed manually):** Manually addressed blocker",
		"    - Addressed manually.",
		"  - **Blocking:** Unfixed blocker",
		"    - No targeted fix run recorded.",
		"  - **Advisory:** Advisory note",
		"  - **Separate task:** Future cleanup",
		"    - Created task: op-2",
		"  **Fix run attempt 2**",
		"  - Summary: `Fix review findings`",
		"  - Description: Addressed review feedback.",
		"### Review attempt 2 — failed",
		"- ⚠️ `lint`",
		"### Review attempt 3 — passed",
		"- ✅ `agent-review`",
		"  - **Blocking:** Resolved blocker",
		"  - **Advisory (downgraded):** Downgraded blocker",
		"    - Downgraded to advisory.",
		"  - **Blocking (waived):** Waived automated blocker",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("body missing %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{
		"Finding details must be omitted.",
		"Technical explanation.",
		"Accepted for this task.",
	} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("body = %q, should not contain %q", body, unwanted)
		}
	}
}
