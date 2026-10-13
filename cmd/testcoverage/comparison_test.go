package main

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
)

const fixtureBase = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const fixtureMerge = "cccccccccccccccccccccccccccccccccccccccc"

func timingFixture(revision string, seconds float64) qualityReport {
	report := testQualityReport()
	report.Execution = decision{Status: statusPass}
	report.Metadata = &measurementMetadata{Source: sourceIdentity{Revision: revision, Clean: true}, Environment: map[string]string{"collector": "same", "resources": "verified"}}
	for _, name := range laneNames {
		lane := report.Lanes[name]
		lane.Command = coverageCommand(name, "sample.cover", "")
		lane.Timings[0].Seconds = seconds
		lane.SelectedTestSeconds = seconds
		lane.PackageTestCounts = map[string]int{"example.test/pkg": 10}
		report.Lanes[name] = lane
	}
	return report
}

func TestSameRunnerComparisonExcludesWarmupsAndBoundsConfirmation(t *testing.T) {
	for _, test := range []struct {
		name   string
		values []float64
		want   string
		pairs  int
	}{
		{name: "unchanged", values: []float64{1}, want: statusPass, pairs: 1},
		{name: "confirmed slowdown", values: []float64{2, 2, 2, 2, 2}, want: statusTimingFailed, pairs: 5},
		{name: "transient slowdown", values: []float64{2, 1, 1, 1, 1}, want: statusPass, pairs: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls []string
			collect := func(_ string, revision string, warmup bool, index int) (qualityReport, string, error) {
				calls = append(calls, fmt.Sprintf("%s/%t/%d", revision[:1], warmup, index))
				seconds := 1.0
				if warmup {
					seconds = 1000
				}
				if revision == fixtureMerge && !warmup {
					seconds = test.values[index-1]
				}
				return timingFixture(revision, seconds), "sample.json", nil
			}

			result, err := compareRevisions(testLocalQualityPolicy(), fixtureBase, fixtureMerge, collect, nil)

			if result.Timing.Status != test.want || (err != nil) != (test.want != statusPass) || result.MeasurementSamples != test.pairs {
				t.Fatalf("comparison = %s, %d pairs, %v; want %s, %d pairs", result.Timing.Status, result.MeasurementSamples, err, test.want, test.pairs)
			}
			wantCalls := []string{"b/true/0", "c/true/0", "b/false/1", "c/false/1"}
			if test.pairs == 5 {
				wantCalls = append(wantCalls, "c/false/2", "b/false/2", "b/false/3", "c/false/3", "c/false/4", "b/false/4", "b/false/5", "c/false/5")
			}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("collection order = %v, want %v", calls, wantCalls)
			}
			if result.Execution.Status != statusPass || result.CoverageDecision.Status != statusPass || result.Comparison.BaseMedians["unit"].SelectedTestSeconds != 1 {
				t.Fatalf("independent results or warm-up exclusion = %#v", result)
			}
		})
	}
}

func TestTimingComparisonPreservesRoundedStrictCeilings(t *testing.T) {
	policy := testLocalQualityPolicy()
	for _, test := range []struct {
		name        string
		base, merge float64
		settings    timingBoundSettings
		fail        bool
	}{
		{name: "suite relative at ceiling", base: 2, merge: 3, settings: policy.Timing.Suite},
		{name: "suite relative above", base: 2, merge: 3.000001, settings: policy.Timing.Suite, fail: true},
		{name: "suite absolute at ceiling", base: 0.1, merge: 0.6, settings: policy.Timing.Suite},
		{name: "suite absolute above", base: 0.1, merge: 0.600001, settings: policy.Timing.Suite, fail: true},
		{name: "package relative at ceiling", base: 2, merge: 3.5, settings: policy.Timing.Package},
		{name: "package absolute at ceiling", base: 0.1, merge: 0.35, settings: policy.Timing.Package},
		{name: "package absolute above", base: 0.1, merge: 0.350001, settings: policy.Timing.Package, fail: true},
		{name: "rounded at ceiling", base: 0.1004, merge: 0.35, settings: policy.Timing.Package},
		{name: "rounded above ceiling", base: 0.1004, merge: 0.35001, settings: policy.Timing.Package, fail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := appendTimingComparison(nil, "unit", "package", "pkg", test.base, test.merge, test.settings)
			if (len(got) > 0) != test.fail {
				t.Fatalf("findings = %#v, want failure %t", got, test.fail)
			}
		})
	}
}

