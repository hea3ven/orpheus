package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"

	"gopkg.in/yaml.v3"
)

const qualityPolicyVersion = 1

type coverageBoundSettings struct {
	SignificancePercentagePoints float64 `yaml:"significance_percentage_points"`
	HeadroomPercentagePoints     float64 `yaml:"headroom_percentage_points"`
}

type localCoverageSettings struct {
	Repository coverageBoundSettings `yaml:"repository"`
	Package    coverageBoundSettings `yaml:"package"`
}

type timingBoundSettings struct {
	RelativeHeadroom        float64 `yaml:"relative_headroom"`
	AbsoluteHeadroomSeconds float64 `yaml:"absolute_headroom_seconds"`
}

type localTimingSettings struct {
	UpdateSamples int                 `yaml:"update_samples"`
	Suite         timingBoundSettings `yaml:"suite"`
	Package       timingBoundSettings `yaml:"package"`
}

type coverageBounds struct {
	FloorPercent float64            `yaml:"floor_percent"`
	Packages     map[string]float64 `yaml:"packages"`
}

type timingBounds struct {
	CeilingSeconds float64            `yaml:"ceiling_seconds"`
	Packages       map[string]float64 `yaml:"packages"`
}

type laneQualityPolicy struct {
	Coverage coverageBounds `yaml:"coverage"`
	Timing   timingBounds   `yaml:"timing"`
}

type localQualityPolicy struct {
	Version  int                          `yaml:"version"`
	Coverage localCoverageSettings        `yaml:"coverage"`
	Timing   localTimingSettings          `yaml:"timing"`
	Lanes    map[string]laneQualityPolicy `yaml:"lanes"`
}

