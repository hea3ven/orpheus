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
		{name: "same-runner comparison", args: []string{"-compare-head", fixtureBase, "-compare-merge", fixtureMerge}, want: options{compareHead: fixtureBase, compareMerge: fixtureMerge}},
		{name: "working-tree comparison", args: []string{"-compare-worktree", "-compare-base", "main"}, want: options{compareWorktree: true, compareBase: "main"}},
		{name: "working-tree explicit output", args: []string{"-compare-worktree", "-compare-base", "main", "-output", "/fixture/new-reports/report.json"}, want: options{compareWorktree: true, compareBase: "main", output: "/fixture/new-reports/report.json"}},
		{name: "working-tree comparison requires explicit base", args: []string{"-compare-worktree"}, wantErr: true},
		{name: "working-tree comparison rejects merge", args: []string{"-compare-worktree", "-compare-base", "main", "-compare-merge", fixtureMerge}, wantErr: true},
		{name: "working-tree comparison rejects PR head", args: []string{"-compare-worktree", "-compare-base", "main", "-compare-head", fixtureBase}, wantErr: true},
		{name: "working-tree comparison rejects policy override", args: []string{"-compare-worktree", "-compare-base", "main", "-policy", "base.yml"}, wantErr: true},
		{name: "working-tree comparison rejects update", args: []string{"-compare-worktree", "-compare-base", "main", "-update-policy"}, wantErr: true},
		{name: "isolated measurement", args: []string{"-measure-revision", fixtureBase}, want: options{measureRevision: fixtureBase}},
		{name: "local base requires working-tree mode", args: []string{"-compare-base", fixtureBase}, wantErr: true},
		{name: "missing comparison head", args: []string{"-compare-merge", fixtureMerge}, wantErr: true},
		{name: "missing comparison merge", args: []string{"-compare-head", fixtureBase}, wantErr: true},
		{name: "CI rejects explicit base", args: []string{"-compare-head", fixtureBase, "-compare-merge", fixtureMerge, "-compare-base", fixtureBase}, wantErr: true},
		{name: "reject comparison plus update", args: []string{"-compare-head", fixtureBase, "-compare-merge", fixtureMerge, "-update-policy"}, wantErr: true},
		{name: "reject comparison plus measurement", args: []string{"-compare-head", fixtureBase, "-compare-merge", fixtureMerge, "-measure-revision", fixtureBase}, wantErr: true},
		{name: "quality policy update", args: []string{"-update-policy"}, want: options{updatePolicy: true}},
		{name: "lint", args: []string{"-lint"}, want: options{lint: true}},
		{name: "reject lint plus update", args: []string{"-lint", "-update-policy"}, wantErr: true},
		{name: "reject lint plus measurement", args: []string{"-lint", "-measure-revision", fixtureBase}, wantErr: true},
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
	for _, test := range []struct {
		name, filename string
		failure        laneFailure
		want, unwanted []string
	}{
		{name: "named assertion", filename: "partial.json", failure: laneFailure{Error: "exit status 1", Stderr: "compiler stderr", TestOutput: "raw json events", Failures: []testFailure{
			{Package: "github.com/hea3ven/orpheus/internal/tasksource/beads", Output: "package diagnostic"},
			{Package: "github.com/hea3ven/orpheus/internal/tasksource/beads", Test: "TestBroken/subtest", Output: "want true, got false"},
			{Package: "example.test/build", Output: "build failed"},
		}}, want: []string{"Test failed: internal/tasksource/beads/TestBroken/subtest.", "Test failed: example.test/build.", "want true, got false", "package diagnostic", "compiler stderr"}, unwanted: []string{"raw json events"}},
		{name: "stderr with raw stdout", filename: "partial.md", failure: laneFailure{Error: "exit status 1", Stderr: "compiler stderr", TestOutput: "raw json events"}, want: []string{"Test failed: lane command.", "compiler stderr", "raw json events"}},
		{name: "undecodable stream with stderr", filename: "partial.json", failure: laneFailure{Error: "decode go test JSON", Stderr: "command warning", TestOutput: "malformed raw stream"}, want: []string{"Test failed: lane command.", "decode go test JSON", "malformed raw stream", "command warning"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			filename := test.filename
			path := filepath.Join(testutil.CanonicalTempDir(t), filename)
			report := testQualityReport()
			report.Complete = false
			report.Execution = decision{Status: statusTestFailed, Findings: []finding{{Kind: "test", Message: "unit lane: " + test.failure.Error}}}
			report.Decision = report.Execution
			lane := report.Lanes["unit"]
			lane.Passed = false
			lane.Failure = &test.failure
			report.Lanes["unit"] = lane

			err := finishReport(path, report, os.ErrInvalid)

			var reported *reportedQualityError
			if !errors.Is(err, os.ErrInvalid) || !errors.As(err, &reported) {
				t.Fatalf("finishReport error = %v, want reported original failure", err)
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
			want := append([]string{"Quality checks failed"}, test.want...)
			unwanted := append([]string{"unit lane: " + test.failure.Error, "Test failed: internal/tasksource/beads."}, test.unwanted...)
			assertReportText(t, string(markdown), want, unwanted)
			var terminal bytes.Buffer
			printReportTo(&terminal, report, path)
			assertReportText(t, terminal.String(), want, unwanted)
		})
	}
}

