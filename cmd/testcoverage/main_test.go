package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestCompareReportsOnlyUnchangedCoverageRegression(t *testing.T) {
	prior := baseline{SchemaVersion: baselineSchemaVersion, Lanes: map[string]laneCoverage{
		"unit": testLane("unit", true, false), "integration": testLane("integration", false, false),
	}}
	current := baseline{SchemaVersion: baselineSchemaVersion, Lanes: map[string]laneCoverage{
		"unit": testLane("unit", false, true), "integration": testLane("integration", false, false),
	}}

	got, err := compare(prior, current)
	if err != nil {
		t.Fatal(err)
	}
	if got.NewlyUncovered != 1 {
		t.Fatalf("newly uncovered = %d, want 1", got.NewlyUncovered)
	}
	if got.NewlyCovered != 1 {
		t.Fatalf("newly covered = %d, want 1", got.NewlyCovered)
	}
	if len(got.NewlyUncoveredBlocks) != 1 || got.NewlyUncoveredBlocks[0].File != "example.test/pkg/file.go" {
		t.Fatalf("newly uncovered blocks = %#v, want actionable source identity", got.NewlyUncoveredBlocks)
	}
	if got.NewlyUncoveredBlocks[0].PriorCoveredBy[0] != "unit" || len(got.NewlyUncoveredBlocks[0].CurrentCoveredBy) != 0 {
		t.Fatalf("newly uncovered lane transition = %#v", got.NewlyUncoveredBlocks[0])
	}
	if got.Combined.Covered != 1 || got.Marginal.Covered != 0 {
		t.Fatalf("combined/marginal = %#v/%#v", got.Combined, got.Marginal)
	}
}

func TestCompareReportsRegressionAfterAnEarlierLineShift(t *testing.T) {
	prior := baseline{SchemaVersion: baselineSchemaVersion, Lanes: map[string]laneCoverage{
		"unit":        withBlockSourceHash(testLane("unit", true, false), 1, "unchanged-statement"),
		"integration": withBlockSourceHash(testLane("integration", false, false), 1, "unchanged-statement"),
	}}
	current := baseline{SchemaVersion: baselineSchemaVersion, Lanes: map[string]laneCoverage{
		"unit":        shiftBlock(withBlockSourceHash(testLane("unit", false, false), 1, "unchanged-statement"), 1, 4),
		"integration": shiftBlock(withBlockSourceHash(testLane("integration", false, false), 1, "unchanged-statement"), 1, 4),
	}}

	got, err := compare(prior, current)
	if err != nil {
		t.Fatal(err)
	}
	if got.NewlyUncovered != 1 || len(got.NewlyUncoveredBlocks) != 1 {
		t.Fatalf("shifted unchanged regression = %#v, want one uncovered statement", got)
	}
	if got.NewlyUncoveredBlocks[0].StartLine != 4 {
		t.Fatalf("shifted regression reports line %d, want current line 4", got.NewlyUncoveredBlocks[0].StartLine)
	}
}

func TestCompareMatchesShiftedBlockDespiteCoordinateCollision(t *testing.T) {
	prior := comparisonBaseline(
		[]block{testBlock(10, "first", true), testBlock(11, "second", false)},
		[]block{testBlock(10, "first", false), testBlock(11, "second", false)},
	)
	current := comparisonBaseline(
		[]block{testBlock(11, "first", false), testBlock(12, "second", false)},
		[]block{testBlock(11, "first", false), testBlock(12, "second", false)},
	)

	got, err := compare(prior, current)
	if err != nil {
		t.Fatal(err)
	}
	if got.NewlyUncovered != 1 || len(got.NewlyUncoveredBlocks) != 1 {
		t.Fatalf("coordinate collision regression = %#v, want one uncovered statement", got)
	}
	if got.NewlyUncoveredBlocks[0].StartLine != 11 {
		t.Fatalf("coordinate collision reports line %d, want current line 11", got.NewlyUncoveredBlocks[0].StartLine)
	}
}

func TestCompareMatchesRepeatedShiftedBlocksInSourceOrder(t *testing.T) {
	prior := comparisonBaseline(
		[]block{testBlock(10, "return-error", true), testBlock(11, "return-error", true)},
		[]block{testBlock(10, "return-error", false), testBlock(11, "return-error", false)},
	)
	current := comparisonBaseline(
		[]block{testBlock(11, "return-error", false), testBlock(12, "return-error", true)},
		[]block{testBlock(11, "return-error", false), testBlock(12, "return-error", false)},
	)

	got, err := compare(prior, current)
	if err != nil {
		t.Fatal(err)
	}
	if got.NewlyUncovered != 1 || len(got.NewlyUncoveredBlocks) != 1 {
		t.Fatalf("repeated shifted regression = %#v, want one uncovered statement", got)
	}
	if got.NewlyUncoveredBlocks[0].StartLine != 11 {
		t.Fatalf("repeated shifted regression reports line %d, want current line 11", got.NewlyUncoveredBlocks[0].StartLine)
	}
}

