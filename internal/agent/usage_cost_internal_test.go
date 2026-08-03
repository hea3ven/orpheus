package agent

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestResolveExecutionUsageCostDoesNotRepriceStoredEstimate(t *testing.T) {
	usage := taskstate.AgentUsage{InputTokens: 100, OutputTokens: 50}
	stored, ok := EstimateCodexUsageCost("gpt-5", usage)
	if !ok {
		t.Fatal("estimate stored cost ok = false, want true")
	}

	original := usagePrices["gpt-5"]
	usagePrices["gpt-5"] = usagePrice{
		model: "gpt-5", inputMicroUSDPerMillion: 999_000_000, outputMicroUSDPerMillion: 999_000_000,
	}
	t.Cleanup(func() { usagePrices["gpt-5"] = original })

	resolved := ResolveExecutionUsageCost(taskstate.AgentExecution{
		Harness: "codex", Model: "gpt-5", Usage: &usage, UsageCost: stored,
	})
	if !resolved.Known || resolved.Cost.AmountMicroUSD != stored.AmountMicroUSD {
		t.Fatalf("resolved = %#v, want unchanged stored amount %d", resolved, stored.AmountMicroUSD)
	}
	if resolved.Cost.Pricing.InputUSDPerMillionTokens != "1.25" {
		t.Fatalf("stored pricing = %#v, want original pricing snapshot", resolved.Cost.Pricing)
	}
}
