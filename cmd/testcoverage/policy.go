package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
)

var errUnsupportedBaseline = errors.New("unsupported baseline schema")

const (
	statusPass                 = "pass"
	statusPolicyUpdateRequired = "policy_update_required"
	statusRefreshRequired      = "refresh_required"
	statusRegression           = "coverage_regression"
	statusTimingFailed         = "timing_budget_exceeded"
	statusTestFailed           = "test_failed"
)

type coveragePolicy struct {
	RepositorySignificancePP      float64 `json:"repository_significance_percentage_points"`
	PackageSignificancePP         float64 `json:"package_significance_percentage_points"`
	RepositoryDenominatorRelative float64 `json:"repository_denominator_relative"`
	RepositoryDenominatorAbsolute int     `json:"repository_denominator_statements"`
	PackageDenominatorRelative    float64 `json:"package_denominator_relative"`
	PackageDenominatorAbsolute    int     `json:"package_denominator_statements"`
}

type timingPolicy struct {
	SuiteRelativeTolerance   float64 `json:"suite_relative_tolerance"`
	SuiteAbsoluteSeconds     float64 `json:"suite_absolute_seconds"`
	PackageRelativeTolerance float64 `json:"package_relative_tolerance"`
	PackageAbsoluteSeconds   float64 `json:"package_absolute_seconds"`
}

type qualityPolicy struct {
	Coverage coveragePolicy `json:"coverage"`
	Timing   timingPolicy   `json:"timing"`
}

type baselinePackage struct {
	Name string `json:"name"`
	coverageMetric
}

type timingBudget struct {
	Name            string  `json:"name"`
	BaselineSeconds float64 `json:"baseline_seconds"`
	Seconds         float64 `json:"seconds"`
}

type laneBaseline struct {
	TestCount            int               `json:"test_count"`
	Coverage             coverageMetric    `json:"coverage"`
	Packages             []baselinePackage `json:"packages"`
	SuiteBaselineSeconds float64           `json:"suite_baseline_seconds"`
	SuiteBudgetSeconds   float64           `json:"suite_budget_seconds"`
	PackageBudgets       []timingBudget    `json:"package_budgets"`
}

type baseline struct {
	SchemaVersion int                     `json:"schema_version"`
	Policy        qualityPolicy           `json:"policy"`
	Lanes         map[string]laneBaseline `json:"lanes"`
	Legacy        bool                    `json:"-"`
}

type finding struct {
	Kind           string  `json:"kind"`
	Lane           string  `json:"lane,omitempty"`
	Scope          string  `json:"scope,omitempty"`
	Name           string  `json:"name,omitempty"`
	Prior          float64 `json:"prior,omitempty"`
	Baseline       float64 `json:"baseline,omitempty"`
	Current        float64 `json:"current,omitempty"`
	Threshold      float64 `json:"threshold,omitempty"`
	BudgetSeconds  float64 `json:"budget_seconds,omitempty"`
	OverageSeconds float64 `json:"overage_seconds,omitempty"`
	Proposed       float64 `json:"proposed,omitempty"`
	Message        string  `json:"message"`
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
	Decision           decision              `json:"decision"`
	Scenarios          []scenarioResult      `json:"scenarios,omitempty"`
}

func defaultPolicy() qualityPolicy {
	return qualityPolicy{
		Coverage: coveragePolicy{
			RepositorySignificancePP:      0.5,
			PackageSignificancePP:         2,
			RepositoryDenominatorRelative: 0.02,
			RepositoryDenominatorAbsolute: 100,
			PackageDenominatorRelative:    0.10,
			PackageDenominatorAbsolute:    25,
		},
		Timing: timingPolicy{
			SuiteRelativeTolerance:   0.25,
			SuiteAbsoluteSeconds:     0.5,
			PackageRelativeTolerance: 0.50,
			PackageAbsoluteSeconds:   0.25,
		},
	}
}

func baselineFromReport(report qualityReport, policy qualityPolicy) baseline {
	return generatedBaseline(report, baseline{}, policy)
}

