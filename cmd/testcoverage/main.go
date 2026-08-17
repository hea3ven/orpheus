// Command testcoverage collects normalized cross-package coverage for test lanes.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/testlane"
)

const (
	baselineSchemaVersion  = 2
	coverageRuns           = 2
	coverageRunEnvironment = "ORPHEUS_COVERAGE_RUN"
)

type options struct {
	baseline       string
	output         string
	writeBaseline  bool
	compareTo      string
	auditScenarios bool
}

type block struct {
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column"`
	EndLine     int    `json:"end_line"`
	EndColumn   int    `json:"end_column"`
	Statements  int    `json:"statements"`
	Covered     bool   `json:"covered"`
	SourceHash  string `json:"source_hash"`
}

type fileCoverage struct {
	Name              string  `json:"name"`
	StatementTotal    int     `json:"statement_total"`
	CoveredStatements int     `json:"covered_statements"`
	Blocks            []block `json:"blocks"`
}

type packageCoverage struct {
	Name              string         `json:"name"`
	StatementTotal    int            `json:"statement_total"`
	CoveredStatements int            `json:"covered_statements"`
	Files             []fileCoverage `json:"files"`
}

type laneCoverage struct {
	Lane              string            `json:"lane"`
	TestCount         int               `json:"test_count"`
	GoVersion         string            `json:"go_version"`
	Command           []string          `json:"command"`
	StatementTotal    int               `json:"statement_total"`
	CoveredStatements int               `json:"covered_statements"`
	Packages          []packageCoverage `json:"packages"`
}

type baseline struct {
	SchemaVersion int                     `json:"schema_version"`
	Lanes         map[string]laneCoverage `json:"lanes"`
}

type value struct {
	Covered    int     `json:"covered_statements"`
	Total      int     `json:"statement_total"`
	Percentage float64 `json:"percentage"`
}

type fileChange struct {
	File           string `json:"file"`
	PriorTotal     int    `json:"prior_total"`
	CurrentTotal   int    `json:"current_total"`
	PriorCovered   int    `json:"prior_covered"`
	CurrentCovered int    `json:"current_covered"`
}

type packageChange struct {
	Package        string `json:"package"`
	PriorTotal     int    `json:"prior_total"`
	CurrentTotal   int    `json:"current_total"`
	PriorCovered   int    `json:"prior_covered"`
	CurrentCovered int    `json:"current_covered"`
}

type statementChange struct {
	File                 string   `json:"file"`
	StartLine            int      `json:"start_line"`
	StartColumn          int      `json:"start_column"`
	EndLine              int      `json:"end_line"`
	EndColumn            int      `json:"end_column"`
	Statements           int      `json:"statements"`
	PriorCoveredBy       []string `json:"prior_covered_by"`
	CurrentCoveredBy     []string `json:"current_covered_by"`
	SourceMatchAmbiguous bool     `json:"source_match_ambiguous,omitempty"`
}

type comparison struct {
	Unit                 value             `json:"unit"`
	Integration          value             `json:"integration"`
	Combined             value             `json:"combined"`
	Marginal             value             `json:"integration_marginal"`
	MainUnit             value             `json:"main_unit"`
	MainIntegration      value             `json:"main_integration"`
	MainCombined         value             `json:"main_combined"`
	MainMarginal         value             `json:"main_integration_marginal"`
	NewlyCovered         int               `json:"newly_covered_statements"`
	NewlyUncovered       int               `json:"newly_uncovered_statements"`
	NewlyCoveredBlocks   []statementChange `json:"newly_covered_blocks"`
	NewlyUncoveredBlocks []statementChange `json:"newly_uncovered_blocks"`
	PackageChanges       []packageChange   `json:"package_changes"`
	FileChanges          []fileChange      `json:"file_changes"`
}

type scenarioResult struct {
	Name                  string  `json:"name"`
	RuntimeSeconds        float64 `json:"runtime_seconds"`
	CoveredStatements     int     `json:"covered_statements"`
	ContainmentPercentage float64 `json:"containment_percentage"`
	SimilarityPercentage  float64 `json:"similarity_percentage"`
	ExclusiveStatements   int     `json:"exclusive_statements"`
}

type report struct {
	Baseline   baseline         `json:"baseline"`
	Comparison *comparison      `json:"comparison,omitempty"`
	Scenarios  []scenarioResult `json:"scenarios,omitempty"`
}

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := execute(opts); err != nil {
		fmt.Fprintln(os.Stderr, "test coverage:", err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	opts := options{}
	flags := flag.NewFlagSet("testcoverage", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&opts.baseline, "baseline", "coverage/test-coverage-baseline.json", "tracked normalized coverage baseline")
	flags.StringVar(&opts.output, "output", "artifacts/test-coverage/report.json", "short-lived JSON report path")
	flags.BoolVar(&opts.writeBaseline, "write-baseline", false, "regenerate the tracked normalized baseline")
	flags.StringVar(&opts.compareTo, "compare-to", "", "normalized baseline to compare with this result")
	flags.BoolVar(&opts.auditScenarios, "audit-scenarios", false, "profile every integration scenario separately (expensive)")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("testcoverage does not accept positional arguments")
	}
	return opts, nil
}

