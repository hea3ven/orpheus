// Command testcoverage runs the classified test lanes and evaluates coverage and same-runner timing.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hea3ven/orpheus/internal/testlane"
)

const reportSchemaVersion = 1

type options struct {
	policy          string
	output          string
	updatePolicy    bool
	auditScenarios  bool
	auditPackages   stringListFlag
	auditRun        string
	compareBase     string
	compareHead     string
	compareMerge    string
	compareWorktree bool
	measureRevision string
}

func (opts options) auditSelection() scenarioSelection {
	packages := append([]string(nil), opts.auditPackages...)
	if len(packages) == 0 {
		packages = []string{"./..."}
	}
	pattern := opts.auditRun
	if pattern == "" {
		pattern = testlane.IntegrationTestPattern
	}
	return scenarioSelection{Packages: packages, TestPattern: pattern}
}

// stringListFlag collects a repeatable command-line option.
type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }

func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("value must not be empty")
	}
	*values = append(*values, value)
	return nil
}

func main() {
	opts, err := parseOptions(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := execute(opts); err != nil {
		fmt.Fprintln(os.Stderr, "quality report:", err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (options, error) {
	opts := options{}
	flags := flag.NewFlagSet("testcoverage", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&opts.policy, "policy", ".quality.yml", "reviewed local quality policy")
	flags.StringVar(&opts.output, "output", "artifacts/test-coverage/report.json", "machine-readable quality report path")
	flags.BoolVar(&opts.updatePolicy, "update-policy", false, "update materially stale coverage floors from five measurements")
	flags.BoolVar(&opts.auditScenarios, "audit-scenarios", false, "profile integration scenarios separately (expensive)")
	flags.Var(&opts.auditPackages, "audit-package", "package pattern to audit; repeat to select more than one (default ./...)")
	flags.StringVar(&opts.auditRun, "audit-run", "", "integration test-name regular expression to audit (default ^TestIntegration)")
	flags.StringVar(&opts.compareBase, "compare-base", "", "explicit local base revision (requires compare-worktree)")
	flags.StringVar(&opts.compareHead, "compare-head", "", "exact expected PR head, the tested merge's second parent")
	flags.StringVar(&opts.compareMerge, "compare-merge", "", "exact tested synthetic merge commit; its first parent supplies the base")
	flags.BoolVar(&opts.compareWorktree, "compare-worktree", false, "compare an immutable on-disk snapshot against compare-base")
	flags.StringVar(&opts.measureRevision, "measure-revision", "", "collect without policy; verify this exact checkout identity")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("testcoverage does not accept positional arguments")
	}
	if err := opts.validate(); err != nil {
		return options{}, err
	}
	if opts.compareWorktree {
		outputSet := false
		flags.Visit(func(option *flag.Flag) { outputSet = outputSet || option.Name == "output" })
		if !outputSet {
			opts.output = ""
		}
	}
	return opts, nil
}

func (opts options) validate() error {
	flagsCompare := opts.compareWorktree || opts.compareBase != "" || opts.compareHead != "" || opts.compareMerge != ""
	if opts.compareWorktree {
		if opts.compareBase == "" || opts.compareHead != "" || opts.compareMerge != "" || opts.policy != ".quality.yml" {
			return errors.New("compare-worktree requires compare-base, uses snapshot .quality.yml, and cannot use compare-head or compare-merge")
		}
	} else if flagsCompare && (opts.compareHead == "" || opts.compareMerge == "" || opts.compareBase != "") {
		return errors.New("CI comparison requires compare-head and compare-merge; compare-base is only for compare-worktree")
	}
	modes := 0
	for _, enabled := range []bool{opts.updatePolicy, opts.auditScenarios, flagsCompare, opts.measureRevision != ""} {
		if enabled {
			modes++
		}
	}
	if modes > 1 {
		return errors.New("quality modes cannot be combined")
	}
	if !opts.auditScenarios && (len(opts.auditPackages) > 0 || opts.auditRun != "") {
		return errors.New("audit-package and audit-run require audit-scenarios")
	}
	return nil
}

func execute(opts options) error {
	if opts.measureRevision != "" {
		return executeMeasurement(opts)
	}
	if opts.compareWorktree || opts.compareMerge != "" {
		return executeComparison(opts)
	}
	policy, err := loadQualityPolicy(opts.policy)
	if err != nil {
		return fmt.Errorf("read quality policy %s: %w", opts.policy, err)
	}
	work, err := os.MkdirTemp("", "orpheus-quality-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	if opts.updatePolicy {
		return executePolicyUpdate(opts, policy, work)
	}
	var audit *scenarioSelection
	if opts.auditScenarios {
		selection := opts.auditSelection()
		audit = &selection
	}
	result, laneErrors := collectQuality(work, audit)
	result.MeasurementSamples = 1
	result.Timing = decision{Status: statusNotEvaluated}
	result.Execution = decision{Status: statusPass}
	if len(laneErrors) > 0 {
		result.Execution = decision{Status: statusTestFailed, Findings: errorFindings(laneErrors)}
		result.Decision = result.Execution
		return finishReport(opts.output, result, errors.Join(laneErrors...))
	}
	result.CoverageDecision = assessQualityPolicy(policy, result)
	result.Decision = result.CoverageDecision
	if result.Decision.Status == statusPass {
		return finishReport(opts.output, result, nil)
	}
	return finishReport(opts.output, result, fmt.Errorf("%s against %s", result.Decision.Status, opts.policy))
}

func executePolicyUpdate(opts options, policy localQualityPolicy, work string) error {
	samples := make([]qualityReport, 0, policy.UpdateSamples)
	for index := 0; index < policy.UpdateSamples; index++ {
		fmt.Printf("Collecting quality policy sample %d/%d.\n", index+1, policy.UpdateSamples)
		result, laneErrors := collectQuality(filepath.Join(work, fmt.Sprintf("sample-%d", index+1)), nil)
		result.MeasurementSamples = index + 1
		if len(laneErrors) > 0 {
			result.Decision = decision{Status: statusTestFailed, Findings: errorFindings(laneErrors)}
			return finishReport(opts.output, result, fmt.Errorf("quality policy sample %d failed: %w", index+1, errors.Join(laneErrors...)))
		}
		samples = append(samples, result)
	}
	representative, changes, err := applyQualityPolicyUpdate(opts.policy, policy, samples)
	if err != nil {
		if len(representative.Lanes) == 0 {
			representative = samples[len(samples)-1]
			representative.Complete = false
		}
		representative.Execution = decision{Status: statusExecutionFailed}
		representative.Decision = decision{Status: statusExecutionFailed, Findings: []finding{{Kind: "measurement", Message: err.Error()}}}
		return finishReport(opts.output, representative, fmt.Errorf("update quality policy: %w", err))
	}
	representative.Timing = decision{Status: statusNotEvaluated}
	representative.Execution = decision{Status: statusPass}
	representative.CoverageDecision = decision{Status: statusPass, Findings: changes}
	representative.Decision = representative.CoverageDecision
	if err := writeQualityReport(opts.output, representative); err != nil {
		return err
	}
	printReport(representative, opts.output)
	if len(changes) == 0 {
		fmt.Printf("Quality policy is current; left %s unchanged.\n", opts.policy)
		return nil
	}
	fmt.Printf("Updated %d quality policy bound(s) in %s.\n", len(changes), opts.policy)
	return nil
}

func collectQuality(work string, audit *scenarioSelection) (qualityReport, []error) {
	result := qualityReport{SchemaVersion: reportSchemaVersion, Lanes: make(map[string]laneReport, 2), Execution: decision{Status: statusNotEvaluated}, CoverageDecision: decision{Status: statusNotEvaluated}, Timing: decision{Status: statusNotEvaluated}}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return result, []error{fmt.Errorf("create measurement directory: %w", err)}
	}
	var laneErrors []error
	detailed := make(map[string]laneCoverage, 2)
	for _, name := range laneNames {
		lane, coverage, err := collectLane(name, filepath.Join(work, name+".cover"))
		result.Lanes[name] = lane
		if coverage.StatementTotal > 0 {
			detailed[name] = coverage
		}
		if err != nil {
			laneErrors = append(laneErrors, fmt.Errorf("%s lane: %w", name, err))
		}
	}
	result.Complete = len(laneErrors) == 0
	result.Execution = decision{Status: statusPass}
	if !result.Complete {
		result.Execution = decision{Status: statusTestFailed, Findings: errorFindings(laneErrors)}
	}
	if result.Complete && audit != nil {
		scenarios, err := auditIntegrationScenarios(work, detailed["integration"], *audit)
		if err != nil {
			laneErrors = append(laneErrors, fmt.Errorf("audit integration scenarios: %w", err))
			result.Complete = false
		} else {
			result.Scenarios = scenarios
		}
	}
	return result, laneErrors
}

func finishReport(path string, result qualityReport, resultErr error) error {
	if err := writeQualityReport(path, result); err != nil {
		if resultErr != nil {
			return errors.Join(resultErr, err)
		}
		return err
	}
	printReport(result, path)
	return resultErr
}

func writeQualityReport(path string, result qualityReport) error {
	if err := writeJSON(path, result); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	if err := writeReportSummary(path, result); err != nil {
		return fmt.Errorf("write report summary: %w", err)
	}
	return nil
}

func errorFindings(errs []error) []finding {
	result := make([]finding, 0, len(errs))
	for _, err := range errs {
		result = append(result, finding{Kind: "test", Message: err.Error()})
	}
	return result
}
