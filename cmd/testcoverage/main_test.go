package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestParseOptionsSupportsQualityModes(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "defaults"},
		{name: "quality policy update", args: []string{"-update-policy"}},
		{name: "scenario audit", args: []string{"-audit-scenarios"}},
		{name: "custom paths", args: []string{"-policy", "policy.yml", "-output", "report.json"}},
		{name: "reject combined quality modes", args: []string{"-update-policy", "-audit-scenarios"}, wantErr: true},
		{name: "reject retired comparison", args: []string{"-compare-to", "base.json"}, wantErr: true},
		{name: "reject retired baseline write", args: []string{"-write-baseline"}, wantErr: true},
		{name: "reject positional argument", args: []string{"extra"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseOptions(test.args)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseOptions(%v) error = %v, wantErr %t", test.args, err, test.wantErr)
			}
		})
	}
}

func TestFinishReportWritesPartialFailureReport(t *testing.T) {
	path := filepath.Join(testutil.CanonicalTempDir(t), "partial.json")
	report := testQualityReport()
	report.Complete = false
	report.Decision = decision{Status: statusTestFailed}
	lane := report.Lanes["unit"]
	lane.Passed = false
	lane.Failure = &laneFailure{Error: "exit status 1", Stderr: "compiler stderr", TestOutput: "json events"}
	report.Lanes["unit"] = lane

	if err := finishReport(path, report, os.ErrInvalid); !errors.Is(err, os.ErrInvalid) {
		t.Fatalf("finishReport error = %v, want original failure", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\"complete\": false", "compiler stderr", "json events"} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("partial report does not contain %q: %s", want, contents)
		}
	}
	markdown, err := os.ReadFile(reportSummaryPath(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Test quality", "**Tests failed.**", "| unit | **Fail** |"} {
		if !strings.Contains(string(markdown), want) {
			t.Fatalf("partial Markdown report does not contain %q:\n%s", want, markdown)
		}
	}
}

func TestDecodeTestEventsExcludesPackagesWithoutSelectedTestsFromTimings(t *testing.T) {
	events := strings.NewReader(strings.Join([]string{
		`{"Action":"start","Package":"example.test/no-tests"}`,
		`{"Action":"pass","Package":"example.test/no-tests","Elapsed":0.472}`,
		`{"Action":"run","Package":"example.test/with-tests","Test":"TestFeature"}`,
		`{"Action":"pass","Package":"example.test/with-tests","Test":"TestFeature","Elapsed":0.01}`,
		`{"Action":"pass","Package":"example.test/with-tests","Elapsed":0.02}`,
	}, "\n"))

	got := decodeTestEvents(events)
	if got.decodeErr != nil {
		t.Fatal(got.decodeErr)
	}
	if got.testCount != 1 {
		t.Fatalf("test count = %d, want 1", got.testCount)
	}
	if !reflect.DeepEqual(got.packages, map[string]float64{"example.test/with-tests": 0.02}) {
		t.Fatalf("package timings = %#v, want only package with selected tests", got.packages)
	}
}

func TestFindingMessageShowsQualityPolicyBounds(t *testing.T) {
	coverage := findingMessage(finding{
		Kind: "coverage_policy", Prior: 75.25, Current: 73.5, Proposed: 71.5, Threshold: 2,
		Message: "coverage is below its policy floor",
	})
	for _, want := range []string{"floor 75.250%", "measured 73.500%", "proposed floor 71.500%", "refresh threshold 2.000 percentage points"} {
		if !strings.Contains(coverage, want) {
			t.Fatalf("coverage finding message %q does not contain %q", coverage, want)
		}
	}

	timing := findingMessage(finding{
		Kind: "timing_policy", Prior: 0.415, Current: 0.472, Proposed: 0.722,
		Message: "timing exceeds its policy ceiling",
	})
	for _, want := range []string{"ceiling 0.415s", "measured 0.472s", "proposed ceiling 0.722s"} {
		if !strings.Contains(timing, want) {
			t.Fatalf("timing finding message %q does not contain %q", timing, want)
		}
	}
}