func TestTimingComparisonSeparatesPackageAndLaneRegressionsAndReportsStructure(t *testing.T) {
	base, merge := timingFixture(fixtureBase, 1), timingFixture(fixtureMerge, 1)
	lane := base.Lanes["unit"]
	lane.Timings = []packageTiming{{Name: "common", Seconds: 1}, {Name: "large", Seconds: 10}, {Name: "removed", Seconds: 0.1}}
	lane.SelectedTestSeconds = selectedTestSeconds(lane.Timings)
	base.Lanes["unit"] = lane
	lane.Timings = []packageTiming{{Name: "common", Seconds: 1.751}, {Name: "large", Seconds: 10}, {Name: "added", Seconds: 0.1}}
	lane.SelectedTestSeconds = selectedTestSeconds(lane.Timings)
	merge.Lanes["unit"] = lane

	got := compareTiming(testLocalQualityPolicy().Timing, base, merge)

	if got.Status != statusTimingFailed || !hasFinding(got, "timing_comparison", "package") || hasFinding(got, "timing_comparison", "suite") || len(got.Warnings) != 2 {
		t.Fatalf("package comparison = %#v", got)
	}
	lane.Timings[0].Seconds = 1
	lane.Timings[2].Seconds = 10
	lane.SelectedTestSeconds = selectedTestSeconds(lane.Timings)
	merge.Lanes["unit"] = lane
	got = compareTiming(testLocalQualityPolicy().Timing, base, merge)
	if !hasFinding(got, "timing_comparison", "suite") || hasFinding(got, "timing_comparison", "package") {
		t.Fatalf("new package work did not enter suite total: %#v", got)
	}
}

func TestConfirmationNeverErasesFirstCandidateCoverageFindings(t *testing.T) {
	for _, test := range []struct {
		name    string
		covered int
		want    string
	}{
		{name: "regression", covered: 494, want: statusRegression},
		{name: "refresh required", covered: 505, want: statusPolicyUpdateRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			collect := func(_ string, revision string, warmup bool, index int) (qualityReport, string, error) {
				seconds := 1.0
				if revision == fixtureMerge && index == 1 {
					seconds = 2
				}
				sample := timingFixture(revision, seconds)
				if revision == fixtureMerge && !warmup && index == 1 {
					setLaneCoverage(sample, "unit", 1000, test.covered)
				}
				return sample, "sample.json", nil
			}

			result, err := compareRevisions(testLocalQualityPolicy(), fixtureBase, fixtureMerge, collect, nil)

			if err == nil || result.Decision.Status != test.want || result.CoverageDecision.Status != test.want || result.Timing.Status != statusPass || result.MeasurementSamples != 5 {
				t.Fatalf("independent confirmed results = %#v, %v", result, err)
			}
			if result.Lanes["unit"].Coverage.CoveredStatements != test.covered {
				t.Fatal("confirmation replaced first-sample coverage")
			}
		})
	}
}

func TestMissingIncompatibleAndFailedEvidenceAbortWithoutSelectiveRetries(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutate     func(*qualityReport)
		collectErr error
	}{
		{name: "failed process", collectErr: errors.New("child exited 1")},
		{name: "incomplete", mutate: func(r *qualityReport) { r.Complete = false }},
		{name: "missing lane", mutate: func(r *qualityReport) { delete(r.Lanes, "unit") }},
		{name: "missing metadata", mutate: func(r *qualityReport) { r.Metadata = nil }},
		{name: "wrong identity", mutate: func(r *qualityReport) { r.Metadata.Source.Revision = fixtureBase }},
		{name: "dirty identity", mutate: func(r *qualityReport) { r.Metadata.Source.Clean = false }},
		{name: "resource drift", mutate: func(r *qualityReport) { r.Metadata.Environment["resources"] = "changed" }},
		{name: "test count drift", mutate: func(r *qualityReport) { lane := r.Lanes["unit"]; lane.TestCount++; r.Lanes["unit"] = lane }},
		{name: "package test count drift", mutate: func(r *qualityReport) { r.Lanes["unit"].PackageTestCounts["example.test/pkg"]++ }},
		{name: "command drift", mutate: func(r *qualityReport) {
			lane := r.Lanes["unit"]
			lane.Command = append(lane.Command, "-p=1")
			r.Lanes["unit"] = lane
		}},
		{name: "coverage structure drift", mutate: func(r *qualityReport) { r.Lanes["unit"].Packages[0].StatementTotal++ }},
		{name: "selected structure drift", mutate: func(r *qualityReport) { r.Lanes["unit"].Timings[0].Name = "other" }},
		{name: "invalid timing", mutate: func(r *qualityReport) { r.Lanes["unit"].Timings[0].Seconds = math.NaN() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			collect := func(_ string, revision string, warmup bool, index int) (qualityReport, string, error) {
				calls++
				sample := timingFixture(revision, 1)
				if revision == fixtureMerge && index == 1 {
					setLaneCoverage(sample, "unit", 1000, 494)
					lane := sample.Lanes["unit"]
					lane.Timings[0].Seconds = 2
					lane.SelectedTestSeconds = 2
					sample.Lanes["unit"] = lane
				}
				if revision == fixtureMerge && index == 2 {
					if test.mutate != nil {
						test.mutate(&sample)
					}
					return sample, "failed.json", test.collectErr
				}
				return sample, "sample.json", nil
			}

			result, err := compareRevisions(testLocalQualityPolicy(), fixtureBase, fixtureMerge, collect, nil)

			if err == nil || result.Timing.Status != statusUnevaluated || result.Complete || calls != 5 {
				t.Fatalf("abort = timing %s, calls %d, complete %t, err %v", result.Timing.Status, calls, result.Complete, err)
			}
			if result.CoverageDecision.Status != statusRegression || len(result.Timing.Findings) == 0 || len(result.Comparison.Samples) != calls {
				t.Fatalf("failure lost coverage/evidence: %#v", result)
			}
		})
	}
}