func loadQualityPolicy(name string) (localQualityPolicy, error) {
	contents, err := os.ReadFile(name)
	if err != nil {
		return localQualityPolicy{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(contents))
	decoder.KnownFields(true)
	var policy localQualityPolicy
	if err := decoder.Decode(&policy); err != nil {
		return localQualityPolicy{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return localQualityPolicy{}, errors.New("quality policy contains multiple YAML documents")
		}
		return localQualityPolicy{}, err
	}
	if err := validateQualityPolicy(policy); err != nil {
		return localQualityPolicy{}, err
	}
	return policy, nil
}

func validateQualityPolicy(policy localQualityPolicy) error {
	if policy.Version != qualityPolicyVersion {
		return fmt.Errorf("unsupported quality policy version %d", policy.Version)
	}
	if err := validateCoverageSettings("repository", policy.Coverage.Repository); err != nil {
		return err
	}
	if err := validateCoverageSettings("package", policy.Coverage.Package); err != nil {
		return err
	}
	if policy.Timing.UpdateSamples < 1 {
		return errors.New("timing update_samples must be at least 1")
	}
	if err := validateTimingSettings("suite", policy.Timing.Suite); err != nil {
		return err
	}
	if err := validateTimingSettings("package", policy.Timing.Package); err != nil {
		return err
	}
	if len(policy.Lanes) != len(laneNames) {
		return errors.New("quality policy must contain exactly the unit and integration lanes")
	}
	for _, name := range laneNames {
		lane, found := policy.Lanes[name]
		if !found {
			return fmt.Errorf("quality policy has no %s lane", name)
		}
		if !validPercentage(lane.Coverage.FloorPercent) {
			return fmt.Errorf("%s lane coverage floor must be finite and between 0 and 100", name)
		}
		if lane.Coverage.Packages == nil {
			return fmt.Errorf("%s lane coverage packages must be a mapping", name)
		}
		if err := validateCoverageFloors(name, lane.Coverage.Packages); err != nil {
			return err
		}
		if !validTiming(lane.Timing.CeilingSeconds) {
			return fmt.Errorf("%s lane timing ceiling must be finite and positive", name)
		}
		if lane.Timing.Packages == nil {
			return fmt.Errorf("%s lane timing packages must be a mapping", name)
		}
		if err := validateTimingCeilings(name, lane.Timing.Packages); err != nil {
			return err
		}
	}
	for name := range policy.Lanes {
		if name != "unit" && name != "integration" {
			return fmt.Errorf("quality policy contains unknown lane %q", name)
		}
	}
	return nil
}

func validateCoverageSettings(scope string, settings coverageBoundSettings) error {
	if !finitePositive(settings.SignificancePercentagePoints) || !finiteNonNegative(settings.HeadroomPercentagePoints) {
		return fmt.Errorf("coverage %s significance must be positive and headroom must be non-negative", scope)
	}
	return nil
}

func validateTimingSettings(scope string, settings timingBoundSettings) error {
	if !finiteNonNegative(settings.RelativeHeadroom) || !finiteNonNegative(settings.AbsoluteHeadroomSeconds) {
		return fmt.Errorf("timing %s headroom must be finite and non-negative", scope)
	}
	if settings.RelativeHeadroom == 0 && settings.AbsoluteHeadroomSeconds == 0 {
		return fmt.Errorf("timing %s must configure relative or absolute headroom", scope)
	}
	return nil
}

func validateCoverageFloors(lane string, floors map[string]float64) error {
	for name, floor := range floors {
		if name == "" || !validPercentage(floor) {
			return fmt.Errorf("%s lane has invalid coverage floor for package %q", lane, name)
		}
	}
	return nil
}

func validateTimingCeilings(lane string, ceilings map[string]float64) error {
	for name, ceiling := range ceilings {
		if name == "" || !validTiming(ceiling) {
			return fmt.Errorf("%s lane has invalid timing ceiling for package %q", lane, name)
		}
	}
	return nil
}

func validPercentage(value float64) bool {
	return finiteNonNegative(value) && value <= 100
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func marshalQualityPolicy(policy localQualityPolicy) ([]byte, error) {
	if err := validateQualityPolicy(policy); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(policy); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func writeQualityPolicy(name string, policy localQualityPolicy) error {
	contents, err := marshalQualityPolicy(policy)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(name), ".quality-*.yml")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, name)
}

func assessQualityPolicy(policy localQualityPolicy, report qualityReport) decision {
	var coverageViolations, timingViolations, updates []finding
	for _, laneName := range laneNames {
		bounds := policy.Lanes[laneName]
		lane := report.Lanes[laneName]
		coverageViolations, updates = assessCoverageBound(
			coverageViolations, updates, laneName, "repository", "",
			bounds.Coverage.FloorPercent, percentage(lane.Coverage), policy.Coverage.Repository,
		)

		measuredCoverage := reportCoveragePercentages(lane.Packages)
		for _, packageName := range sortedUnionKeys(bounds.Coverage.Packages, measuredCoverage) {
			floor, hasFloor := bounds.Coverage.Packages[packageName]
			measured, hasMeasurement := measuredCoverage[packageName]
			if !hasFloor || !hasMeasurement {
				updates = append(updates, structureFinding("coverage_package", laneName, packageName, hasMeasurement))
				continue
			}
			coverageViolations, updates = assessCoverageBound(
				coverageViolations, updates, laneName, "package", packageName,
				floor, measured, policy.Coverage.Package,
			)
		}

		suiteSeconds := laneSelectedTestSeconds(lane)
		timingViolations, updates = assessTimingBound(
			timingViolations, updates, laneName, "suite", "",
			bounds.Timing.CeilingSeconds, suiteSeconds, policy.Timing.Suite,
		)
		measuredTimings := timingMap(lane.Timings)
		for _, packageName := range sortedUnionKeys(bounds.Timing.Packages, measuredTimings) {
			ceiling, hasCeiling := bounds.Timing.Packages[packageName]
			measured, hasMeasurement := measuredTimings[packageName]
			if !hasCeiling || !hasMeasurement {
				updates = append(updates, structureFinding("timing_package", laneName, packageName, hasMeasurement))
				continue
			}
			timingViolations, updates = assessTimingBound(
				timingViolations, updates, laneName, "package", packageName,
				ceiling, measured, policy.Timing.Package,
			)
		}
	}
	switch {
	case len(coverageViolations) > 0:
		findings := make([]finding, 0, len(coverageViolations)+len(timingViolations)+len(updates))
		findings = append(findings, coverageViolations...)
		findings = append(findings, timingViolations...)
		findings = append(findings, updates...)
		return decision{Status: statusRegression, Findings: findings}
	case len(timingViolations) > 0:
		return decision{Status: statusTimingFailed, Findings: append(timingViolations, updates...)}
	case len(updates) > 0:
		return decision{Status: statusPolicyUpdateRequired, Findings: updates}
	default:
		return decision{Status: statusPass}
	}
}

func assessCoverageBound(violations, updates []finding, lane, scope, name string, floor, measured float64, settings coverageBoundSettings) ([]finding, []finding) {
	proposed := coverageFloor(measured, settings.HeadroomPercentagePoints)
	item := finding{
		Kind: "coverage_policy", Lane: lane, Scope: scope, Name: name, Prior: floor,
		Current: measured, Threshold: settings.SignificancePercentagePoints, Proposed: proposed,
	}
	if measured < floor {
		item.Message = "coverage is below its policy floor; run make quality-policy-update if the regression is intentional"
		return append(violations, item), updates
	}
	if math.Abs(proposed-floor) >= settings.SignificancePercentagePoints {
		item.Message = "coverage moved beyond its refresh threshold; run make quality-policy-update"
		updates = append(updates, item)
	}
	return violations, updates
}

func assessTimingBound(violations, updates []finding, lane, scope, name string, ceiling, measured float64, settings timingBoundSettings) ([]finding, []finding) {
	proposed := timingCeiling(measured, settings)
	item := finding{
		Kind: "timing_policy", Lane: lane, Scope: scope, Name: name, Prior: ceiling,
		Current: measured, Threshold: timingHeadroom(timingReference(ceiling, settings), settings),
		BudgetSeconds: ceiling, Proposed: proposed,
	}
	if measured > ceiling {
		item.OverageSeconds = measured - ceiling
		item.Message = "timing exceeds its policy ceiling; run make quality-policy-update if the regression is intentional"
		return append(violations, item), updates
	}
	if timingBoundNeedsUpdate(ceiling, measured, settings) {
		item.Message = "timing moved beyond its refresh threshold; run make quality-policy-update"
		updates = append(updates, item)
	}
	return violations, updates
}

func structureFinding(kind, lane, packageName string, added bool) finding {
	direction := "removed"
	if added {
		direction = "added"
	}
	return finding{
		Kind: kind, Lane: lane, Scope: "package", Name: packageName,
		Message: fmt.Sprintf("package was %s; run make quality-policy-update", direction),
	}
}

func coverageFloor(measured, headroom float64) float64 {
	return roundPolicyValue(math.Max(0, measured-headroom))
}

func timingCeiling(measured float64, settings timingBoundSettings) float64 {
	return roundPolicyValue(measured + timingHeadroom(measured, settings))
}

func timingHeadroom(measured float64, settings timingBoundSettings) float64 {
	return math.Max(measured*settings.RelativeHeadroom, settings.AbsoluteHeadroomSeconds)
}

func timingReference(ceiling float64, settings timingBoundSettings) float64 {
	if settings.RelativeHeadroom == 0 {
		return math.Max(0, ceiling-settings.AbsoluteHeadroomSeconds)
	}
	crossover := settings.AbsoluteHeadroomSeconds / settings.RelativeHeadroom
	if ceiling >= crossover+settings.AbsoluteHeadroomSeconds {
		return ceiling / (1 + settings.RelativeHeadroom)
	}
	return math.Max(0, ceiling-settings.AbsoluteHeadroomSeconds)
}

func timingBoundNeedsUpdate(ceiling, measured float64, settings timingBoundSettings) bool {
	if measured > ceiling {
		return true
	}
	reference := timingReference(ceiling, settings)
	return math.Abs(measured-reference) >= timingHeadroom(reference, settings)
}

func roundPolicyValue(value float64) float64 {
	return math.Round(value*1000) / 1000
}

func reportCoveragePercentages(packages []packageMetric) map[string]float64 {
	result := make(map[string]float64, len(packages))
	for _, pkg := range packages {
		result[pkg.Name] = percentage(pkg.coverageMetric)
	}
	return result
}

func laneSelectedTestSeconds(lane laneReport) float64 {
	if lane.SelectedTestSeconds > 0 {
		return lane.SelectedTestSeconds
	}
	return selectedTestSeconds(lane.Timings)
}

func updatedQualityPolicy(policy localQualityPolicy, report qualityReport) (localQualityPolicy, []finding) {
	updated := cloneQualityPolicy(policy)
	var changes []finding
	for _, laneName := range laneNames {
		lanePolicy := updated.Lanes[laneName]
		lane := report.Lanes[laneName]

		lanePolicy.Coverage.FloorPercent, changes = updateCoverageBound(
			lanePolicy.Coverage.FloorPercent, percentage(lane.Coverage), policy.Coverage.Repository,
			changes, laneName, "repository", "",
		)
		measuredCoverage := reportCoveragePercentages(lane.Packages)
		lanePolicy.Coverage.Packages, changes = updateCoveragePackages(
			lanePolicy.Coverage.Packages, measuredCoverage, policy.Coverage.Package, changes, laneName,
		)

		lanePolicy.Timing.CeilingSeconds, changes = updateTimingBound(
			lanePolicy.Timing.CeilingSeconds, laneSelectedTestSeconds(lane), policy.Timing.Suite,
			changes, laneName, "suite", "",
		)
		lanePolicy.Timing.Packages, changes = updateTimingPackages(
			lanePolicy.Timing.Packages, timingMap(lane.Timings), policy.Timing.Package, changes, laneName,
		)
		updated.Lanes[laneName] = lanePolicy
	}
	return updated, changes
}

func cloneQualityPolicy(policy localQualityPolicy) localQualityPolicy {
	result := policy
	result.Lanes = make(map[string]laneQualityPolicy, len(policy.Lanes))
	for name, lane := range policy.Lanes {
		lane.Coverage.Packages = cloneFloatMap(lane.Coverage.Packages)
		lane.Timing.Packages = cloneFloatMap(lane.Timing.Packages)
		result.Lanes[name] = lane
	}
	return result
}

func cloneFloatMap(values map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(values))
	for name, value := range values {
		result[name] = value
	}
	return result
}