func TestReportFindingsShowConciseBoundariesWithoutChangingEvidence(t *testing.T) {
	for _, test := range []struct {
		name     string
		item     finding
		settings coverageBoundSettings
		want     string
	}{
		{name: "coverage regression", item: finding{Prior: 63, Current: 61.2}, settings: coverageBoundSettings{SignificancePercentagePoints: 0.5, HeadroomPercentagePoints: 2}, want: "Coverage decreased significantly to 61.2%, min is 63.0%."},
		{name: "upward refresh", item: finding{Prior: 51.6, Current: 54.2}, settings: coverageBoundSettings{SignificancePercentagePoints: 0.5, HeadroomPercentagePoints: 2}, want: "Coverage improved significantly to 54.2%, refresh at 54.1%."},
		{name: "downward refresh above floor", item: finding{Prior: 53, Current: 54.9}, settings: coverageBoundSettings{SignificancePercentagePoints: 2, HeadroomPercentagePoints: 4}, want: "Coverage decreased significantly to 54.9%, refresh at 55.0%."},
		{name: "coverage precision", item: finding{Prior: 63, Current: 62.9996}, settings: coverageBoundSettings{SignificancePercentagePoints: 0.5, HeadroomPercentagePoints: 2}, want: "Coverage decreased significantly to 62.9996%, min is 63.0000%."},
		{name: "refresh rounding boundary", item: finding{Prior: 51.6, Current: 54.0996}, settings: coverageBoundSettings{SignificancePercentagePoints: 0.5, HeadroomPercentagePoints: 2}, want: "Coverage improved significantly to 54.100%, refresh at 54.099%."},
		{name: "confirmed timing", item: finding{Kind: "timing_comparison", Current: 3.44, BudgetSeconds: 1.38}, want: "Timing increased significantly to 3.44s, max is 1.38s."},
		{name: "timing precision", item: finding{Kind: "timing_comparison", Current: 1.3804, BudgetSeconds: 1.38}, want: "Timing increased significantly to 1.3804s, max is 1.3800s."},
		{name: "zero boundary", item: finding{Kind: "timing_comparison", Current: 0.0001}, want: "Timing increased significantly to 0.0001s, max is 0.0000s."},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := test.item
			status := statusTimingFailed
			if item.Kind == "" {
				violations, updates := assessCoverageBound(nil, nil, "unit", "package", "github.com/hea3ven/orpheus/internal/tasksource/beads", item.Prior, item.Current, test.settings)
				items := violations
				items = append(items, updates...)
				if len(items) != 1 {
					t.Fatalf("coverage assessment = %#v, want one finding", items)
				}
				item = items[0]
				status = statusRegression
				if len(violations) == 0 {
					status = statusPolicyUpdateRequired
				}
			} else {
				item.Lane, item.Scope, item.Name = "unit", "package", "github.com/hea3ven/orpheus/internal/tasksource/beads"
			}
			report := testQualityReport()
			report.Decision = decision{Status: status, Findings: []finding{item}}
			if status == statusTimingFailed {
				report.Timing = report.Decision
			} else {
				report.CoverageDecision = report.Decision
			}
			before, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}

			var terminal, markdown bytes.Buffer
			printReportTo(&terminal, report, "report.json")
			renderReportSummary(&markdown, report, "report.json")

			for _, contents := range []string{terminal.String(), markdown.String()} {
				assertReportText(t, contents, []string{test.want, "unit/internal/tasksource/beads"}, []string{"make quality-policy-update", "proposed floor", "refresh threshold", " overage ", "github.com/hea3ven/orpheus/"})
			}
			after, err := json.Marshal(report)
			if err != nil || !bytes.Equal(before, after) || !bytes.Contains(after, []byte("github.com/hea3ven/orpheus/internal/tasksource/beads")) {
				t.Fatalf("rendering changed machine evidence: %s, %v", after, err)
			}
		})
	}
}