func generatedBaseline(report qualityReport, prior baseline, policy qualityPolicy) baseline {
	result := baseline{SchemaVersion: baselineSchemaVersion, Policy: policy, Lanes: make(map[string]laneBaseline, len(laneNames))}
	for _, name := range laneNames {
		lane := report.Lanes[name]
		priorLane, hasPrior := prior.Lanes[name]
		item := laneBaseline{
			TestCount: lane.TestCount,
			Coverage:  lane.Coverage,
			Packages:  make([]baselinePackage, 0, len(lane.Packages)),
		}
		for _, pkg := range lane.Packages {
			item.Packages = append(item.Packages, baselinePackage(pkg))
		}
		if hasPrior {
			item.SuiteBaselineSeconds = priorLane.SuiteBaselineSeconds
			item.SuiteBudgetSeconds = priorLane.SuiteBudgetSeconds
		}
		testSeconds := selectedTestSeconds(lane.Timings)
		if item.SuiteBaselineSeconds <= 0 {
			item.SuiteBaselineSeconds = testSeconds
		}
		if item.SuiteBudgetSeconds <= 0 {
			item.SuiteBudgetSeconds = timingAllowance(testSeconds, policy.Timing.SuiteRelativeTolerance, policy.Timing.SuiteAbsoluteSeconds)
		}
		priorBudgets := timingBudgetMap(priorLane.PackageBudgets)
		item.PackageBudgets = make([]timingBudget, 0, len(lane.Timings))
		for _, timing := range lane.Timings {
			budget, found := priorBudgets[timing.Name]
			if !found {
				budget = timingBudget{Name: timing.Name, Seconds: timingAllowance(timing.Seconds, policy.Timing.PackageRelativeTolerance, policy.Timing.PackageAbsoluteSeconds)}
			}
			if budget.BaselineSeconds <= 0 {
				budget.BaselineSeconds = timing.Seconds
			}
			item.PackageBudgets = append(item.PackageBudgets, budget)
		}
		result.Lanes[name] = item
	}
	return result
}

func timingAllowance(measured, relative, absolute float64) float64 {
	allowance := measured + math.Max(measured*relative, absolute)
	return math.Ceil(allowance*1000) / 1000
}

func loadBaseline(name string) (baseline, error) {
	contents, err := os.ReadFile(name)
	if err != nil {
		return baseline{}, err
	}
	var schema struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(contents, &schema); err != nil {
		return baseline{}, err
	}
	if schema.SchemaVersion == 2 {
		return loadLegacyBaseline(contents)
	}
	var value baseline
	if err := json.Unmarshal(contents, &value); err != nil {
		return baseline{}, err
	}
	if err := validateBaseline(value); err != nil {
		return baseline{}, err
	}
	return value, nil
}

// loadLegacyBaseline supports the one-time transition from the detailed block
// inventory. Its coverage aggregates remain trusted; timing is supplied by the
// generated schema-three baseline during base-branch comparison.
func loadLegacyBaseline(contents []byte) (baseline, error) {
	type legacyLane struct {
		TestCount         int               `json:"test_count"`
		StatementTotal    int               `json:"statement_total"`
		CoveredStatements int               `json:"covered_statements"`
		Packages          []baselinePackage `json:"packages"`
	}
	var legacy struct {
		Lanes map[string]legacyLane `json:"lanes"`
	}
	if err := json.Unmarshal(contents, &legacy); err != nil {
		return baseline{}, err
	}
	result := baseline{SchemaVersion: baselineSchemaVersion, Policy: defaultPolicy(), Lanes: make(map[string]laneBaseline, 2), Legacy: true}
	for _, name := range laneNames {
		lane, found := legacy.Lanes[name]
		if !found {
			return baseline{}, fmt.Errorf("legacy baseline has no %s lane", name)
		}
		metric := coverageMetric{StatementTotal: lane.StatementTotal, CoveredStatements: lane.CoveredStatements}
		if err := validatePackages(lane.Packages, metric); err != nil {
			return baseline{}, fmt.Errorf("invalid legacy %s packages: %w", name, err)
		}
		result.Lanes[name] = laneBaseline{TestCount: lane.TestCount, Coverage: metric, Packages: lane.Packages}
	}
	return result, nil
}