func updateCoverageBound(current, measured float64, settings coverageBoundSettings, changes []finding, lane, scope, name string) (float64, []finding) {
	proposed := coverageFloor(measured, settings.HeadroomPercentagePoints)
	if measured >= current && math.Abs(proposed-current) < settings.SignificancePercentagePoints {
		return current, changes
	}
	changes = append(changes, changedBoundFinding("coverage_policy", lane, scope, name, current, proposed, measured))
	return proposed, changes
}

func updateCoveragePackages(current, measured map[string]float64, settings coverageBoundSettings, changes []finding, lane string) (map[string]float64, []finding) {
	result := make(map[string]float64, len(measured))
	for _, name := range sortedKeys(measured) {
		value := measured[name]
		prior, found := current[name]
		if !found {
			proposed := coverageFloor(value, settings.HeadroomPercentagePoints)
			result[name] = proposed
			changes = append(changes, addedBoundFinding("coverage_policy", lane, name, proposed, value))
			continue
		}
		result[name], changes = updateCoverageBound(prior, value, settings, changes, lane, "package", name)
	}
	for _, name := range sortedKeys(current) {
		if _, found := measured[name]; !found {
			changes = append(changes, removedBoundFinding("coverage_policy", lane, name, current[name]))
		}
	}
	return result, changes
}