func TestCrossRevisionCountsAndStructureMayChange(t *testing.T) {
	collect := func(_ string, revision string, warmup bool, index int) (qualityReport, string, error) {
		sample := timingFixture(revision, 1)
		if revision == fixtureMerge {
			lane := sample.Lanes["unit"]
			lane.TestCount = 11
			lane.PackageTestCounts["example.test/pkg"] = 11
			lane.Packages[0].StatementTotal = 1001
			lane.Coverage.StatementTotal = 1001
			lane.Timings = append(lane.Timings, packageTiming{Name: "new", Seconds: 0.01})
			lane.SelectedTestSeconds = selectedTestSeconds(lane.Timings)
			sample.Lanes["unit"] = lane
		}
		return sample, "sample.json", nil
	}

	result, err := compareRevisions(testLocalQualityPolicy(), fixtureBase, fixtureMerge, collect, nil)

	if err != nil || result.Timing.Status != statusPass || len(result.Timing.Warnings) != 1 {
		t.Fatalf("legitimate cross-revision change = %#v, %v", result, err)
	}
}

func TestLocalQualityDoesNotEvaluateOrUpdateMachineTiming(t *testing.T) {
	policy := testLocalQualityPolicy()
	report := timingFixture(fixtureMerge, 1000000)

	got := assessQualityPolicy(policy, report)
	updated, changes := updatedQualityPolicy(policy, report)

	if got.Status != statusPass || len(changes) != 0 || !reflect.DeepEqual(updated, policy) {
		t.Fatalf("local timings affected coverage policy: %#v, %#v", got, changes)
	}
	var output strings.Builder
	report.Timing = decision{Status: statusNotEvaluated}
	renderReportSummary(&output, report, "report.json")
	if !strings.Contains(output.String(), "| Timing | not evaluated |") {
		t.Fatalf("local timing mislabeled: %s", output.String())
	}
}

func TestIncompleteTestStreamsCannotBecomeSuccessfulMeasurements(t *testing.T) {
	for _, input := range []string{"", `{"Action":"run","Package":"pkg","Test":"TestA"}`, `{"Action":"pass","Package":"pkg","Test":"TestA"}`} {
		events := decodeTestEvents(strings.NewReader(input))
		if err := completeTestEvents(events); err == nil {
			t.Fatalf("incomplete stream %q was accepted", input)
		}
	}
}

func TestConfirmationPublishesCoverageBeforeFurtherMeasurements(t *testing.T) {
	var progress []qualityReport
	collect := func(_ string, revision string, warmup bool, index int) (qualityReport, string, error) {
		if index == 2 && len(progress) != 1 {
			t.Fatal("first-pair evidence was not published before confirmation")
		}
		seconds := 1.0
		if revision == fixtureMerge && index == 1 {
			seconds = 2
		}
		sample := timingFixture(revision, seconds)
		if revision == fixtureMerge && index == 1 {
			setLaneCoverage(sample, "unit", 1000, 494)
		}
		return sample, "sample.json", nil
	}

	result, err := compareRevisions(testLocalQualityPolicy(), fixtureBase, fixtureMerge, collect, func(report qualityReport) error {
		progress = append(progress, report)
		return nil
	})

	if err == nil || len(progress) != 5 || result.CoverageDecision.Status != statusRegression {
		t.Fatalf("published comparison = %d reports, %#v, %v", len(progress), result, err)
	}
	for _, report := range progress {
		if report.Timing.Status != statusUnevaluated || report.CoverageDecision.Status != statusRegression || report.Complete {
			t.Fatalf("in-progress comparison falsely passed or lost coverage: %#v", report)
		}
	}
}