func execute(opts options) error {
	work, err := os.MkdirTemp("", "orpheus-test-coverage-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	current, err := collectBaseline(work)
	if err != nil {
		return err
	}
	result := report{Baseline: current}
	if opts.auditScenarios {
		result.Scenarios, err = auditIntegrationScenarios(work, current.Lanes["integration"])
		if err != nil {
			return err
		}
	}

	if opts.writeBaseline {
		if err := writeJSON(opts.baseline, current); err != nil {
			return fmt.Errorf("write baseline: %w", err)
		}
		fmt.Printf("Wrote normalized coverage baseline to %s.\n", opts.baseline)
	} else if err := checkBaseline(opts.baseline, current); err != nil {
		return err
	}
	if opts.compareTo != "" {
		prior, err := loadBaseline(opts.compareTo)
		if err != nil {
			return fmt.Errorf("read comparison baseline: %w", err)
		}
		comp, err := compare(prior, current)
		if err != nil {
			return err
		}
		result.Comparison = &comp
	}
	if err := writeJSON(opts.output, result); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	printReport(result)
	fmt.Printf("Coverage report: %s\n", opts.output)
	return nil
}

func checkBaseline(path string, current baseline) error {
	existing, err := loadBaseline(path)
	if err != nil {
		return fmt.Errorf("read tracked baseline: %w (run make coverage-baseline)", err)
	}
	if !sameCoverage(existing, current) {
		return fmt.Errorf("tracked baseline %s is stale; run make coverage-baseline and commit the generated file", path)
	}
	return nil
}

func collectBaseline(work string) (baseline, error) {
	lanes := make(map[string]laneCoverage, 2)
	for _, lane := range []string{"unit", "integration"} {
		profile := filepath.Join(work, lane+".cover")
		coverage, err := collectLane(lane, profile)
		if err != nil {
			return baseline{}, err
		}
		lanes[lane] = coverage
	}
	if err := validateDenominators(lanes["unit"], lanes["integration"]); err != nil {
		return baseline{}, err
	}
	return baseline{SchemaVersion: baselineSchemaVersion, Lanes: lanes}, nil
}

func collectLane(lane, profile string) (laneCoverage, error) {
	var result laneCoverage
	var testCount int
	for sample := 0; sample < coverageRuns; sample++ {
		sampleProfile := fmt.Sprintf("%s.%d", profile, sample+1)
		command := coverageCommand(lane, sampleProfile, "")
		currentTestCount, err := runGoTest(command)
		if err != nil {
			return laneCoverage{}, fmt.Errorf("run %s coverage sample %d: %w", lane, sample+1, err)
		}
		coverage, err := normalizeProfile(sampleProfile)
		if err != nil {
			return laneCoverage{}, fmt.Errorf("normalize %s coverage profile: %w", lane, err)
		}
		coverage, err = addSourceHashes(coverage)
		if err != nil {
			return laneCoverage{}, fmt.Errorf("fingerprint %s coverage profile: %w", lane, err)
		}
		if sample == 0 {
			result = coverage
			testCount = currentTestCount
		} else {
			if testCount != currentTestCount {
				return laneCoverage{}, fmt.Errorf("%s coverage samples have different test-event counts (%d and %d)", lane, testCount, currentTestCount)
			}
			if result, err = mergeCoverage(result, coverage); err != nil {
				return laneCoverage{}, fmt.Errorf("merge %s coverage sample %d: %w", lane, sample+1, err)
			}
		}
		result.Command = recordedCommand(command)
	}
	result.Lane = lane
	result.TestCount = testCount
	result.GoVersion = runtime.Version()
	return result, nil
}

func recordedCommand(command []string) []string {
	result := append([]string(nil), command...)
	for index, argument := range result {
		if strings.HasPrefix(argument, "-coverprofile=") {
			result[index] = "-coverprofile=<temporary-profile>"
		}
	}
	return result
}

func coverageCommand(lane, profile, testName string) []string {
	return coverageCommandForPackages(lane, profile, testName, []string{"./..."})
}

func coverageCommandForPackages(lane, profile, testName string, packages []string) []string {
	command := []string{"go", "test", "-json", "-count=1", "-p=1", "-parallel=1", "-covermode=set", "-coverpkg=./...", "-coverprofile=" + profile}
	if lane == "integration" {
		command = append(command, "-tags="+testlane.IntegrationBuildTag, "-run", testlane.IntegrationTestPattern)
	}
	if testName != "" {
		command[len(command)-1] = "^" + testName + "$"
	}
	return append(command, packages...)
}

type testEvent struct {
	Action  string `json:"Action"`
	Package string `json:"Package"`
	Test    string `json:"Test"`
	Output  string `json:"Output"`
}

func runGoTest(command []string) (int, error) {
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = coverageEnvironment(os.Environ())
	var stderr bytes.Buffer
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	decoder := json.NewDecoder(stdout)
	testCount := 0
	for {
		var event testEvent
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = cmd.Wait()
			return 0, fmt.Errorf("decode go test JSON: %w", err)
		}
		if event.Action == "pass" && event.Test != "" {
			testCount++
		}
	}
	if err := cmd.Wait(); err != nil {
		return 0, fmt.Errorf("%w\n%s", err, stderr.String())
	}
	return testCount, nil
}

// coverageEnvironment removes application settings that can select different
// test paths. It deliberately retains toolchain settings such as PATH and Go
// caches from the caller, while preventing an operator's Orpheus, Codex, or Pi
// session from changing the normalized baseline.
func coverageEnvironment(base []string) []string {
	ignored := map[string]struct{}{
		"NO_COLOR":                           {},
		"ORPHEUS_AGENT_PURPOSE":              {},
		"ORPHEUS_ALTERNATE_REVIEWER_PROFILE": {},
		"ORPHEUS_EXHAUSTIVE_REVIEW_CONTEXT":  {},
		"ORPHEUS_RESUME_SESSIONS":            {},
		"ORPHEUS_REVIEWER_ROLE":              {},
		"CODEX_HOME":                         {},
		"PI_CODING_AGENT_DIR":                {},
		"PI_CODING_AGENT_SESSION_DIR":        {},
		coverageRunEnvironment:               {},
	}
	result := make([]string, 0, len(base)+1)
	for _, entry := range base {
		key, _, found := strings.Cut(entry, "=")
		if !found {
			result = append(result, entry)
			continue
		}
		if _, found := ignored[key]; !found {
			result = append(result, entry)
		}
	}
	return append(result, coverageRunEnvironment+"=1")
}

