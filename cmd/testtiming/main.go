// Command testtiming measures the repository test lanes and evaluates their budgets.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/testlane"
)

const (
	reportSchemaVersion   = 2
	baselineSchemaVersion = 1
	defaultSamples        = 5
)

type options struct {
	lane            string
	samples         int
	output          string
	baseline        string
	initBaseline    bool
	replaceBaseline bool
	updateBaseline  bool
}

type duration struct {
	Name    string  `json:"name"`
	Seconds float64 `json:"seconds"`
	Samples int     `json:"samples"`
}

type run struct {
	WallSeconds float64            `json:"wall_seconds"`
	Packages    map[string]float64 `json:"packages"`
	Tests       map[string]float64 `json:"tests"`
}

type testFailure struct {
	Package string `json:"package"`
	Test    string `json:"test,omitempty"`
	Output  string `json:"output,omitempty"`
}

type sampleFailure struct {
	Sample     int           `json:"sample"`
	Command    []string      `json:"command"`
	Error      string        `json:"error"`
	Failures   []testFailure `json:"failures,omitempty"`
	Stderr     string        `json:"stderr,omitempty"`
	TestOutput string        `json:"test_output,omitempty"`
}

type summary struct {
	WallSeconds float64    `json:"wall_seconds"`
	TestCount   int        `json:"test_count"`
	Packages    []duration `json:"packages"`
	Tests       []duration `json:"tests,omitempty"`
}

