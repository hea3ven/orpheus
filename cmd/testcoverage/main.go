// Command testcoverage runs the classified test lanes once and evaluates quality budgets.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const reportSchemaVersion = 1

type options struct {
	policy         string
	output         string
	updatePolicy   bool
	auditScenarios bool
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
	flags.BoolVar(&opts.updatePolicy, "update-policy", false, "update materially stale policy bounds from repeated measurements")
	flags.BoolVar(&opts.auditScenarios, "audit-scenarios", false, "profile every integration scenario separately (expensive)")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("testcoverage does not accept positional arguments")
	}
	if opts.updatePolicy && opts.auditScenarios {
		return options{}, errors.New("update-policy and audit-scenarios cannot be combined")
	}
	return opts, nil
}

func execute(opts options) error {
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
	result, laneErrors := collectQuality(work, opts.auditScenarios)
	result.MeasurementSamples = 1
	if len(laneErrors) > 0 {
		result.Decision = decision{Status: statusTestFailed, Findings: errorFindings(laneErrors)}
		return finishReport(opts.output, result, errors.Join(laneErrors...))
	}
	result.Decision = assessQualityPolicy(policy, result)
	if result.Decision.Status == statusPass {
		return finishReport(opts.output, result, nil)
	}
	return finishReport(opts.output, result, fmt.Errorf("%s against %s", result.Decision.Status, opts.policy))
}

func executePolicyUpdate(opts options, policy localQualityPolicy, work string) error {
	samples := make([]qualityReport, 0, policy.Timing.UpdateSamples)
	for index := 0; index < policy.Timing.UpdateSamples; index++ {
		fmt.Printf("Collecting quality policy sample %d/%d.\n", index+1, policy.Timing.UpdateSamples)
		result, laneErrors := collectQuality(filepath.Join(work, fmt.Sprintf("sample-%d", index+1)), false)
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
		representative.Decision = decision{Status: statusTestFailed, Findings: []finding{{Kind: "measurement", Message: err.Error()}}}
		return finishReport(opts.output, representative, fmt.Errorf("update quality policy: %w", err))
	}
	representative.Decision = decision{Status: statusPass, Findings: changes}
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

func collectQuality(work string, auditScenarios bool) (qualityReport, []error) {
	result := qualityReport{SchemaVersion: reportSchemaVersion, Lanes: make(map[string]laneReport, 2)}
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
	if result.Complete && auditScenarios {
		scenarios, err := auditIntegrationScenarios(work, detailed["integration"])
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
