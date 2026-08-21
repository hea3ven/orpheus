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
	path := filepath.Join(t.TempDir(), "partial.json")
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
		"unit: 10/15 statements (66.67%), 10 test events, 1.00s",
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

func TestWriteBaselineCreatesGeneratedFiles(t *testing.T) {
	dir := t.TempDir()
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
	dir := t.TempDir()
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
	dir := t.TempDir()
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
	profile := filepath.Join(t.TempDir(), "coverage.out")
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

func TestTimingBudgetsFailOnlyAboveBoundary(t *testing.T) {
	report := testQualityReport()
	prior := baselineFromReport(report, defaultPolicy())
	lane := prior.Lanes["unit"]

	atBoundary := cloneReport(t, report)
	current := atBoundary.Lanes["unit"]
	current.WallSeconds = lane.SuiteBudgetSeconds
	current.Timings[0].Seconds = lane.PackageBudgets[0].Seconds
	atBoundary.Lanes["unit"] = current
	if got := assess(prior, atBoundary); got.Status != statusPass {
		t.Fatalf("timing at budget = %#v, want pass", got)
	}

	over := cloneReport(t, atBoundary)
	current = over.Lanes["unit"]
	current.Timings[0].Seconds += 0.001
	over.Lanes["unit"] = current
	if got := assess(prior, over); got.Status != statusTimingFailed || !hasFinding(got, "timing", "package") {
		t.Fatalf("timing over budget = %#v, want failure", got)
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
	path := filepath.Join(t.TempDir(), "baseline.json")
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
