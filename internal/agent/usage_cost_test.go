package agent_test

import (
	"testing"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

func TestEstimateUsageCostUsesKnownModelPricing(t *testing.T) {
	cost, ok := agent.EstimateUsageCost("openai-codex/gpt-5:medium", taskstate.AgentUsage{
		InputTokens:           123,
		CachedInputTokens:     45,
		OutputTokens:          67,
		ReasoningOutputTokens: 8,
		TotalTokens:           190,
	})
	if !ok {
		t.Fatal("estimate cost ok = false, want true")
	}
	if cost.Kind != agent.UsageCostKindEstimatedAPIEquivalent {
		t.Fatalf("cost kind = %q, want estimated API-equivalent", cost.Kind)
	}
	if cost.Currency != "USD" {
		t.Fatalf("currency = %q, want USD", cost.Currency)
	}
	if cost.AmountMicroUSD != 773 {
		t.Fatalf("amount_micro_usd = %d, want 773", cost.AmountMicroUSD)
	}
	if cost.Pricing.Model != "gpt-5" ||
		cost.Pricing.InputUSDPerMillionTokens != "1.25" ||
		cost.Pricing.CachedUSDPerMillionTokens != "0.125" ||
		cost.Pricing.OutputUSDPerMillionTokens != "10" {
		t.Fatalf("pricing metadata = %#v, want GPT-5 rates", cost.Pricing)
	}
	if cost.Pricing.Source == "" || cost.Pricing.SourceURL == "" {
		t.Fatalf("pricing source metadata = %#v, want source context", cost.Pricing)
	}
}

func TestEstimateUsageCostUsesReasoningOutputWhenOutputIsMissing(t *testing.T) {
	cost, ok := agent.EstimateUsageCost("gpt-5.4-mini", taskstate.AgentUsage{
		ReasoningOutputTokens: 10,
	})
	if !ok {
		t.Fatal("estimate cost ok = false, want true")
	}
	if cost.AmountMicroUSD != 45 {
		t.Fatalf("amount_micro_usd = %d, want 45", cost.AmountMicroUSD)
	}
	if cost.Pricing.ReasoningOutputTreatment == "" {
		t.Fatalf("reasoning output treatment = %q, want metadata", cost.Pricing.ReasoningOutputTreatment)
	}
}

func TestEstimateUsageCostLeavesUnknownModelUnpriced(t *testing.T) {
	_, ok := agent.EstimateUsageCost("vendor/unknown-model", taskstate.AgentUsage{
		InputTokens:  100,
		OutputTokens: 50,
	})
	if ok {
		t.Fatal("estimate cost ok = true, want false")
	}
}

func TestResolveExecutionUsageCostUsesStoredCost(t *testing.T) {
	resolved := agent.ResolveExecutionUsageCost(taskstate.AgentExecution{
		Harness: "pi",
		Model:   "openai-codex/gpt-5.5",
		Usage:   &taskstate.AgentUsage{InputTokens: 100, OutputTokens: 50},
		UsageCost: &taskstate.AgentUsageCost{
			Kind:           agent.UsageCostKindPiReportedEstimated,
			Currency:       "USD",
			AmountMicroUSD: 1240,
			Source:         "Pi usage.cost.total",
		},
	})

	if !resolved.Known {
		t.Fatalf("known = false, want true; reason = %q", resolved.UnknownReason)
	}
	if resolved.Cost.Kind != agent.UsageCostKindPiReportedEstimated {
		t.Fatalf("cost kind = %q, want Pi reported estimate", resolved.Cost.Kind)
	}
	if resolved.Cost.AmountMicroUSD != 1240 {
		t.Fatalf("amount_micro_usd = %d, want 1240", resolved.Cost.AmountMicroUSD)
	}
}

func TestResolveExecutionUsageCostLeavesPiWithoutStoredCostUnknown(t *testing.T) {
	resolved := agent.ResolveExecutionUsageCost(taskstate.AgentExecution{
		Harness: "pi",
		Model:   "openai-codex/gpt-5.5",
		Usage:   &taskstate.AgentUsage{InputTokens: 100, OutputTokens: 50},
	})

	if resolved.Known {
		t.Fatalf("known = true, want false; cost = %#v", resolved.Cost)
	}
	if resolved.UnknownReason != agent.UsageCostUnknownPiReportedCostMissing {
		t.Fatalf(
			"unknown reason = %q, want %q",
			resolved.UnknownReason,
			agent.UsageCostUnknownPiReportedCostMissing,
		)
	}
}

func TestResolveExecutionUsageCostFallsBackToPricingForNonPi(t *testing.T) {
	resolved := agent.ResolveExecutionUsageCost(taskstate.AgentExecution{
		Harness: "codex",
		Model:   "openai-codex/gpt-5:medium",
		Usage:   &taskstate.AgentUsage{InputTokens: 100, OutputTokens: 50},
	})

	if !resolved.Known {
		t.Fatalf("known = false, want true; reason = %q", resolved.UnknownReason)
	}
	if resolved.Cost.Kind != agent.UsageCostKindEstimatedAPIEquivalent {
		t.Fatalf("cost kind = %q, want estimated API-equivalent", resolved.Cost.Kind)
	}
}

func TestResolveExecutionUsageCostDoesNotFallbackPastInvalidStoredCost(t *testing.T) {
	resolved := agent.ResolveExecutionUsageCost(taskstate.AgentExecution{
		Harness:   "codex",
		Model:     "openai-codex/gpt-5:medium",
		Usage:     &taskstate.AgentUsage{InputTokens: 100, OutputTokens: 50},
		UsageCost: &taskstate.AgentUsageCost{Kind: agent.UsageCostKindEstimatedAPIEquivalent},
	})

	if resolved.Known {
		t.Fatalf("known = true, want false; cost = %#v", resolved.Cost)
	}
	if resolved.UnknownReason != agent.UsageCostUnknownStoredCostInvalid {
		t.Fatalf(
			"unknown reason = %q, want %q",
			resolved.UnknownReason,
			agent.UsageCostUnknownStoredCostInvalid,
		)
	}
}
