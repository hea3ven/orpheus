package main

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
)

type sampleReference struct {
	Subject  string `json:"subject"`
	Revision string `json:"revision"`
	Warmup   bool   `json:"warmup"`
	Path     string `json:"path"`
}

type comparisonEvidence struct {
	Settings      localTimingSettings   `json:"tolerances"`
	BaseRevision  string                `json:"base_revision"`
	MergeRevision string                `json:"merge_revision"`
	Snapshot      *workingTreeSnapshot  `json:"working_tree_snapshot,omitempty"`
	Samples       []sampleReference     `json:"samples"`
	BaseMedians   map[string]laneReport `json:"base_medians,omitempty"`
	MergeMedians  map[string]laneReport `json:"merge_medians,omitempty"`
}

type comparisonSubject struct {
	Name     string
	Revision string
}

type sampleCollector func(subject, revision string, warmup bool, index int) (qualityReport, string, error)

// compareRevisions keeps coverage tied to the first complete candidate sample.
// Warmups establish compatibility but never enter the timing medians.
func compareRevisions(policy localQualityPolicy, base, merge string, collect sampleCollector, publish func(qualityReport) error) (qualityReport, error) {
	result := qualityReport{
		SchemaVersion:     reportSchemaVersion,
		PackageExclusions: append([]string(nil), policy.PackageExclusions...),
		Execution:         decision{Status: statusUnevaluated}, CoverageDecision: decision{Status: statusUnevaluated},
		Timing:     decision{Status: statusUnevaluated},
		Comparison: &comparisonEvidence{BaseRevision: base, MergeRevision: merge, Settings: policy.Timing},
	}
	if !commitPattern.MatchString(base) || !commitPattern.MatchString(merge) {
		return abortComparison(result, errors.New("comparison requires exact full commit IDs"))
	}
	subjects := []comparisonSubject{{Name: "base", Revision: base}, {Name: "merge", Revision: merge}}
	samples := map[string][]qualityReport{"base": nil, "merge": nil}
	references := make(map[string]qualityReport, 2)
	if err := collectWarmups(&result, references, subjects, collect); err != nil {
		return abortComparison(result, err)
	}
	pairs := 1
	for index := 1; index <= pairs; index++ {
		order := []comparisonSubject{subjects[0], subjects[1]}
		if index%2 == 0 {
			order = []comparisonSubject{subjects[1], subjects[0]}
		}
		for _, subject := range order {
			sample, err := collectValidated(&result, subject, false, index, collect)
			if err != nil {
				return abortComparison(result, err)
			}
			if subject.Name == "merge" && index == 1 {
				result.Lanes = withCoverageExclusions(sample, policy.PackageExclusions).Lanes
				result.CoverageDecision = assessQualityPolicy(policy, sample)
			}
			if err := compatibleWithReferences(references, subject.Name, sample); err != nil {
				return abortComparison(result, err)
			}
			samples[subject.Name] = append(samples[subject.Name], sample)
		}
		if err := aggregateTiming(&result, samples["base"], samples["merge"], policy.Timing); err != nil {
			return abortComparison(result, err)
		}
		if index == 1 && result.Timing.Status == statusTimingFailed {
			pairs = 5
		}
		if publish != nil {
			progress := result
			progress.Timing = decision{Status: statusUnevaluated}
			progress.Decision = combinedDecision(progress)
			if err := publish(progress); err != nil {
				return abortComparison(result, err)
			}
		}
	}
	result.Complete = true
	result.Execution = decision{Status: statusPass}
	result.Decision = combinedDecision(result)
	if result.Decision.Status != statusPass {
		return result, fmt.Errorf("quality gate: %s", result.Decision.Status)
	}
	return result, nil
}

func collectWarmups(result *qualityReport, references map[string]qualityReport, subjects []comparisonSubject, collect sampleCollector) error {
	for _, subject := range subjects {
		sample, err := collectValidated(result, subject, true, 0, collect)
		if err != nil {
			return err
		}
		if err := compatibleWithReferences(references, subject.Name, sample); err != nil {
			return err
		}
		references[subject.Name] = sample
	}
	return nil
}

func aggregateTiming(result *qualityReport, base, merge []qualityReport, settings localTimingSettings) error {
	baseMedian, err := aggregateTimingSamples(base)
	if err != nil {
		return err
	}
	mergeMedian, err := aggregateTimingSamples(merge)
	if err != nil {
		return err
	}
	result.MeasurementSamples = len(base)
	result.Comparison.BaseMedians = baseMedian.Lanes
	result.Comparison.MergeMedians = mergeMedian.Lanes
	result.Timing = compareTiming(settings, baseMedian, mergeMedian)
	return nil
}