type profileBlock struct {
	file  string
	block block
}

func normalizeProfile(profile string) (laneCoverage, error) {
	file, err := os.Open(profile)
	if err != nil {
		return laneCoverage{}, err
	}
	defer func() { _ = file.Close() }()

	blocks := make(map[string]profileBlock)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	if !scanner.Scan() || scanner.Text() != "mode: set" {
		return laneCoverage{}, errors.New("coverage profile must use mode: set")
	}
	for scanner.Scan() {
		parsed, err := parseProfileLine(scanner.Text())
		if err != nil {
			return laneCoverage{}, err
		}
		key := parsed.file + ":" + blockKey(parsed.block)
		prior, exists := blocks[key]
		if !exists || parsed.block.Covered {
			if exists {
				parsed.block.Covered = prior.block.Covered || parsed.block.Covered
			}
			blocks[key] = parsed
		}
	}
	if err := scanner.Err(); err != nil {
		return laneCoverage{}, err
	}
	return coverageFromBlocks(blocks), nil
}

func parseProfileLine(line string) (profileBlock, error) {
	parts := strings.Fields(line)
	if len(parts) != 3 {
		return profileBlock{}, fmt.Errorf("invalid coverage profile line %q", line)
	}
	file, positions, found := strings.Cut(parts[0], ":")
	if !found {
		return profileBlock{}, fmt.Errorf("invalid coverage position %q", parts[0])
	}
	start, end, found := strings.Cut(positions, ",")
	if !found {
		return profileBlock{}, fmt.Errorf("invalid coverage range %q", positions)
	}
	startLine, startColumn, err := parsePosition(start)
	if err != nil {
		return profileBlock{}, err
	}
	endLine, endColumn, err := parsePosition(end)
	if err != nil {
		return profileBlock{}, err
	}
	statements, err := strconv.Atoi(parts[1])
	if err != nil {
		return profileBlock{}, err
	}
	count, err := strconv.Atoi(parts[2])
	if err != nil {
		return profileBlock{}, err
	}
	return profileBlock{file: file, block: block{StartLine: startLine, StartColumn: startColumn, EndLine: endLine, EndColumn: endColumn, Statements: statements, Covered: count > 0}}, nil
}

func parsePosition(position string) (int, int, error) {
	line, column, found := strings.Cut(position, ".")
	if !found {
		return 0, 0, fmt.Errorf("invalid source position %q", position)
	}
	lineNumber, err := strconv.Atoi(line)
	if err != nil {
		return 0, 0, err
	}
	columnNumber, err := strconv.Atoi(column)
	if err != nil {
		return 0, 0, err
	}
	return lineNumber, columnNumber, nil
}

func coverageFromBlocks(blocks map[string]profileBlock) laneCoverage {
	byFile := make(map[string][]block)
	for _, item := range blocks {
		byFile[item.file] = append(byFile[item.file], item.block)
	}
	packages := make(map[string][]fileCoverage)
	for name, values := range byFile {
		sortBlocks(values)
		file := fileCoverage{Name: name, Blocks: values}
		for _, value := range values {
			file.StatementTotal += value.Statements
			if value.Covered {
				file.CoveredStatements += value.Statements
			}
		}
		packages[path.Dir(name)] = append(packages[path.Dir(name)], file)
	}
	result := laneCoverage{Packages: make([]packageCoverage, 0, len(packages))}
	for name, files := range packages {
		sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
		pkg := packageCoverage{Name: name, Files: files}
		for _, file := range files {
			pkg.StatementTotal += file.StatementTotal
			pkg.CoveredStatements += file.CoveredStatements
		}
		result.StatementTotal += pkg.StatementTotal
		result.CoveredStatements += pkg.CoveredStatements
		result.Packages = append(result.Packages, pkg)
	}
	sort.Slice(result.Packages, func(i, j int) bool { return result.Packages[i].Name < result.Packages[j].Name })
	return result
}

func blockKey(block block) string {
	return fmt.Sprintf("%d.%d,%d.%d/%d", block.StartLine, block.StartColumn, block.EndLine, block.EndColumn, block.Statements)
}

func sortBlocks(blocks []block) {
	sort.Slice(blocks, func(i, j int) bool { return blockKey(blocks[i]) < blockKey(blocks[j]) })
}

// addSourceHashes adds a stable fingerprint for each production block. Coverage
// positions shift when unrelated source is inserted above a statement, so source
// content—not coordinates—identifies unchanged blocks across baselines.
func addSourceHashes(coverage laneCoverage) (laneCoverage, error) {
	modulePath, err := currentModulePath()
	if err != nil {
		return laneCoverage{}, err
	}
	blocks := profileBlocksForLane(coverage)
	for key, item := range blocks {
		sourcePath, err := sourcePathForProfile(modulePath, item.file)
		if err != nil {
			return laneCoverage{}, err
		}
		contents, err := os.ReadFile(sourcePath)
		if err != nil {
			return laneCoverage{}, err
		}
		hash, err := sourceBlockHash(contents, item.block)
		if err != nil {
			return laneCoverage{}, fmt.Errorf("fingerprint %s: %w", item.file, err)
		}
		item.block.SourceHash = hash
		blocks[key] = item
	}
	return coverageFromBlocks(blocks), nil
}

