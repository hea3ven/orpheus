package main

import (
	"fmt"
	"io"
	"os"
)

func printReport(result qualityReport, output string) {
	printReportTo(os.Stdout, result, output)
}

func printReportTo(outputWriter io.Writer, result qualityReport, output string) {
	for _, name := range laneNames {
		lane := result.Lanes[name]
		_, _ = fmt.Fprintf(
			outputWriter,
			"%s: %d/%d statements (%.2f%%), %d test events, %.2fs selected tests (%.2fs wall)\n",
			name,
			lane.Coverage.CoveredStatements,
			lane.Coverage.StatementTotal,
			percentage(lane.Coverage),
			lane.TestCount,
			laneSelectedTestSeconds(lane),
			lane.WallSeconds,
		)
		printPackageSummary(outputWriter, lane)
		if lane.Failure != nil {
			_, _ = fmt.Fprintf(outputWriter, "  failed: %s (%d decoded failures)\n", lane.Failure.Error, len(lane.Failure.Failures))
		}
	}
	_, _ = fmt.Fprintf(outputWriter, "quality decision: %s\n", result.Decision.Status)
	for _, item := range result.Decision.Findings {
		_, _ = fmt.Fprintf(outputWriter, "  - %s%s\n", findingLocation(item), findingMessage(item))
	}
	for _, warning := range result.Decision.Warnings {
		_, _ = fmt.Fprintf(outputWriter, "  - warning (non-blocking): %s%s\n", findingLocation(warning), findingMessage(warning))
	}
	for _, scenario := range result.Scenarios {
		_, _ = fmt.Fprintf(outputWriter, "scenario %s: %.2fs, containment %.2f%%, similarity %.2f%%, %d exclusive statements\n", scenario.Name, scenario.RuntimeSeconds, scenario.ContainmentPercentage, scenario.SimilarityPercentage, scenario.ExclusiveStatements)
	}
	_, _ = fmt.Fprintf(outputWriter, "Quality report: %s\n", output)
}

func printPackageSummary(output io.Writer, lane laneReport) {
	timings := timingMap(lane.Timings)
	for _, pkg := range lane.Packages {
		timing, measured := timings[pkg.Name]
		if !measured {
			_, _ = fmt.Fprintf(output, "  %s: %d/%d statements (%.2f%%), no selected-test timing\n", pkg.Name, pkg.CoveredStatements, pkg.StatementTotal, percentage(pkg.coverageMetric))
			continue
		}
		_, _ = fmt.Fprintf(output, "  %s: %d/%d statements (%.2f%%), %.2fs\n", pkg.Name, pkg.CoveredStatements, pkg.StatementTotal, percentage(pkg.coverageMetric), timing)
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
	if item.Name != "" {
		if location != "" {
			location += "/"
		}
		location += item.Name
	} else if item.Scope != "" {
		if location != "" {
			location += "/"
		}
		location += item.Scope
	}
	return location
}

func findingMessage(item finding) string {
	switch item.Kind {
	case "coverage_policy":
		if item.Message == "removed obsolete bound" {
			return fmt.Sprintf("%s (floor %.3f%%)", item.Message, item.Prior)
		}
		if item.Threshold > 0 {
			return fmt.Sprintf("%s (floor %.3f%%, measured %.3f%%, proposed floor %.3f%%; refresh threshold %.3f percentage points)",
				item.Message, item.Prior, item.Current, item.Proposed, item.Threshold)
		}
		return fmt.Sprintf("%s (floor %.3f%%, measured %.3f%%, proposed floor %.3f%%)", item.Message, item.Prior, item.Current, item.Proposed)
	case "timing_policy":
		if item.Message == "removed obsolete bound" {
			return fmt.Sprintf("%s (ceiling %.3fs)", item.Message, item.Prior)
		}
		return fmt.Sprintf("%s (ceiling %.3fs, measured %.3fs, proposed ceiling %.3fs)", item.Message, item.Prior, item.Current, item.Proposed)
	default:
		return item.Message
	}
}
