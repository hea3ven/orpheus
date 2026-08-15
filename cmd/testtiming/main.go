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
	reportSchemaVersion   = 1
	baselineSchemaVersion = 1
	defaultSamples        = 5
)

type options struct {
	lane           string
	samples        int
	output         string
	baseline       string
	initBaseline   bool
	updateBaseline bool
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

type summary struct {
	WallSeconds float64    `json:"wall_seconds"`
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
	SchemaVersion int         `json:"schema_version"`
	CreatedAt     time.Time   `json:"created_at"`
	Lane          string      `json:"lane"`
	Samples       int         `json:"samples"`
	Command       []string    `json:"command"`
	Environment   environment `json:"environment"`
	Runs          []run       `json:"runs"`
	Median        summary     `json:"median"`
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
	flags.StringVar(&opts.lane, "lane", "fast", "test lane to measure: fast or integration")
	flags.IntVar(&opts.samples, "samples", defaultSamples, "number of uncached samples")
	flags.StringVar(&opts.output, "output", os.Getenv("TEST_TIMING_OUTPUT"), "JSON report path")
	flags.StringVar(&opts.baseline, "baseline", "performance/test-timing-baseline.json", "tracked JSON budget baseline")
	flags.BoolVar(&opts.initBaseline, "init-baseline", false, "create a missing lane baseline")
	flags.BoolVar(&opts.updateBaseline, "update-baseline", false, "record lower measurements and ratchet budgets down")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("testtiming does not accept positional arguments")
	}
	if opts.lane != "fast" && opts.lane != "integration" {
		return options{}, fmt.Errorf("unknown lane %q (want fast or integration)", opts.lane)
	}
	if opts.samples < 1 {
		return options{}, errors.New("samples must be at least 1")
	}
	if opts.initBaseline && opts.updateBaseline {
		return options{}, errors.New("init-baseline and update-baseline cannot be used together")
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

	report, err := measure(opts)
	if err != nil {
		return err
	}
	if err := writeJSON(opts.output, report); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	printReport(report, opts.output)

	return handleBaseline(opts, report)
}

func handleBaseline(opts options, report report) error {
	loaded, err := loadBaseline(opts.baseline)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read baseline: %w", err)
	}
	switch {
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
		measurement, err := runTests(command)
		if err != nil {
			fmt.Println("failed")
			return report{}, fmt.Errorf("sample %d: %w", i+1, err)
		}
		fmt.Printf("%.2fs\n", measurement.WallSeconds)
		result.Runs = append(result.Runs, measurement)
	}
	result.Median = summarize(result.Runs)
	return result, nil
}

func testCommand(lane string) []string {
	command := []string{"go", "test", "-json", "-count=1"}
	if lane == "integration" {
		command = append(command, "-tags="+testlane.IntegrationBuildTag, "-run", testlane.IntegrationTestPattern)
	}
	return append(command, "./...")
}

func runTests(command []string) (run, error) {
	started := time.Now()
	cmd := exec.Command(command[0], command[1:]...)
	var stderr bytes.Buffer
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return run{}, err
	}
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return run{}, err
	}

	measurement := run{Packages: make(map[string]float64), Tests: make(map[string]float64)}
	var testOutput string
	decoder := json.NewDecoder(stdout)
	for {
		var event testEvent
		err := decoder.Decode(&event)
		if errors.Is(err, os.ErrClosed) {
			break
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if waitErr := cmd.Wait(); waitErr != nil {
				return run{}, fmt.Errorf("decode go test JSON: %w (test command: %w)", err, waitErr)
			}
			return run{}, fmt.Errorf("decode go test JSON: %w", err)
		}
		if event.Action == "output" {
			testOutput = appendTail(testOutput, event.Output, 4096)
			continue
		}
		if event.Action != "pass" {
			continue
		}
		if event.Test == "" {
			measurement.Packages[event.Package] = event.Elapsed
			continue
		}
		measurement.Tests[event.Package+"."+event.Test] = event.Elapsed
	}
	if err := cmd.Wait(); err != nil {
		diagnostics := strings.TrimSpace(stderr.String() + testOutput)
		if diagnostics != "" {
			return run{}, fmt.Errorf("%w: %s", err, diagnostics)
		}
		return run{}, err
	}
	measurement.WallSeconds = time.Since(started).Seconds()
	return measurement, nil
}

func appendTail(current, next string, limit int) string {
	combined := current + next
	if len(combined) <= limit {
		return combined
	}
	return combined[len(combined)-limit:]
}

func summarize(runs []run) summary {
	result := summary{WallSeconds: median(wallTimes(runs)), Packages: summarizeDurations(runs, func(r run) map[string]float64 { return r.Packages }), Tests: summarizeDurations(runs, func(r run) map[string]float64 { return r.Tests })}
	return result
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
	fmt.Printf("Median wall time: %.2fs\n", report.Median.WallSeconds)
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