type environment struct {
	GoVersion  string `json:"go_version"`
	GOOS       string `json:"goos"`
	GOARCH     string `json:"goarch"`
	CPUCount   int    `json:"cpu_count"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	CPUModel   string `json:"cpu_model,omitempty"`
}

type report struct {
	SchemaVersion int            `json:"schema_version"`
	CreatedAt     time.Time      `json:"created_at"`
	Lane          string         `json:"lane"`
	Samples       int            `json:"samples"`
	Command       []string       `json:"command"`
	Environment   environment    `json:"environment"`
	Runs          []run          `json:"runs"`
	Median        summary        `json:"median"`
	Complete      bool           `json:"complete"`
	Failure       *sampleFailure `json:"failure,omitempty"`
}

type budgetPolicy struct {
	PackageRelativeTolerance float64 `json:"package_relative_tolerance"`
	PackageAbsoluteSeconds   float64 `json:"package_absolute_seconds"`
	SuiteRelativeTolerance   float64 `json:"suite_relative_tolerance"`
	SuiteAbsoluteSeconds     float64 `json:"suite_absolute_seconds"`
}

type laneBaseline struct {
	RecordedAt  time.Time   `json:"recorded_at"`
	Environment environment `json:"environment"`
	Median      summary     `json:"median"`
	SuiteBudget float64     `json:"suite_budget_seconds"`
	Budgets     []duration  `json:"package_budgets"`
}

type baseline struct {
	SchemaVersion int                     `json:"schema_version"`
	Policy        budgetPolicy            `json:"policy"`
	Lanes         map[string]laneBaseline `json:"lanes"`
}

type testEvent struct {
	Action  string  `json:"Action"`
	Package string  `json:"Package"`
	Test    string  `json:"Test"`
	Elapsed float64 `json:"Elapsed"`
	Output  string  `json:"Output"`
}

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := execute(opts); err != nil {
		fmt.Fprintln(os.Stderr, "test timing:", err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	opts := options{}
	flags := flag.NewFlagSet("testtiming", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&opts.lane, "lane", "unit", "test lane to measure: unit or integration")
	flags.IntVar(&opts.samples, "samples", defaultSamples, "number of uncached samples")
	flags.StringVar(&opts.output, "output", os.Getenv("TEST_TIMING_OUTPUT"), "JSON report path")
	flags.StringVar(&opts.baseline, "baseline", "performance/test-timing-baseline.json", "tracked JSON budget baseline")
	flags.BoolVar(&opts.initBaseline, "init-baseline", false, "create a missing lane baseline")
	flags.BoolVar(&opts.replaceBaseline, "replace-baseline", false, "replace a lane baseline from a complete sample set")
	flags.BoolVar(&opts.updateBaseline, "update-baseline", false, "record lower measurements and ratchet budgets down")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("testtiming does not accept positional arguments")
	}
	if opts.lane != "unit" && opts.lane != "integration" {
		return options{}, fmt.Errorf("unknown lane %q (want unit or integration)", opts.lane)
	}
	if opts.samples < 1 {
		return options{}, errors.New("samples must be at least 1")
	}
	baselineActions := 0
	for _, enabled := range []bool{opts.initBaseline, opts.replaceBaseline, opts.updateBaseline} {
		if enabled {
			baselineActions++
		}
	}
	if baselineActions > 1 {
		return options{}, errors.New("only one of init-baseline, replace-baseline, and update-baseline may be used")
	}
	if opts.output == "" {
		opts.output = filepath.Join("artifacts", "test-timing", fmt.Sprintf("%s-%s.json", opts.lane, time.Now().UTC().Format("20060102T150405Z")))
	}
	return opts, nil
}

func execute(opts options) error {
	if opts.lane == "integration" {
		if _, err := exec.LookPath("bd"); err != nil {
			return errors.New("beads integration timings require bd; install Beads or ensure bd is on PATH")
		}
	}

	report, measureErr := measure(opts)
	if err := writeJSON(opts.output, report); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	printReport(report, opts.output)
	if measureErr != nil {
		return fmt.Errorf("%w; incomplete report: %s", measureErr, opts.output)
	}

	return handleBaseline(opts, report)
}

func handleBaseline(opts options, report report) error {
	if err := validateCompleteReport(report); err != nil {
		return err
	}
	loaded, err := loadBaseline(opts.baseline)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read baseline: %w", err)
	}
	switch {
	case opts.replaceBaseline:
		return replaceBaseline(opts, report, loaded, err)
	case opts.initBaseline:
		if err == nil {
			if _, exists := loaded.Lanes[report.Lane]; exists {
				return fmt.Errorf("baseline %s already has a %s lane; use -update-baseline to ratchet it down", opts.baseline, report.Lane)
			}
			loaded.Lanes[report.Lane] = baselineLane(report, loaded.Policy)
		} else {
			loaded = initialBaseline(report)
		}
		if err := writeJSON(opts.baseline, loaded); err != nil {
			return fmt.Errorf("write baseline: %w", err)
		}
		fmt.Printf("Created %s lane baseline in %s.\n", opts.lane, opts.baseline)
		return nil
	case opts.updateBaseline:
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("baseline %s does not exist; use -init-baseline first", opts.baseline)
		}
		if existing, exists := loaded.Lanes[report.Lane]; exists && existing.Median.TestCount != report.Median.TestCount {
			return fmt.Errorf("baseline %s has %d test events for %s; regenerate it from the complete current suite", opts.baseline, existing.Median.TestCount, report.Lane)
		}
		changed := loaded.update(report)
		if err := writeJSON(opts.baseline, loaded); err != nil {
			return fmt.Errorf("write baseline: %w", err)
		}
		if changed {
			fmt.Printf("Updated %s with lower timing budgets.\n", opts.baseline)
		} else {
			fmt.Printf("No lower timings found; left budgets in %s unchanged.\n", opts.baseline)
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("baseline %s does not exist; run again with -init-baseline", opts.baseline)
	}

	failures := loaded.check(report)
	if len(failures) == 0 {
		fmt.Println("Timing budgets passed.")
		return nil
	}
	for _, failure := range failures {
		fmt.Fprintln(os.Stderr, "budget exceeded:", failure)
	}
	return fmt.Errorf("%d timing budget(s) exceeded", len(failures))
}

func replaceBaseline(opts options, report report, loaded baseline, loadErr error) error {
	if errors.Is(loadErr, os.ErrNotExist) {
		loaded = initialBaseline(report)
	} else {
		loaded.Lanes[report.Lane] = baselineLane(report, loaded.Policy)
	}
	if err := writeJSON(opts.baseline, loaded); err != nil {
		return fmt.Errorf("write baseline: %w", err)
	}
	fmt.Printf("Replaced %s lane baseline in %s.\n", opts.lane, opts.baseline)
	return nil
}

func measure(opts options) (report, error) {
	command := testCommand(opts.lane)
	result := report{
		SchemaVersion: reportSchemaVersion,
		CreatedAt:     time.Now().UTC(),
		Lane:          opts.lane,
		Samples:       opts.samples,
		Command:       command,
		Environment:   currentEnvironment(),
		Runs:          make([]run, 0, opts.samples),
	}
	fmt.Printf("Measuring %s tests with %d uncached samples.\n", opts.lane, opts.samples)
	for i := 0; i < opts.samples; i++ {
		fmt.Printf("  sample %d/%d... ", i+1, opts.samples)
		measurement, failure, err := runTests(command)
		if err != nil {
			fmt.Println("failed")
			if failure != nil {
				failure.Sample = i + 1
				result.Failure = failure
			}
			return result, fmt.Errorf("sample %d: %w", i+1, err)
		}
		fmt.Printf("%.2fs\n", measurement.WallSeconds)
		result.Runs = append(result.Runs, measurement)
	}
	result.Median = summarize(result.Runs)
	result.Complete = true
	return result, nil
}

func testCommand(lane string) []string {
	command := []string{"go", "test", "-json", "-count=1"}
	if lane == "integration" {
		command = append(command, "-tags="+testlane.IntegrationBuildTag, "-run", testlane.IntegrationTestPattern)
	}
	return append(command, "./...")
}

func runTests(command []string) (run, *sampleFailure, error) {
	started := time.Now()
	cmd := exec.Command(command[0], command[1:]...)
	var stderr bytes.Buffer
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return run{}, nil, err
	}
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return run{}, nil, err
	}

	measurement := run{Packages: make(map[string]float64), Tests: make(map[string]float64)}
	outputs := make(map[testID]string)
	failed := make(map[testID]bool)
	var testOutput bytes.Buffer
	decoder := json.NewDecoder(io.TeeReader(stdout, &testOutput))
	var decodeErr error
	for {
		var event testEvent
		err := decoder.Decode(&event)
		if errors.Is(err, os.ErrClosed) || errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			decodeErr = err
			break
		}
		recordTestEvent(measurement, outputs, failed, event)
	}
	if decodeErr != nil {
		_, _ = io.Copy(&testOutput, stdout)
	}
	waitErr := cmd.Wait()
	if decodeErr != nil || waitErr != nil {
		failure := &sampleFailure{
			Command:    append([]string(nil), command...),
			Error:      testCommandError(decodeErr, waitErr),
			Failures:   failuresFromEvents(outputs, failed),
			Stderr:     stderr.String(),
			TestOutput: testOutput.String(),
		}
		return run{}, failure, errors.New(failure.Error)
	}
	measurement.WallSeconds = time.Since(started).Seconds()
	return measurement, nil, nil
}

type testID struct {
	Package string
	Test    string
}

func recordTestEvent(measurement run, outputs map[testID]string, failed map[testID]bool, event testEvent) {
	id := testID{Package: event.Package, Test: event.Test}
	switch event.Action {
	case "output":
		outputs[id] += event.Output
	case "fail":
		failed[id] = true
	case "pass":
		if event.Test == "" {
			measurement.Packages[event.Package] = event.Elapsed
			return
		}
		measurement.Tests[event.Package+"."+event.Test] = event.Elapsed
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

func testCommandError(decodeErr, waitErr error) string {
	if decodeErr != nil && waitErr != nil {
		return fmt.Sprintf("decode go test JSON: %v (test command: %v)", decodeErr, waitErr)
	}
	if decodeErr != nil {
		return fmt.Sprintf("decode go test JSON: %v", decodeErr)
	}
	return waitErr.Error()
}

func summarize(runs []run) summary {
	tests := summarizeDurations(runs, func(r run) map[string]float64 { return r.Tests })
	return summary{
		WallSeconds: median(wallTimes(runs)),
		TestCount:   len(tests),
		Packages:    summarizeDurations(runs, func(r run) map[string]float64 { return r.Packages }),
		Tests:       tests,
	}
}

func wallTimes(runs []run) []float64 {
	values := make([]float64, 0, len(runs))
	for _, run := range runs {
		values = append(values, run.WallSeconds)
	}
	return values
}

func summarizeDurations(runs []run, values func(run) map[string]float64) []duration {
	byName := make(map[string][]float64)
	for _, run := range runs {
		for name, seconds := range values(run) {
			byName[name] = append(byName[name], seconds)
		}
	}
	result := make([]duration, 0, len(byName))
	for name, observations := range byName {
		result = append(result, duration{Name: name, Seconds: median(observations), Samples: len(observations)})
	}
	sortDurations(result)
	return result
}

func median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]float64(nil), values...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	if len(ordered)%2 == 1 {
		return ordered[middle]
	}
	return (ordered[middle-1] + ordered[middle]) / 2
}

func sortDurations(values []duration) {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Seconds == values[j].Seconds {
			return values[i].Name < values[j].Name
		}
		return values[i].Seconds > values[j].Seconds
	})
}

func currentEnvironment() environment {
	return environment{
		GoVersion:  runtime.Version(),
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		CPUCount:   runtime.NumCPU(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		CPUModel:   cpuModel(),
	}
}

func cpuModel() string {
	contents, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(contents), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && (strings.TrimSpace(key) == "model name" || strings.TrimSpace(key) == "Hardware") {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func writeJSON(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(contents, '\n'), 0o644)
}

func loadBaseline(path string) (baseline, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return baseline{}, err
	}
	var loaded baseline
	if err := json.Unmarshal(contents, &loaded); err != nil {
		return baseline{}, err
	}
	if loaded.SchemaVersion != baselineSchemaVersion {
		return baseline{}, fmt.Errorf("unsupported baseline schema version %d", loaded.SchemaVersion)
	}
	if loaded.Lanes == nil {
		return baseline{}, errors.New("baseline has no lanes")
	}
	return loaded, nil
}

func validateCompleteReport(report report) error {
	if !report.Complete || report.Failure != nil || len(report.Runs) != report.Samples {
		return errors.New("cannot update or check a baseline from an incomplete or failed sample set")
	}
	return nil
}

func initialBaseline(report report) baseline {
	policy := defaultPolicy()
	lane := baselineLane(report, policy)
	return baseline{SchemaVersion: baselineSchemaVersion, Policy: policy, Lanes: map[string]laneBaseline{report.Lane: lane}}
}

func defaultPolicy() budgetPolicy {
	return budgetPolicy{
		PackageRelativeTolerance: 0.50,
		PackageAbsoluteSeconds:   0.25,
		SuiteRelativeTolerance:   0.25,
		SuiteAbsoluteSeconds:     0.50,
	}
}

func baselineLane(report report, policy budgetPolicy) laneBaseline {
	return laneBaseline{
		RecordedAt:  report.CreatedAt,
		Environment: report.Environment,
		Median: summary{
			WallSeconds: report.Median.WallSeconds,
			TestCount:   report.Median.TestCount,
			Packages:    report.Median.Packages,
		},
		SuiteBudget: budget(report.Median.WallSeconds, policy.SuiteRelativeTolerance, policy.SuiteAbsoluteSeconds),
		Budgets:     budgetsFor(report.Median.Packages, policy),
	}
}

func budgetsFor(values []duration, policy budgetPolicy) []duration {
	result := make([]duration, len(values))
	for i, value := range values {
		result[i] = duration{Name: value.Name, Seconds: budget(value.Seconds, policy.PackageRelativeTolerance, policy.PackageAbsoluteSeconds), Samples: value.Samples}
	}
	return result
}

func budget(value, relative, absolute float64) float64 {
	return math.Max(value*(1+relative), value+absolute)
}

// update only records lower medians. This prevents a slow host or regression from
// loosening a tracked budget while allowing optimization work to ratchet it down.
func (b *baseline) update(report report) bool {
	existing, ok := b.Lanes[report.Lane]
	if !ok {
		b.Lanes[report.Lane] = baselineLane(report, b.Policy)
		return true
	}
	changed := false
	if report.Median.WallSeconds < existing.Median.WallSeconds {
		existing.Median.WallSeconds = report.Median.WallSeconds
		existing.SuiteBudget = budget(report.Median.WallSeconds, b.Policy.SuiteRelativeTolerance, b.Policy.SuiteAbsoluteSeconds)
		changed = true
	}
	existing.Median.Packages, existing.Budgets, changed = updatePackageBaselines(existing.Median.Packages, existing.Budgets, report.Median.Packages, b.Policy, changed)
	if changed {
		existing.RecordedAt = report.CreatedAt
		existing.Environment = report.Environment
		b.Lanes[report.Lane] = existing
	}
	return changed
}

func updatePackageBaselines(existing, budgets, observed []duration, policy budgetPolicy, changed bool) ([]duration, []duration, bool) {
	current := make(map[string]duration, len(existing))
	for _, value := range existing {
		current[value.Name] = value
	}
	currentBudgets := make(map[string]duration, len(budgets))
	for _, value := range budgets {
		currentBudgets[value.Name] = value
	}
	for _, value := range observed {
		prior, found := current[value.Name]
		if !found || value.Seconds < prior.Seconds {
			current[value.Name] = value
			currentBudgets[value.Name] = duration{Name: value.Name, Seconds: budget(value.Seconds, policy.PackageRelativeTolerance, policy.PackageAbsoluteSeconds), Samples: value.Samples}
			changed = true
		}
	}
	return durationsFromMap(current), durationsFromMap(currentBudgets), changed
}

func durationsFromMap(values map[string]duration) []duration {
	result := make([]duration, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sortDurations(result)
	return result
}

func (b baseline) check(report report) []string {
	lane, ok := b.Lanes[report.Lane]
	if !ok {
		return []string{fmt.Sprintf("baseline has no %s lane", report.Lane)}
	}
	var failures []string
	if report.Median.TestCount != lane.Median.TestCount {
		failures = append(failures, fmt.Sprintf("test event count %d differs from baseline %d", report.Median.TestCount, lane.Median.TestCount))
	}
	if report.Median.WallSeconds > lane.SuiteBudget {
		failures = append(failures, fmt.Sprintf("suite median %.2fs exceeds %.2fs", report.Median.WallSeconds, lane.SuiteBudget))
	}
	budgets := make(map[string]duration, len(lane.Budgets))
	for _, value := range lane.Budgets {
		budgets[value.Name] = value
	}
	for _, value := range report.Median.Packages {
		limit, found := budgets[value.Name]
		if !found {
			failures = append(failures, fmt.Sprintf("package %s has no recorded budget", value.Name))
			continue
		}
		if value.Seconds > limit.Seconds {
			failures = append(failures, fmt.Sprintf("package %s median %.2fs exceeds %.2fs", value.Name, value.Seconds, limit.Seconds))
		}
	}
	return failures
}

func printReport(report report, output string) {
	fmt.Printf("Environment: %s %s/%s, %d CPUs, GOMAXPROCS=%d", report.Environment.GoVersion, report.Environment.GOOS, report.Environment.GOARCH, report.Environment.CPUCount, report.Environment.GOMAXPROCS)
	if report.Environment.CPUModel != "" {
		fmt.Printf(", %s", report.Environment.CPUModel)
	}
	fmt.Println()
	if !report.Complete {
		fmt.Printf("Incomplete sample set: %d/%d samples completed.\n", len(report.Runs), report.Samples)
		if report.Failure != nil {
			fmt.Printf("Failed sample %d: %s\n", report.Failure.Sample, report.Failure.Error)
			for _, failure := range report.Failure.Failures {
				name := failure.Package
				if failure.Test != "" {
					name += "." + failure.Test
				}
				fmt.Printf("  failed: %s\n", name)
			}
		}
		fmt.Printf("JSON report: %s\n", output)
		return
	}
	fmt.Printf("Median wall time: %.2fs (%d test events across %d samples)\n", report.Median.WallSeconds, report.Median.TestCount, report.Samples)
	printDurations("Slow packages", report.Median.Packages)
	printDurations("Slow tests and subtests", report.Median.Tests)
	fmt.Printf("JSON report: %s\n", output)
}

func printDurations(label string, values []duration) {
	fmt.Println(label + ":")
	if len(values) == 0 {
		fmt.Println("  (no timings reported)")
		return
	}
	limit := len(values)
	if limit > 10 {
		limit = 10
	}
	for _, value := range values[:limit] {
		fmt.Printf("  %7.2fs  %s\n", value.Seconds, value.Name)
	}
}