func TestCrossRevisionMeasurementConfigurationAndEnvironmentMustMatch(t *testing.T) {
	for _, mutate := range []func(*qualityReport){
		func(sample *qualityReport) { sample.Metadata.Environment["collector"] = "different" },
		func(sample *qualityReport) {
			lane := sample.Lanes["unit"]
			lane.Command = append(lane.Command, "-p=1")
			sample.Lanes["unit"] = lane
		},
	} {
		calls := 0
		collect := func(_ string, revision string, warmup bool, index int) (qualityReport, string, error) {
			calls++
			sample := timingFixture(revision, 1)
			if revision == fixtureMerge {
				mutate(&sample)
			}
			return sample, "sample.json", nil
		}

		result, err := compareRevisions(testLocalQualityPolicy(), fixtureBase, fixtureMerge, collect, nil)

		if err == nil || result.Timing.Status != statusUnevaluated || calls != 2 {
			t.Fatalf("incompatible warmups = %#v, %d calls, %v", result, calls, err)
		}
	}
}

func TestFirstCompleteCandidateCoverageRemainsVisibleWhenTimingIsIncompatible(t *testing.T) {
	collect := func(_ string, revision string, warmup bool, index int) (qualityReport, string, error) {
		sample := timingFixture(revision, 1)
		if revision == fixtureMerge && index == 1 {
			setLaneCoverage(sample, "unit", 1000, 494)
			sample.Metadata.Environment["resources"] = "incompatible"
		}
		return sample, "sample.json", nil
	}

	result, err := compareRevisions(testLocalQualityPolicy(), fixtureBase, fixtureMerge, collect, nil)

	if err == nil || result.Timing.Status != statusUnevaluated || result.CoverageDecision.Status != statusRegression || result.Execution.Status != statusPass {
		t.Fatalf("incompatible timing erased independently valid coverage: %#v, %v", result, err)
	}
}

type failedEvidenceWriter struct{}

func (failedEvidenceWriter) Write([]byte) (int, error) {
	return 0, errors.New("evidence storage failed")
}

func TestEvidenceWriteFailureDrainsChildOutputWithoutAcceptingMeasurement(t *testing.T) {
	input := strings.NewReader(strings.Repeat(`{"Action":"pass","Package":"pkg"}`+"\n", 10000))

	result := decodeTestEventsWithEvidence(input, failedEvidenceWriter{})

	if result.decodeErr == nil || input.Len() != 0 {
		t.Fatalf("evidence write failure left child output undrained or was accepted: %v, %d unread bytes", result.decodeErr, input.Len())
	}
}

func TestComparisonMeasuresIdenticalCommitIDsAsSeparateSubjects(t *testing.T) {
	var calls []string
	collect := func(subject, revision string, warmup bool, index int) (qualityReport, string, error) {
		calls = append(calls, fmt.Sprintf("%s/%t/%d", subject, warmup, index))
		return timingFixture(revision, 1), subject + ".json", nil
	}

	result, err := compareRevisions(testLocalQualityPolicy(), fixtureBase, fixtureBase, collect, nil)

	if err != nil || result.Decision.Status != statusPass || result.MeasurementSamples != 1 {
		t.Fatalf("identical source comparison = %#v, %v", result, err)
	}
	want := []string{"base/true/0", "merge/true/0", "base/false/1", "merge/false/1"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("subject collection order = %v, want %v", calls, want)
	}
	for index, reference := range result.Comparison.Samples {
		if reference.Subject != []string{"base", "merge"}[index%2] || reference.Revision != fixtureBase {
			t.Fatalf("identical revision lost its subject: %#v", reference)
		}
	}
}

func TestIdenticalCommitSubjectsStillRequireWithinRevisionStructure(t *testing.T) {
	collect := func(subject, revision string, warmup bool, index int) (qualityReport, string, error) {
		sample := timingFixture(revision, 1)
		if subject == "merge" {
			lane := sample.Lanes["unit"]
			lane.TestCount++
			lane.PackageTestCounts["example.test/pkg"]++
			sample.Lanes["unit"] = lane
		}
		return sample, subject + ".json", nil
	}

	result, err := compareRevisions(testLocalQualityPolicy(), fixtureBase, fixtureBase, collect, nil)

	if err == nil || result.Timing.Status != statusUnevaluated || result.Complete || len(result.Comparison.Samples) != 2 {
		t.Fatalf("same revision accepted inconsistent structure: %#v, %v", result, err)
	}
}
