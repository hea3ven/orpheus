package cli

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/taskstats"
)

func TestTaskStatsRowDisplaysResumeProvenance(t *testing.T) {
	record := taskStatsExecutionRecord{
		activity: "implementation",
		attempt:  "2",
		step:     "-",
		status:   "succeeded",
		execution: taskstate.AgentExecution{
			Profile: "implementer",
			Harness: "pi",
			Launch: &taskstate.AgentLaunch{
				Mode:             taskstate.AgentLaunchResumed,
				SourceRunAttempt: 1,
				SourceSession:    &taskstate.AgentSession{ID: "session-1", LogPath: "/tmp/session.jsonl"},
			},
		},
	}

	row := taskStatsRow(record)
	if row[11] != "resumed" || row[12] != "run 1 / session session-1" || row[13] != "-" {
		t.Fatalf("resume columns = %#v", row[11:14])
	}
}

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

func TestFormatTaskStatsTotalTokensShowsPartialKnownTotals(t *testing.T) {
	got := formatTaskStatsTotalTokens(taskstats.IntCohort{Total: 150, Samples: 1})
	if got != "150" {
		t.Fatalf("total tokens = %q, want %q", got, "150")
	}
}

func TestFormatTaskStatsTotalTokensHidesAllUnknownZeroTotals(t *testing.T) {
	got := formatTaskStatsTotalTokens(taskstats.IntCohort{Samples: 1})
	if got != "-" {
		t.Fatalf("total tokens = %q, want %q", got, "-")
	}
}

func TestFormatTaskStatsTotalCostShowsPartialKnownTotals(t *testing.T) {
	got := formatTaskStatsTotalCost(taskstats.CostCohort{TotalMicroUSD: 625, Samples: 1})
	if got != "$0.000625" {
		t.Fatalf("total cost = %q, want %q", got, "$0.000625")
	}
}

func TestFormatTaskStatsTotalCostHidesAllUnknownZeroTotals(t *testing.T) {
	got := formatTaskStatsTotalCost(taskstats.CostCohort{Samples: 1})
	if got != "-" {
		t.Fatalf("total cost = %q, want %q", got, "-")
	}
}