func currentModulePath() (string, error) {
	contents, err := os.ReadFile("go.mod")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", errors.New("module path is missing from go.mod")
}

func sourcePathForProfile(modulePath, profilePath string) (string, error) {
	prefix := modulePath + "/"
	relative, found := strings.CutPrefix(profilePath, prefix)
	if !found || relative == "" || filepath.IsAbs(relative) || strings.HasPrefix(filepath.Clean(relative), "..") {
		return "", fmt.Errorf("coverage path %q is outside module %q", profilePath, modulePath)
	}
	return filepath.FromSlash(relative), nil
}

func sourceBlockHash(contents []byte, item block) (string, error) {
	start, err := sourceOffset(contents, item.StartLine, item.StartColumn)
	if err != nil {
		return "", err
	}
	end, err := sourceOffset(contents, item.EndLine, item.EndColumn)
	if err != nil {
		return "", err
	}
	if end < start {
		return "", errors.New("coverage block ends before it starts")
	}
	digest := sha256.Sum256(contents[start:end])
	return hex.EncodeToString(digest[:]), nil
}

func sourceOffset(contents []byte, line, column int) (int, error) {
	if line < 1 || column < 1 {
		return 0, fmt.Errorf("invalid source position %d.%d", line, column)
	}
	lineStart := 0
	for currentLine := 1; currentLine < line; currentLine++ {
		next := bytes.IndexByte(contents[lineStart:], '\n')
		if next < 0 {
			return 0, fmt.Errorf("source line %d is outside file", line)
		}
		lineStart += next + 1
	}
	lineEnd := len(contents)
	if next := bytes.IndexByte(contents[lineStart:], '\n'); next >= 0 {
		lineEnd = lineStart + next
	}
	offset := lineStart + column - 1
	if offset > lineEnd {
		return 0, fmt.Errorf("source column %d is outside line %d", column, line)
	}
	return offset, nil
}

func mergeCoverage(left, right laneCoverage) (laneCoverage, error) {
	leftBlocks, rightBlocks := profileBlocksForLane(left), profileBlocksForLane(right)
	if len(leftBlocks) != len(rightBlocks) {
		return laneCoverage{}, errors.New("coverage samples have different production denominators")
	}
	for key, rightBlock := range rightBlocks {
		leftBlock, found := leftBlocks[key]
		if !found {
			return laneCoverage{}, fmt.Errorf("coverage samples differ at %s", key)
		}
		leftBlock.block.Covered = leftBlock.block.Covered || rightBlock.block.Covered
		leftBlocks[key] = leftBlock
	}
	return coverageFromBlocks(leftBlocks), nil
}

func profileBlocksForLane(lane laneCoverage) map[string]profileBlock {
	result := make(map[string]profileBlock)
	for _, pkg := range lane.Packages {
		for _, file := range pkg.Files {
			for _, block := range file.Blocks {
				result[file.Name+":"+blockKey(block)] = profileBlock{file: file.Name, block: block}
			}
		}
	}
	return result
}

func validateDenominators(unit, integration laneCoverage) error {
	unitBlocks := blocksForLane(unit)
	integrationBlocks := blocksForLane(integration)
	if len(unitBlocks) != len(integrationBlocks) {
		return fmt.Errorf("unit and integration profiles have different production denominators (%d and %d blocks)", len(unitBlocks), len(integrationBlocks))
	}
	for key := range unitBlocks {
		if _, found := integrationBlocks[key]; !found {
			return fmt.Errorf("unit and integration profiles have different production denominators (missing %s)", key)
		}
	}
	return nil
}

func blocksForLane(lane laneCoverage) map[string]block {
	result := make(map[string]block)
	for _, pkg := range lane.Packages {
		for _, file := range pkg.Files {
			for _, block := range file.Blocks {
				result[file.Name+":"+blockKey(block)] = block
			}
		}
	}
	return result
}

func loadBaseline(name string) (baseline, error) {
	contents, err := os.ReadFile(name)
	if err != nil {
		return baseline{}, err
	}
	var value baseline
	if err := json.Unmarshal(contents, &value); err != nil {
		return baseline{}, err
	}
	if value.SchemaVersion != baselineSchemaVersion || value.Lanes["unit"].Lane != "unit" || value.Lanes["integration"].Lane != "integration" {
		return baseline{}, errors.New("unsupported or incomplete coverage baseline")
	}
	if err := validateBaseline(value); err != nil {
		return baseline{}, fmt.Errorf("invalid coverage baseline: %w", err)
	}
	return value, nil
}

func writeJSON(name string, value any) error {
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(name, append(contents, '\n'), 0o644)
}

// validateBaseline checks that generated aggregate values still agree with the
// normalized blocks they describe. Go version remains audit metadata, but all
// values used for reports and the recorded test command must be trustworthy.
func validateBaseline(data baseline) error {
	for _, name := range []string{"unit", "integration"} {
		lane := data.Lanes[name]
		if lane.GoVersion == "" || len(lane.Command) == 0 {
			return fmt.Errorf("%s lane is missing required metadata", name)
		}
		expected := coverageFromBlocks(profileBlocksForLane(lane))
		if lane.StatementTotal != expected.StatementTotal || lane.CoveredStatements != expected.CoveredStatements || !reflect.DeepEqual(lane.Packages, expected.Packages) {
			return fmt.Errorf("%s lane aggregates do not match normalized blocks", name)
		}
	}
	return validateDenominators(data.Lanes["unit"], data.Lanes["integration"])
}