func validateBaseline(value baseline) error {
	if value.SchemaVersion != baselineSchemaVersion {
		return fmt.Errorf("%w version %d", errUnsupportedBaseline, value.SchemaVersion)
	}
	if err := validatePolicy(value.Policy); err != nil {
		return fmt.Errorf("invalid policy: %w", err)
	}
	for _, name := range laneNames {
		lane, found := value.Lanes[name]
		if !found {
			return fmt.Errorf("baseline has no %s lane", name)
		}
		if lane.TestCount < 0 || !validMetric(lane.Coverage) || !validOptionalTiming(lane.SuiteBaselineSeconds) || !validTiming(lane.SuiteBudgetSeconds) {
			return fmt.Errorf("invalid %s lane aggregates", name)
		}
		if err := validatePackages(lane.Packages, lane.Coverage); err != nil {
			return fmt.Errorf("invalid %s packages: %w", name, err)
		}
		if err := validateBudgets(lane.PackageBudgets); err != nil {
			return fmt.Errorf("invalid %s package budgets: %w", name, err)
		}
	}
	return nil
}

func validatePolicy(policy qualityPolicy) error {
	values := []float64{
		policy.Coverage.RepositorySignificancePP,
		policy.Coverage.PackageSignificancePP,
		policy.Coverage.RepositoryDenominatorRelative,
		policy.Coverage.PackageDenominatorRelative,
		policy.Timing.SuiteRelativeTolerance,
		policy.Timing.SuiteAbsoluteSeconds,
		policy.Timing.PackageRelativeTolerance,
		policy.Timing.PackageAbsoluteSeconds,
	}
	for _, value := range values {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("thresholds must be finite and non-negative")
		}
	}
	if policy.Coverage.RepositoryDenominatorAbsolute < 0 || policy.Coverage.PackageDenominatorAbsolute < 0 {
		return errors.New("denominator statement thresholds must be non-negative")
	}
	return nil
}

func validMetric(metric coverageMetric) bool {
	return metric.StatementTotal >= 0 && metric.CoveredStatements >= 0 && metric.CoveredStatements <= metric.StatementTotal
}

func validatePackages(packages []baselinePackage, total coverageMetric) error {
	sum := coverageMetric{}
	priorName := ""
	for _, pkg := range packages {
		if pkg.Name == "" || pkg.Name <= priorName || !validMetric(pkg.coverageMetric) {
			return errors.New("packages must be valid, unique, and sorted by name")
		}
		priorName = pkg.Name
		sum.StatementTotal += pkg.StatementTotal
		sum.CoveredStatements += pkg.CoveredStatements
	}
	if sum != total {
		return fmt.Errorf("package totals %d/%d do not equal repository totals %d/%d", sum.CoveredStatements, sum.StatementTotal, total.CoveredStatements, total.StatementTotal)
	}
	return nil
}

func validateBudgets(budgets []timingBudget) error {
	priorName := ""
	for _, budget := range budgets {
		if budget.Name == "" || budget.Name <= priorName || !validOptionalTiming(budget.BaselineSeconds) || !validTiming(budget.Seconds) {
			return errors.New("budgets must be positive, unique, and sorted by name")
		}
		priorName = budget.Name
	}
	return nil
}

func validOptionalTiming(seconds float64) bool {
	return seconds >= 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0)
}

func validTiming(seconds float64) bool {
	return seconds > 0 && !math.IsNaN(seconds) && !math.IsInf(seconds, 0)
}

