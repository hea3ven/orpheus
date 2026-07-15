package agent

import (
	"strings"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

const (
	// UsageCostKindEstimatedAPIEquivalent marks token cost derived from public API rates.
	UsageCostKindEstimatedAPIEquivalent = "estimated_api_equivalent"

	// UsageCostKindPiReportedEstimated marks Pi's own usage.cost.total value.
	UsageCostKindPiReportedEstimated = "pi_reported_estimated"

	// UsageCostUnknownNoUsage means the execution has no captured token usage.
	UsageCostUnknownNoUsage = "usage_not_recorded"

	// UsageCostUnknownStoredCostInvalid means a stored cost was incomplete.
	UsageCostUnknownStoredCostInvalid = "stored_usage_cost_invalid"

	// UsageCostUnknownPiReportedCostMissing means Pi did not report cost.
	UsageCostUnknownPiReportedCostMissing = "pi_reported_cost_not_captured"

	// UsageCostUnknownPricingMetadataMissing means Orpheus has no pricing row.
	UsageCostUnknownPricingMetadataMissing = "pricing_metadata_missing"

	usageCostCurrencyUSD = "USD"
	usageCostUnit        = "usd_per_1m_tokens"

	microUSDPerUSD            = int64(1_000_000)
	tokensPerPricingUnit      = int64(1_000_000)
	reasoningOutputAsOutput   = "billed at output rate using max(output_tokens, reasoning_output_tokens)"
	subscriptionBillingCaveat = "Estimate only; may not match subscription billing or vendor invoices."
	piReportedCostSource      = "Pi usage.cost.total"
	piReportedCostCaveat      = "Pi-reported estimate only; not exact billed cost or invoice reconciliation."
	openAIAPIPricingSource    = "OpenAI API pricing"
	openAIAPIPricingSourceURL = "https://developers.openai.com/api/docs/pricing"
	openAIAPIPricingAccessed  = "2026-07-10"
	openAIGPT5Source          = "OpenAI GPT-5 developer launch and GPT-5.1 prompt-cache pricing"
	openAIGPT5SourceURL       = "https://openai.com/index/introducing-gpt-5-for-developers/; " +
		"https://openai.com/index/gpt-5-1-for-developers/"
	openAIGPT5SourcePublished = "2025-08-07; 2025-11"
)

// UsageCost records an estimated API-equivalent cost for token usage.
type UsageCost struct {
	Kind           string
	Currency       string
	AmountMicroUSD int64
	Pricing        UsagePricingMetadata
}

// UsagePricingMetadata records the public pricing row used for a cost estimate.
type UsagePricingMetadata struct {
	Provider                  string
	Model                     string
	ServiceTier               string
	Unit                      string
	InputUSDPerMillionTokens  string
	CachedUSDPerMillionTokens string
	OutputUSDPerMillionTokens string
	ReasoningOutputTreatment  string
	Source                    string
	SourceURL                 string
	SourceAccessed            string
	SourcePublished           string
	Notes                     string
}

// ResolvedUsageCost reports whether an execution has known cost and why not.
type ResolvedUsageCost struct {
	Cost          UsageCost
	Known         bool
	UnknownReason string
}

type usagePrice struct {
	model                    string
	serviceTier              string
	inputMicroUSDPerMillion  int64
	cachedMicroUSDPerMillion int64
	outputMicroUSDPerMillion int64
	source                   string
	sourceURL                string
	sourceAccessed           string
	sourcePublished          string
}

var usagePrices = map[string]usagePrice{
	"gpt-5":             legacyGPT5Price("gpt-5", 1_250_000, 125_000, 10_000_000),
	"gpt-5-mini":        legacyGPT5Price("gpt-5-mini", 250_000, 25_000, 2_000_000),
	"gpt-5-nano":        legacyGPT5Price("gpt-5-nano", 50_000, 5_000, 400_000),
	"gpt-5-chat-latest": legacyGPT5Price("gpt-5-chat-latest", 1_250_000, 125_000, 10_000_000),
	"gpt-5.1":           legacyGPT5Price("gpt-5.1", 1_250_000, 125_000, 10_000_000),
	"gpt-5.1-chat-latest": legacyGPT5Price(
		"gpt-5.1-chat-latest",
		1_250_000,
		125_000,
		10_000_000,
	),
	"gpt-5.4":       openAIAPIPrice("gpt-5.4", 2_500_000, 250_000, 15_000_000),
	"gpt-5.4-mini":  openAIAPIPrice("gpt-5.4-mini", 750_000, 75_000, 4_500_000),
	"gpt-5.4-nano":  openAIAPIPrice("gpt-5.4-nano", 200_000, 20_000, 1_250_000),
	"gpt-5.5":       openAIAPIPrice("gpt-5.5", 5_000_000, 500_000, 30_000_000),
	"gpt-5.6-sol":   openAIAPIPrice("gpt-5.6-sol", 5_000_000, 500_000, 30_000_000),
	"gpt-5.6-terra": openAIAPIPrice("gpt-5.6-terra", 2_500_000, 250_000, 15_000_000),
	"gpt-5.6-luna":  openAIAPIPrice("gpt-5.6-luna", 1_000_000, 100_000, 6_000_000),
	"gpt-5.3-codex": openAIAPIPrice("gpt-5.3-codex", 1_750_000, 175_000, 14_000_000),
	"chat-latest":   openAIAPIPrice("chat-latest", 5_000_000, 500_000, 30_000_000),
}

// PiReportedUsageCost records a Pi-reported estimated cost in micro-USD.
func PiReportedUsageCost(amountMicroUSD int64) taskstate.AgentUsageCost {
	if amountMicroUSD < 0 {
		amountMicroUSD = 0
	}
	return taskstate.AgentUsageCost{
		Kind:           UsageCostKindPiReportedEstimated,
		Currency:       usageCostCurrencyUSD,
		AmountMicroUSD: amountMicroUSD,
		Source:         piReportedCostSource,
		Notes:          piReportedCostCaveat,
	}
}

// UsageCostFromStored converts a persisted usage cost into report metadata.
func UsageCostFromStored(stored taskstate.AgentUsageCost) (UsageCost, bool) {
	if stored.AmountMicroUSD <= 0 {
		return UsageCost{}, false
	}
	kind := strings.TrimSpace(stored.Kind)
	if kind == "" {
		kind = UsageCostKindEstimatedAPIEquivalent
	}
	currency := strings.TrimSpace(stored.Currency)
	if currency == "" {
		currency = usageCostCurrencyUSD
	}
	return UsageCost{
		Kind:           kind,
		Currency:       currency,
		AmountMicroUSD: stored.AmountMicroUSD,
		Pricing: UsagePricingMetadata{
			Source: strings.TrimSpace(stored.Source),
			Notes:  strings.TrimSpace(stored.Notes),
		},
	}, true
}

// ResolveExecutionUsageCost applies the cost policy for one recorded execution.
func ResolveExecutionUsageCost(execution taskstate.AgentExecution) ResolvedUsageCost {
	if execution.Usage == nil {
		return ResolvedUsageCost{UnknownReason: UsageCostUnknownNoUsage}
	}
	if execution.UsageCost != nil {
		cost, ok := UsageCostFromStored(*execution.UsageCost)
		if !ok {
			return ResolvedUsageCost{UnknownReason: UsageCostUnknownStoredCostInvalid}
		}
		return ResolvedUsageCost{Cost: cost, Known: true}
	}
	if strings.TrimSpace(execution.Harness) == piHarness {
		return ResolvedUsageCost{UnknownReason: UsageCostUnknownPiReportedCostMissing}
	}
	cost, ok := EstimateUsageCost(execution.Model, *execution.Usage)
	if !ok {
		return ResolvedUsageCost{UnknownReason: UsageCostUnknownPricingMetadataMissing}
	}
	return ResolvedUsageCost{Cost: cost, Known: true}
}

// EstimateUsageCost estimates the API-equivalent cost for recorded token usage.
func EstimateUsageCost(model string, usage taskstate.AgentUsage) (UsageCost, bool) {
	price, ok := usagePriceForModel(model)
	if !ok {
		return UsageCost{}, false
	}

	usage = normalizeUsage(usage)
	cachedInputTokens := minNonNegative(usage.CachedInputTokens, usage.InputTokens)
	uncachedInputTokens := usage.InputTokens - cachedInputTokens
	outputTokens := usage.OutputTokens
	if usage.ReasoningOutputTokens > outputTokens {
		outputTokens = usage.ReasoningOutputTokens
	}

	amount := roundedTokenCostMicroUSD(
		int64(uncachedInputTokens),
		price.inputMicroUSDPerMillion,
		int64(cachedInputTokens),
		price.cachedMicroUSDPerMillion,
		int64(outputTokens),
		price.outputMicroUSDPerMillion,
	)

	return UsageCost{
		Kind:           UsageCostKindEstimatedAPIEquivalent,
		Currency:       usageCostCurrencyUSD,
		AmountMicroUSD: amount,
		Pricing: UsagePricingMetadata{
			Provider:                  "openai",
			Model:                     price.model,
			ServiceTier:               price.serviceTier,
			Unit:                      usageCostUnit,
			InputUSDPerMillionTokens:  formatMicroUSDAsPrice(price.inputMicroUSDPerMillion),
			CachedUSDPerMillionTokens: formatMicroUSDAsPrice(price.cachedMicroUSDPerMillion),
			OutputUSDPerMillionTokens: formatMicroUSDAsPrice(price.outputMicroUSDPerMillion),
			ReasoningOutputTreatment:  reasoningOutputAsOutput,
			Source:                    price.source,
			SourceURL:                 price.sourceURL,
			SourceAccessed:            price.sourceAccessed,
			SourcePublished:           price.sourcePublished,
			Notes:                     subscriptionBillingCaveat,
		},
	}, true
}

// FormatUsageCostUSD formats a micro-USD cost amount for human-facing reports.
func FormatUsageCostUSD(amountMicroUSD int64) string {
	if amountMicroUSD < 0 {
		amountMicroUSD = 0
	}
	whole := amountMicroUSD / microUSDPerUSD
	fraction := amountMicroUSD % microUSDPerUSD
	return "$" + formatInt64(whole) + "." + formatSixDigitFraction(fraction)
}

func usagePriceForModel(model string) (usagePrice, bool) {
	price, ok := usagePrices[canonicalUsageModel(model)]
	return price, ok
}

func canonicalUsageModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if index := strings.LastIndex(model, "/"); index >= 0 {
		model = model[index+1:]
	}
	if value, _, ok := strings.Cut(model, ":"); ok {
		model = value
	}
	return strings.TrimSpace(model)
}

