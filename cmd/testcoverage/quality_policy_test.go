package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestQualityPolicySerializationIsDeterministicAndRoundTrips(t *testing.T) {
	policy := testLocalQualityPolicy()
	policy.PackageExclusions = []string{"internal/infra", "cmd/dev"}
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
		{name: "invalid timing", contents: strings.Replace(valid, "relative_headroom: 0.5", "relative_headroom: -1", 1), want: "finite and non-negative"},
		{name: "absolute timing is not policy", contents: strings.Replace(valid, "    coverage:", "    timing:\n      ceiling_seconds: 1\n    coverage:", 1), want: "field timing not found"},
		{name: "coverage requires five samples", contents: strings.Replace(valid, "coverage_update_samples: 5", "coverage_update_samples: 1", 1), want: "exactly 5"},
		{name: "second document", contents: valid + "---\nversion: 1\n", want: "multiple YAML documents"},
	}
	for _, exclusion := range []string{"''", "'/internal/infra'", "'./internal/infra'", "'internal/../infra'", "'internal/infra/'", "'internal/*'", "'internal/...'", "'internal/infra '", "'internal\\infra'", "'" + modulePath + "/internal/infra'", "'internal/infra'\n  - 'internal/infra'"} {
		tests = append(tests, struct{ name, contents, want string }{
			name:     "invalid exclusion " + exclusion,
			contents: strings.Replace(valid, "version: 1", "version: 1\npackage_exclusions:\n  - "+exclusion, 1),
			want:     "package exclusion",
		})
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

	for _, covered := range []int{0, 1000} {
		t.Run(fmt.Sprintf("excluded statements covered %d", covered), func(t *testing.T) {
			policy := testLocalQualityPolicy()
			policy.PackageExclusions = []string{"internal/infra", "internal/absent", "internal/new"}
			for _, laneName := range laneNames {
				// Stale and absent excluded floors must not require a refresh or removal.
				policy.Lanes[laneName].Coverage.Packages[modulePath+"/internal/infra"] = 99
				policy.Lanes[laneName].Coverage.Packages[modulePath+"/internal/absent"] = 99
			}
			report := testQualityReportWithPackages(
				packageMetric{Name: "example.test/pkg", coverageMetric: coverageMetric{StatementTotal: 1000, CoveredStatements: 500}},
				packageMetric{Name: modulePath + "/internal/infra", coverageMetric: coverageMetric{StatementTotal: 1000, CoveredStatements: covered}},
				packageMetric{Name: modulePath + "/internal/new", coverageMetric: coverageMetric{StatementTotal: 1000, CoveredStatements: covered}},
			)
			if got := assessQualityPolicy(policy, report); got.Status != statusPass || len(got.Findings) != 0 {
				t.Fatalf("excluded coverage affected guarded 50%% coverage: %#v", got)
			}
			for _, name := range []string{modulePath + "/internal/infra-extra", modulePath + "/internal/infra/child", "other.test/internal/infra"} {
				candidate := cloneReport(t, report)
				lane := candidate.Lanes["unit"]
				lane.Packages = append(lane.Packages, packageMetric{Name: name})
				candidate.Lanes["unit"] = lane
				got := assessQualityPolicy(policy, candidate)
				if got.Status != statusPolicyUpdateRequired || len(got.Findings) != 1 || got.Findings[0].Kind != "coverage_package" || got.Findings[0].Name != name {
					t.Fatalf("unlisted package %s escaped coverage structure enforcement: %#v", name, got)
				}
			}
		})
	}
}

func TestQualityPolicyUpdateMovesCrossedBoundsAndReconcilesPackages(t *testing.T) {
	policy := testLocalQualityPolicy()
	policy.PackageExclusions = []string{"internal/infra", "internal/absent"}
	unit := policy.Lanes["unit"]
	unit.Coverage.Packages[modulePath+"/internal/infra"] = 90
	unit.Coverage.Packages[modulePath+"/internal/absent"] = 90
	unit.Coverage.Packages["example.test/removed"] = 25
	policy.Lanes["unit"] = unit

	report := testQualityReportWithPackages(
		packageMetric{Name: "example.test/pkg", coverageMetric: coverageMetric{StatementTotal: 100, CoveredStatements: 40}},
		packageMetric{Name: "example.test/new", coverageMetric: coverageMetric{StatementTotal: 100, CoveredStatements: 80}},
		packageMetric{Name: modulePath + "/internal/infra", coverageMetric: coverageMetric{StatementTotal: 100, CoveredStatements: 100}},
	)
	lane := report.Lanes["unit"]
	lane.SelectedTestSeconds = 2
	lane.Timings = []packageTiming{{Name: "example.test/new", Seconds: 0.2}, {Name: "example.test/pkg", Seconds: 2}}
	report.Lanes["unit"] = lane

	path := filepath.Join(testutil.CanonicalTempDir(t), ".quality.yml")
	samples := make([]qualityReport, 5)
	for i := range samples {
		samples[i] = report
	}
	_, changes, err := applyQualityPolicyUpdate(path, policy, samples)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := loadQualityPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated.PackageExclusions, []string{"internal/infra", "internal/absent"}) {
		t.Fatalf("updated exclusions = %v", updated.PackageExclusions)
	}
	for _, laneName := range laneNames {
		for _, name := range updated.PackageExclusions {
			if _, found := updated.Lanes[laneName].Coverage.Packages[modulePath+"/"+name]; found {
				t.Fatalf("%s policy recreated excluded floor for %s", laneName, name)
			}
		}
	}
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
	if len(changes) < 3 {
		t.Fatalf("changes = %#v, want changed, added, and removed coverage bounds", changes)
	}
	var output strings.Builder
	updatedReport := report
	updatedReport.Decision = decision{Status: statusPass, Findings: changes}
	printReportTo(&output, updatedReport, "report.json")
	for _, want := range []string{"proposed floor 59.500%", "proposed floor 38.000%", "proposed floor 78.000%", "removed obsolete bound (floor 25.000%)"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("explicit policy update missing %q: %s", want, output.String())
		}
	}
}

