package main

import "sort"

const (
	statusPass                 = "pass"
	statusNotEvaluated         = "not_evaluated"
	statusUnevaluated          = "unevaluated"
	statusPolicyUpdateRequired = "policy_update_required"
	statusRegression           = "coverage_regression"
	statusTimingFailed         = "timing_budget_exceeded"
	statusExecutionFailed      = "execution_failure"
	statusTestFailed           = "test_failed"
)

type finding struct {
	Kind           string  `json:"kind"`
	Lane           string  `json:"lane,omitempty"`
	Scope          string  `json:"scope,omitempty"`
	Name           string  `json:"name,omitempty"`
	Prior          float64 `json:"prior,omitempty"`
	Current        float64 `json:"current,omitempty"`
	Threshold      float64 `json:"threshold,omitempty"`
	BudgetSeconds  float64 `json:"budget_seconds,omitempty"`
	OverageSeconds float64 `json:"overage_seconds,omitempty"`
	Proposed       float64 `json:"proposed,omitempty"`
	Message        string  `json:"message"`

	// headroomPercent is presentation-only; policy evidence is unchanged in JSON.
	headroomPercent float64
}

type decision struct {
	Status   string    `json:"status"`
	Findings []finding `json:"findings,omitempty"`
	Warnings []finding `json:"warnings,omitempty"`
}

type qualityReport struct {
	SchemaVersion      int                   `json:"schema_version"`
	Complete           bool                  `json:"complete"`
	MeasurementSamples int                   `json:"measurement_samples,omitempty"`
	Lanes              map[string]laneReport `json:"lanes"`
	Execution          decision              `json:"execution"`
	CoverageDecision   decision              `json:"coverage_decision"`
	Timing             decision              `json:"timing"`
	Metadata           *measurementMetadata  `json:"metadata,omitempty"`
	Comparison         *comparisonEvidence   `json:"comparison,omitempty"`
	Decision           decision              `json:"decision"`
	Scenarios          []scenarioResult      `json:"scenarios,omitempty"`

	// sampleFailures retains comparison diagnostics for presentation without
	// replacing first-candidate coverage or changing persisted sample evidence.
	sampleFailures []reportFailure
}

func percentage(metric coverageMetric) float64 {
	if metric.StatementTotal == 0 {
		return 0
	}
	return float64(metric.CoveredStatements) * 100 / float64(metric.StatementTotal)
}

func timingMap(items []packageTiming) map[string]float64 {
	result := make(map[string]float64, len(items))
	for _, item := range items {
		result[item.Name] = item.Seconds
	}
	return result
}

func sortedUnionKeys[T any, U any](left map[string]T, right map[string]U) []string {
	keys := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		keys[key] = struct{}{}
	}
	for key := range right {
		keys[key] = struct{}{}
	}
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
