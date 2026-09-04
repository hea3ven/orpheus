package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestQualityPolicySerializationIsDeterministicAndRoundTrips(t *testing.T) {
	policy := testLocalQualityPolicy()
	policy.Lanes["unit"].Coverage.Packages["example.test/z"] = 60
	policy.Lanes["unit"].Coverage.Packages["example.test/a"] = 70

	first, err := marshalQualityPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := marshalQualityPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("quality policy serialization changed between calls:\n%s\n%s", first, second)
	}
	if strings.Index(string(first), "example.test/a") > strings.Index(string(first), "example.test/z") {
		t.Fatalf("package entries are not sorted:\n%s", first)
	}

	path := filepath.Join(testutil.CanonicalTempDir(t), ".quality.yml")
	if err := os.WriteFile(path, first, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadQualityPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, policy) {
		t.Fatalf("loaded policy = %#v, want %#v", loaded, policy)
	}
}

func TestTrackedQualityPolicyUsesCanonicalSerialization(t *testing.T) {
	path := filepath.Join("..", "..", ".quality.yml")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := loadQualityPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := marshalQualityPolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(contents, canonical) {
		t.Fatalf("tracked quality policy is not canonical; serialize it through writeQualityPolicy")
	}
}

func TestLoadQualityPolicyRejectsMalformedAndUnknownData(t *testing.T) {
	valid := validPolicyYAML(t)
	tests := []struct {
		name     string
		contents string
		want     string
	}{
		{name: "malformed YAML", contents: "version: [", want: "did not find expected node content"},
		{name: "unknown field", contents: strings.Replace(valid, "version: 1", "version: 1\nunknown: true", 1), want: "field unknown not found"},
		{name: "missing lane", contents: strings.Replace(valid, "  integration:\n", "  other:\n", 1), want: "no integration lane"},
		{name: "invalid floor", contents: strings.Replace(valid, "floor_percent: 49.5", "floor_percent: 101", 1), want: "between 0 and 100"},
		{name: "invalid timing", contents: strings.Replace(valid, "ceiling_seconds: 1", "ceiling_seconds: 0", 1), want: "finite and positive"},
		{name: "second document", contents: valid + "---\nversion: 1\n", want: "multiple YAML documents"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(testutil.CanonicalTempDir(t), ".quality.yml")
			if err := os.WriteFile(path, []byte(test.contents), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := loadQualityPolicy(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadQualityPolicy error = %v, want text %q", err, test.want)
			}
		})
	}
}

func TestQualityPolicyCoverageBoundaries(t *testing.T) {
	policy := testLocalQualityPolicy()

	inside := testQualityReport()
	setLaneCoverage(inside, "unit", 1000, 496)
	if got := assessQualityPolicy(policy, inside); got.Status != statusPass {
		t.Fatalf("coverage inside refresh band = %#v, want pass", got)
	}

	atRefresh := testQualityReport()
	setLaneCoverage(atRefresh, "unit", 1000, 495)
	if got := assessQualityPolicy(policy, atRefresh); got.Status != statusPolicyUpdateRequired || !hasFinding(got, "coverage_policy", "repository") {
		t.Fatalf("coverage at lower refresh boundary = %#v, want policy update", got)
	}

	improved := testQualityReport()
	setLaneCoverage(improved, "unit", 1000, 505)
	if got := assessQualityPolicy(policy, improved); got.Status != statusPolicyUpdateRequired || !hasFinding(got, "coverage_policy", "repository") {
		t.Fatalf("coverage at upper refresh boundary = %#v, want policy update", got)
	}

	violated := testQualityReport()
	setLaneCoverage(violated, "unit", 1000, 494)
	if got := assessQualityPolicy(policy, violated); got.Status != statusRegression || !hasFinding(got, "coverage_policy", "repository") {
		t.Fatalf("coverage below floor = %#v, want coverage violation", got)
	}
}