func assertReportText(t *testing.T, contents string, want, unwanted []string) {
	t.Helper()
	for _, phrase := range want {
		if strings.Count(contents, phrase) != 1 {
			t.Fatalf("report must contain %q once:\n%s", phrase, contents)
		}
	}
	for _, phrase := range unwanted {
		if strings.Contains(contents, phrase) {
			t.Fatalf("report contains %q:\n%s", phrase, contents)
		}
	}
}

func TestPrintReportShowsPackageCoverageAndSelectedTestTiming(t *testing.T) {
	report := testQualityReportWithPackages(
		packageMetric{Name: "github.com/hea3ven/orpheus/internal/tasksource/beads", coverageMetric: coverageMetric{StatementTotal: 10, CoveredStatements: 8}},
		packageMetric{Name: "example.test/not-timed", coverageMetric: coverageMetric{StatementTotal: 5, CoveredStatements: 2}},
	)
	for _, name := range laneNames {
		lane := report.Lanes[name]
		lane.Timings = []packageTiming{{Name: "github.com/hea3ven/orpheus/internal/tasksource/beads", Seconds: 0.25}}
		report.Lanes[name] = lane
	}
	report.Scenarios = []scenarioResult{{Name: "github.com/hea3ven/orpheus/internal/tasksource/beads/TestIntegrationScenario"}}
	annotated := withCoverageExclusions(report, []string{"internal/tasksource/beads"})
	persisted := cloneReport(t, annotated)
	for _, name := range laneNames {
		lane := persisted.Lanes[name]
		if lane.GuardedCoverage == nil || *lane.GuardedCoverage != (coverageMetric{StatementTotal: 5, CoveredStatements: 2}) || lane.Coverage != (coverageMetric{StatementTotal: 15, CoveredStatements: 10}) || len(lane.Packages) != 2 || lane.Timings[0].Seconds != 0.25 {
			t.Fatalf("%s guarded/raw evidence = %#v", name, lane)
		}
		if report.Lanes[name].GuardedCoverage != nil {
			t.Fatal("annotating report mutated raw sample")
		}
	}
	if !reflect.DeepEqual(persisted.PackageExclusions, []string{"internal/tasksource/beads"}) {
		t.Fatalf("persisted exclusions = %v", persisted.PackageExclusions)
	}
	var output bytes.Buffer
	printReportTo(&output, persisted, "report.json")
	contents := output.String()
	for _, want := range []string{
		"unit: guarded coverage: 2/5 (40.00%); raw coverage: 10/15 statements (66.67%), 10 test events, 0.25s selected package work, 1.00s command wall time",
		"internal/tasksource/beads: 8/10 raw statements (80.00%), coverage excluded, 0.25s",
		"Coverage exclusions (exact packages): internal/tasksource/beads. Raw measurements are retained; tests and timing are unchanged.",
		"scenario internal/tasksource/beads/TestIntegrationScenario:",
		"example.test/not-timed: 2/5 raw statements (40.00%), no selected package work",
	} {
		if !strings.Contains(contents, want) {
			t.Fatalf("report does not contain %q:\n%s", want, contents)
		}
	}
	if strings.Contains(contents, "start_line") || strings.Contains(contents, "blocks") || strings.Contains(contents, "github.com/hea3ven/orpheus/") {
		t.Fatalf("report includes source-level coverage detail:\n%s", contents)
	}
	output.Reset()
	renderReportSummary(&output, persisted, "report.json")
	for _, want := range []string{"Guarded coverage | Raw coverage", "| unit | **Pass** | 2/5 (40.00%) | 10/15 (66.67%) |", "internal/tasksource/beads | 8/10 (80.00%) | Excluded | 0.25s", "example.test/not-timed | 2/5 (40.00%) | Guarded | Not measured", "Coverage exclusions (exact packages): internal/tasksource/beads."} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("Markdown missing %q:\n%s", want, output.String())
		}
	}
}

