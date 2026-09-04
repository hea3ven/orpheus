package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func writeReportSummary(reportPath string, result qualityReport) error {
	path := reportSummaryPath(reportPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var contents strings.Builder
	renderReportSummary(&contents, result, reportPath)
	return os.WriteFile(path, []byte(contents.String()), 0o644)
}

func reportSummaryPath(reportPath string) string {
	extension := filepath.Ext(reportPath)
	base := strings.TrimSuffix(reportPath, extension)
	if strings.EqualFold(extension, ".json") {
		return base + ".md"
	}
	return base + ".summary.md"
}

func renderReportSummary(output io.Writer, result qualityReport, reportPath string) {
	alert, title, description := decisionPresentation(result.Decision.Status)
	_, _ = fmt.Fprintf(output, "## Test quality\n\n> [!%s]\n> **%s.** %s\n\n", alert, title, description)

	_, _ = fmt.Fprint(output, "### Lane results\n\n")
	_, _ = fmt.Fprintln(output, "| Lane | Result | Coverage | Test events | Selected-test time | Wall time |")
	_, _ = fmt.Fprintln(output, "| --- | --- | ---: | ---: | ---: | ---: |")
	for _, name := range laneNames {
		lane := result.Lanes[name]
		laneResult := "Pass"
		if !lane.Passed || lane.Failure != nil {
			laneResult = "Fail"
		}
		_, _ = fmt.Fprintf(
			output,
			"| %s | **%s** | %d/%d (%.2f%%) | %d | %.2fs | %.2fs |\n",
			markdownEscape(name),
			laneResult,
			lane.Coverage.CoveredStatements,
			lane.Coverage.StatementTotal,
			percentage(lane.Coverage),
			lane.TestCount,
			selectedTestSeconds(lane.Timings),
			lane.WallSeconds,
		)
	}

	if len(result.Decision.Findings) > 0 {
		heading := "Blocking issues"
		if result.Decision.Status == statusPass {
			heading = "Notes"
		}
		renderFindingTable(output, heading, result.Decision.Findings)
	}
	if len(result.Decision.Warnings) > 0 {
		renderFindingTable(output, "Warnings", result.Decision.Warnings)
	}

	renderPackageTable(output, result)
	if len(result.Scenarios) > 0 {
		renderScenarioTable(output, result.Scenarios)
	}
	_, _ = fmt.Fprintf(output, "\nMachine-readable report: `%s`\n", markdownEscape(reportPath))
}

func decisionPresentation(status string) (alert, title, description string) {
	switch status {
	case statusPass:
		return "TIP", "Quality checks passed", "No blocking test, coverage, or timing issues were found."
	case statusRefreshRequired:
		return "WARNING", "Baseline refresh required", "Regenerate the tracked quality baseline and include it in this pull request."
	case statusRegression:
		return "CAUTION", "Coverage regression", "Coverage fell beyond the configured significance policy."
	case statusTimingFailed:
		return "CAUTION", "Timing budget exceeded", "A test suite ran past its blocking timing budget."
	case statusTestFailed:
		return "CAUTION", "Tests failed", "At least one test lane did not complete successfully."
	default:
		return "WARNING", "Quality result unavailable", "Inspect the uploaded diagnostics for the execution failure."
	}
}

func renderFindingTable(output io.Writer, heading string, findings []finding) {
	_, _ = fmt.Fprintf(output, "\n### %s\n\n", heading)
	_, _ = fmt.Fprintln(output, "| Location | Issue |")
	_, _ = fmt.Fprintln(output, "| --- | --- |")
	for _, item := range findings {
		location := findingLocationLabel(item)
		if location == "" {
			location = "Quality gate"
		}
		_, _ = fmt.Fprintf(output, "| %s | %s |\n", markdownEscape(location), markdownEscape(findingMessage(item)))
	}
}

func renderPackageTable(output io.Writer, result qualityReport) {
	_, _ = fmt.Fprintln(output, "\n<details>")
	_, _ = fmt.Fprint(output, "<summary>Package coverage and timing</summary>\n\n")
	_, _ = fmt.Fprintln(output, "| Lane | Package | Coverage | Selected-test time |")
	_, _ = fmt.Fprintln(output, "| --- | --- | ---: | ---: |")
	for _, name := range laneNames {
		lane := result.Lanes[name]
		timings := timingMap(lane.Timings)
		for _, pkg := range lane.Packages {
			timing := "Not measured"
			if seconds, measured := timings[pkg.Name]; measured {
				timing = fmt.Sprintf("%.2fs", seconds)
			}
			_, _ = fmt.Fprintf(
				output,
				"| %s | %s | %d/%d (%.2f%%) | %s |\n",
				markdownEscape(name),
				markdownEscape(pkg.Name),
				pkg.CoveredStatements,
				pkg.StatementTotal,
				percentage(pkg.coverageMetric),
				timing,
			)
		}
	}
	_, _ = fmt.Fprintln(output, "\n</details>")
}

func renderScenarioTable(output io.Writer, scenarios []scenarioResult) {
	_, _ = fmt.Fprintln(output, "\n<details>")
	_, _ = fmt.Fprint(output, "<summary>Integration scenario audit</summary>\n\n")
	_, _ = fmt.Fprintln(output, "| Scenario | Runtime | Containment | Similarity | Exclusive statements |")
	_, _ = fmt.Fprintln(output, "| --- | ---: | ---: | ---: | ---: |")
	for _, scenario := range scenarios {
		_, _ = fmt.Fprintf(
			output,
			"| %s | %.2fs | %.2f%% | %.2f%% | %d |\n",
			markdownEscape(scenario.Name),
			scenario.RuntimeSeconds,
			scenario.ContainmentPercentage,
			scenario.SimilarityPercentage,
			scenario.ExclusiveStatements,
		)
	}
	_, _ = fmt.Fprintln(output, "\n</details>")
}

func markdownEscape(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\r\n", "<br>")
	value = strings.ReplaceAll(value, "\n", "<br>")
	return value
}
