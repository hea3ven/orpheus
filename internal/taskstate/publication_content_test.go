package taskstate_test

import (
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImplementationHandoffReplacesOnlySuppliedPublicationFieldsUntilFirstProceed(t *testing.T) {
	for _, consented := range []bool{false, true} {
		name := "passed review without consent"
		if consented {
			name = "earlier consent survives renewed publication attempt"
		}
		t.Run(name, func(t *testing.T) {
			for _, update := range []struct {
				name                       string
				subject, body, description string
			}{
				{name: "no publication update"},
				{name: "subject", subject: "feat: validated task filters"},
				{name: "body", body: "Cumulative public rationale.\n"},
				{name: "PR description", description: "## Cumulative outcome\n\nUpdated user-facing description.\n"},
			} {
				t.Run(update.name, func(t *testing.T) {
					history, err := testutil.DeliveredPRWork("repo", "task", "/fixture/task", "task", "https://example.test/pr/1")
					require.NoError(t, err)
					w := &history.Workflows[0]
					w.Publication = &work.PublicationContent{CommitSubject: "feat: task filters", CommitBody: "Original public rationale", PRDescription: "Original cumulative description"}
					// Arrange a passed review independently of this handoff. A renewed
					// unconsented selection must not unlock an earlier proceed.
					p := w.Phases[3].(*work.PublicationPhase)
					p.Consent, p.Delivery = nil, nil
					w.Phases = w.Phases[:4]
					if consented {
						require.NoError(t, history.AuthorizePublication(0, work.PublicationRef{Phase: 3}, time.Date(2026, 1, 1, 0, 0, 7, 0, time.UTC), "Operator proceed"))
						w.Phases = append(w.Phases, &work.PublicationPhase{Review: p.Review, Input: p.Input, Destination: p.Destination, Comparison: p.Comparison, Target: p.Target})
					}
					ref := work.AttemptRef{RepositoryID: "repo", TaskID: "task", Phase: len(w.Phases)}
					w.Phases = append(w.Phases, &work.ImplementationPhase{Input: &work.OutputRef{Phase: 1}, Execution: work.AgentExecution{StartedAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), Process: &work.RunningProcess{}}})
					store, raw := newPolicyStore(t, history)
					before, err := raw.Load("repo", "task")
					require.NoError(t, err)
					opts := taskstate.CompleteRunOptions{CommitSubject: "fix: handle unknown repositories", CommitBody: "Local repair rationale", TechnicalExplanation: "Technical repair detail", PublicationCommitSubject: update.subject, PublicationCommitBody: update.body, PRDescription: update.description}

					result, err := store.CompleteImplementation(ref, opts)

					if consented && update.name != "no publication update" {
						require.ErrorContains(t, err, "read-only after operator proceed")
						preserved, loadErr := raw.Load("repo", "task")
						require.NoError(t, loadErr)
						assert.Equal(t, before, preserved, "refusal must preserve both publication and local handoff state")
						opts.PublicationCommitSubject, opts.PublicationCommitBody, opts.PRDescription = "", "", ""
						result, err = store.CompleteImplementation(ref, opts)
					}
					require.NoError(t, err)
					assert.Equal(t, "fix: handle unknown repositories", result.Completion.CommitSubject)
					assert.Equal(t, "Local repair rationale", result.Completion.CommitBody)
					after, err := raw.Load("repo", "task")
					require.NoError(t, err)
					expected := work.PublicationContent{CommitSubject: "feat: task filters", CommitBody: "Original public rationale", PRDescription: "Original cumulative description"}
					if !consented {
						switch update.name {
						case "subject":
							expected.CommitSubject = "feat: validated task filters"
						case "body":
							expected.CommitBody = "Cumulative public rationale.\n"
						case "PR description":
							expected.PRDescription = "## Cumulative outcome\n\nUpdated user-facing description.\n"
						}
					}
					assert.Equal(t, expected, *after.Workflows[0].Publication)
					assert.Equal(t, before.Workflows[0].Phases[:ref.Phase], after.Workflows[0].Phases[:ref.Phase], "completion cannot rewrite review, consent, or prior efforts")
					_, err = store.CompleteImplementation(ref, taskstate.CompleteRunOptions{CommitSubject: "ignored retry", CommitBody: "Ignored", PRDescription: "Ignored publication replacement"})
					require.NoError(t, err)
					repeated, err := raw.Load("repo", "task")
					require.NoError(t, err)
					assert.Equal(t, after, repeated, "same-attempt retry is still authoritative first-handoff no-op")
				})
			}
		})
	}
}
