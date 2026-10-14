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
		want    options
	}{
		{name: "defaults"},
		{name: "same-runner comparison", args: []string{"-compare-base", fixtureBase, "-compare-merge", fixtureMerge}, want: options{compareBase: fixtureBase, compareMerge: fixtureMerge}},
		{name: "working-tree comparison", args: []string{"-compare-worktree", "-compare-base", "main"}, want: options{compareWorktree: true, compareBase: "main"}},
		{name: "working-tree explicit output", args: []string{"-compare-worktree", "-compare-base", "main", "-output", "/fixture/new-reports/report.json"}, want: options{compareWorktree: true, compareBase: "main", output: "/fixture/new-reports/report.json"}},
		{name: "working-tree comparison requires explicit base", args: []string{"-compare-worktree"}, wantErr: true},
		{name: "working-tree comparison rejects merge", args: []string{"-compare-worktree", "-compare-base", "main", "-compare-merge", fixtureMerge}, wantErr: true},
		{name: "working-tree comparison rejects policy override", args: []string{"-compare-worktree", "-compare-base", "main", "-policy", "base.yml"}, wantErr: true},
		{name: "working-tree comparison rejects update", args: []string{"-compare-worktree", "-compare-base", "main", "-update-policy"}, wantErr: true},
		{name: "isolated measurement", args: []string{"-measure-revision", fixtureBase}, want: options{measureRevision: fixtureBase}},
		{name: "missing comparison identity", args: []string{"-compare-base", fixtureBase}, wantErr: true},
		{name: "reject comparison plus update", args: []string{"-compare-base", fixtureBase, "-compare-merge", fixtureMerge, "-update-policy"}, wantErr: true},
		{name: "reject comparison plus measurement", args: []string{"-compare-base", fixtureBase, "-compare-merge", fixtureMerge, "-measure-revision", fixtureBase}, wantErr: true},
		{name: "quality policy update", args: []string{"-update-policy"}, want: options{updatePolicy: true}},
		{name: "scenario audit", args: []string{"-audit-scenarios"}, want: options{auditScenarios: true}},
		{name: "focused scenario audit", args: []string{
			"-audit-scenarios",
			"-audit-package", "./internal/git",
			"-audit-package", "./internal/tasksource/beads",
			"-audit-run", "^TestIntegrationAdapterContract",
		}, want: options{auditScenarios: true, auditPackages: stringListFlag{"./internal/git", "./internal/tasksource/beads"}, auditRun: "^TestIntegrationAdapterContract"}},
		{name: "custom paths", args: []string{"-policy", "policy.yml", "-output", "report.json"}, want: options{policy: "policy.yml", output: "report.json"}},
		{name: "reject combined quality modes", args: []string{"-update-policy", "-audit-scenarios"}, wantErr: true},
		{name: "reject audit package without audit", args: []string{"-audit-package", "./internal/git"}, wantErr: true},
		{name: "reject audit regex without audit", args: []string{"-audit-run", "Git"}, wantErr: true},
		{name: "reject empty audit package", args: []string{"-audit-scenarios", "-audit-package", ""}, wantErr: true},
		{name: "reject retired comparison", args: []string{"-compare-to", "base.json"}, wantErr: true},
		{name: "reject retired baseline write", args: []string{"-write-baseline"}, wantErr: true},
		{name: "reject positional argument", args: []string{"extra"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseOptions(test.args)
			if (err != nil) != test.wantErr {
				t.Fatalf("parseOptions(%v) error = %v, wantErr %t", test.args, err, test.wantErr)
			}
			if test.wantErr {
				return
			}
			want := test.want
			if want.policy == "" {
				want.policy = ".quality.yml"
			}
			if want.output == "" && !want.compareWorktree {
				want.output = "artifacts/test-coverage/report.json"
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("parseOptions(%v) = %#v, want %#v", test.args, got, want)
			}
		})
	}
}

func TestAuditSelectionPreservesDefaultsAndRequestedPackagesInDiscoveryCommand(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want scenarioSelection
	}{
		{name: "defaults", args: []string{"-audit-scenarios"}, want: scenarioSelection{Packages: []string{"./..."}, TestPattern: "^TestIntegration"}},
		{name: "focused", args: []string{"-audit-scenarios", "-audit-package", "./internal/git", "-audit-package", "./internal/tasksource/beads", "-audit-run", "Git|Beads"}, want: scenarioSelection{Packages: []string{"./internal/git", "./internal/tasksource/beads"}, TestPattern: "Git|Beads"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			opts, err := parseOptions(test.args)
			if err != nil {
				t.Fatal(err)
			}

			got := opts.auditSelection()
			command := integrationListCommand(got)

			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("audit selection = %#v, want %#v", got, test.want)
			}
			wantCommand := append([]string{"go", "test", "-json", "-tags=integration", "-list", test.want.TestPattern}, test.want.Packages...)
			if !reflect.DeepEqual(command, wantCommand) {
				t.Fatalf("discovery command = %v, want %v", command, wantCommand)
			}
		})
	}
}

