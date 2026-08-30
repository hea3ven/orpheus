package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestResolveDetailedDescriptionReadsFileExactly(t *testing.T) {
	body := "## Summary\n\nPreserve trailing newline.\n"
	path := filepath.Join(testutil.CanonicalTempDir(t), "body.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write detailed description: %v", err)
	}

	got, err := resolveDetailedDescription("", path)
	if err != nil {
		t.Fatalf("resolve detailed description: %v", err)
	}
	if got != body {
		t.Fatalf("detailed description = %q, want exact file content %q", got, body)
	}
}

func TestResolveTechnicalExplanationReadsFileExactly(t *testing.T) {
	body := "## Technical pitch\n\nPreserve trailing newline.\n"
	path := filepath.Join(testutil.CanonicalTempDir(t), "technical.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write technical explanation: %v", err)
	}

	got, err := resolveTechnicalExplanation("", path)
	if err != nil {
		t.Fatalf("resolve technical explanation: %v", err)
	}
	if got != body {
		t.Fatalf("technical explanation = %q, want exact file content %q", got, body)
	}
}

func TestPersistAgentReviewFindingSelectsReviewerStore(t *testing.T) {
	tests := []struct {
		name         string
		role         string
		wantMethod   string
		wantReviewer string
	}{
		{name: "primary reviewer", role: "primary", wantMethod: "primary", wantReviewer: "primary"},
		{name: "alternate reviewer", role: "alternate", wantMethod: "alternate"},
		{name: "unspecified reviewer", wantMethod: "primary"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &recordingAgentReviewFindingStore{attempt: taskstate.ReviewAttempt{Attempt: 3}}
			ctx := agent.ReviewContext{
				Repository: agent.ContextRepository{ID: "alpha"},
				Task:       agent.ContextTask{ID: "op-1"},
				Review:     agent.ContextReview{Attempt: 3, EnvStep: "ai-review"},
			}

			attempt, err := persistAgentReviewFinding(store, ctx, test.role, taskstate.ReviewFinding{Title: "Finding"})
			if err != nil {
				t.Fatalf("persist finding: %v", err)
			}
			if attempt.Attempt != 3 {
				t.Fatalf("attempt = %d, want 3", attempt.Attempt)
			}
			if store.method != test.wantMethod {
				t.Fatalf("method = %q, want %q", store.method, test.wantMethod)
			}
			if store.repoID != "alpha" || store.taskID != "op-1" || store.reviewAttempt != 3 {
				t.Fatalf("persistence target = %s/%s attempt %d", store.repoID, store.taskID, store.reviewAttempt)
			}
			if store.finding.Reviewer != test.wantReviewer {
				t.Fatalf("reviewer = %q, want %q", store.finding.Reviewer, test.wantReviewer)
			}
			if test.role == "alternate" && store.step != "ai-review" {
				t.Fatalf("alternate step = %q, want ai-review", store.step)
			}
		})
	}
}

func TestRenderAgentReviewAddResultUsesAuthoritativeFindingCount(t *testing.T) {
	tests := []struct {
		name        string
		findingType string
		findings    []taskstate.ReviewFinding
		want        string
	}{
		{
			name:        "primary append",
			findingType: "blocking",
			findings:    []taskstate.ReviewFinding{{}, {}},
			want:        "Recorded blocking review finding 2 for op-1.\n",
		},
		{
			name:        "alternate append does not change authoritative count",
			findingType: "advisory",
			want:        "Recorded advisory review finding 0 for op-1.\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := renderAgentReviewAddResult(
				&output,
				test.findingType,
				taskstate.ReviewAttempt{Findings: test.findings},
				"op-1",
			)
			if err != nil {
				t.Fatalf("render result: %v", err)
			}
			if output.String() != test.want {
				t.Fatalf("output = %q, want %q", output.String(), test.want)
			}
		})
	}
}

type recordingAgentReviewFindingStore struct {
	method        string
	repoID        string
	taskID        string
	reviewAttempt int
	step          string
	finding       taskstate.ReviewFinding
	attempt       taskstate.ReviewAttempt
}

func (s *recordingAgentReviewFindingStore) RecordReviewFinding(
	repoID string,
	taskID string,
	attempt int,
	finding taskstate.ReviewFinding,
) (taskstate.ReviewAttempt, error) {
	s.method = "primary"
	s.record(repoID, taskID, attempt, "", finding)
	return s.attempt, nil
}

func (s *recordingAgentReviewFindingStore) RecordAlternateReviewFinding(
	repoID string,
	taskID string,
	attempt int,
	step string,
	finding taskstate.ReviewFinding,
) (taskstate.ReviewAttempt, error) {
	s.method = "alternate"
	s.record(repoID, taskID, attempt, step, finding)
	return s.attempt, nil
}

func (s *recordingAgentReviewFindingStore) record(
	repoID string,
	taskID string,
	attempt int,
	step string,
	finding taskstate.ReviewFinding,
) {
	s.repoID = repoID
	s.taskID = taskID
	s.reviewAttempt = attempt
	s.step = step
	s.finding = finding
}