func TestQualityPolicyTimingBoundariesAndPackageViolation(t *testing.T) {
	policy := testLocalQualityPolicy()
	unitPolicy := policy.Lanes["unit"]
	unitPolicy.Timing.CeilingSeconds = 1.5
	unitPolicy.Timing.Packages["example.test/pkg"] = 1.5
	policy.Lanes["unit"] = unitPolicy

	inside := testQualityReport()
	lane := inside.Lanes["unit"]
	lane.Timings[0].Seconds = 1.49
	lane.SelectedTestSeconds = 1.49
	inside.Lanes["unit"] = lane
	if got := assessQualityPolicy(policy, inside); got.Status != statusPass {
		t.Fatalf("timing inside refresh band = %#v, want pass", got)
	}

	atRefresh := cloneReport(t, inside)
	lane = atRefresh.Lanes["unit"]
	lane.Timings[0].Seconds = 1.5
	lane.SelectedTestSeconds = 1.5
	atRefresh.Lanes["unit"] = lane
	if got := assessQualityPolicy(policy, atRefresh); got.Status != statusPolicyUpdateRequired {
		t.Fatalf("timing at refresh boundary = %#v, want policy update", got)
	}

	packageViolation := cloneReport(t, inside)
	lane = packageViolation.Lanes["unit"]
	lane.Timings[0].Seconds = 1.501
	lane.SelectedTestSeconds = 1
	packageViolation.Lanes["unit"] = lane
	got := assessQualityPolicy(policy, packageViolation)
	if got.Status != statusTimingFailed || !hasFinding(got, "timing_policy", "package") {
		t.Fatalf("package timing above ceiling = %#v, want blocking timing violation", got)
	}
}

func TestQualityPolicyUpdateMovesCrossedBoundsAndReconcilesPackages(t *testing.T) {
	policy := testLocalQualityPolicy()
	unit := policy.Lanes["unit"]
	unit.Coverage.Packages["example.test/removed"] = 25
	unit.Timing.Packages["example.test/removed"] = 2
	policy.Lanes["unit"] = unit

	report := testQualityReportWithPackages(
		packageMetric{Name: "example.test/pkg", coverageMetric: coverageMetric{StatementTotal: 100, CoveredStatements: 40}},
		packageMetric{Name: "example.test/new", coverageMetric: coverageMetric{StatementTotal: 100, CoveredStatements: 80}},
	)
	lane := report.Lanes["unit"]
	lane.SelectedTestSeconds = 2
	lane.Timings = []packageTiming{{Name: "example.test/new", Seconds: 0.2}, {Name: "example.test/pkg", Seconds: 2}}
	report.Lanes["unit"] = lane

	updated, changes := updatedQualityPolicy(policy, report)
	got := updated.Lanes["unit"]
	if got.Coverage.FloorPercent != 59.5 {
		t.Fatalf("unit coverage floor = %v, want 59.5", got.Coverage.FloorPercent)
	}
	if got.Coverage.Packages["example.test/pkg"] != 38 || got.Coverage.Packages["example.test/new"] != 78 {
		t.Fatalf("updated package coverage = %#v", got.Coverage.Packages)
	}
	if _, found := got.Coverage.Packages["example.test/removed"]; found {
		t.Fatal("removed coverage package remained in policy")
	}
	if got.Timing.CeilingSeconds != 2.5 || got.Timing.Packages["example.test/pkg"] != 3 || got.Timing.Packages["example.test/new"] != 0.45 {
		t.Fatalf("updated timings = %#v, suite %v", got.Timing.Packages, got.Timing.CeilingSeconds)
	}
	if _, found := got.Timing.Packages["example.test/removed"]; found {
		t.Fatal("removed timing package remained in policy")
	}
	if len(changes) < 6 {
		t.Fatalf("changes = %#v, want changed, added, and removed bounds", changes)
	}
}

func TestQualityPolicyUpdateLowersTimingCeilingsAfterMaterialImprovement(t *testing.T) {
	policy := testLocalQualityPolicy()
	unit := policy.Lanes["unit"]
	unit.Timing.CeilingSeconds = 3
	unit.Timing.Packages["example.test/pkg"] = 3
	policy.Lanes["unit"] = unit

	report := testQualityReport()
	lane := report.Lanes["unit"]
	lane.SelectedTestSeconds = 1
	lane.Timings[0].Seconds = 1
	report.Lanes["unit"] = lane

	updated, _ := updatedQualityPolicy(policy, report)
	unit = updated.Lanes["unit"]
	if unit.Timing.CeilingSeconds != 1.5 || unit.Timing.Packages["example.test/pkg"] != 1.5 {
		t.Fatalf("improved timing ceilings = suite %v, packages %#v", unit.Timing.CeilingSeconds, unit.Timing.Packages)
	}
}

func TestQualityPolicyUpdateLeavesInBandBoundsUnchanged(t *testing.T) {
	policy := testLocalQualityPolicy()
	report := testQualityReport()
	lane := report.Lanes["unit"]
	lane.Coverage = coverageMetric{StatementTotal: 1000, CoveredStatements: 496}
	lane.Packages[0].coverageMetric = lane.Coverage
	lane.SelectedTestSeconds = 0.51
	lane.Timings[0].Seconds = 0.51
	report.Lanes["unit"] = lane

	updated, changes := updatedQualityPolicy(policy, report)
	if !reflect.DeepEqual(updated, policy) {
		t.Fatalf("in-band update changed policy:\nold %#v\nnew %#v", policy, updated)
	}
	if len(changes) != 0 {
		t.Fatalf("in-band update reported changes: %#v", changes)
	}
}

