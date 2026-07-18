package cli

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstats"
)

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