func updateTimingBound(current, measured float64, settings timingBoundSettings, changes []finding, lane, scope, name string) (float64, []finding) {
	if !timingBoundNeedsUpdate(current, measured, settings) {
		return current, changes
	}
	proposed := timingCeiling(measured, settings)
	changes = append(changes, changedBoundFinding("timing_policy", lane, scope, name, current, proposed, measured))
	return proposed, changes
}

func updateTimingPackages(current, measured map[string]float64, settings timingBoundSettings, changes []finding, lane string) (map[string]float64, []finding) {
	result := make(map[string]float64, len(measured))
	for _, name := range sortedKeys(measured) {
		value := measured[name]
		prior, found := current[name]
		if !found {
			proposed := timingCeiling(value, settings)
			result[name] = proposed
			changes = append(changes, addedBoundFinding("timing_policy", lane, name, proposed, value))
			continue
		}
		result[name], changes = updateTimingBound(prior, value, settings, changes, lane, "package", name)
	}
	for _, name := range sortedKeys(current) {
		if _, found := measured[name]; !found {
			changes = append(changes, removedBoundFinding("timing_policy", lane, name, current[name]))
		}
	}
	return result, changes
}

func changedBoundFinding(kind, lane, scope, name string, prior, proposed, measured float64) finding {
	return finding{Kind: kind, Lane: lane, Scope: scope, Name: name, Prior: prior, Current: measured, Proposed: proposed, Message: "updated accepted bound"}
}

