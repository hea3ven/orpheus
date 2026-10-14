package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/taskstats"
)

func TestTaskHistoryEventDisplaysFreshFallbackReason(t *testing.T) {
	event := taskstate.Event{Type: taskstate.EventRunStarted, Attempt: 2}
	runs := []taskstate.RunAttempt{{
		Attempt: 2,
		Execution: taskstate.AgentExecution{Launch: &taskstate.AgentLaunch{
			Mode:           taskstate.AgentLaunchFresh,
			FallbackReason: "session log was deleted",
		}},
	}}

	got := taskHistoryEventDisplay(event, runs)
	want := "Run attempt 2 started (fresh); resume fallback: session log was deleted"
	if got != want {
		t.Fatalf("history = %q, want %q", got, want)
	}
}

func TestRenderedTaskStatsDisplaysResumeProvenance(t *testing.T) {
	state := taskstate.TaskState{Runs: []taskstate.RunAttempt{{
		Attempt: 2, Status: taskstate.RunStatusSucceeded,
		Execution: taskstate.AgentExecution{
			Profile: "implementer", Harness: "pi",
			Launch: &taskstate.AgentLaunch{Mode: taskstate.AgentLaunchResumed, SourceRunAttempt: 1, SourceSession: &taskstate.AgentSession{ID: "session-1", LogPath: "/fixture/session.jsonl"}},
		},
	}}}
	var output bytes.Buffer

	if err := renderTaskStats(&output, state); err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"LAUNCH_MODE", "RESUME_SOURCE", "RESUME_FALLBACK", "resumed", "run 1 / session session-1"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("stats missing %q:\n%s", want, output.String())
		}
	}
}

func TestTaskStatsCohortPresentationDistinguishesUnknownKnownAndZero(t *testing.T) {
	for _, test := range []struct {
		name                 string
		known, samples       int
		tokens, cost         int64
		wantTokens, wantCost string
	}{
		{"partial", 1, 2, 150, 625, "150", "$0.000625"},
		{"unknown", 0, 1, 0, 0, "-", "-"},
		{"known zero", 1, 1, 0, 0, "0", "$0.000000"},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotTokens := formatTaskStatsTotalTokens(taskstats.IntCohort{Known: test.known, Samples: test.samples, Total: int(test.tokens)})
			gotCost := formatTaskStatsTotalCost(taskstats.CostCohort{Known: test.known, Samples: test.samples, TotalMicroUSD: test.cost})
			if gotTokens != test.wantTokens || gotCost != test.wantCost {
				t.Fatalf("tokens/cost = %q / %q, want %q / %q", gotTokens, gotCost, test.wantTokens, test.wantCost)
			}
		})
	}
}