func collectValidated(result *qualityReport, subject comparisonSubject, warmup bool, index int, collect sampleCollector) (qualityReport, error) {
	sample, path, err := collect(subject.Name, subject.Revision, warmup, index)
	result.Comparison.Samples = append(result.Comparison.Samples, sampleReference{Subject: subject.Name, Revision: subject.Revision, Warmup: warmup, Path: path})
	for _, name := range laneNames {
		if failure := sample.Lanes[name].Failure; failure != nil {
			result.sampleFailures = append(result.sampleFailures, reportFailure{subject: subject.Name, lane: name, failure: failure})
		}
	}
	if err != nil {
		result.Execution = decision{Status: statusExecutionFailed, Findings: sample.Execution.Findings}
		if sample.Execution.Status == statusTestFailed {
			result.Execution.Status = statusTestFailed
		}
		return sample, fmt.Errorf("measurement %s warmup=%t sample=%d: %w", subject.Revision, warmup, index, err)
	}
	if err := validateTimingSample(sample, subject.Revision); err != nil {
		result.Execution = decision{Status: statusExecutionFailed}
		return sample, err
	}
	result.Execution = decision{Status: statusPass}
	return sample, nil
}

func compatibleWithReferences(references map[string]qualityReport, subject string, sample qualityReport) error {
	for priorSubject, reference := range references {
		if err := compatibleMeasurements(reference, sample); err != nil {
			return err
		}
		if priorSubject == subject || reference.Metadata.Source.Revision == sample.Metadata.Source.Revision {
			for _, lane := range laneNames {
				if err := comparableLane(reference.Lanes[lane], sample.Lanes[lane]); err != nil {
					return fmt.Errorf("%s %s within-revision sample changed: %w", subject, lane, err)
				}
			}
		}
	}
	return nil
}

func validateTimingSample(sample qualityReport, revision string) error {
	if sample.SchemaVersion != reportSchemaVersion || sample.Execution.Status != statusPass || !sample.Complete || len(sample.Lanes) != len(laneNames) {
		return errors.New("missing or incomplete measurement report")
	}
	if sample.Metadata == nil || sample.Metadata.Source.Revision != revision || !sample.Metadata.Source.Clean || len(sample.Metadata.Environment) == 0 {
		return errors.New("missing or inconsistent measurement source identity or environment")
	}
	for _, name := range laneNames {
		lane, found := sample.Lanes[name]
		if !found || lane.Lane != name || !lane.Passed || lane.Failure != nil || lane.TestCount <= 0 || len(lane.Packages) == 0 || len(lane.Timings) == 0 || len(lane.Command) == 0 {
			return fmt.Errorf("%s measurement is failed, empty, or incomplete", name)
		}
		if !reflect.DeepEqual(recordedCommand(lane.Command), recordedCommand(coverageCommand(name, "profile.cover", ""))) {
			return errors.New("sample has an unexpected measurement configuration")
		}
		if err := validateCoverageEvidence(lane); err != nil {
			return err
		}
		if !finiteNonNegative(lane.WallSeconds) || !finiteNonNegative(lane.SelectedTestSeconds) {
			return errors.New("invalid elapsed measurement")
		}
		seen := make(map[string]bool)
		for _, timing := range lane.Timings {
			if timing.Name == "" || seen[timing.Name] || !finiteNonNegative(timing.Seconds) {
				return errors.New("invalid package timing measurement")
			}
			seen[timing.Name] = true
		}
		if math.Abs(lane.SelectedTestSeconds-selectedTestSeconds(lane.Timings)) > 0.000001 {
			return errors.New("lane total disagrees with package work")
		}
	}
	return nil
}

func validateCoverageEvidence(lane laneReport) error {
	var total, covered, tests int
	seen := make(map[string]bool)
	for _, pkg := range lane.Packages {
		if pkg.Name == "" || seen[pkg.Name] || pkg.StatementTotal < 0 || pkg.CoveredStatements < 0 || pkg.CoveredStatements > pkg.StatementTotal {
			return errors.New("invalid package coverage evidence")
		}
		seen[pkg.Name] = true
		total += pkg.StatementTotal
		covered += pkg.CoveredStatements
	}
	if total <= 0 || total != lane.Coverage.StatementTotal || covered != lane.Coverage.CoveredStatements {
		return errors.New("coverage totals disagree with package evidence")
	}
	for _, count := range lane.PackageTestCounts {
		if count < 0 {
			return errors.New("invalid package test count")
		}
		tests += count
	}
	if tests != lane.TestCount {
		return errors.New("test counts disagree with package evidence")
	}
	return nil
}

