// Command testcoverage runs the classified test lanes once and evaluates quality budgets.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

const (
	baselineSchemaVersion = 3
	reportSchemaVersion   = 1
)

type options struct {
	baseline       string
	output         string
	writeBaseline  bool
	forceBaseline  bool
	compareTo      string
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
	flags.StringVar(&opts.baseline, "baseline", "coverage/test-coverage-baseline.json", "tracked aggregate quality baseline")
	flags.StringVar(&opts.output, "output", "artifacts/test-coverage/report.json", "machine-readable quality report path")
	flags.BoolVar(&opts.writeBaseline, "write-baseline", false, "generate eligible aggregate baseline updates")
	flags.BoolVar(&opts.forceBaseline, "force-baseline", false, "generate a replacement baseline after an explicit maintainer override")
	flags.StringVar(&opts.compareTo, "compare-to", "", "trusted prior baseline (normally the base branch baseline)")
	flags.BoolVar(&opts.auditScenarios, "audit-scenarios", false, "profile every integration scenario separately (expensive)")
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	if flags.NArg() != 0 {
		return options{}, errors.New("testcoverage does not accept positional arguments")
	}
	if opts.writeBaseline && opts.compareTo != "" {
		return options{}, errors.New("write-baseline and compare-to cannot be combined")
	}
	if opts.forceBaseline && !opts.writeBaseline {
		return options{}, errors.New("force-baseline requires write-baseline")
	}
	return opts, nil
}

func execute(opts options) error {
	work, err := os.MkdirTemp("", "orpheus-quality-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()

	result, laneErrors := collectQuality(work, opts.auditScenarios)
	if len(laneErrors) > 0 {
		result.Decision = decision{Status: statusTestFailed, Findings: errorFindings(laneErrors)}
		return finishReport(opts.output, result, errors.Join(laneErrors...))
	}
	return evaluateQuality(opts, result)
}

func collectQuality(work string, auditScenarios bool) (qualityReport, []error) {
	result := qualityReport{SchemaVersion: reportSchemaVersion, Lanes: make(map[string]laneReport, 2)}
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

func evaluateQuality(opts options, result qualityReport) error {
	current := baselineFromReport(result, defaultPolicy())
	tracked, trackedErr := loadBaseline(opts.baseline)
	if opts.writeBaseline {
		return writeBaseline(opts, result, current, tracked, trackedErr)
	}
	if trackedErr != nil {
		result.Decision = decision{Status: statusRefreshRequired, Findings: []finding{{Kind: "baseline", Message: fmt.Sprintf("read tracked baseline: %v", trackedErr)}}}
		return finishReport(opts.output, result, fmt.Errorf("read tracked baseline: %w (run make coverage-baseline)", trackedErr))
	}

	reference, referencePath := tracked, opts.baseline
	if opts.compareTo != "" {
		var err error
		reference, err = loadBaseline(opts.compareTo)
		if err != nil {
			result.Decision = decision{Status: statusRegression, Findings: []finding{{Kind: "baseline", Message: fmt.Sprintf("read trusted baseline: %v", err)}}}
			return finishReport(opts.output, result, fmt.Errorf("read trusted baseline: %w", err))
		}
		referencePath = opts.compareTo
		if reference.Legacy {
			reference = legacyBaselineWithTiming(reference, tracked)
		}
	}

	assessment := assess(reference, result)
	if opts.compareTo != "" {
		assessment = verifyTrackedBaseline(reference, tracked, result, assessment)
	}
	result.Decision = assessment
	if assessment.Status == statusPass {
		return finishReport(opts.output, result, nil)
	}
	return finishReport(opts.output, result, fmt.Errorf("%s against %s", assessment.Status, referencePath))
}

func writeBaseline(opts options, result qualityReport, current, tracked baseline, trackedErr error) error {
	if opts.forceBaseline {
		if trackedErr == nil {
			current = baselineFromReport(result, tracked.Policy)
		}
		return writeGeneratedBaseline(opts, result, current, "generated aggregate baseline after explicit maintainer override", nil)
	}
	if trackedErr != nil && !errors.Is(trackedErr, os.ErrNotExist) && !errors.Is(trackedErr, errUnsupportedBaseline) {
		result.Decision = decision{Status: statusRefreshRequired, Findings: []finding{{Kind: "baseline", Message: trackedErr.Error()}}}
		return finishReport(opts.output, result, fmt.Errorf("read tracked baseline: %w", trackedErr))
	}
	candidate := current
	var warnings []finding
	if trackedErr == nil {
		reference := tracked
		if tracked.Legacy {
			reference = legacyBaselineWithTiming(tracked, current)
		}
		assessment := assess(reference, result)
		if assessment.Status == statusRegression || assessment.Status == statusTimingFailed {
			result.Decision = assessment
			return finishReport(opts.output, result, errors.New("baseline generation refused a coverage regression or timing budget failure"))
		}
		warnings = assessment.Warnings
		candidate = generatedBaseline(result, reference, tracked.Policy)
	}
	return writeGeneratedBaseline(opts, result, candidate, "generated aggregate baseline", warnings)
}

func writeGeneratedBaseline(opts options, result qualityReport, candidate baseline, message string, warnings []finding) error {
	if err := writeJSON(opts.baseline, candidate); err != nil {
		return fmt.Errorf("write baseline: %w", err)
	}
	result.Decision = decision{Status: statusPass, Findings: []finding{{Kind: "baseline", Message: message}}, Warnings: warnings}
	if err := writeQualityReport(opts.output, result); err != nil {
		return err
	}
	printReport(result, opts.output)
	fmt.Printf("Wrote aggregate quality baseline to %s.\n", opts.baseline)
	return nil
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