func TestReportSummaryPrioritizesBlockingDecisionAndCollapsesPackageDetails(t *testing.T) {
	report := testQualityReportWithPackages(
		packageMetric{Name: "github.com/hea3ven/orpheus/internal/tasksource/beads", coverageMetric: coverageMetric{StatementTotal: 10, CoveredStatements: 8}},
		packageMetric{Name: "github.com/hea3ven/orpheus/internal/other/beads", coverageMetric: coverageMetric{StatementTotal: 5, CoveredStatements: 2}},
		packageMetric{Name: "github.com/hea3ven/orpheus-extra/pkg"},
	)
	report.Decision = decision{
		Status:   statusRegression,
		Findings: []finding{{Kind: "coverage_policy", Lane: "unit", Scope: "repository", Prior: 70, Current: 66.67, Proposed: 66.17, Threshold: 0.5, Message: "coverage is below its policy floor"}},
		Warnings: []finding{{Kind: "timing_package", Lane: "unit", Scope: "package", Name: "github.com/hea3ven/orpheus/internal/tasksource/beads", Message: "selected-test package added (included in lane total)"}},
	}
	report.Scenarios = []scenarioResult{{Name: "github.com/hea3ven/orpheus/internal/tasksource/beads/TestIntegrationScenario"}}
	var output bytes.Buffer

	renderReportSummary(&output, report, "artifacts/test-coverage/report.json")

	contents := output.String()
	for _, fact := range []string{"Quality checks failed", "internal/tasksource/beads/TestIntegrationScenario", "unit/repository", "Coverage decreased significantly to 66.7%, min is 70.0%.", "66.67%", "selected-test package added", "internal/tasksource/beads", "internal/other/beads", "github.com/hea3ven/orpheus-extra/pkg", "artifacts/test-coverage/report.json"} {
		if !strings.Contains(contents, fact) {
			t.Fatalf("summary missing %q: %s", fact, contents)
		}
	}
	if strings.Contains(contents, "github.com/hea3ven/orpheus/") {
		t.Fatalf("summary retained a full module label: %s", contents)
	}
	start, end := strings.Index(contents, "<details>"), strings.Index(contents, "</details>")
	summary, closing := strings.Index(contents, "<summary>"), strings.Index(contents, "</summary>")
	blocking := strings.Index(contents, "unit/repository")
	if start < 0 || end <= start || summary <= start || closing <= summary || closing >= end || blocking >= start {
		t.Fatalf("blocking decision must precede collapsible details: %s", contents)
	}
	for _, pkg := range []string{"internal/tasksource/beads", "internal/other/beads"} {
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
		"PATH=/test/bin", "NO_COLOR=1", "CODEX_HOME=/operator/codex",
		coverageRunEnvironment + "=operator-value", "UNRELATED=value",
	}))
	for _, name := range []string{"NO_COLOR", "CODEX_HOME"} {
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
	report := qualityReport{SchemaVersion: reportSchemaVersion, Complete: true, Lanes: make(map[string]laneReport, 2), Execution: decision{Status: statusPass}, CoverageDecision: decision{Status: statusPass}, Timing: decision{Status: statusNotEvaluated}, Decision: decision{Status: statusPass}}
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