func TestCompareMatchesRepeatedShiftedBlocksAtUnusedCoordinates(t *testing.T) {
	prior := comparisonBaseline(
		[]block{testBlock(10, "return-error", true), testBlock(11, "return-error", true)},
		[]block{testBlock(10, "return-error", false), testBlock(11, "return-error", false)},
	)
	current := comparisonBaseline(
		[]block{testBlock(20, "return-error", false), testBlock(21, "return-error", true)},
		[]block{testBlock(20, "return-error", false), testBlock(21, "return-error", false)},
	)

	got, err := compare(prior, current)
	if err != nil {
		t.Fatal(err)
	}
	if got.NewlyUncovered != 1 || len(got.NewlyUncoveredBlocks) != 1 {
		t.Fatalf("repeated unmatched regression = %#v, want one uncovered statement", got)
	}
	if got.NewlyUncoveredBlocks[0].StartLine != 20 {
		t.Fatalf("repeated unmatched regression reports line %d, want current line 20", got.NewlyUncoveredBlocks[0].StartLine)
	}
}

func TestCompareMatchesRepeatedBlocksAfterAnInsertedCopy(t *testing.T) {
	prior := comparisonBaseline(
		[]block{testBlock(10, "return-error", true), testBlock(20, "return-error", false)},
		[]block{testBlock(10, "return-error", false), testBlock(20, "return-error", false)},
	)
	current := comparisonBaseline(
		[]block{testBlock(10, "return-error", true), testBlock(11, "return-error", false), testBlock(21, "return-error", false)},
		[]block{testBlock(10, "return-error", false), testBlock(11, "return-error", false), testBlock(21, "return-error", false)},
	)

	got, err := compare(prior, current)
	if err != nil {
		t.Fatal(err)
	}
	if got.NewlyUncovered != 1 || len(got.NewlyUncoveredBlocks) != 1 {
		t.Fatalf("inserted repeated copy regression = %#v, want one uncovered statement", got)
	}
	if got.NewlyUncoveredBlocks[0].StartLine != 11 {
		t.Fatalf("inserted repeated copy reports line %d, want current line 11", got.NewlyUncoveredBlocks[0].StartLine)
	}
}

func TestCompareReportsAmbiguousEqualRepeatedOffsets(t *testing.T) {
	prior := comparisonBaseline(
		[]block{testBlock(20, "return-error", true), testBlock(40, "return-error", false)},
		[]block{testBlock(20, "return-error", false), testBlock(40, "return-error", false)},
	)
	current := comparisonBaseline(
		[]block{testBlock(10, "return-error", true), testBlock(30, "return-error", false), testBlock(50, "return-error", false)},
		[]block{testBlock(10, "return-error", false), testBlock(30, "return-error", false), testBlock(50, "return-error", false)},
	)

	for range 100 {
		got, err := compare(prior, current)
		if err != nil {
			t.Fatal(err)
		}
		if got.NewlyUncovered != 1 || len(got.NewlyUncoveredBlocks) != 1 {
			t.Fatalf("equal-offset regression = %#v, want one uncovered statement", got)
		}
		if got.NewlyUncoveredBlocks[0].StartLine != 30 {
			t.Fatalf("equal-offset regression reports line %d, want possible regression line 30", got.NewlyUncoveredBlocks[0].StartLine)
		}
		if !got.NewlyUncoveredBlocks[0].SourceMatchAmbiguous {
			t.Fatal("equal-offset regression was not marked ambiguous")
		}
	}
}

