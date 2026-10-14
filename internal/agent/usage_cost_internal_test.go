package agent

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestResolveExecutionUsageCostDoesNotRepriceStoredEstimate(t *testing.T) {
	stored := &taskstate.AgentUsageCost{
		Kind: UsageCostKindEstimatedAPIEquivalent, Currency: "USD", AmountMicroUSD: 17,
		Pricing: &taskstate.AgentUsagePricing{
			Model: "gpt-5", InputUSDPerMillionTokens: "0.01", OutputUSDPerMillionTokens: "0.32",
			Source: "historical price snapshot", EffectiveDate: "2025-01-01",
		},
	}

	resolved := ResolveExecutionUsageCost(taskstate.AgentExecution{
		Harness: "codex", Model: "gpt-5",
		Usage: &taskstate.AgentUsage{InputTokens: 100, OutputTokens: 50}, UsageCost: stored,
	})

	if !resolved.Known || resolved.Cost.AmountMicroUSD != 17 {
		t.Fatalf("resolved = %#v, want stored historical amount 17", resolved)
	}
	if resolved.Cost.Pricing.InputUSDPerMillionTokens != "0.01" ||
		resolved.Cost.Pricing.OutputUSDPerMillionTokens != "0.32" ||
		resolved.Cost.Pricing.EffectiveDate != "2025-01-01" {
		t.Fatalf("pricing = %#v, want unchanged historical snapshot", resolved.Cost.Pricing)
	}
}
