package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/hea3ven/orpheus/internal/testutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseOptionsSupportsQualityModes(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{name: "defaults"},
		{name: "trusted comparison", args: []string{"-compare-to", "base.json"}},
		{name: "generated baseline", args: []string{"-write-baseline"}},
		{name: "reject combined modes", args: []string{"-write-baseline", "-compare-to", "base.json"}, wantErr: true},
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
	forced, err := parseOptions([]string{"-write-baseline", "-force-baseline"})
	if err != nil || !forced.writeBaseline || !forced.forceBaseline {
		t.Fatalf("parse forced baseline options = %#v, %v", forced, err)
	}
	if _, err := parseOptions([]string{"-force-baseline"}); err == nil {
		t.Fatal("parse force-baseline without write-baseline succeeded")
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

func TestCoverageFindingMessageShowsRegressionSize(t *testing.T) {
	got := findingMessage(finding{
		Kind:      "coverage",
		Prior:     75.25,
		Current:   73.5,
		Threshold: 0.5,
		Message:   "coverage regressed beyond the significance policy",
	})
	for _, want := range []string{"baseline 75.25%", "current 73.50%", "down 1.75 percentage points", "significance threshold 0.50 percentage points"} {
		if !strings.Contains(got, want) {
			t.Fatalf("finding message %q does not contain %q", got, want)
		}
	}
}

func TestTimingFindingMessageShowsCurrentBaselineBudgetAndDifferences(t *testing.T) {
	got := findingMessage(finding{Kind: "timing", Baseline: 0.165, Prior: 0.415, Current: 0.472, Message: "package timing budget exceeded"})
	for _, want := range []string{"current 0.472s", "baseline 0.165s", "difference +0.307s", "+186.1%", "budget 0.415s", "over budget +0.057s"} {
		if !strings.Contains(got, want) {
			t.Fatalf("finding message %q does not contain %q", got, want)
		}
	}

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
			Kind: "coverage", Lane: "unit", Scope: "repository", Prior: 70, Current: 66.67, Threshold: 0.5,
			Message: "coverage regressed beyond the significance policy",
		}},
		Warnings: []finding{{
			Kind: "timing", Lane: "unit", Scope: "package", Name: "example.test/first",
			Prior: 0.75, Baseline: 0.5, Current: 0.8, BudgetSeconds: 0.75, OverageSeconds: 0.05,
			Message: "non-blocking package timing budget exceeded",
		}},
	}

	var output bytes.Buffer
	renderReportSummary(&output, report, "artifacts/test-coverage/report.json")
	contents := output.String()
	for _, want := range []string{
		"> [!CAUTION]",
		"**Coverage regression.**",
		"| Lane | Result | Coverage | Test events | Selected-test time | Wall time |",
		"| unit | **Pass** | 10/15 (66.67%) | 10 | 0.50s | 1.00s |",
		"### Blocking issues",
		"unit/repository",
		"down 3.33 percentage points",
		"### Warnings",
		"over budget +0.050s",
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

func TestWriteBaselineCreatesGeneratedFiles(t *testing.T) {
	dir := testutil.CanonicalTempDir(t)
	opts := options{baseline: filepath.Join(dir, "baseline.json"), output: filepath.Join(dir, "report.json")}
	report := testQualityReport()
	current := baselineFromReport(report, defaultPolicy())
	if err := writeBaseline(opts, report, current, baseline{}, os.ErrNotExist); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBaseline(opts.baseline); err != nil {
		t.Fatalf("load written baseline: %v", err)
	}
	if _, err := os.Stat(opts.output); err != nil {
		t.Fatalf("stat written report: %v", err)
	}

	prior := baselineFromReport(report, defaultPolicy())
	regressed := cloneReport(t, report)
	setLaneCoverage(regressed, "unit", 1000, 490)
	forceOpts := options{
		baseline:      opts.baseline,
		output:        filepath.Join(dir, "forced-report.json"),
		writeBaseline: true,
		forceBaseline: true,
	}
	if err := writeBaseline(forceOpts, regressed, baselineFromReport(regressed, defaultPolicy()), prior, nil); err != nil {
		t.Fatalf("force baseline: %v", err)
	}
	written, err := loadBaseline(opts.baseline)
	if err != nil {
		t.Fatal(err)
	}
	if got := written.Lanes["unit"].Coverage.CoveredStatements; got != 490 {
		t.Fatalf("forced unit covered statements = %d, want 490", got)
	}
}

func TestForcedBaselineRegeneratesUsingTrackedPolicy(t *testing.T) {
	dir := testutil.CanonicalTempDir(t)
	report := testQualityReport()
	tracked := baselineFromReport(report, defaultPolicy())
	tracked.Policy.Coverage.PackageSignificancePP = 3
	tracked.Policy.Timing.SuiteRelativeTolerance = 1.5
	tracked.Policy.Timing.SuiteAbsoluteSeconds = 2
	tracked.Policy.Timing.PackageRelativeTolerance = 0.1
	tracked.Policy.Timing.PackageAbsoluteSeconds = 0.01

	current := cloneReport(t, report)
	for _, name := range laneNames {
		lane := current.Lanes[name]
		lane.WallSeconds = 2
		lane.Timings[0].Seconds = 1
		current.Lanes[name] = lane
	}
	opts := options{
		baseline:      filepath.Join(dir, "baseline.json"),
		output:        filepath.Join(dir, "report.json"),
		writeBaseline: true,
		forceBaseline: true,
	}
	if err := writeBaseline(opts, current, baselineFromReport(current, defaultPolicy()), tracked, nil); err != nil {
		t.Fatalf("force baseline: %v", err)
	}
	written, err := loadBaseline(opts.baseline)
	if err != nil {
		t.Fatal(err)
	}
	want := baselineFromReport(current, tracked.Policy)
	if !reflect.DeepEqual(written, want) {
		t.Fatalf("forced baseline = %#v, want %#v", written, want)
	}
}

func TestWriteBaselineMigratesSchemaTwoAndRefusesRegression(t *testing.T) {
	contents := `{"schema_version":2,"lanes":{` +
		`"unit":{"test_count":3,"statement_total":10,"covered_statements":5,"packages":[{"name":"example.test/pkg","statement_total":10,"covered_statements":5,"files":[]}]},` +
		`"integration":{"test_count":2,"statement_total":10,"covered_statements":6,"packages":[{"name":"example.test/pkg","statement_total":10,"covered_statements":6,"files":[]}]}}}`
	dir := testutil.CanonicalTempDir(t)
	migrationPath := filepath.Join(dir, "legacy-migrate.json")
	regressionPath := filepath.Join(dir, "legacy-regression.json")
	for _, path := range []string{migrationPath, regressionPath} {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	legacy, err := loadBaseline(migrationPath)
	if err != nil {
		t.Fatal(err)
	}
	if !legacy.Legacy || legacy.Lanes["unit"].Coverage != (coverageMetric{StatementTotal: 10, CoveredStatements: 5}) {
		t.Fatalf("legacy baseline = %#v", legacy)
	}
	improved := testQualityReportWithPackages(packageMetric{Name: "example.test/pkg", coverageMetric: coverageMetric{StatementTotal: 10, CoveredStatements: 7}})
	migrationOpts := options{baseline: migrationPath, output: filepath.Join(dir, "migration-report.json")}
	if err := writeBaseline(migrationOpts, improved, baselineFromReport(improved, defaultPolicy()), legacy, nil); err != nil {
		t.Fatalf("migrate schema-two baseline: %v", err)
	}
	migrated, err := loadBaseline(migrationPath)
	if err != nil {
		t.Fatalf("load migrated baseline: %v", err)
	}
	if migrated.Legacy || migrated.Lanes["unit"].SuiteBudgetSeconds <= 0 || len(migrated.Lanes["unit"].PackageBudgets) == 0 {
		t.Fatalf("migrated baseline has no generated timing budgets: %#v", migrated.Lanes["unit"])
	}

	legacy, err = loadBaseline(regressionPath)
	if err != nil {
		t.Fatal(err)
	}
	regressed := testQualityReportWithPackages(packageMetric{Name: "example.test/pkg", coverageMetric: coverageMetric{StatementTotal: 10, CoveredStatements: 4}})
	regressionOpts := options{baseline: regressionPath, output: filepath.Join(dir, "regression-report.json")}
	if err := writeBaseline(regressionOpts, regressed, baselineFromReport(regressed, defaultPolicy()), legacy, nil); err == nil {
		t.Fatal("schema-two migration accepted a trusted coverage regression")
	}
	retained, err := loadBaseline(regressionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !retained.Legacy {
		t.Fatal("refused regression rewrote the schema-two baseline")
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

func TestCoverageSignificanceBoundariesApplyToBothLanes(t *testing.T) {
	for _, laneName := range laneNames {
		t.Run(laneName, func(t *testing.T) {
			baseReport := testQualityReport()
			prior := baselineFromReport(baseReport, defaultPolicy())

			inside := cloneReport(t, baseReport)
			setLaneCoverage(inside, laneName, 1000, 504)
			if got := assess(prior, inside); got.Status != statusPass {
				t.Fatalf("0.4pp drift decision = %#v, want pass", got)
			}

			improved := cloneReport(t, baseReport)
			setLaneCoverage(improved, laneName, 1000, 505)
			if got := assess(prior, improved); got.Status != statusRefreshRequired || !hasFinding(got, "coverage", "repository") {
				t.Fatalf("+0.5pp decision = %#v, want refresh", got)
			}

			regressed := cloneReport(t, baseReport)
			setLaneCoverage(regressed, laneName, 1000, 495)
			if got := assess(prior, regressed); got.Status != statusRegression || !hasFinding(got, "coverage", "repository") {
				t.Fatalf("-0.5pp decision = %#v, want regression", got)
			}
		})
	}
}

func TestPackageSignificanceBoundaryDoesNotDependOnRepositoryBoundary(t *testing.T) {
	report := testQualityReportWithPackages(
		packageMetric{Name: "example.test/large", coverageMetric: coverageMetric{StatementTotal: 990, CoveredStatements: 495}},
		packageMetric{Name: "example.test/small", coverageMetric: coverageMetric{StatementTotal: 10, CoveredStatements: 5}},
	)
	prior := baselineFromReport(report, defaultPolicy())
	current := cloneReport(t, report)
	for _, laneName := range laneNames {
		lane := current.Lanes[laneName]
		lane.Packages[1].CoveredStatements = 6
		lane.Coverage.CoveredStatements = 501
		current.Lanes[laneName] = lane
	}
	got := assess(prior, current)
	if got.Status != statusRefreshRequired || !hasFinding(got, "coverage", "package") {
		t.Fatalf("small-package improvement decision = %#v, want package refresh", got)
	}
}

func TestDenominatorDriftUsesGreaterAbsoluteThresholdForSmallPackages(t *testing.T) {
	report := testQualityReportWithPackages(
		packageMetric{Name: "example.test/large", coverageMetric: coverageMetric{StatementTotal: 990, CoveredStatements: 495}},
		packageMetric{Name: "example.test/small", coverageMetric: coverageMetric{StatementTotal: 10, CoveredStatements: 5}},
	)
	prior := baselineFromReport(report, defaultPolicy())

	inside := cloneReport(t, report)
	resizePackage(inside, "unit", "example.test/small", 34, 17)
	if got := assess(prior, inside); got.Status != statusPass {
		t.Fatalf("24-statement package drift = %#v, want pass", got)
	}

	boundary := cloneReport(t, report)
	resizePackage(boundary, "unit", "example.test/small", 35, 18)
	got := assess(prior, boundary)
	if got.Status != statusRefreshRequired || !hasFinding(got, "denominator", "package") {
		t.Fatalf("25-statement package drift = %#v, want refresh", got)
	}
}

func TestRepositoryDenominatorBoundaryIsInclusive(t *testing.T) {
	report := testQualityReport()
	prior := baselineFromReport(report, defaultPolicy())
	current := cloneReport(t, report)
	setLaneCoverage(current, "integration", 1100, 550)
	got := assess(prior, current)
	if got.Status != statusRefreshRequired || !hasFinding(got, "denominator", "repository") {
		t.Fatalf("100-statement repository drift = %#v, want refresh", got)
	}
}

func TestSuiteTimingUsesSelectedPackageTestsInsteadOfCommandWallTime(t *testing.T) {
	report := testQualityReport()
	prior := baselineFromReport(report, defaultPolicy())
	if got := prior.Lanes["unit"].SuiteBaselineSeconds; got != 0.5 {
		t.Fatalf("suite baseline = %v, want selected package total 0.5", got)
	}

	coldBuild := cloneReport(t, report)
	lane := coldBuild.Lanes["unit"]
	lane.WallSeconds = 100
	coldBuild.Lanes["unit"] = lane
	if got := assess(prior, coldBuild); got.Status != statusPass {
		t.Fatalf("cold-build timing decision = %#v, want pass", got)
	}
}

func TestPackageTimingOverrunIsNonBlockingWarning(t *testing.T) {
	report := testQualityReport()
	prior := baselineFromReport(report, defaultPolicy())
	budget := prior.Lanes["unit"].PackageBudgets[0]
	current := cloneReport(t, report)
	lane := current.Lanes["unit"]
	lane.Timings[0].Seconds = budget.Seconds + 0.001
	current.Lanes["unit"] = lane

	got := assess(prior, current)
	if got.Status != statusPass {
		t.Fatalf("package-only timing decision = %#v, want pass", got)
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("package timing warnings = %#v, want one warning", got.Warnings)
	}
	warning := got.Warnings[0]
	if warning.Lane != "unit" || warning.Scope != "package" || warning.Name != "example.test/pkg" || warning.Current != lane.Timings[0].Seconds || warning.Baseline != budget.BaselineSeconds || warning.BudgetSeconds != budget.Seconds || warning.OverageSeconds <= 0 {
		t.Fatalf("package timing warning = %#v", warning)
	}

	contents, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\"warnings\"", "non-blocking package timing budget exceeded", "\"budget_seconds\"", "\"overage_seconds\""} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("warning JSON does not contain %q: %s", want, contents)
		}
	}

	current.Decision = got
	var output bytes.Buffer
	printReportTo(&output, current, "report.json")
	for _, want := range []string{"quality decision: pass", "warning (non-blocking): unit/example.test/pkg", "current 0.751s", "baseline 0.500s", "budget 0.750s", "over budget +0.001s"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("command output does not contain %q:\n%s", want, output.String())
		}
	}
}

func TestTimingBudgetsFailOnlyAboveTheirBoundaries(t *testing.T) {
	report := testQualityReport()
	prior := baselineFromReport(report, defaultPolicy())
	lane := prior.Lanes["unit"]

	atPackageBoundary := cloneReport(t, report)
	current := atPackageBoundary.Lanes["unit"]
	current.Timings[0].Seconds = lane.PackageBudgets[0].Seconds
	atPackageBoundary.Lanes["unit"] = current
	if got := assess(prior, atPackageBoundary); got.Status != statusPass || len(got.Warnings) != 0 {
		t.Fatalf("package timing at budget = %#v, want pass without warnings", got)
	}

	atSuiteBoundary := cloneReport(t, report)
	lane.PackageBudgets[0].Seconds = lane.SuiteBudgetSeconds + 1
	prior.Lanes["unit"] = lane
	current = atSuiteBoundary.Lanes["unit"]
	current.Timings[0].Seconds = lane.SuiteBudgetSeconds
	atSuiteBoundary.Lanes["unit"] = current
	if got := assess(prior, atSuiteBoundary); got.Status != statusPass || len(got.Warnings) != 0 {
		t.Fatalf("suite timing at budget = %#v, want pass without warnings", got)
	}
}

func TestSuiteTimingOverrunFailsAndKeepsPackageWarning(t *testing.T) {
	report := testQualityReport()
	prior := baselineFromReport(report, defaultPolicy())
	current := cloneReport(t, report)
	lane := current.Lanes["unit"]
	lane.Timings[0].Seconds = prior.Lanes["unit"].SuiteBudgetSeconds + 0.001
	current.Lanes["unit"] = lane

	got := assess(prior, current)
	if got.Status != statusTimingFailed || !hasFinding(got, "timing", "suite") || !hasWarning(got, "timing", "package") {
		t.Fatalf("suite timing overrun = %#v, want suite failure with package warning", got)
	}
}

func TestCoverageRegressionKeepsPackageTimingWarning(t *testing.T) {
	report := testQualityReport()
	prior := baselineFromReport(report, defaultPolicy())
	current := cloneReport(t, report)
	setLaneCoverage(current, "unit", 1000, 490)
	lane := current.Lanes["unit"]
	lane.Timings[0].Seconds = prior.Lanes["unit"].PackageBudgets[0].Seconds + 0.001
	current.Lanes["unit"] = lane

	got := assess(prior, current)
	if got.Status != statusRegression || !hasFinding(got, "coverage", "repository") || !hasWarning(got, "timing", "package") {
		t.Fatalf("mixed coverage and timing decision = %#v, want coverage regression with package warning", got)
	}
}

func TestGeneratedRefreshPreservesExistingTimingBudgets(t *testing.T) {
	report := testQualityReport()
	prior := baselineFromReport(report, defaultPolicy())
	changed := cloneReport(t, report)
	setLaneCoverage(changed, "unit", 1000, 510)
	lane := changed.Lanes["unit"]
	lane.WallSeconds = 0.1
	lane.Timings[0].Seconds = 0.01
	changed.Lanes["unit"] = lane

	got := generatedBaseline(changed, prior, prior.Policy)
	if got.Lanes["unit"].SuiteBudgetSeconds != prior.Lanes["unit"].SuiteBudgetSeconds || !reflect.DeepEqual(got.Lanes["unit"].PackageBudgets, prior.Lanes["unit"].PackageBudgets) {
		t.Fatalf("coverage refresh changed timing budgets:\nprior %#v\ncurrent %#v", prior.Lanes["unit"], got.Lanes["unit"])
	}
	if !reflect.DeepEqual(got, generatedBaseline(changed, prior, prior.Policy)) {
		t.Fatal("generated baseline is nondeterministic")
	}
}

func TestWriteBaselineAllowsPackageTimingWarnings(t *testing.T) {
	for _, test := range []struct {
		name    string
		refresh func(qualityReport)
	}{
		{
			name: "coverage",
			refresh: func(report qualityReport) {
				setLaneCoverage(report, "unit", 1000, 510)
			},
		},
		{
			name: "test structure",
			refresh: func(report qualityReport) {
				lane := report.Lanes["unit"]
				lane.TestCount++
				report.Lanes["unit"] = lane
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := testutil.CanonicalTempDir(t)
			base := testQualityReport()
			trusted := baselineFromReport(base, defaultPolicy())
			current := cloneReport(t, base)
			test.refresh(current)
			lane := current.Lanes["unit"]
			lane.Timings[0].Seconds = trusted.Lanes["unit"].PackageBudgets[0].Seconds + 0.001
			current.Lanes["unit"] = lane

			assessment := assess(trusted, current)
			if assessment.Status != statusRefreshRequired || !hasWarning(assessment, "timing", "package") {
				t.Fatalf("refresh assessment = %#v, want refresh with package warning", assessment)
			}
			opts := options{baseline: filepath.Join(dir, "baseline.json"), output: filepath.Join(dir, "report.json"), writeBaseline: true}
			if err := writeBaseline(opts, current, baselineFromReport(current, defaultPolicy()), trusted, nil); err != nil {
				t.Fatalf("write baseline with package warning: %v", err)
			}
			written, err := loadBaseline(opts.baseline)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(written.Lanes["unit"].PackageBudgets, trusted.Lanes["unit"].PackageBudgets) || written.Lanes["unit"].SuiteBudgetSeconds != trusted.Lanes["unit"].SuiteBudgetSeconds {
				t.Fatalf("baseline refresh changed trusted timing budgets:\ntrusted %#v\nwritten %#v", trusted.Lanes["unit"], written.Lanes["unit"])
			}
			if got := verifyTrackedBaseline(trusted, written, current, assessment); got.Status != statusPass || !hasWarning(got, "timing", "package") {
				t.Fatalf("verified baseline refresh = %#v, want pass with package warning", got)
			}
			contents, err := os.ReadFile(opts.output)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(contents), "non-blocking package timing budget exceeded") {
				t.Fatalf("baseline report omitted package warning: %s", contents)
			}
		})
	}
}

