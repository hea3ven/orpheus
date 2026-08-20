package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/testlane"
)

const coverageRunEnvironment = "ORPHEUS_COVERAGE_RUN"

var laneNames = []string{"unit", "integration"}

type block struct {
	StartLine   int
	StartColumn int
	EndLine     int
	EndColumn   int
	Statements  int
	Covered     bool
}

type fileCoverage struct {
	Name              string
	StatementTotal    int
	CoveredStatements int
	Blocks            []block
}

type packageCoverage struct {
	Name              string
	StatementTotal    int
	CoveredStatements int
	Files             []fileCoverage
}

type laneCoverage struct {
	StatementTotal    int
	CoveredStatements int
	Packages          []packageCoverage
}

type coverageMetric struct {
	StatementTotal    int `json:"statement_total"`
	CoveredStatements int `json:"covered_statements"`
}

type packageMetric struct {
	Name string `json:"name"`
	coverageMetric
}

type packageTiming struct {
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
}

type testFailure struct {
	Package string `json:"package"`
	Test    string `json:"test,omitempty"`
	Output  string `json:"output,omitempty"`
}

type laneFailure struct {
	Error      string        `json:"error"`
	Failures   []testFailure `json:"failures,omitempty"`
	Stderr     string        `json:"stderr,omitempty"`
	TestOutput string        `json:"test_output,omitempty"`
}

type laneReport struct {
	Lane        string          `json:"lane"`
	Command     []string        `json:"command"`
	Passed      bool            `json:"passed"`
	WallSeconds float64         `json:"wall_seconds"`
	TestCount   int             `json:"test_count"`
	Coverage    coverageMetric  `json:"coverage"`
	Packages    []packageMetric `json:"packages,omitempty"`
	Timings     []packageTiming `json:"package_timings,omitempty"`
	Failure     *laneFailure    `json:"failure,omitempty"`
}

type testEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
	Output  string  `json:"Output"`
}

type testID struct {
	Package string
	Test    string
}

func collectLane(lane, profile string) (laneReport, laneCoverage, error) {
	command := coverageCommand(lane, profile, "")
	report, err := runGoTest(lane, command)
	coverage, profileErr := normalizeProfile(profile)
	if profileErr == nil {
		report.Coverage = metricForCoverage(coverage)
		report.Packages = metricsForPackages(coverage.Packages)
	}
	if err != nil && profileErr != nil {
		return report, coverage, errors.Join(err, fmt.Errorf("read partial coverage: %w", profileErr))
	}
	if err != nil {
		return report, coverage, err
	}
	if profileErr != nil {
		report.Passed = false
		report.Failure = &laneFailure{Error: profileErr.Error()}
		return report, coverage, fmt.Errorf("normalize coverage profile: %w", profileErr)
	}
	return report, coverage, nil
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
		if lane == "integration" {
			command[len(command)-1] = "^" + testName + "$"
		} else {
			command = append(command, "-run", "^"+testName+"$")
		}
	}
	return append(command, packages...)
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

func runGoTest(lane string, command []string) (laneReport, error) {
	started := time.Now()
	result := laneReport{Lane: lane, Command: recordedCommand(command)}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Env = coverageEnvironment(os.Environ())
	var stderr bytes.Buffer
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result, err
	}
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return result, err
	}

	events := decodeTestEvents(stdout)
	waitErr := cmd.Wait()
	result.WallSeconds = time.Since(started).Seconds()
	result.TestCount = events.testCount
	result.Timings = sortedTimings(events.packages)
	if events.decodeErr == nil && waitErr == nil {
		result.Passed = true
		return result, nil
	}
	failure := &laneFailure{
		Error:      testCommandError(events.decodeErr, waitErr),
		Failures:   failuresFromEvents(events.outputs, events.failed),
		Stderr:     stderr.String(),
		TestOutput: events.raw.String(),
	}
	result.Failure = failure
	return result, errors.New(failure.Error)
}

type decodedTestEvents struct {
	testCount    int
	packages     map[string]float64
	testPackages map[string]bool
	outputs      map[testID]string
	failed       map[testID]bool
	raw          bytes.Buffer
	decodeErr    error
}