// sameCoverage checks reproducible aggregate values, command metadata, source
// blocks, and hit states. Go version is audit metadata and may vary by runner.
func sameCoverage(prior, current baseline) bool {
	for _, name := range []string{"unit", "integration"} {
		left, right := prior.Lanes[name], current.Lanes[name]
		if left.TestCount != right.TestCount || left.StatementTotal != right.StatementTotal || left.CoveredStatements != right.CoveredStatements || !reflect.DeepEqual(left.Command, right.Command) || !reflect.DeepEqual(left.Packages, right.Packages) {
			return false
		}
		if !sameBlocks(blocksForLane(left), blocksForLane(right)) {
			return false
		}
	}
	return true
}

func sameBlocks(left, right map[string]block) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftBlock := range left {
		rightBlock, found := right[key]
		if !found || leftBlock != rightBlock {
			return false
		}
	}
	return true
}

func compare(prior, current baseline) (comparison, error) {
	if err := validateBaseline(prior); err != nil {
		return comparison{}, fmt.Errorf("validate main baseline: %w", err)
	}
	if err := validateBaseline(current); err != nil {
		return comparison{}, fmt.Errorf("validate current baseline: %w", err)
	}
	result := comparison{
		Unit:        coverageValue(current.Lanes["unit"]),
		Integration: coverageValue(current.Lanes["integration"]),
	}
	result.Combined, result.Marginal = combinedValues(current.Lanes["unit"], current.Lanes["integration"])
	result.MainUnit = coverageValue(prior.Lanes["unit"])
	result.MainIntegration = coverageValue(prior.Lanes["integration"])
	result.MainCombined, result.MainMarginal = combinedValues(prior.Lanes["unit"], prior.Lanes["integration"])

	priorCombined := combinedBlocks(prior.Lanes["unit"], prior.Lanes["integration"])
	currentCombined := combinedBlocks(current.Lanes["unit"], current.Lanes["integration"])
	newlyCovered := make(map[string]statementChange)
	newlyUncovered := make(map[string]statementChange)
	for _, match := range matchedBlocks(priorCombined, currentCombined) {
		if !match.prior.Covered && match.current.Covered {
			addStatementChange(newlyCovered, match, prior, current)
		}
		if match.prior.Covered && !match.current.Covered {
			addStatementChange(newlyUncovered, match, prior, current)
		}
	}
	result.NewlyCoveredBlocks = statementChanges(newlyCovered)
	result.NewlyUncoveredBlocks = statementChanges(newlyUncovered)
	for _, change := range result.NewlyCoveredBlocks {
		result.NewlyCovered += change.Statements
	}
	for _, change := range result.NewlyUncoveredBlocks {
		result.NewlyUncovered += change.Statements
	}
	result.PackageChanges = changedPackages(prior, current)
	result.FileChanges = changedFiles(prior, current)
	return result, nil
}

func combinedValues(unit, integration laneCoverage) (value, value) {
	unitBlocks, integrationBlocks := blocksForLane(unit), blocksForLane(integration)
	combined, marginal := 0, 0
	for key, unitBlock := range unitBlocks {
		integrationBlock := integrationBlocks[key]
		if unitBlock.Covered || integrationBlock.Covered {
			combined += unitBlock.Statements
		}
		if integrationBlock.Covered && !unitBlock.Covered {
			marginal += unitBlock.Statements
		}
	}
	return makeValue(combined, unit.StatementTotal), makeValue(marginal, unit.StatementTotal)
}

func combinedBlocks(unit, integration laneCoverage) map[string]block {
	result := blocksForLane(unit)
	for key, integrationBlock := range blocksForLane(integration) {
		unitBlock := result[key]
		unitBlock.Covered = unitBlock.Covered || integrationBlock.Covered
		result[key] = unitBlock
	}
	return result
}

type matchedBlock struct {
	priorKey, currentKey string
	prior, current       block
	ambiguous            bool
}

// matchedBlocks pairs source-content fingerprints. Repeated fingerprints use
// the source-line offset shared by the largest ordered subset, so an inserted
// identical statement does not displace all subsequent matches in the group.
func matchedBlocks(prior, current map[string]block) []matchedBlock {
	priorGroups := blocksByFingerprint(prior)
	currentGroups := blocksByFingerprint(current)
	matches := make([]matchedBlock, 0, min(len(prior), len(current)))
	for fingerprint, priorBlocks := range priorGroups {
		matches = append(matches, matchFingerprintGroup(priorBlocks, currentGroups[fingerprint])...)
	}
	return matches
}

func matchFingerprintGroup(prior, current []blockReference) []matchedBlock {
	var matches []matchedBlock
	for len(prior) > 0 && len(current) > 0 {
		alignments := bestOffsetPairs(prior, current)
		if len(alignments) == 0 {
			break
		}
		if len(alignments) > 1 {
			// Identical source blocks do not carry enough identity to choose
			// between equal alignments. Preserve every possible transition as
			// an explicitly ambiguous review signal rather than silently hiding
			// a covered-to-uncovered statement.
			return append(matches, matchedPairs(alignments, true)...)
		}
		pairs := alignments[0]
		// A one-to-one group is unambiguous. A lone pair from larger remaining
		// groups is not: it could instead be an inserted or deleted copy.
		if len(pairs) == 1 && len(prior) > 1 && len(current) > 1 {
			break
		}
		matches = append(matches, matchedPairs([][]blockPair{pairs}, false)...)
		matchedPrior := make(map[string]struct{}, len(pairs))
		matchedCurrent := make(map[string]struct{}, len(pairs))
		for _, pair := range pairs {
			matchedPrior[pair.prior.key] = struct{}{}
			matchedCurrent[pair.current.key] = struct{}{}
		}
		prior = unmatchedReferences(prior, matchedPrior)
		current = unmatchedReferences(current, matchedCurrent)
	}
	return matches
}

