package agent_test

import (
	"testing"
	"time"

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

func TestEstimateUsageCostAtSelectsEffectiveDatedPricingInUTC(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		startedAt     time.Time
		amount        int64
		effectiveDate string
		inputRate     string
		cachedRate    string
		outputRate    string
	}{
		{
			name: "Terra launch rate", model: "gpt-5.6-terra",
			startedAt: time.Date(2026, 7, 30, 0, 30, 0, 0, time.FixedZone("UTC+1", 3600)),
			amount:    17_750_000, effectiveDate: "2026-07-09", inputRate: "2.5", cachedRate: "0.25", outputRate: "15",
		},
		{
			name: "Terra reduced rate", model: "gpt-5.6-terra",
			startedAt: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
			amount:    14_200_000, effectiveDate: "2026-07-30", inputRate: "2", cachedRate: "0.2", outputRate: "12",
		},
		{
			name: "Luna launch rate", model: "gpt-5.6-luna",
			startedAt: time.Date(2026, 7, 29, 23, 59, 59, 0, time.UTC),
			amount:    7_100_000, effectiveDate: "2026-07-09", inputRate: "1", cachedRate: "0.1", outputRate: "6",
		},
		{
			name: "Luna reduced rate", model: "gpt-5.6-luna",
			startedAt: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
			amount:    1_420_000, effectiveDate: "2026-07-30", inputRate: "0.2", cachedRate: "0.02", outputRate: "1.2",
		},
	}

	usage := taskstate.AgentUsage{InputTokens: 2_000_000, CachedInputTokens: 1_000_000, OutputTokens: 1_000_000}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cost, ok := agent.EstimateUsageCostAt(test.model, usage, test.startedAt)
			if !ok {
				t.Fatal("estimate cost ok = false, want true")
			}
			if cost.AmountMicroUSD != test.amount ||
				cost.Pricing.EffectiveDate != test.effectiveDate ||
				cost.Pricing.InputUSDPerMillionTokens != test.inputRate ||
				cost.Pricing.CachedUSDPerMillionTokens != test.cachedRate ||
				cost.Pricing.OutputUSDPerMillionTokens != test.outputRate {
				t.Fatalf("cost = %#v, want selected effective price", cost)
			}
			if cost.Pricing.Source == "" || cost.Pricing.SourceURL == "" || cost.Pricing.SourceAccessed == "" {
				t.Fatalf("pricing provenance = %#v, want source provenance", cost.Pricing)
			}
		})
	}
}

func TestEstimateUsageCostAtLeavesEffectiveDatedHistoryWithoutAPriceUnknown(t *testing.T) {
	usage := taskstate.AgentUsage{InputTokens: 1}
	for _, startedAt := range []time.Time{
		time.Time{},
		time.Date(2026, 7, 8, 23, 59, 59, 0, time.UTC),
	} {
		if _, ok := agent.EstimateUsageCostAt("gpt-5.6-terra", usage, startedAt); ok {
			t.Fatalf("estimate at %s ok = true, want unknown", startedAt)
		}
	}
}

