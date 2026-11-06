//go:build integration

package cli_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationWorkflowReviewContinuationUsesNamedLivePositionWithoutRetrospectiveEffects(t *testing.T) {
	for _, boundary := range []struct {
		name, kind string
		answered   bool
	}{
		{name: "manual assessment", kind: "manual"},
		{name: "automated finding decision", kind: "check"},
		{name: "recorded manual answer", kind: "manual", answered: true},
	} {
		t.Run(boundary.name, func(t *testing.T) {
			kind := boundary.kind
			f := newReviewWorkflowFixture(t, "op-config", "Live review", "Continue only forward.")
			first := f.check("first", checkResult{})
			pending := map[string]any{"kind": kind, "name": "second"}
			answer := "a\n"
			wantChecks := []string{"first", "later"}
			if kind == "check" {
				pending["command"] = f.check("second", checkResult{code: 7})
				answer = "w\nAccepted risk.\n"
				wantChecks = []string{"first", "second", "later"}
			}
			steps := []map[string]any{{"kind": "check", "name": "first", "command": first}, pending}
			if !boundary.answered {
				steps = append(steps, map[string]any{"kind": "check", "name": "removed-later", "command": "must-not-run"})
			}
			f.pipelines("standard", map[string][]map[string]any{"standard": steps})
			f.run("p\n", "task", "run", "op-config")
			paused, _ := f.loadFinalTask("op-config")
			require.Len(t, paused.Reviews[0].Steps, 2)
			oldContract := paused.Work.Workflows[0].ReviewContracts[0]
			if boundary.answered {
				// Model interruption after the response persisted but before the
				// controller selected its next phase. The completed gate stays done.
				_, err := f.taskStore.RecordManualDecision(paused.Reviews[0].Steps[1].Address, taskstate.ReviewStatusPassed)
				require.NoError(t, err)
				assert.Equal(t, taskstate.ReviewStatusPassed, loadLatestReview(t, f, "op-config").Status)
				answer = ""
			}
			later := f.check("later", checkResult{})
			// The default changes, earlier policy changes and an earlier insertion
			// must not affect this continuation. Only the new later step is reached.
			f.pipelines("other", map[string][]map[string]any{
				"other": {{"kind": "check", "name": "wrong-configuration", "command": "must-not-run"}},
				"standard": {
					{"kind": "check", "name": "inserted-before", "command": "must-not-run"},
					{"kind": "agent_review", "name": "first", "agent": "missing-earlier-profile"},
					pending, {"kind": "check", "name": "later", "command": later},
				},
			})
			if !boundary.answered {
				f.run("p\n", "task", "run", "op-config")
				stillPending := loadLatestReview(t, f, "op-config")
				assert.Equal(t, paused.Reviews[0], stillPending, "configuration changes do not erase results, findings or the pending interaction")
			}

			f.run(answer, "task", "run", "op-config")

			final, item := f.loadFinalTask("op-config")
			require.Len(t, final.Reviews, 1)
			current := final.Reviews[0]
			assert.Equal(t, taskstate.ReviewStatusPassed, current.Status)
			assert.Equal(t, paused.Reviews[0].StartedAt, current.StartedAt)
			require.Len(t, current.Steps, 3)
			assert.Equal(t, paused.Reviews[0].Steps[0], current.Steps[0])
			assert.Equal(t, paused.Reviews[0].Steps[1].Address, current.Steps[1].Address)
			assert.Equal(t, "later", current.Steps[2].Name)
			assert.Equal(t, wantChecks, f.checkCalls)
			assert.Empty(t, f.agent.launches)
			assert.Equal(t, oldContract, final.Work.Workflows[0].ReviewContracts[0], "the original contract stays immutable history")
			assert.Empty(t, item.OrpheusMetadata().PRURL)
			assert.Empty(t, f.candidate.pushes)
			assert.Empty(t, f.tasks.closed)
			assertUnconsentedPublication(t, final)
		})
	}
}

func TestIntegrationWorkflowReviewContinuationRefusesMissingNamedPositionWithoutEffects(t *testing.T) {
	for _, change := range []string{"missing configuration", "missing step", "changed step type"} {
		t.Run(change, func(t *testing.T) {
			f := newReviewWorkflowFixture(t, "op-config", "Live review", "Restore the recorded position.")
			first := f.check("first", checkResult{})
			steps := []map[string]any{{"kind": "check", "name": "first", "command": first}, {"kind": "manual", "name": "second"}}
			f.pipelines("standard", map[string][]map[string]any{"standard": steps})
			f.run("p\n", "task", "run", "op-config")
			before, itemBefore := f.loadFinalTask("op-config")
			advice := "second"
			switch change {
			case "missing configuration":
				f.pipelines("replacement", map[string][]map[string]any{"replacement": steps})
				advice = "standard"
			case "missing step":
				f.pipelines("standard", map[string][]map[string]any{"standard": {steps[0], {"kind": "manual", "name": "replacement"}}})
			case "changed step type":
				f.pipelines("standard", map[string][]map[string]any{"standard": {steps[0], {"kind": "check", "name": "second", "command": "must-not-run"}}})
			}

			_, _, err := f.runError("a\n", "task", "run", "op-config")

			require.ErrorContains(t, err, advice)
			assert.Contains(t, err.Error(), "restore")
			assert.Contains(t, err.Error(), "config.yaml")
			after, itemAfter := f.loadFinalTask("op-config")
			assert.Equal(t, before, after, "refusal must preserve the pending interaction and all recorded results")
			assert.Equal(t, itemBefore, itemAfter)
			assert.Equal(t, []string{"first"}, f.checkCalls)
			assert.Empty(t, f.agent.launches)
			assert.Empty(t, f.candidate.pushes)
			assert.Empty(t, f.tasks.closed)

			f.pipelines("standard", map[string][]map[string]any{"standard": steps})
			f.run("a\n", "task", "run", "op-config")
			repaired, _ := f.loadFinalTask("op-config")
			require.Len(t, repaired.Reviews, 1)
			assert.Equal(t, taskstate.ReviewStatusPassed, repaired.Reviews[0].Status)
			assert.Equal(t, before.Reviews[0].StartedAt, repaired.Reviews[0].StartedAt)
			assert.Equal(t, work.ReviewManual, repaired.Work.Workflows[0].ReviewContracts[0].Slots[1].Kind)
			assert.Equal(t, []string{"first"}, f.checkCalls)
		})
	}
}