func TestAggregatePolicySamplesUsesFiveTimingMedians(t *testing.T) {
	samples := make([]qualityReport, 5)
	for index, seconds := range []float64{5, 1, 4, 2, 3} {
		samples[index] = testQualityReport()
		for _, laneName := range laneNames {
			lane := samples[index].Lanes[laneName]
			lane.SelectedTestSeconds = seconds
			lane.WallSeconds = seconds / 4
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
		if lane.SelectedTestSeconds != 3 || lane.WallSeconds != 0.75 || lane.Timings[0].Seconds != 1.5 {
			t.Fatalf("%s timing medians = work %v, wall %v, package %#v", laneName, lane.SelectedTestSeconds, lane.WallSeconds, lane.Timings)
		}
	}
}

func TestInBandSamplesLeaveQualityPolicyFileAndProposedBoundsUnchanged(t *testing.T) {
	for _, covered := range []int{496, 500} {
		t.Run(fmt.Sprint(covered), func(t *testing.T) {
			path := filepath.Join(testutil.CanonicalTempDir(t), ".quality.yml")
			policy := testLocalQualityPolicy()
			if err := writeQualityPolicy(path, policy); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			samples := make([]qualityReport, policy.UpdateSamples)
			for i := range samples {
				samples[i] = testQualityReport()
				setLaneCoverage(samples[i], "unit", 1000, covered)
			}

			proposed, changes := updatedQualityPolicy(policy, samples[0])
			if !reflect.DeepEqual(proposed, policy) || len(changes) != 0 {
				t.Fatalf("in-band proposal = %#v, changes %#v", proposed, changes)
			}
			if _, changes, err := applyQualityPolicyUpdate(path, policy, samples); err != nil || len(changes) != 0 {
				t.Fatalf("apply in-band update = %#v, %v", changes, err)
			}

			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("in-band update rewrote the policy")
			}
		})
	}
}

func TestFailedSamplesCannotModifyQualityPolicy(t *testing.T) {
	path := filepath.Join(testutil.CanonicalTempDir(t), ".quality.yml")
	policy := testLocalQualityPolicy()
	policy.PackageExclusions = []string{"internal/infra"}
	if err := writeQualityPolicy(path, policy); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"incomplete", "excluded test failure", "excluded statement drift", "too few samples"} {
		t.Run(name, func(t *testing.T) {
			samples := make([]qualityReport, policy.UpdateSamples)
			for index := range samples {
				samples[index] = testQualityReportWithPackages(
					packageMetric{Name: "example.test/pkg", coverageMetric: coverageMetric{StatementTotal: 1000, CoveredStatements: 500}},
					packageMetric{Name: modulePath + "/internal/infra", coverageMetric: coverageMetric{StatementTotal: 100}},
				)
			}
			lane := samples[2].Lanes["unit"]
			switch name {
			case "incomplete":
				samples[2].Complete = false
			case "excluded test failure":
				lane.Passed = false
				lane.Failure = &laneFailure{Failures: []testFailure{{Package: modulePath + "/internal/infra", Test: "TestBroken"}}}
			case "excluded statement drift":
				lane.Packages[1].StatementTotal++
				lane.Coverage.StatementTotal++
			case "too few samples":
				samples = samples[:4]
			}
			samples[2].Lanes["unit"] = lane
			if _, _, err := applyQualityPolicyUpdate(path, policy, samples); err == nil {
				t.Fatal("invalid samples produced a policy update")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("invalid samples modified the policy")
			}
		})
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
		Version:       qualityPolicyVersion,
		UpdateSamples: 5,
		Coverage: localCoverageSettings{
			Repository: coverageBoundSettings{SignificancePercentagePoints: 0.5, HeadroomPercentagePoints: 0.5},
			Package:    coverageBoundSettings{SignificancePercentagePoints: 2, HeadroomPercentagePoints: 2},
		},
		Timing: localTimingSettings{
			Suite:   timingBoundSettings{RelativeHeadroom: 0.5, AbsoluteHeadroomSeconds: 0.5},
			Package: timingBoundSettings{RelativeHeadroom: 0.75, AbsoluteHeadroomSeconds: 0.25},
		},
		Lanes: make(map[string]laneQualityPolicy, len(laneNames)),
	}
	for _, laneName := range laneNames {
		policy.Lanes[laneName] = laneQualityPolicy{
			Coverage: coverageBounds{FloorPercent: 49.5, Packages: map[string]float64{"example.test/pkg": 48}},
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