func matchedPairs(alignments [][]blockPair, ambiguous bool) []matchedBlock {
	seen := make(map[string]struct{})
	var matches []matchedBlock
	for _, alignment := range alignments {
		for _, pair := range alignment {
			key := pair.prior.key + "\x00" + pair.current.key
			if _, found := seen[key]; found {
				continue
			}
			seen[key] = struct{}{}
			matches = append(matches, matchedBlock{
				priorKey: pair.prior.key, currentKey: pair.current.key,
				prior: pair.prior.block, current: pair.current.block, ambiguous: ambiguous,
			})
		}
	}
	return matches
}

type blockPair struct{ prior, current blockReference }

// bestOffsetPairs finds all largest sets of blocks that preserve a common line
// offset. Equal candidates are intentionally retained for an ambiguity-aware
// comparison instead of being selected by map iteration or a directional bias.
func bestOffsetPairs(prior, current []blockReference) [][]blockPair {
	lineOffsets := make(map[int]struct{})
	for _, priorBlock := range prior {
		for _, currentBlock := range current {
			if priorBlock.block.StartColumn == currentBlock.block.StartColumn {
				lineOffsets[currentBlock.block.StartLine-priorBlock.block.StartLine] = struct{}{}
			}
		}
	}
	offsets := make([]int, 0, len(lineOffsets))
	for offset := range lineOffsets {
		offsets = append(offsets, offset)
	}
	sort.Ints(offsets)
	var best [][]blockPair
	for _, offset := range offsets {
		pairs := pairsForLineOffset(prior, current, offset)
		switch {
		case len(best) == 0 || len(pairs) > len(best[0]):
			best = [][]blockPair{pairs}
		case len(pairs) == len(best[0]):
			best = append(best, pairs)
		}
	}
	return best
}

func pairsForLineOffset(prior, current []blockReference, offset int) []blockPair {
	currentByPosition := make(map[string][]blockReference, len(current))
	for _, item := range current {
		position := sourcePosition(item.block)
		currentByPosition[position] = append(currentByPosition[position], item)
	}
	var pairs []blockPair
	for _, priorItem := range prior {
		position := sourcePosition(block{StartLine: priorItem.block.StartLine + offset, StartColumn: priorItem.block.StartColumn})
		candidates := currentByPosition[position]
		if len(candidates) == 0 {
			continue
		}
		pairs = append(pairs, blockPair{prior: priorItem, current: candidates[0]})
		currentByPosition[position] = candidates[1:]
	}
	return pairs
}

func sourcePosition(item block) string {
	return strconv.Itoa(item.StartLine) + "." + strconv.Itoa(item.StartColumn)
}

func unmatchedReferences(items []blockReference, matched map[string]struct{}) []blockReference {
	result := make([]blockReference, 0, len(items)-len(matched))
	for _, item := range items {
		if _, found := matched[item.key]; !found {
			result = append(result, item)
		}
	}
	return result
}

type blockReference struct {
	key   string
	block block
}

func blocksByFingerprint(blocks map[string]block) map[string][]blockReference {
	result := make(map[string][]blockReference)
	for key, item := range blocks {
		if item.SourceHash == "" {
			continue
		}
		file, _, _ := strings.Cut(key, ":")
		fingerprint := file + "\x00" + item.SourceHash + "\x00" + strconv.Itoa(item.Statements)
		result[fingerprint] = append(result[fingerprint], blockReference{key: key, block: item})
	}
	for _, group := range result {
		sort.Slice(group, func(i, j int) bool {
			left, right := group[i].block, group[j].block
			if left.StartLine != right.StartLine {
				return left.StartLine < right.StartLine
			}
			if left.StartColumn != right.StartColumn {
				return left.StartColumn < right.StartColumn
			}
			if left.EndLine != right.EndLine {
				return left.EndLine < right.EndLine
			}
			return left.EndColumn < right.EndColumn
		})
	}
	return result
}

func addStatementChange(changes map[string]statementChange, match matchedBlock, prior, current baseline) {
	change := statementChangeFor(match.priorKey, match.currentKey, match.current, prior, current)
	change.SourceMatchAmbiguous = match.ambiguous
	if existing, found := changes[match.currentKey]; found {
		change.SourceMatchAmbiguous = existing.SourceMatchAmbiguous || change.SourceMatchAmbiguous
	}
	changes[match.currentKey] = change
}

func statementChanges(changes map[string]statementChange) []statementChange {
	result := make([]statementChange, 0, len(changes))
	for _, change := range changes {
		result = append(result, change)
	}
	sortStatementChanges(result)
	return result
}

func statementChangeFor(priorKey, currentKey string, item block, prior, current baseline) statementChange {
	file, _, _ := strings.Cut(currentKey, ":")
	return statementChange{
		File:             file,
		StartLine:        item.StartLine,
		StartColumn:      item.StartColumn,
		EndLine:          item.EndLine,
		EndColumn:        item.EndColumn,
		Statements:       item.Statements,
		PriorCoveredBy:   coveredByLanes(priorKey, prior),
		CurrentCoveredBy: coveredByLanes(currentKey, current),
	}
}