func TestCompareReportsRegressionAfterDeletingRepeatedBlock(t *testing.T) {
	prior := comparisonBaseline(
		[]block{testBlock(10, "return-error", false), testBlock(20, "return-error", true), testBlock(30, "return-error", true)},
		[]block{testBlock(10, "return-error", false), testBlock(20, "return-error", false), testBlock(30, "return-error", false)},
	)
	current := comparisonBaseline(
		[]block{testBlock(10, "return-error", false), testBlock(20, "return-error", true)},
		[]block{testBlock(10, "return-error", false), testBlock(20, "return-error", false)},
	)

	got, err := compare(prior, current)
	if err != nil {
		t.Fatal(err)
	}
	if got.NewlyUncovered != 1 || len(got.NewlyUncoveredBlocks) != 1 {
		t.Fatalf("deleted repeated copy regression = %#v, want one uncovered statement", got)
	}
	if got.NewlyUncoveredBlocks[0].StartLine != 10 {
		t.Fatalf("deleted repeated copy reports line %d, want possible regression line 10", got.NewlyUncoveredBlocks[0].StartLine)
	}
	if !got.NewlyUncoveredBlocks[0].SourceMatchAmbiguous {
		t.Fatal("deleted repeated copy regression was not marked ambiguous")
	}
}

func TestSameCoverageRejectsStaleAggregateMetadata(t *testing.T) {
	prior := generatedTestBaseline()
	current := generatedTestBaseline()
	staleUnit := prior.Lanes["unit"]
	staleUnit.CoveredStatements = 0
	prior.Lanes["unit"] = staleUnit

	if sameCoverage(prior, current) {
		t.Fatal("sameCoverage accepted a stale covered-statements aggregate")
	}
	if _, err := compare(prior, current); err == nil {
		t.Fatal("compare accepted a stale covered-statements aggregate")
	}
	path := filepath.Join(t.TempDir(), "baseline.json")
	if err := writeJSON(path, prior); err != nil {
		t.Fatal(err)
	}
	if _, err := loadBaseline(path); err == nil {
		t.Fatal("loadBaseline accepted a stale covered-statements aggregate")
	}
}

func TestSameCoverageRejectsChangedCommandMetadata(t *testing.T) {
	prior := generatedTestBaseline()
	current := generatedTestBaseline()
	current.Lanes["unit"] = withCommand(current.Lanes["unit"], "go", "test", "-count=2")

	if sameCoverage(prior, current) {
		t.Fatal("sameCoverage accepted a changed coverage command")
	}
}

func TestSameCoverageRejectsChangedHitStates(t *testing.T) {
	prior := baseline{SchemaVersion: baselineSchemaVersion, Lanes: map[string]laneCoverage{
		"unit": testLane("unit", true, false), "integration": testLane("integration", false, true),
	}}
	current := baseline{SchemaVersion: baselineSchemaVersion, Lanes: map[string]laneCoverage{
		"unit": testLane("unit", false, false), "integration": testLane("integration", false, true),
	}}

	if sameCoverage(prior, current) {
		t.Fatal("sameCoverage accepted a changed coverage hit state")
	}
}

func TestPackageAndFileTotalsUseCombinedCoverage(t *testing.T) {
	data := baseline{SchemaVersion: baselineSchemaVersion, Lanes: map[string]laneCoverage{
		"unit": testLane("unit", true, false), "integration": testLane("integration", false, true),
	}}

	if got := packageTotals(data)["example.test/pkg"]; got != (totals{total: 2, covered: 2}) {
		t.Fatalf("package total = %#v, want combined 2/2", got)
	}
	if got := fileTotals(data)["example.test/pkg/file.go"]; got != (totals{total: 2, covered: 2}) {
		t.Fatalf("file total = %#v, want combined 2/2", got)
	}
}

func TestValidateDenominatorsRejectsDifferentProfiles(t *testing.T) {
	unit := testLane("unit", true, false)
	integration := testLane("integration", true, false)
	integration.Packages[0].Files[0].Blocks[0].EndColumn++
	if err := validateDenominators(unit, integration); err == nil {
		t.Fatal("validateDenominators accepted different source blocks")
	}
}

func TestCoverageCommandUsesCrossPackageDenominator(t *testing.T) {
	command := strings.Join(coverageCommand("integration", "result.cover", ""), " ")
	for _, want := range []string{"-coverpkg=./...", "-covermode=set", "-tags=integration", "-run ^TestIntegration"} {
		if !strings.Contains(command, want) {
			t.Fatalf("command %q does not contain %q", command, want)
		}
	}
}