func TestAggregatePolicySamplesUsesFiveTimingMedians(t *testing.T) {
	samples := make([]qualityReport, 5)
	for index, seconds := range []float64{5, 1, 4, 2, 3} {
		samples[index] = testQualityReport()
		for _, laneName := range laneNames {
			lane := samples[index].Lanes[laneName]
			lane.SelectedTestSeconds = seconds
			lane.Timings[0].Seconds = seconds / 2
			samples[index].Lanes[laneName] = lane
		}
	}

	got, err := aggregatePolicySamples(samples)
	if err != nil {
		t.Fatal(err)
	}
	if got.MeasurementSamples != 5 {
		t.Fatalf("measurement samples = %d, want 5", got.MeasurementSamples)
	}
	for _, laneName := range laneNames {
		lane := got.Lanes[laneName]
		if lane.SelectedTestSeconds != 3 || lane.Timings[0].Seconds != 1.5 {
			t.Fatalf("%s timing medians = suite %v, package %#v", laneName, lane.SelectedTestSeconds, lane.Timings)
		}
	}
}

func TestInBandSamplesLeaveQualityPolicyFileUnchanged(t *testing.T) {
	path := filepath.Join(testutil.CanonicalTempDir(t), ".quality.yml")
	policy := testLocalQualityPolicy()
	if err := writeQualityPolicy(path, policy); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]qualityReport, policy.Timing.UpdateSamples)
	for index := range samples {
		samples[index] = testQualityReport()
	}
	if _, changes, err := applyQualityPolicyUpdate(path, policy, samples); err != nil || len(changes) != 0 {
		t.Fatalf("apply in-band policy update = %d changes, %v", len(changes), err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("in-band samples rewrote the policy")
	}
}

func TestFailedSamplesCannotModifyQualityPolicy(t *testing.T) {
	path := filepath.Join(testutil.CanonicalTempDir(t), ".quality.yml")
	policy := testLocalQualityPolicy()
	if err := writeQualityPolicy(path, policy); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	samples := make([]qualityReport, policy.Timing.UpdateSamples)
	for index := range samples {
		samples[index] = testQualityReport()
	}
	samples[2].Complete = false
	if _, _, err := applyQualityPolicyUpdate(path, policy, samples); err == nil {
		t.Fatal("incomplete sample produced a policy update")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("incomplete sample modified the policy")
	}
}

func TestAggregatePolicySamplesRejectsFailedAndInconsistentMeasurements(t *testing.T) {
	failed := []qualityReport{testQualityReport(), testQualityReport()}
	failed[1].Complete = false
	if _, err := aggregatePolicySamples(failed); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("incomplete sample error = %v", err)
	}

	inconsistent := []qualityReport{testQualityReport(), testQualityReport()}
	lane := inconsistent[1].Lanes["integration"]
	lane.TestCount++
	inconsistent[1].Lanes["integration"] = lane
	if _, err := aggregatePolicySamples(inconsistent); err == nil || !strings.Contains(err.Error(), "not comparable") {
		t.Fatalf("inconsistent sample error = %v", err)
	}
}

func testLocalQualityPolicy() localQualityPolicy {
	policy := localQualityPolicy{
		Version: qualityPolicyVersion,
		Coverage: localCoverageSettings{
			Repository: coverageBoundSettings{SignificancePercentagePoints: 0.5, HeadroomPercentagePoints: 0.5},
			Package:    coverageBoundSettings{SignificancePercentagePoints: 2, HeadroomPercentagePoints: 2},
		},
		Timing: localTimingSettings{
			UpdateSamples: 5,
			Suite:         timingBoundSettings{RelativeHeadroom: 0.25, AbsoluteHeadroomSeconds: 0.5},
			Package:       timingBoundSettings{RelativeHeadroom: 0.5, AbsoluteHeadroomSeconds: 0.25},
		},
		Lanes: make(map[string]laneQualityPolicy, len(laneNames)),
	}
	for _, laneName := range laneNames {
		policy.Lanes[laneName] = laneQualityPolicy{
			Coverage: coverageBounds{FloorPercent: 49.5, Packages: map[string]float64{"example.test/pkg": 48}},
			Timing:   timingBounds{CeilingSeconds: 1, Packages: map[string]float64{"example.test/pkg": 1}},
		}
	}
	return policy
}

func validPolicyYAML(t *testing.T) string {
	t.Helper()
	contents, err := marshalQualityPolicy(testLocalQualityPolicy())
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