func TestEstimateCodexUsageCostAtPersistsEffectivePricingSnapshot(t *testing.T) {
	stored, ok := agent.EstimateCodexUsageCostAt(
		"gpt-5.6-luna",
		taskstate.AgentUsage{InputTokens: 1_000_000},
		time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC),
	)
	if !ok || stored.Pricing == nil {
		t.Fatalf("stored cost = %#v, ok = %t; want pricing snapshot", stored, ok)
	}
	if stored.Pricing.EffectiveDate != "2026-07-30" ||
		stored.Pricing.InputUSDPerMillionTokens != "0.2" ||
		stored.Pricing.Source == "" ||
		stored.Pricing.SourceAccessed == "" {
		t.Fatalf("pricing snapshot = %#v, want selected effective version and provenance", stored.Pricing)
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

func TestEstimateUsageCostPricesCapturedCachedInputWithoutInputTotal(t *testing.T) {
	cost, ok := agent.EstimateUsageCost("gpt-5", taskstate.AgentUsage{CachedInputTokens: 1_000_000})
	if !ok || cost.AmountMicroUSD != 125_000 {
		t.Fatalf("cached-only cost = %#v, ok = %t; want $0.125000", cost, ok)
	}
}

func TestEstimateUsageCostLeavesTotalOnlyUsageUnpriced(t *testing.T) {
	_, ok := agent.EstimateUsageCost("gpt-5", taskstate.AgentUsage{TotalTokens: 100})
	if ok {
		t.Fatal("estimate total-only usage ok = true, want false")
	}
	resolved := agent.ResolveExecutionUsageCost(taskstate.AgentExecution{
		Harness: "codex", Model: "gpt-5", Usage: &taskstate.AgentUsage{TotalTokens: 100},
	})
	if resolved.Known || resolved.UnknownReason != agent.UsageCostUnknownBillableUsageMissing {
		t.Fatalf("resolved = %#v, want unknown billable usage", resolved)
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

func TestEstimateCodexUsageCostPersistsCompletePricingSnapshot(t *testing.T) {
	stored, ok := agent.EstimateCodexUsageCost("gpt-5", taskstate.AgentUsage{
		InputTokens: 1,
	})
	if !ok {
		t.Fatal("estimate stored Codex cost ok = false, want true")
	}
	if stored.AmountMicroUSD != 1 || stored.Pricing == nil {
		t.Fatalf("stored cost = %#v, want amount and pricing snapshot", stored)
	}
	if stored.Pricing.Provider != "openai" ||
		stored.Pricing.Model != "gpt-5" ||
		stored.Pricing.ServiceTier != "standard" ||
		stored.Pricing.InputUSDPerMillionTokens != "1.25" ||
		stored.Pricing.CachedUSDPerMillionTokens != "0.125" ||
		stored.Pricing.OutputUSDPerMillionTokens != "10" ||
		stored.Pricing.ReasoningOutputTreatment == "" ||
		stored.Pricing.Source == "" ||
		stored.Pricing.SourcePublished == "" {
		t.Fatalf("pricing snapshot = %#v, want complete persisted metadata", stored.Pricing)
	}
}

func TestResolveExecutionUsageCostKeepsStoredEstimateWhenUsageFactsChange(t *testing.T) {
	stored, ok := agent.EstimateCodexUsageCost("gpt-5", taskstate.AgentUsage{InputTokens: 100, OutputTokens: 50})
	if !ok {
		t.Fatal("estimate stored cost ok = false, want true")
	}
	resolved := agent.ResolveExecutionUsageCost(taskstate.AgentExecution{
		Harness:   "codex",
		Model:     "gpt-5-nano",
		Usage:     &taskstate.AgentUsage{InputTokens: 1},
		UsageCost: stored,
	})
	if !resolved.Known || resolved.Cost.AmountMicroUSD != stored.AmountMicroUSD {
		t.Fatalf("resolved = %#v, want stored amount %d", resolved, stored.AmountMicroUSD)
	}
	if resolved.Cost.Pricing.Model != "gpt-5" {
		t.Fatalf("resolved pricing = %#v, want stored gpt-5 snapshot", resolved.Cost.Pricing)
	}
}

func TestResolveExecutionUsageCostUsesStoredZeroEstimate(t *testing.T) {
	stored, ok := agent.EstimateCodexUsageCost("gpt-5-nano", taskstate.AgentUsage{InputTokens: 1})
	if !ok || stored.AmountMicroUSD != 0 {
		t.Fatalf("stored zero cost = %#v, ok = %t; want known zero", stored, ok)
	}
	resolved := agent.ResolveExecutionUsageCost(taskstate.AgentExecution{
		Harness:   "codex",
		Model:     "unknown-model-that-must-not-be-priced",
		Usage:     &taskstate.AgentUsage{InputTokens: 999},
		UsageCost: stored,
	})
	if !resolved.Known || resolved.Cost.AmountMicroUSD != 0 {
		t.Fatalf("resolved = %#v, want known stored zero", resolved)
	}
	if resolved.Cost.Pricing.Model != "gpt-5-nano" {
		t.Fatalf("stored pricing = %#v, want gpt-5-nano snapshot", resolved.Cost.Pricing)
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