func coveredByLanes(key string, data baseline) []string {
	var lanes []string
	for _, name := range []string{"unit", "integration"} {
		if blocksForLane(data.Lanes[name])[key].Covered {
			lanes = append(lanes, name)
		}
	}
	return lanes
}

func sortStatementChanges(changes []statementChange) {
	sort.Slice(changes, func(i, j int) bool {
		left, right := changes[i], changes[j]
		if left.File != right.File {
			return left.File < right.File
		}
		if left.StartLine != right.StartLine {
			return left.StartLine < right.StartLine
		}
		return left.StartColumn < right.StartColumn
	})
}

func coverageValue(lane laneCoverage) value {
	return makeValue(lane.CoveredStatements, lane.StatementTotal)
}
func makeValue(covered, total int) value {
	percentage := 0.0
	if total > 0 {
		percentage = float64(covered) * 100 / float64(total)
	}
	return value{Covered: covered, Total: total, Percentage: percentage}
}

func changedPackages(prior, current baseline) []packageChange {
	priorPackages := packageTotals(prior)
	currentPackages := packageTotals(current)
	keys := make(map[string]struct{}, len(priorPackages)+len(currentPackages))
	for key := range priorPackages {
		keys[key] = struct{}{}
	}
	for key := range currentPackages {
		keys[key] = struct{}{}
	}
	var changes []packageChange
	for key := range keys {
		before, after := priorPackages[key], currentPackages[key]
		if before != after {
			changes = append(changes, packageChange{Package: key, PriorTotal: before.total, CurrentTotal: after.total, PriorCovered: before.covered, CurrentCovered: after.covered})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Package < changes[j].Package })
	return changes
}

func changedFiles(prior, current baseline) []fileChange {
	priorFiles := fileTotals(prior)
	currentFiles := fileTotals(current)
	keys := make(map[string]struct{}, len(priorFiles)+len(currentFiles))
	for key := range priorFiles {
		keys[key] = struct{}{}
	}
	for key := range currentFiles {
		keys[key] = struct{}{}
	}
	var changes []fileChange
	for key := range keys {
		before, after := priorFiles[key], currentFiles[key]
		if before != after {
			changes = append(changes, fileChange{File: key, PriorTotal: before.total, CurrentTotal: after.total, PriorCovered: before.covered, CurrentCovered: after.covered})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].File < changes[j].File })
	return changes
}

type totals struct{ total, covered int }

func packageTotals(data baseline) map[string]totals {
	result := make(map[string]totals)
	for key, item := range combinedBlocks(data.Lanes["unit"], data.Lanes["integration"]) {
		file, _, _ := strings.Cut(key, ":")
		value := result[path.Dir(file)]
		value.total += item.Statements
		if item.Covered {
			value.covered += item.Statements
		}
		result[path.Dir(file)] = value
	}
	return result
}

func fileTotals(data baseline) map[string]totals {
	result := make(map[string]totals)
	for key, item := range combinedBlocks(data.Lanes["unit"], data.Lanes["integration"]) {
		file, _, _ := strings.Cut(key, ":")
		value := result[file]
		value.total += item.Statements
		if item.Covered {
			value.covered += item.Statements
		}
		result[file] = value
	}
	return result
}

type scenarioTarget struct {
	Package string
	Test    string
}

func (target scenarioTarget) String() string {
	return target.Package + "/" + target.Test
}

func auditIntegrationScenarios(work string, integration laneCoverage) ([]scenarioResult, error) {
	targets, err := integrationScenarioNames()
	if err != nil {
		return nil, err
	}
	profiles, runtimes, err := scenarioProfiles(work, targets)
	if err != nil {
		return nil, err
	}
	all := blocksForLane(integration)
	result := make([]scenarioResult, 0, len(targets))
	for index, target := range targets {
		others := append(append([]map[string]block(nil), profiles[:index]...), profiles[index+1:]...)
		result = append(result, scenarioMetrics(target.String(), runtimes[index], profiles[index], all, others))
	}
	return result, nil
}

func integrationScenarioNames() ([]scenarioTarget, error) {
	list := exec.Command("go", "test", "-json", "-tags="+testlane.IntegrationBuildTag, "-list", testlane.IntegrationTestPattern, "./...")
	list.Env = coverageEnvironment(os.Environ())
	output, err := list.Output()
	if err != nil {
		return nil, fmt.Errorf("list integration scenarios: %w", err)
	}
	return scenarioTargets(bytes.NewReader(output))
}

func scenarioTargets(input io.Reader) ([]scenarioTarget, error) {
	decoder := json.NewDecoder(input)
	var targets []scenarioTarget
	for {
		var event testEvent
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode integration scenario list: %w", err)
		}
		if event.Action != "output" {
			continue
		}
		for _, line := range strings.Split(event.Output, "\n") {
			name := strings.TrimSpace(line)
			if strings.HasPrefix(name, "TestIntegration") {
				targets = append(targets, scenarioTarget{Package: event.Package, Test: name})
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].String() < targets[j].String() })
	return targets, nil
}

func scenarioProfiles(work string, targets []scenarioTarget) ([]map[string]block, []float64, error) {
	profiles := make([]map[string]block, 0, len(targets))
	runtimes := make([]float64, 0, len(targets))
	for index, target := range targets {
		profile := filepath.Join(work, fmt.Sprintf("scenario-%d.cover", index))
		started := time.Now()
		if _, err := runGoTest(coverageCommandForPackages("integration", profile, target.Test, []string{target.Package})); err != nil {
			return nil, nil, fmt.Errorf("profile %s: %w", target, err)
		}
		lane, err := normalizeProfile(profile)
		if err != nil {
			return nil, nil, err
		}
		profiles = append(profiles, blocksForLane(lane))
		runtimes = append(runtimes, time.Since(started).Seconds())
	}
	return profiles, runtimes, nil
}