func assess(prior baseline, report qualityReport) decision {
	var regressions, suiteTimingFailures, packageTimingWarnings, refresh []finding
	for _, name := range laneNames {
		baselineLane := prior.Lanes[name]
		current := report.Lanes[name]
		if baselineLane.TestCount != current.TestCount {
			refresh = append(refresh, finding{Kind: "test_structure", Lane: name, Scope: "suite", Prior: float64(baselineLane.TestCount), Current: float64(current.TestCount), Message: "test-event count changed"})
		}
		regressions, refresh = assessMetric(regressions, refresh, name, "repository", "", baselineLane.Coverage, current.Coverage, prior.Policy.Coverage.RepositorySignificancePP, prior.Policy.Coverage.RepositoryDenominatorRelative, prior.Policy.Coverage.RepositoryDenominatorAbsolute)

		priorPackages := baselinePackageMap(baselineLane.Packages)
		currentPackages := reportPackageMap(current.Packages)
		for _, packageName := range sortedUnionKeys(priorPackages, currentPackages) {
			before, hadBefore := priorPackages[packageName]
			after, hasAfter := currentPackages[packageName]
			if !hadBefore || !hasAfter {
				refresh = append(refresh, finding{Kind: "package_structure", Lane: name, Scope: "package", Name: packageName, Message: "coverage package was added or removed"})
				continue
			}
			regressions, refresh = assessMetric(regressions, refresh, name, "package", packageName, before, after, prior.Policy.Coverage.PackageSignificancePP, prior.Policy.Coverage.PackageDenominatorRelative, prior.Policy.Coverage.PackageDenominatorAbsolute)
		}

		currentTestSeconds := selectedTestSeconds(current.Timings)
		if currentTestSeconds > baselineLane.SuiteBudgetSeconds {
			suiteTimingFailures = append(suiteTimingFailures, timingFinding(name, "suite", "", baselineLane.SuiteBudgetSeconds, baselineLane.SuiteBaselineSeconds, currentTestSeconds, "suite timing budget exceeded"))
		}
		priorTiming := timingBudgetMap(baselineLane.PackageBudgets)
		currentTiming := timingMap(current.Timings)
		for _, packageName := range sortedUnionKeys(priorTiming, currentTiming) {
			budget, hadBudget := priorTiming[packageName]
			seconds, measured := currentTiming[packageName]
			if !hadBudget || !measured {
				refresh = append(refresh, finding{Kind: "timing_structure", Lane: name, Scope: "package", Name: packageName, Message: "timed package was added or removed"})
				continue
			}
			if seconds > budget.Seconds {
				packageTimingWarnings = append(packageTimingWarnings, timingFinding(name, "package", packageName, budget.Seconds, budget.BaselineSeconds, seconds, "non-blocking package timing budget exceeded"))
			}
		}
	}
	switch {
	case len(regressions) > 0:
		return decision{Status: statusRegression, Findings: append(regressions, suiteTimingFailures...), Warnings: packageTimingWarnings}
	case len(suiteTimingFailures) > 0:
		return decision{Status: statusTimingFailed, Findings: suiteTimingFailures, Warnings: packageTimingWarnings}
	case len(refresh) > 0:
		return decision{Status: statusRefreshRequired, Findings: refresh, Warnings: packageTimingWarnings}
	default:
		return decision{Status: statusPass, Warnings: packageTimingWarnings}
	}
}

func timingFinding(lane, scope, name string, budget, baseline, current float64, message string) finding {
	return finding{
		Kind:           "timing",
		Lane:           lane,
		Scope:          scope,
		Name:           name,
		Prior:          budget,
		Baseline:       baseline,
		Current:        current,
		BudgetSeconds:  budget,
		OverageSeconds: current - budget,
		Message:        message,
	}
}

func assessMetric(regressions, refresh []finding, lane, scope, name string, prior, current coverageMetric, significance, denominatorRelative float64, denominatorAbsolute int) ([]finding, []finding) {
	priorPercentage := percentage(prior)
	currentPercentage := percentage(current)
	delta := currentPercentage - priorPercentage
	if delta <= -significance && delta < 0 {
		regressions = append(regressions, finding{Kind: "coverage", Lane: lane, Scope: scope, Name: name, Prior: priorPercentage, Current: currentPercentage, Threshold: significance, Message: "coverage regressed beyond the significance policy"})
	} else if delta >= significance && delta > 0 {
		refresh = append(refresh, finding{Kind: "coverage", Lane: lane, Scope: scope, Name: name, Prior: priorPercentage, Current: currentPercentage, Threshold: significance, Message: "coverage improvement requires baseline refresh"})
	}
	drift := math.Abs(float64(current.StatementTotal - prior.StatementTotal))
	threshold := math.Max(float64(prior.StatementTotal)*denominatorRelative, float64(denominatorAbsolute))
	if drift >= threshold && drift > 0 {
		refresh = append(refresh, finding{Kind: "denominator", Lane: lane, Scope: scope, Name: name, Prior: float64(prior.StatementTotal), Current: float64(current.StatementTotal), Threshold: threshold, Message: "statement denominator drift requires baseline refresh"})
	}
	return regressions, refresh
}

func percentage(metric coverageMetric) float64 {
	if metric.StatementTotal == 0 {
		return 0
	}
	return float64(metric.CoveredStatements) * 100 / float64(metric.StatementTotal)
}