func addedBoundFinding(kind, lane, name string, proposed, measured float64) finding {
	return finding{Kind: kind, Lane: lane, Scope: "package", Name: name, Current: measured, Proposed: proposed, Message: "added accepted bound"}
}

func removedBoundFinding(kind, lane, name string, prior float64) finding {
	return finding{Kind: kind, Lane: lane, Scope: "package", Name: name, Prior: prior, Message: "removed obsolete bound"}
}

func sortedKeys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func applyQualityPolicyUpdate(name string, policy localQualityPolicy, samples []qualityReport) (qualityReport, []finding, error) {
	if len(samples) != policy.Timing.UpdateSamples {
		return qualityReport{}, nil, fmt.Errorf("quality policy update requires %d samples, got %d", policy.Timing.UpdateSamples, len(samples))
	}
	representative, err := aggregatePolicySamples(samples)
	if err != nil {
		return qualityReport{}, nil, err
	}
	updated, changes := updatedQualityPolicy(policy, representative)
	if len(changes) == 0 {
		return representative, nil, nil
	}
	if err := writeQualityPolicy(name, updated); err != nil {
		return representative, nil, err
	}
	return representative, changes, nil
}

func aggregatePolicySamples(samples []qualityReport) (qualityReport, error) {
	if len(samples) == 0 {
		return qualityReport{}, errors.New("quality policy update requires at least one complete sample")
	}
	for index, sample := range samples {
		if !sample.Complete {
			return qualityReport{}, fmt.Errorf("quality policy sample %d is incomplete", index+1)
		}
		for _, laneName := range laneNames {
			lane := sample.Lanes[laneName]
			if !lane.Passed || lane.Failure != nil {
				return qualityReport{}, fmt.Errorf("quality policy sample %d has a failed %s lane", index+1, laneName)
			}
		}
	}
	result := samples[0]
	result.Lanes = make(map[string]laneReport, len(laneNames))
	result.MeasurementSamples = len(samples)
	for _, laneName := range laneNames {
		reference := samples[0].Lanes[laneName]
		suiteSamples := make([]float64, 0, len(samples))
		packageSamples := make(map[string][]float64, len(reference.Timings))
		for index, sample := range samples {
			lane := sample.Lanes[laneName]
			if err := comparableLane(reference, lane); err != nil {
				return qualityReport{}, fmt.Errorf("quality policy sample %d %s lane is not comparable: %w", index+1, laneName, err)
			}
			suiteSamples = append(suiteSamples, laneSelectedTestSeconds(lane))
			for _, timing := range lane.Timings {
				packageSamples[timing.Name] = append(packageSamples[timing.Name], timing.Seconds)
			}
		}
		aggregated := reference
		aggregated.SelectedTestSeconds = medianFloat64(suiteSamples)
		aggregated.Timings = make([]packageTiming, 0, len(packageSamples))
		for _, name := range sortedKeys(packageSamples) {
			aggregated.Timings = append(aggregated.Timings, packageTiming{Name: name, Seconds: medianFloat64(packageSamples[name])})
		}
		result.Lanes[laneName] = aggregated
	}
	return result, nil
}

func comparableLane(reference, candidate laneReport) error {
	if reference.TestCount != candidate.TestCount {
		return fmt.Errorf("test-event count changed from %d to %d", reference.TestCount, candidate.TestCount)
	}
	if !reflect.DeepEqual(packageStatementTotals(reference.Packages), packageStatementTotals(candidate.Packages)) {
		return errors.New("coverage package structure changed")
	}
	if !reflect.DeepEqual(timingNames(reference.Timings), timingNames(candidate.Timings)) {
		return errors.New("selected-test package structure changed")
	}
	return nil
}

func packageStatementTotals(packages []packageMetric) map[string]int {
	result := make(map[string]int, len(packages))
	for _, pkg := range packages {
		result[pkg.Name] = pkg.StatementTotal
	}
	return result
}

func timingNames(timings []packageTiming) []string {
	result := make([]string, len(timings))
	for index, timing := range timings {
		result[index] = timing.Name
	}
	sort.Strings(result)
	return result
}

func medianFloat64(values []float64) float64 {
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle]
	}
	return (ordered[middle-1] + ordered[middle]) / 2
}