func TestGeneratedRefreshPassesAgainstTrustedPrior(t *testing.T) {
	report := testQualityReport()
	trusted := baselineFromReport(report, defaultPolicy())
	improved := cloneReport(t, report)
	setLaneCoverage(improved, "unit", 1000, 510)
	tracked := generatedBaseline(improved, trusted, trusted.Policy)

	trustedDecision := assess(trusted, improved)
	if trustedDecision.Status != statusRefreshRequired {
		t.Fatalf("trusted decision = %#v, want refresh", trustedDecision)
	}
	if got := verifyTrackedBaseline(trusted, tracked, improved, trustedDecision); got.Status != statusPass {
		t.Fatalf("generated refresh decision = %#v, want pass", got)
	}
}

func TestPolicyEditCannotLoosenNewPackageBudgetBeforeItIsTrusted(t *testing.T) {
	report := testQualityReport()
	trusted := baselineFromReport(report, defaultPolicy())
	for _, laneName := range laneNames {
		lane := trusted.Lanes[laneName]
		lane.SuiteBudgetSeconds = 2
		trusted.Lanes[laneName] = lane
	}
	changed := cloneReport(t, report)
	for _, laneName := range laneNames {
		lane := changed.Lanes[laneName]
		lane.Timings = append(lane.Timings, packageTiming{Name: "example.test/new", Seconds: 1})
		changed.Lanes[laneName] = lane
	}
	loosePolicy := trusted.Policy
	loosePolicy.Timing.PackageAbsoluteSeconds = 100
	tracked := generatedBaseline(changed, trusted, trusted.Policy)
	tracked.Policy = loosePolicy

	trustedDecision := assess(trusted, changed)
	if got := verifyTrackedBaseline(trusted, tracked, changed, trustedDecision); got.Status != statusPass {
		t.Fatalf("policy-only edit with trusted generated budget = %#v, want pass", got)
	}
	loose := generatedBaseline(changed, trusted, loosePolicy)
	loose.Policy = loosePolicy
	if got := verifyTrackedBaseline(trusted, loose, changed, trustedDecision); got.Status != statusRefreshRequired {
		t.Fatalf("policy edit loosened new budget before trust: %#v", got)
	}
}