func verifyTrackedBaseline(trusted, tracked baseline, report qualityReport, trustedDecision decision) decision {
	if trustedDecision.Status == statusRegression || trustedDecision.Status == statusTimingFailed {
		return trustedDecision
	}
	if reflect.DeepEqual(tracked, trusted) {
		return trustedDecision
	}
	// Policy is explicitly configurable in the tracked file, but it must not
	// influence aggregates or new-package budgets until it becomes trusted on
	// the base branch.
	expected := generatedBaseline(report, trusted, trusted.Policy)
	expected.Policy = tracked.Policy
	if !adoptValidNewPackageBudgets(trusted, tracked, report, &expected) || !reflect.DeepEqual(tracked, expected) {
		return decision{
			Status:   statusRefreshRequired,
			Findings: []finding{{Kind: "baseline", Message: "tracked baseline is neither the trusted baseline nor the generated current baseline"}},
			Warnings: trustedDecision.Warnings,
		}
	}
	if trustedDecision.Status == statusRefreshRequired {
		trustedDecision.Status = statusPass
		trustedDecision.Findings = append(trustedDecision.Findings, finding{Kind: "baseline", Message: "generated refresh matches current aggregates and preserves trusted timing budgets"})
		return trustedDecision
	}
	return decision{Status: statusPass, Findings: []finding{{Kind: "baseline", Message: "generated baseline update matches current aggregates and preserves trusted timing budgets"}}, Warnings: trustedDecision.Warnings}
}

// adoptValidNewPackageBudgets permits normal timing variance between baseline
// generation and CI verification for packages absent from the trusted baseline.
// Existing trusted budgets remain exact, while new budgets must be derived from
// a plausible measurement under the trusted policy and still cover the current
// selected-test timing.
func adoptValidNewPackageBudgets(trusted, tracked baseline, report qualityReport, expected *baseline) bool {
	for _, laneName := range laneNames {
		trustedBudgets := timingBudgetMap(trusted.Lanes[laneName].PackageBudgets)
		trackedBudgets := timingBudgetMap(tracked.Lanes[laneName].PackageBudgets)
		currentTimings := timingMap(report.Lanes[laneName].Timings)
		expectedLane := expected.Lanes[laneName]
		for index, budget := range expectedLane.PackageBudgets {
			if _, trustedPackage := trustedBudgets[budget.Name]; trustedPackage {
				continue
			}
			trackedBudget, ok := trackedBudgets[budget.Name]
			if !ok || !validNewPackageBudget(trackedBudget, currentTimings[budget.Name], trusted.Policy.Timing) {
				return false
			}
			expectedLane.PackageBudgets[index] = trackedBudget
		}
		expected.Lanes[laneName] = expectedLane
	}
	return true
}

func validNewPackageBudget(budget timingBudget, currentSeconds float64, policy timingPolicy) bool {
	if budget.BaselineSeconds <= 0 || currentSeconds <= 0 {
		return false
	}
	if budget.Seconds != timingAllowance(budget.BaselineSeconds, policy.PackageRelativeTolerance, policy.PackageAbsoluteSeconds) {
		return false
	}
	return currentSeconds <= budget.Seconds &&
		budget.BaselineSeconds <= timingAllowance(currentSeconds, policy.PackageRelativeTolerance, policy.PackageAbsoluteSeconds)
}

func legacyBaselineWithTiming(legacy, generated baseline) baseline {
	for _, name := range laneNames {
		lane := legacy.Lanes[name]
		generatedLane := generated.Lanes[name]
		lane.SuiteBaselineSeconds = generatedLane.SuiteBaselineSeconds
		lane.SuiteBudgetSeconds = generatedLane.SuiteBudgetSeconds
		lane.PackageBudgets = append([]timingBudget(nil), generatedLane.PackageBudgets...)
		legacy.Lanes[name] = lane
	}
	return legacy
}

func baselinePackageMap(items []baselinePackage) map[string]coverageMetric {
	result := make(map[string]coverageMetric, len(items))
	for _, item := range items {
		result[item.Name] = item.coverageMetric
	}
	return result
}

func reportPackageMap(items []packageMetric) map[string]coverageMetric {
	result := make(map[string]coverageMetric, len(items))
	for _, item := range items {
		result[item.Name] = item.coverageMetric
	}
	return result
}

func timingBudgetMap(items []timingBudget) map[string]timingBudget {
	result := make(map[string]timingBudget, len(items))
	for _, item := range items {
		result[item.Name] = item
	}
	return result
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