func legacyGPT5Price(
	model string,
	inputMicroUSDPerMillion int64,
	cachedMicroUSDPerMillion int64,
	outputMicroUSDPerMillion int64,
) usagePrice {
	return usagePrice{
		model:                    model,
		serviceTier:              "standard",
		inputMicroUSDPerMillion:  inputMicroUSDPerMillion,
		cachedMicroUSDPerMillion: cachedMicroUSDPerMillion,
		outputMicroUSDPerMillion: outputMicroUSDPerMillion,
		source:                   openAIGPT5Source,
		sourceURL:                openAIGPT5SourceURL,
		sourcePublished:          openAIGPT5SourcePublished,
	}
}

func openAIAPIPrice(
	model string,
	inputMicroUSDPerMillion int64,
	cachedMicroUSDPerMillion int64,
	outputMicroUSDPerMillion int64,
) usagePrice {
	return usagePrice{
		model:                    model,
		serviceTier:              "standard",
		inputMicroUSDPerMillion:  inputMicroUSDPerMillion,
		cachedMicroUSDPerMillion: cachedMicroUSDPerMillion,
		outputMicroUSDPerMillion: outputMicroUSDPerMillion,
		source:                   openAIAPIPricingSource,
		sourceURL:                openAIAPIPricingSourceURL,
		sourceAccessed:           openAIAPIPricingAccessed,
	}
}