func compatibleMeasurements(base, merge qualityReport) error {
	if !reflect.DeepEqual(base.Metadata.Environment, merge.Metadata.Environment) {
		return errors.New("measurement environments are incompatible")
	}
	for _, lane := range laneNames {
		if !reflect.DeepEqual(recordedCommand(base.Lanes[lane].Command), recordedCommand(merge.Lanes[lane].Command)) {
			return errors.New("measurement configurations are incompatible")
		}
	}
	return nil
}

func compareTiming(settings localTimingSettings, base, merge qualityReport) decision {
	result := decision{Status: statusPass}
	for _, name := range laneNames {
		result.Findings = appendTimingComparison(result.Findings, name, "suite", "", laneSelectedTestSeconds(base.Lanes[name]), laneSelectedTestSeconds(merge.Lanes[name]), settings.Suite)
		prior, candidate := timingMap(base.Lanes[name].Timings), timingMap(merge.Lanes[name].Timings)
		for _, pkg := range sortedUnionKeys(prior, candidate) {
			old, inBase := prior[pkg]
			current, inMerge := candidate[pkg]
			if !inBase || !inMerge {
				direction := "removed"
				if inMerge {
					direction = "added (included in lane total)"
				}
				result.Warnings = append(result.Warnings, finding{Kind: "timing_package", Lane: name, Scope: "package", Name: pkg, Message: "selected-test package " + direction})
				continue
			}
			result.Findings = appendTimingComparison(result.Findings, name, "package", pkg, old, current, settings.Package)
		}
	}
	if len(result.Findings) > 0 {
		result.Status = statusTimingFailed
	}
	return result
}

func appendTimingComparison(findings []finding, lane, scope, name string, base, merge float64, settings timingBoundSettings) []finding {
	ceiling := timingCeiling(base, settings)
	if merge <= ceiling {
		return findings
	}
	return append(findings, finding{
		Kind: "timing_comparison", Lane: lane, Scope: scope, Name: name, Prior: base, Current: merge,
		BudgetSeconds: ceiling, OverageSeconds: merge - ceiling,
		Message: "candidate work exceeds the same-runner base allowance",
	})
}

func abortComparison(result qualityReport, err error) (qualityReport, error) {
	result.Complete = false
	result.Timing = decision{Status: statusUnevaluated, Findings: []finding{{Kind: "measurement", Message: err.Error()}}}
	result.Decision = combinedDecision(result)
	return result, err
}

func combinedDecision(result qualityReport) decision {
	status := statusPass
	for _, check := range []decision{result.Execution, result.CoverageDecision, result.Timing} {
		if check.Status != statusPass && status == statusPass {
			status = check.Status
		}
	}
	findings := append([]finding(nil), result.Execution.Findings...)
	findings = append(findings, result.CoverageDecision.Findings...)
	findings = append(findings, result.Timing.Findings...)
	return decision{Status: status, Findings: findings, Warnings: result.Timing.Warnings}
}

func aggregateTimingSamples(samples []qualityReport) (qualityReport, error) {
	if len(samples) == 0 {
		return qualityReport{}, errors.New("timing aggregation requires at least one complete sample")
	}
	for index, sample := range samples {
		if !sample.Complete {
			return qualityReport{}, fmt.Errorf("timing sample %d is incomplete", index+1)
		}
		for _, laneName := range laneNames {
			lane := sample.Lanes[laneName]
			if !lane.Passed || lane.Failure != nil {
				return qualityReport{}, fmt.Errorf("timing sample %d has a failed %s lane", index+1, laneName)
			}
		}
	}
	result := samples[0]
	result.Lanes = make(map[string]laneReport, len(laneNames))
	result.MeasurementSamples = len(samples)
	for _, laneName := range laneNames {
		reference := samples[0].Lanes[laneName]
		suiteSamples := make([]float64, 0, len(samples))
		wallSamples := make([]float64, 0, len(samples))
		packageSamples := make(map[string][]float64, len(reference.Timings))
		for index, sample := range samples {
			lane := sample.Lanes[laneName]
			if err := comparableLane(reference, lane); err != nil {
				return qualityReport{}, fmt.Errorf("timing sample %d %s lane is not comparable: %w", index+1, laneName, err)
			}
			suiteSamples = append(suiteSamples, laneSelectedTestSeconds(lane))
			wallSamples = append(wallSamples, lane.WallSeconds)
			for _, timing := range lane.Timings {
				packageSamples[timing.Name] = append(packageSamples[timing.Name], timing.Seconds)
			}
		}
		aggregated := reference
		aggregated.WallSeconds = medianFloat64(wallSamples)
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
	if !reflect.DeepEqual(recordedCommand(reference.Command), recordedCommand(candidate.Command)) {
		return errors.New("test command changed")
	}
	if !reflect.DeepEqual(reference.PackageTestCounts, candidate.PackageTestCounts) {
		return errors.New("per-package test-event counts changed")
	}
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