func TestGeneratedRefreshAcceptsNewPackageTimingVariance(t *testing.T) {
	report := testQualityReport()
	trusted := baselineFromReport(report, defaultPolicy())
	for _, laneName := range laneNames {
		lane := trusted.Lanes[laneName]
		lane.SuiteBudgetSeconds = 2
		trusted.Lanes[laneName] = lane
	}

	generatedReport := cloneReport(t, report)
	verifiedReport := cloneReport(t, report)
	for _, laneName := range laneNames {
		generatedLane := generatedReport.Lanes[laneName]
		generatedLane.Timings = append(generatedLane.Timings, packageTiming{Name: "example.test/new", Seconds: 1})
		generatedReport.Lanes[laneName] = generatedLane

		verifiedLane := verifiedReport.Lanes[laneName]
		verifiedLane.Timings = append(verifiedLane.Timings, packageTiming{Name: "example.test/new", Seconds: 0.9})
		verifiedReport.Lanes[laneName] = verifiedLane
	}
	tracked := generatedBaseline(generatedReport, trusted, trusted.Policy)
	trustedDecision := assess(trusted, verifiedReport)
	if got := verifyTrackedBaseline(trusted, tracked, verifiedReport, trustedDecision); got.Status != statusPass {
		t.Fatalf("generated refresh with timing variance = %#v, want pass", got)
	}

	for _, laneName := range laneNames {
		lane := tracked.Lanes[laneName]
		budget := timingBudgetMap(lane.PackageBudgets)["example.test/new"]
		budget.BaselineSeconds = 100
		budget.Seconds = timingAllowance(100, trusted.Policy.Timing.PackageRelativeTolerance, trusted.Policy.Timing.PackageAbsoluteSeconds)
		for index := range lane.PackageBudgets {
			if lane.PackageBudgets[index].Name == budget.Name {
				lane.PackageBudgets[index] = budget
			}
		}
		tracked.Lanes[laneName] = lane
	}
	if got := verifyTrackedBaseline(trusted, tracked, verifiedReport, trustedDecision); got.Status != statusRefreshRequired {
		t.Fatalf("inflated new-package budget decision = %#v, want refresh", got)
	}
}