func decodeTestEvents(input io.Reader) decodedTestEvents {
	result := decodedTestEvents{
		packages:     make(map[string]float64),
		testPackages: make(map[string]bool),
		outputs:      make(map[testID]string),
		failed:       make(map[testID]bool),
	}
	decoder := json.NewDecoder(io.TeeReader(input, &result.raw))
	for {
		var event testEvent
		if err := decoder.Decode(&event); err != nil {
			if !errors.Is(err, io.EOF) {
				result.decodeErr = err
				_, _ = io.Copy(&result.raw, input)
			}
			return result
		}
		recordTestEvent(&result, event)
	}
}

func recordTestEvent(result *decodedTestEvents, event testEvent) {
	id := testID{Package: event.Package, Test: event.Test}
	if event.Test != "" && (event.Action == "run" || event.Action == "pass" || event.Action == "fail") {
		result.testPackages[event.Package] = true
	}
	switch event.Action {
	case "output":
		result.outputs[id] += event.Output
	case "fail":
		result.failed[id] = true
		if event.Test == "" && result.testPackages[event.Package] {
			result.packages[event.Package] = event.Elapsed
		}
	case "pass":
		if event.Test == "" {
			if result.testPackages[event.Package] {
				result.packages[event.Package] = event.Elapsed
			}
		} else {
			result.testCount++
		}
	}
}

func testCommandError(decodeErr, waitErr error) string {
	switch {
	case decodeErr != nil && waitErr != nil:
		return fmt.Sprintf("decode go test JSON: %v (test command: %v)", decodeErr, waitErr)
	case decodeErr != nil:
		return fmt.Sprintf("decode go test JSON: %v", decodeErr)
	case waitErr != nil:
		return waitErr.Error()
	default:
		return "test command failed"
	}
}

func failuresFromEvents(outputs map[testID]string, failed map[testID]bool) []testFailure {
	failures := make([]testFailure, 0, len(failed))
	for id := range failed {
		failures = append(failures, testFailure{Package: id.Package, Test: id.Test, Output: outputs[id]})
	}
	sort.Slice(failures, func(i, j int) bool {
		if failures[i].Package == failures[j].Package {
			return failures[i].Test < failures[j].Test
		}
		return failures[i].Package < failures[j].Package
	})
	return failures
}

func sortedTimings(values map[string]float64) []packageTiming {
	result := make([]packageTiming, 0, len(values))
	for name, seconds := range values {
		result = append(result, packageTiming{Name: name, Seconds: seconds})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

func coverageEnvironment(base []string) []string {
	ignored := map[string]struct{}{
		"NO_COLOR": {}, "ORPHEUS_AGENT_PURPOSE": {}, "ORPHEUS_ALTERNATE_REVIEWER_PROFILE": {},
		"ORPHEUS_EXHAUSTIVE_REVIEW_CONTEXT": {}, "ORPHEUS_RESUME_SESSIONS": {},
		"ORPHEUS_REVIEWER_ROLE": {}, "CODEX_HOME": {}, "PI_CODING_AGENT_DIR": {},
		"PI_CODING_AGENT_SESSION_DIR": {}, coverageRunEnvironment: {},
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
		if exists {
			parsed.block.Covered = prior.block.Covered || parsed.block.Covered
		}
		blocks[key] = parsed
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
		sort.Slice(values, func(i, j int) bool { return blockKey(values[i]) < blockKey(values[j]) })
		file := fileCoverage{Name: name, Blocks: values}
		for _, item := range values {
			file.StatementTotal += item.Statements
			if item.Covered {
				file.CoveredStatements += item.Statements
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

func blockKey(item block) string {
	return fmt.Sprintf("%d.%d,%d.%d/%d", item.StartLine, item.StartColumn, item.EndLine, item.EndColumn, item.Statements)
}

func metricForCoverage(value laneCoverage) coverageMetric {
	return coverageMetric{StatementTotal: value.StatementTotal, CoveredStatements: value.CoveredStatements}
}

func metricsForPackages(packages []packageCoverage) []packageMetric {
	result := make([]packageMetric, 0, len(packages))
	for _, pkg := range packages {
		result = append(result, packageMetric{Name: pkg.Name, coverageMetric: coverageMetric{StatementTotal: pkg.StatementTotal, CoveredStatements: pkg.CoveredStatements}})
	}
	return result
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