func TestCoverageEnvironmentRemovesOperatorSettings(t *testing.T) {
	got := environmentValues(coverageEnvironment([]string{
		"PATH=/test/bin",
		"NO_COLOR=1",
		"ORPHEUS_ALTERNATE_REVIEWER_PROFILE=alternate",
		"ORPHEUS_RESUME_SESSIONS=1",
		"CODEX_HOME=/operator/codex",
		"PI_CODING_AGENT_SESSION_DIR=/operator/pi",
		coverageRunEnvironment + "=operator-value",
		"UNRELATED=value",
	}))
	for _, name := range []string{
		"NO_COLOR",
		"ORPHEUS_ALTERNATE_REVIEWER_PROFILE",
		"ORPHEUS_RESUME_SESSIONS",
		"CODEX_HOME",
		"PI_CODING_AGENT_SESSION_DIR",
	} {
		if _, found := got[name]; found {
			t.Errorf("coverage environment retained %s", name)
		}
	}
	if got["PATH"] != "/test/bin" || got["UNRELATED"] != "value" {
		t.Fatalf("coverage environment discarded tool settings: %#v", got)
	}
	if got[coverageRunEnvironment] != "1" {
		t.Fatalf("coverage environment = %#v, want %s=1", got, coverageRunEnvironment)
	}
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

func TestScenarioTargetsRetainPackageIdentity(t *testing.T) {
	input := strings.NewReader("{\"Action\":\"output\",\"Package\":\"example.test/first\",\"Output\":\"TestIntegrationSame\\n\"}\n" +
		"{\"Action\":\"output\",\"Package\":\"example.test/second\",\"Output\":\"TestIntegrationSame\\n\"}\n")

	targets, err := scenarioTargets(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 2 || targets[0].Package == targets[1].Package || targets[0].Test != targets[1].Test {
		t.Fatalf("scenario targets = %#v, want separately package-qualified duplicate names", targets)
	}
}

func withBlockSourceHash(lane laneCoverage, line int, sourceHash string) laneCoverage {
	for packageIndex := range lane.Packages {
		for fileIndex := range lane.Packages[packageIndex].Files {
			for blockIndex := range lane.Packages[packageIndex].Files[fileIndex].Blocks {
				item := &lane.Packages[packageIndex].Files[fileIndex].Blocks[blockIndex]
				if item.StartLine == line {
					item.SourceHash = sourceHash
				}
			}
		}
	}
	return lane
}

func shiftBlock(lane laneCoverage, from, to int) laneCoverage {
	for packageIndex := range lane.Packages {
		for fileIndex := range lane.Packages[packageIndex].Files {
			for blockIndex := range lane.Packages[packageIndex].Files[fileIndex].Blocks {
				item := &lane.Packages[packageIndex].Files[fileIndex].Blocks[blockIndex]
				if item.StartLine == from {
					delta := to - from
					item.StartLine += delta
					item.EndLine += delta
				}
			}
		}
	}
	return normalizedTestLane(lane)
}

func normalizedTestLane(lane laneCoverage) laneCoverage {
	normalized := coverageFromBlocks(profileBlocksForLane(lane))
	normalized.Lane = lane.Lane
	normalized.GoVersion = lane.GoVersion
	normalized.Command = lane.Command
	return normalized
}

func comparisonBaseline(unitBlocks, integrationBlocks []block) baseline {
	return baseline{SchemaVersion: baselineSchemaVersion, Lanes: map[string]laneCoverage{
		"unit":        testLaneWithBlocks("unit", unitBlocks),
		"integration": testLaneWithBlocks("integration", integrationBlocks),
	}}
}

func generatedTestBaseline() baseline {
	result := comparisonBaseline(
		[]block{testBlock(1, "first", true), testBlock(2, "second", false)},
		[]block{testBlock(1, "first", false), testBlock(2, "second", false)},
	)
	for name, lane := range result.Lanes {
		result.Lanes[name] = withCommand(lane, "go", "test", "-coverpkg=./...")
	}
	return result
}

func withCommand(lane laneCoverage, command ...string) laneCoverage {
	lane.GoVersion = "go test"
	lane.Command = command
	return lane
}

func testBlock(line int, sourceHash string, covered bool) block {
	return block{
		StartLine: line, StartColumn: 1, EndLine: line, EndColumn: 2,
		Statements: 1, Covered: covered, SourceHash: sourceHash,
	}
}

func testLaneWithBlocks(name string, items []block) laneCoverage {
	blocks := make(map[string]profileBlock, len(items))
	for _, item := range items {
		key := "example.test/pkg/file.go:" + blockKey(item)
		blocks[key] = profileBlock{file: "example.test/pkg/file.go", block: item}
	}
	lane := coverageFromBlocks(blocks)
	lane.Lane = name
	lane.GoVersion = "go test"
	lane.Command = []string{"go", "test", "-coverpkg=./..."}
	return lane
}

func testLane(name string, firstCovered, secondCovered bool) laneCoverage {
	return testLaneWithBlocks(name, []block{
		testBlock(1, "first", firstCovered),
		testBlock(2, "second", secondCovered),
	})
}
