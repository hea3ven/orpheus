package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func printReport(result qualityReport, output string) {
	printReportTo(os.Stdout, result, output)
}

func printReportTo(outputWriter io.Writer, result qualityReport, output string) {
	_, verdict := decisionPresentation(result.Decision.Status)
	_, _ = fmt.Fprintf(outputWriter, "%s.\n", verdict)
	_, _ = fmt.Fprintf(outputWriter, "tests: %s; coverage: %s; timing: %s\n", checkLabel(result.Execution), checkLabel(result.CoverageDecision), checkLabel(result.Timing))
	for _, name := range laneNames {
		lane := result.Lanes[name]
		_, _ = fmt.Fprintf(
			outputWriter,
			"%s: %d/%d statements (%.2f%%), %d test events, %.2fs selected package work, %.2fs command wall time\n",
			name,
			lane.Coverage.CoveredStatements,
			lane.Coverage.StatementTotal,
			percentage(lane.Coverage),
			lane.TestCount,
			laneSelectedTestSeconds(lane),
			lane.WallSeconds,
		)
		printPackageSummary(outputWriter, lane)
	}
	if evidence := result.Comparison; evidence != nil {
		_, _ = fmt.Fprintf(outputWriter, "comparison base: %s; candidate: %s; measured pairs: %d\n", evidence.BaseRevision, evidence.MergeRevision, result.MeasurementSamples)
	}
	for _, item := range reportFindings(result) {
		_, _ = fmt.Fprintf(outputWriter, "  - %s%s\n", findingLocation(item), findingMessage(item))
	}
	for _, warning := range result.Decision.Warnings {
		_, _ = fmt.Fprintf(outputWriter, "  - warning (non-blocking): %s%s\n", findingLocation(warning), findingMessage(warning))
	}
	for _, failure := range reportFailures(result) {
		if details := failureDiagnostics(failure.failure); details != "" {
			_, _ = fmt.Fprintf(outputWriter, "%s test diagnostics:\n%s\n", failure.location(), details)
		}
	}
	for _, scenario := range result.Scenarios {
		_, _ = fmt.Fprintf(outputWriter, "scenario %s: %.2fs, containment %.2f%%, similarity %.2f%%, %d exclusive statements\n", packageLabel(scenario.Name), scenario.RuntimeSeconds, scenario.ContainmentPercentage, scenario.SimilarityPercentage, scenario.ExclusiveStatements)
	}
	_, _ = fmt.Fprintf(outputWriter, "Quality report: %s\n", output)
}

func printPackageSummary(output io.Writer, lane laneReport) {
	timings := timingMap(lane.Timings)
	for _, pkg := range lane.Packages {
		timing, measured := timings[pkg.Name]
		if !measured {
			_, _ = fmt.Fprintf(output, "  %s: %d/%d statements (%.2f%%), no selected package work\n", packageLabel(pkg.Name), pkg.CoveredStatements, pkg.StatementTotal, percentage(pkg.coverageMetric))
			continue
		}
		_, _ = fmt.Fprintf(output, "  %s: %d/%d statements (%.2f%%), %.2fs\n", packageLabel(pkg.Name), pkg.CoveredStatements, pkg.StatementTotal, percentage(pkg.coverageMetric), timing)
	}
}

func findingLocation(item finding) string {
	location := findingLocationLabel(item)
	if location != "" {
		return location + ": "
	}
	return ""
}

func findingLocationLabel(item finding) string {
	location := item.Lane
	if item.Kind == "test_failure" {
		return location
	}
	if item.Name != "" {
		if location != "" {
			location += "/"
		}
		location += packageLabel(item.Name)
	} else if item.Scope != "" {
		if location != "" {
			location += "/"
		}
		location += item.Scope
	}
	return location
}

// packageLabel shortens only this module's package and test identities.
// Machine-readable reports and raw diagnostic streams keep their original values.
func packageLabel(name string) string {
	return strings.TrimPrefix(name, "github.com/hea3ven/orpheus/")
}

func findingMessage(item finding) string {
	switch item.Kind {
	case "coverage_policy":
		return coverageFindingMessage(item)
	case "coverage_package":
		if strings.HasPrefix(item.Message, "package was added") {
			return "Coverage package added."
		}
		return "Coverage package removed."
	case "timing_comparison":
		current, ceiling := compactBoundary(item.Current, item.BudgetSeconds, 2)
		return fmt.Sprintf("Timing increased significantly to %ss, max is %ss.", current, ceiling)
	case "test_failure":
		return fmt.Sprintf("Test failed: %s.", packageLabel(item.Name))
	default:
		return item.Message
	}
}

func coverageFindingMessage(item finding) string {
	// Policy-update findings retain the accepted/proposed bounds. Routine
	// assessments instead show only the measurement and its relevant boundary.
	if item.Threshold == 0 {
		if item.Message == "removed obsolete bound" {
			return fmt.Sprintf("%s (floor %.3f%%)", item.Message, item.Prior)
		}
		return fmt.Sprintf("%s (floor %.3f%%, measured %.3f%%, proposed floor %.3f%%)", item.Message, item.Prior, item.Current, item.Proposed)
	}
	if item.Current < item.Prior {
		current, minimum := compactBoundary(item.Current, item.Prior, 1)
		return fmt.Sprintf("Coverage decreased significantly to %s%%, min is %s%%.", current, minimum)
	}
	direction := "improved"
	if item.Proposed < item.Prior {
		direction = "decreased"
	}
	current, refresh := compactBoundary(item.Current, coverageRefreshPercent(item), 1)
	return fmt.Sprintf("Coverage %s significantly to %s%%, refresh at %s%%.", direction, current, refresh)
}

func coverageRefreshPercent(item finding) float64 {
	boundary := item.Prior + item.Threshold + item.headroomPercent
	if item.Proposed < item.Prior {
		boundary = item.Prior - item.Threshold + item.headroomPercent
	}
	// Accepted floors are rounded to three decimals. Only expose that half-step
	// when the measurement crossed it without reaching the nominal boundary.
	if item.Proposed > item.Prior && item.Current < boundary {
		boundary -= 0.0005
	} else if item.Proposed < item.Prior && item.Current > boundary {
		boundary += 0.0005
	}
	return boundary
}

// compactBoundary uses more precision only when rounding would conceal a
// difference between the measurement and the boundary explaining the finding.
func compactBoundary(current, boundary float64, precision int) (string, string) {
	for {
		value := strconv.FormatFloat(current, 'f', precision, 64)
		limit := strconv.FormatFloat(boundary, 'f', precision, 64)
		if current == boundary || value != limit || precision == 17 {
			return value, limit
		}
		precision++
	}
}

func checkLabel(check decision) string {
	switch check.Status {
	case statusPass:
		return "passed"
	case statusRegression, statusTimingFailed, statusExecutionFailed, statusTestFailed:
		return "failed"
	case statusPolicyUpdateRequired:
		return "refresh required"
	case statusUnevaluated:
		return "unevaluated"
	default:
		return "not evaluated"
	}
}