func TestPrintReportShowsPackageCoverageAndSelectedTestTiming(t *testing.T) {
	report := testQualityReportWithPackages(
		packageMetric{Name: "example.test/timed", coverageMetric: coverageMetric{StatementTotal: 10, CoveredStatements: 8}},
		packageMetric{Name: "example.test/not-timed", coverageMetric: coverageMetric{StatementTotal: 5, CoveredStatements: 2}},
	)
	for _, name := range laneNames {
		lane := report.Lanes[name]
		lane.Timings = []packageTiming{{Name: "example.test/timed", Seconds: 0.25}}
		report.Lanes[name] = lane
	}
	var output bytes.Buffer
	printReportTo(&output, report, "report.json")
	contents := output.String()
	for _, want := range []string{
		"unit: 10/15 statements (66.67%), 10 test events, 0.25s selected tests (1.00s wall)",
		"example.test/timed: 8/10 statements (80.00%), 0.25s",
		"example.test/not-timed: 2/5 statements (40.00%), no selected-test timing",
	} {
		if !strings.Contains(contents, want) {
			t.Fatalf("report does not contain %q:\n%s", want, contents)
		}
	}
	if strings.Contains(contents, "start_line") || strings.Contains(contents, "blocks") {
		t.Fatalf("report includes source-level coverage detail:\n%s", contents)
	}
}

func TestReportSummaryPathDoesNotOverwriteNonJSONReport(t *testing.T) {
	if got, want := reportSummaryPath("report.md"), "report.summary.md"; got != want {
		t.Fatalf("reportSummaryPath(report.md) = %q, want %q", got, want)
	}
}

func TestReportSummaryUsesTablesAndCollapsesPackageDetails(t *testing.T) {
	report := testQualityReportWithPackages(
		packageMetric{Name: "example.test/first", coverageMetric: coverageMetric{StatementTotal: 10, CoveredStatements: 8}},
		packageMetric{Name: "example.test/second", coverageMetric: coverageMetric{StatementTotal: 5, CoveredStatements: 2}},
	)
	report.Decision = decision{
		Status: statusRegression,
		Findings: []finding{{
			Kind: "coverage_policy", Lane: "unit", Scope: "repository", Prior: 70, Current: 66.67, Proposed: 66.17, Threshold: 0.5,
			Message: "coverage is below its policy floor",
		}},
		Warnings: []finding{{
			Kind: "timing_policy", Lane: "unit", Scope: "package", Name: "example.test/first", Prior: 0.75, Current: 0.8, Proposed: 1.05,
			Message: "timing diagnostic",
		}},
	}

	var output bytes.Buffer
	renderReportSummary(&output, report, "artifacts/test-coverage/report.json")
	contents := output.String()
	for _, want := range []string{
		"> [!CAUTION]",
		"**Coverage floor violated.**",
		"| Lane | Result | Coverage | Test events | Selected-test time | Wall time |",
		"| unit | **Pass** | 10/15 (66.67%) | 10 | 0.50s | 1.00s |",
		"### Blocking issues",
		"unit/repository",
		"floor 70.000%",
		"### Warnings",
		"ceiling 0.750s",
		"<details>",
		"<summary>Package coverage and timing</summary>",
		"Machine-readable report: `artifacts/test-coverage/report.json`",
	} {
		if !strings.Contains(contents, want) {
			t.Fatalf("Markdown report does not contain %q:\n%s", want, contents)
		}
	}
	if strings.Index(contents, "### Blocking issues") > strings.Index(contents, "<summary>Package coverage and timing</summary>") {
		t.Fatalf("blocking issues appear after package details:\n%s", contents)
	}
}