func TestFinishReportPreservesFailureEvidenceAndSeparateArtifacts(t *testing.T) {
	for _, filename := range []string{"partial.json", "partial.md"} {
		t.Run(filename, func(t *testing.T) {
			path := filepath.Join(testutil.CanonicalTempDir(t), filename)
			report := testQualityReport()
			report.Complete = false
			report.Decision = decision{Status: statusTestFailed}
			lane := report.Lanes["unit"]
			lane.Passed = false
			lane.Failure = &laneFailure{Error: "exit status 1", Stderr: "compiler stderr", TestOutput: "json events"}
			report.Lanes["unit"] = lane

			err := finishReport(path, report, os.ErrInvalid)

			if !errors.Is(err, os.ErrInvalid) {
				t.Fatalf("finishReport error = %v, want original failure", err)
			}
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var persisted qualityReport
			if err := json.Unmarshal(contents, &persisted); err != nil {
				t.Fatal(err)
			}
			if persisted.Complete || persisted.Lanes["unit"].Passed || persisted.Lanes["unit"].Failure == nil || !reflect.DeepEqual(persisted.Lanes["unit"].Failure, lane.Failure) {
				t.Fatalf("persisted failure = %#v", persisted)
			}
			summaryPath := strings.TrimSuffix(path, filepath.Ext(path)) + ".md"
			if filename == "partial.md" {
				summaryPath = strings.TrimSuffix(path, ".md") + ".summary.md"
			}
			markdown, err := os.ReadFile(summaryPath)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"Tests failed", "unit", "Fail"} {
				if !strings.Contains(string(markdown), want) {
					t.Fatalf("failure summary missing %q: %s", want, markdown)
				}
			}
		})
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
		Kind: "timing_comparison", Prior: 0.415, Current: 0.472, BudgetSeconds: 0.722, OverageSeconds: 0.01,
		Message: "timing exceeds its policy ceiling",
	})
	for _, want := range []string{"base 0.415s", "candidate 0.472s", "ceiling 0.722s"} {
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
		"unit: 10/15 statements (66.67%), 10 test events, 0.25s selected package work, 1.00s command wall time",
		"example.test/timed: 8/10 statements (80.00%), 0.25s",
		"example.test/not-timed: 2/5 statements (40.00%), no selected package work",
	} {
		if !strings.Contains(contents, want) {
			t.Fatalf("report does not contain %q:\n%s", want, contents)
		}
	}
	if strings.Contains(contents, "start_line") || strings.Contains(contents, "blocks") {
		t.Fatalf("report includes source-level coverage detail:\n%s", contents)
	}
}

func TestReportSummaryPrioritizesBlockingDecisionAndCollapsesPackageDetails(t *testing.T) {
	report := testQualityReportWithPackages(
		packageMetric{Name: "example.test/first", coverageMetric: coverageMetric{StatementTotal: 10, CoveredStatements: 8}},
		packageMetric{Name: "example.test/second", coverageMetric: coverageMetric{StatementTotal: 5, CoveredStatements: 2}},
	)
	report.Decision = decision{
		Status:   statusRegression,
		Findings: []finding{{Kind: "coverage_policy", Lane: "unit", Scope: "repository", Prior: 70, Current: 66.67, Proposed: 66.17, Threshold: 0.5, Message: "coverage is below its policy floor"}},
		Warnings: []finding{{Kind: "timing_comparison", Lane: "unit", Scope: "package", Name: "example.test/first", Prior: 0.5, Current: 0.8, BudgetSeconds: 0.75, Message: "timing diagnostic"}},
	}
	var output bytes.Buffer

	renderReportSummary(&output, report, "artifacts/test-coverage/report.json")

	contents := output.String()
	for _, fact := range []string{"Coverage floor violated", "unit/repository", "floor 70.000%", "66.67%", "ceiling 0.750s", "example.test/first", "example.test/second", "artifacts/test-coverage/report.json"} {
		if !strings.Contains(contents, fact) {
			t.Fatalf("summary missing %q: %s", fact, contents)
		}
	}
	start, end := strings.Index(contents, "<details>"), strings.Index(contents, "</details>")
	summary, closing := strings.Index(contents, "<summary>"), strings.Index(contents, "</summary>")
	blocking := strings.Index(contents, "unit/repository")
	if start < 0 || end <= start || summary <= start || closing <= summary || closing >= end || blocking >= start {
		t.Fatalf("blocking decision must precede collapsible details: %s", contents)
	}
	for _, pkg := range []string{"example.test/first", "example.test/second"} {
		if !strings.Contains(contents[closing:end], pkg) {
			t.Fatalf("package %s is outside collapsible details: %s", pkg, contents)
		}
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

func TestFailedTestEventsRetainNamedAssertionOutput(t *testing.T) {
	input := strings.NewReader(strings.Join([]string{
		"{\"Action\":\"run\",\"Package\":\"example.test/pkg\",\"Test\":\"TestBroken\"}",
		"{\"Action\":\"output\",\"Package\":\"example.test/pkg\",\"Test\":\"TestBroken\",\"Output\":\"want true, got false\\n\"}",
		"{\"Action\":\"fail\",\"Package\":\"example.test/pkg\",\"Test\":\"TestBroken\"}",
		"{\"Action\":\"pass\",\"Package\":\"example.test/pkg\",\"Test\":\"TestOther\"}",
		"{\"Action\":\"fail\",\"Package\":\"example.test/pkg\"}",
	}, "\n"))

	events := decodeTestEvents(input)
	failures := failuresFromEvents(events.outputs, events.failed)

	if events.decodeErr != nil {
		t.Fatal(events.decodeErr)
	}
	want := []testFailure{{Package: "example.test/pkg"}, {Package: "example.test/pkg", Test: "TestBroken", Output: "want true, got false\n"}}
	if !reflect.DeepEqual(failures, want) {
		t.Fatalf("failures = %#v, want %#v", failures, want)
	}
}

func TestCoverageCommandsAllowConcurrentPackagesButSerializeTests(t *testing.T) {
	for _, lane := range laneNames {
		t.Run(lane, func(t *testing.T) {
			command := strings.Join(coverageCommand(lane, "result.cover", ""), " ")
			want := "go test -json -count=1 -parallel=1 -covermode=set -coverpkg=./... -coverprofile=result.cover"
			if lane == "integration" {
				want += " -tags=integration -run ^TestIntegration"
			}
			want += " ./..."
			if command != want {
				t.Fatalf("command = %q, want %q", command, want)
			}
		})
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