func normalizeUsage(usage taskstate.AgentUsage) taskstate.AgentUsage {
	if usage.InputTokens < 0 {
		usage.InputTokens = 0
	}
	if usage.CachedInputTokens < 0 {
		usage.CachedInputTokens = 0
	}
	if usage.OutputTokens < 0 {
		usage.OutputTokens = 0
	}
	if usage.ReasoningOutputTokens < 0 {
		usage.ReasoningOutputTokens = 0
	}
	if usage.TotalTokens < 0 {
		usage.TotalTokens = 0
	}
	return usage
}

func roundedTokenCostMicroUSD(tokenRates ...int64) int64 {
	var numerator int64
	for index := 0; index+1 < len(tokenRates); index += 2 {
		tokens := tokenRates[index]
		rateMicroUSDPerMillion := tokenRates[index+1]
		if tokens <= 0 || rateMicroUSDPerMillion <= 0 {
			continue
		}
		numerator += tokens * rateMicroUSDPerMillion
	}
	if numerator == 0 {
		return 0
	}
	return (numerator + tokensPerPricingUnit/2) / tokensPerPricingUnit
}

func minNonNegative(a int, b int) int {
	if a < 0 {
		a = 0
	}
	if b < 0 {
		b = 0
	}
	if a < b {
		return a
	}
	return b
}

func formatMicroUSDAsPrice(amount int64) string {
	if amount < 0 {
		amount = 0
	}
	whole := amount / microUSDPerUSD
	fraction := amount % microUSDPerUSD
	text := strings.TrimRight(formatSixDigitFraction(fraction), "0")
	if text == "" {
		return formatInt64(whole)
	}
	return formatInt64(whole) + "." + text
}

func formatSixDigitFraction(value int64) string {
	digits := "000000" + formatInt64(value)
	return digits[len(digits)-6:]
}

func formatInt64(value int64) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	index := len(digits)
	for value > 0 {
		index--
		digits[index] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[index:])
}