func TestTrustedRegressionCannotBeHiddenByEditedBaseline(t *testing.T) {
	report := testQualityReport()
	trusted := baselineFromReport(report, defaultPolicy())
	regressed := cloneReport(t, report)
	setLaneCoverage(regressed, "unit", 1000, 490)
	edited := generatedBaseline(regressed, trusted, trusted.Policy)

	trustedDecision := assess(trusted, regressed)
	if trustedDecision.Status != statusRegression {
		t.Fatalf("trusted decision = %#v, want regression", trustedDecision)
	}
	if got := verifyTrackedBaseline(trusted, edited, regressed, trustedDecision); got.Status != statusRegression {
		t.Fatalf("edited baseline hid trusted regression: %#v", got)
	}
}

func TestGeneratedBaselineIsCompactAndValid(t *testing.T) {
	generated := baselineFromReport(testQualityReport(), defaultPolicy())
	contents, err := json.Marshal(generated)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"start_line", "start_column", "source_hash", "files"} {
		if strings.Contains(string(contents), forbidden) {
			t.Fatalf("compact baseline contains %q: %s", forbidden, contents)
		}
	}
	path := filepath.Join(testutil.CanonicalTempDir(t), "baseline.json")
	if err := writeJSON(path, generated); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBaseline(path); err != nil {
		t.Fatalf("load generated baseline: %v", err)
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

func resizePackage(report qualityReport, laneName, packageName string, total, covered int) {
	lane := report.Lanes[laneName]
	for index := range lane.Packages {
		if lane.Packages[index].Name != packageName {
			continue
		}
		lane.Coverage.StatementTotal += total - lane.Packages[index].StatementTotal
		lane.Coverage.CoveredStatements += covered - lane.Packages[index].CoveredStatements
		lane.Packages[index].StatementTotal = total
		lane.Packages[index].CoveredStatements = covered
	}
	report.Lanes[laneName] = lane
}

func hasFinding(value decision, kind, scope string) bool {
	for _, item := range value.Findings {
		if item.Kind == kind && item.Scope == scope {
			return true
		}
	}
	return false
}

func hasWarning(value decision, kind, scope string) bool {
	for _, warning := range value.Warnings {
		if warning.Kind == kind && warning.Scope == scope {
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