func scenarioMetrics(name string, runtimeSeconds float64, scenario map[string]block, all map[string]block, integration []map[string]block) scenarioResult {
	covered, contained := coveredAndContained(scenario, all)
	intersection, union, exclusive := scenarioOverlap(scenario, integration)
	return scenarioResult{Name: name, RuntimeSeconds: runtimeSeconds, CoveredStatements: covered, ContainmentPercentage: ratio(contained, covered), SimilarityPercentage: ratio(intersection, union), ExclusiveStatements: exclusive}
}

func coveredAndContained(scenario, all map[string]block) (int, int) {
	covered, contained := 0, 0
	for key, item := range scenario {
		if !item.Covered {
			continue
		}
		covered += item.Statements
		if all[key].Covered {
			contained += item.Statements
		}
	}
	return covered, contained
}

func scenarioOverlap(scenario map[string]block, scenarios []map[string]block) (int, int, int) {
	intersection, union, exclusive := 0, 0, 0
	for key, item := range scenario {
		if !item.Covered {
			continue
		}
		if coveredByOther(key, scenarios) {
			intersection += item.Statements
		} else {
			exclusive += item.Statements
		}
	}
	for _, item := range unionBlocks(scenario, scenarios) {
		if item.Covered {
			union += item.Statements
		}
	}
	return intersection, union, exclusive
}

func coveredByOther(key string, scenarios []map[string]block) bool {
	for _, other := range scenarios {
		if other[key].Covered {
			return true
		}
	}
	return false
}

func unionBlocks(scenario map[string]block, scenarios []map[string]block) map[string]block {
	result := make(map[string]block)
	for key, item := range scenario {
		result[key] = item
	}
	for _, other := range scenarios {
		for key, item := range other {
			current, found := result[key]
			if !found || item.Covered {
				item.Covered = item.Covered || current.Covered
				result[key] = item
			}
		}
	}
	return result
}

func ratio(part, total int) float64 {
	if total == 0 {
		return 100
	}
	return float64(part) * 100 / float64(total)
}

func printReport(result report) {
	for _, name := range []string{"unit", "integration"} {
		lane := result.Baseline.Lanes[name]
		fmt.Printf("%s: %d/%d statements (%.2f%%), %d test events\n", name, lane.CoveredStatements, lane.StatementTotal, coverageValue(lane).Percentage, lane.TestCount)
	}
	if result.Comparison != nil {
		printValue := func(name string, item, main value) {
			fmt.Printf("%s: %d/%d statements (%.2f%%; main %.2f%%, %+.2fpp)\n", name, item.Covered, item.Total, item.Percentage, main.Percentage, item.Percentage-main.Percentage)
		}
		printValue("unit", result.Comparison.Unit, result.Comparison.MainUnit)
		printValue("integration", result.Comparison.Integration, result.Comparison.MainIntegration)
		printValue("combined", result.Comparison.Combined, result.Comparison.MainCombined)
		printValue("integration-marginal", result.Comparison.Marginal, result.Comparison.MainMarginal)
		fmt.Printf("unchanged statements: +%d newly covered, -%d newly uncovered\n", result.Comparison.NewlyCovered, result.Comparison.NewlyUncovered)
		printStatementChanges("Newly uncovered unchanged statements", result.Comparison.NewlyUncoveredBlocks)
		printStatementChanges("Newly covered unchanged statements", result.Comparison.NewlyCoveredBlocks)
		fmt.Printf("package/file changes: %d packages, %d files\n", len(result.Comparison.PackageChanges), len(result.Comparison.FileChanges))
	}
	for _, scenario := range result.Scenarios {
		fmt.Printf("scenario %s: %.2fs, containment %.2f%%, similarity %.2f%%, %d exclusive statements\n", scenario.Name, scenario.RuntimeSeconds, scenario.ContainmentPercentage, scenario.SimilarityPercentage, scenario.ExclusiveStatements)
	}
}

const maxPrintedStatementChanges = 20

func printStatementChanges(title string, changes []statementChange) {
	if len(changes) == 0 {
		return
	}
	fmt.Printf("\n### %s\n\n", title)
	fmt.Println("| File | Range | Statements | Main covered by | Current covered by | Source match |")
	fmt.Println("| --- | --- | ---: | --- | --- | --- |")
	for index, change := range changes {
		if index == maxPrintedStatementChanges {
			fmt.Printf("\n%d additional source blocks are available in the JSON report.\n", len(changes)-index)
			break
		}
		fmt.Printf("| `%s` | %d.%d-%d.%d | %d | %s | %s | %s |\n",
			markdownCell(change.File),
			change.StartLine,
			change.StartColumn,
			change.EndLine,
			change.EndColumn,
			change.Statements,
			coveredByCell(change.PriorCoveredBy),
			coveredByCell(change.CurrentCoveredBy),
			sourceMatchCell(change.SourceMatchAmbiguous),
		)
	}
}

func markdownCell(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}

func coveredByCell(lanes []string) string {
	if len(lanes) == 0 {
		return "—"
	}
	return strings.Join(lanes, ", ")
}

func sourceMatchCell(ambiguous bool) string {
	if ambiguous {
		return "ambiguous"
	}
	return "exact"
}