func TestNormalizeProfileDeduplicatesAndUnionsCoverage(t *testing.T) {
	profile := filepath.Join(testutil.CanonicalTempDir(t), "coverage.out")
	contents := "mode: set\n" +
		"example.test/collaborator/work.go:10.2,12.3 2 0\n" +
		"example.test/collaborator/work.go:10.2,12.3 2 1\n" +
		"example.test/consumer/consumer.go:5.1,6.2 1 0\n"
	if err := os.WriteFile(profile, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := normalizeProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if got.StatementTotal != 3 || got.CoveredStatements != 2 {
		t.Fatalf("normalized totals = %d/%d, want 2/3", got.CoveredStatements, got.StatementTotal)
	}
	if len(got.Packages) != 2 || len(got.Packages[0].Files[0].Blocks) != 1 {
		t.Fatalf("duplicate source block was retained: %#v", got.Packages)
	}
}

func TestFailuresFromEventsPreservesDecodedAssertionOutput(t *testing.T) {
	id := testID{Package: "example.test/pkg", Test: "TestBroken"}
	got := failuresFromEvents(map[testID]string{id: "want true, got false\n"}, map[testID]bool{id: true})
	if len(got) != 1 || got[0].Output != "want true, got false\n" {
		t.Fatalf("decoded failures = %#v", got)
	}
}

func TestCoverageCommandUsesOneCrossPackageCoverageExecution(t *testing.T) {
	command := strings.Join(coverageCommand("integration", "result.cover", ""), " ")
	for _, want := range []string{"-count=1", "-coverpkg=./...", "-covermode=set", "-tags=integration", "-run ^TestIntegration"} {
		if !strings.Contains(command, want) {
			t.Fatalf("command %q does not contain %q", command, want)
		}
	}
}

func TestCoverageEnvironmentRemovesOperatorSettings(t *testing.T) {
	got := environmentValues(coverageEnvironment([]string{
		"PATH=/test/bin", "NO_COLOR=1", "ORPHEUS_RESUME_SESSIONS=1", "CODEX_HOME=/operator/codex",
		coverageRunEnvironment + "=operator-value", "UNRELATED=value",
	}))
	for _, name := range []string{"NO_COLOR", "ORPHEUS_RESUME_SESSIONS", "CODEX_HOME"} {
		if _, found := got[name]; found {
			t.Errorf("coverage environment retained %s", name)
		}
	}
	if got["PATH"] != "/test/bin" || got["UNRELATED"] != "value" || got[coverageRunEnvironment] != "1" {
		t.Fatalf("coverage environment = %#v", got)
	}
}

func TestScenarioTargetsRetainPackageIdentity(t *testing.T) {
	input := strings.NewReader("{\"Action\":\"output\",\"Package\":\"example.test/first\",\"Output\":\"TestIntegrationSame\\n\"}\n" +
		"{\"Action\":\"output\",\"Package\":\"example.test/second\",\"Output\":\"TestIntegrationSame\\n\"}\n")
	targets, err := scenarioTargets(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Package == targets[1].Package || targets[0].Test != targets[1].Test {
		t.Fatalf("scenario targets = %#v", targets)
	}
}

func testQualityReport() qualityReport {
	return testQualityReportWithPackages(packageMetric{Name: "example.test/pkg", coverageMetric: coverageMetric{StatementTotal: 1000, CoveredStatements: 500}})
}

func testQualityReportWithPackages(packages ...packageMetric) qualityReport {
	total := coverageMetric{}
	for _, pkg := range packages {
		total.StatementTotal += pkg.StatementTotal
		total.CoveredStatements += pkg.CoveredStatements
	}
	report := qualityReport{SchemaVersion: reportSchemaVersion, Complete: true, Lanes: make(map[string]laneReport, 2)}
	for _, name := range laneNames {
		report.Lanes[name] = laneReport{
			Lane: name, Passed: true, WallSeconds: 1, TestCount: 10, Coverage: total,
			Packages: append([]packageMetric(nil), packages...),
			Timings:  []packageTiming{{Name: "example.test/pkg", Seconds: 0.5}},
		}
	}
	return report
}

func cloneReport(t *testing.T, value qualityReport) qualityReport {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result qualityReport
	if err := json.Unmarshal(contents, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func setLaneCoverage(report qualityReport, laneName string, total, covered int) {
	lane := report.Lanes[laneName]
	lane.Coverage = coverageMetric{StatementTotal: total, CoveredStatements: covered}
	lane.Packages = []packageMetric{{Name: "example.test/pkg", coverageMetric: lane.Coverage}}
	report.Lanes[laneName] = lane
}

func hasFinding(value decision, kind, scope string) bool {
	return containsFinding(value.Findings, kind, scope)
}

func hasWarning(value decision, kind, scope string) bool {
	return containsFinding(value.Warnings, kind, scope)
}

func containsFinding(findings []finding, kind, scope string) bool {
	for _, item := range findings {
		if item.Kind == kind && item.Scope == scope {
			return true
		}
	}
	return false
}

func environmentValues(entries []string) map[string]string {
	values := make(map[string]string, len(entries))
	for _, entry := range entries {
		name, value, found := strings.Cut(entry, "=")
		if found {
			values[name] = value
		}
	}
	return values
}
