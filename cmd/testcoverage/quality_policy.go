package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
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
	RelativeHeadroom        float64 `yaml:"relative_headroom" json:"relative_headroom"`
	AbsoluteHeadroomSeconds float64 `yaml:"absolute_headroom_seconds" json:"absolute_headroom_seconds"`
}

type localTimingSettings struct {
	Suite   timingBoundSettings `yaml:"suite" json:"suite"`
	Package timingBoundSettings `yaml:"package" json:"package"`
}

type coverageBounds struct {
	FloorPercent float64            `yaml:"floor_percent"`
	Packages     map[string]float64 `yaml:"packages"`
}

type laneQualityPolicy struct {
	Coverage coverageBounds `yaml:"coverage"`
}

type localQualityPolicy struct {
	Version           int                          `yaml:"version"`
	PackageExclusions []string                     `yaml:"package_exclusions,omitempty"`
	Coverage          localCoverageSettings        `yaml:"coverage"`
	Timing            localTimingSettings          `yaml:"timing"`
	Lanes             map[string]laneQualityPolicy `yaml:"lanes"`
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
	if err := validatePackageExclusions(policy.PackageExclusions); err != nil {
		return err
	}
	if err := validateCoverageSettings("repository", policy.Coverage.Repository); err != nil {
		return err
	}
	if err := validateCoverageSettings("package", policy.Coverage.Package); err != nil {
		return err
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
	var coverageViolations, updates []finding
	for _, laneName := range laneNames {
		bounds := policy.Lanes[laneName]
		lane := report.Lanes[laneName]
		coverageViolations, updates = assessCoverageBound(
			coverageViolations, updates, laneName, "repository", "",
			bounds.Coverage.FloorPercent, percentage(guardedCoverage(lane, policy.PackageExclusions)), policy.Coverage.Repository,
		)

		measuredCoverage := reportCoveragePercentages(lane.Packages)
		for _, packageName := range sortedUnionKeys(bounds.Coverage.Packages, measuredCoverage) {
			if packageExcluded(policy.PackageExclusions, packageName) {
				continue
			}
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

	}
	switch {
	case len(coverageViolations) > 0:
		return decision{Status: statusRegression, Findings: append(coverageViolations, updates...)}
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
		headroomPercent: settings.HeadroomPercentagePoints,
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
			lanePolicy.Coverage.FloorPercent, percentage(guardedCoverage(lane, policy.PackageExclusions)), policy.Coverage.Repository,
			changes, laneName, "repository", "",
		)
		measuredCoverage := guardedCoverageFloors(reportCoveragePercentages(lane.Packages), policy.PackageExclusions)
		lanePolicy.Coverage.Packages, changes = updateCoveragePackages(
			lanePolicy.Coverage.Packages, measuredCoverage, policy.Coverage.Package, changes, laneName,
		)

		updated.Lanes[laneName] = lanePolicy
	}
	return updated, changes
}

func cloneQualityPolicy(policy localQualityPolicy) localQualityPolicy {
	result := policy
	result.PackageExclusions = append([]string(nil), policy.PackageExclusions...)
	result.Lanes = make(map[string]laneQualityPolicy, len(policy.Lanes))
	for name, lane := range policy.Lanes {
		lane.Coverage.Packages = cloneFloatMap(lane.Coverage.Packages)
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

func applyQualityPolicyUpdate(name string, policy localQualityPolicy, report qualityReport) (qualityReport, []finding, error) {
	if !report.Complete {
		return report, nil, errors.New("quality policy collection is incomplete")
	}
	for _, laneName := range laneNames {
		lane, found := report.Lanes[laneName]
		if !found || !lane.Passed || lane.Failure != nil {
			report.Complete = false
			return report, nil, fmt.Errorf("quality policy collection has a missing or failed %s lane", laneName)
		}
	}
	report = withCoverageExclusions(report, policy.PackageExclusions)
	updated, changes := updatedQualityPolicy(policy, report)
	if len(changes) == 0 {
		return report, nil, nil
	}
	if err := writeQualityPolicy(name, updated); err != nil {
		return report, nil, err
	}
	return report, changes, nil
}
