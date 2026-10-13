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
	return writeReportFile(path, []byte(contents.String()))
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

	_, _ = fmt.Fprintln(output, "| Check | Result |\n| --- | --- |")
	_, _ = fmt.Fprintf(output, "| Tests/execution | %s |\n| Coverage | %s |\n| Timing | %s |\n\n", checkLabel(result.Execution), checkLabel(result.CoverageDecision), checkLabel(result.Timing))
	renderComparisonSummary(output, result)
	_, _ = fmt.Fprint(output, "### Lane results\n\n")
	_, _ = fmt.Fprintln(output, "| Lane | Result | Coverage | Test events | Selected package work | Command wall time |")
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
			laneSelectedTestSeconds(lane),
			lane.WallSeconds,
		)
	}

	_, _ = fmt.Fprintln(output, "\nCommand wall time measures developer wait and is diagnostic only. Selected package work sums elapsed times for packages that ran tests, including overlapping packages; CI compares this work against the same-runner base; local timing is diagnostic only.")

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
		return "TIP", "Quality checks passed", "All blocking checks passed. Independent results are listed below."
	case statusPolicyUpdateRequired:
		return "WARNING", "Quality policy update required", "Run `make quality-policy-update` and review the `.quality.yml` diff."
	case statusRegression:
		return "CAUTION", "Coverage floor violated", "A lane or package fell below its accepted coverage floor."
	case statusTimingFailed:
		return "CAUTION", "Timing ceiling exceeded", "Five-sample medians confirm a regression against the same-runner base."
	case statusExecutionFailed:
		return "CAUTION", "Execution failed", "Inspect the retained command and measurement diagnostics."
	case statusUnevaluated:
		return "CAUTION", "Timing unevaluated", "Missing, failed, incomplete, or incompatible evidence blocks CI; no regression is claimed."
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
	_, _ = fmt.Fprintln(output, "| Lane | Package | Coverage | Selected package work |")
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

func renderComparisonSummary(output io.Writer, result qualityReport) {
	evidence := result.Comparison
	if evidence == nil {
		return
	}
	_, _ = fmt.Fprintf(output, "### Same-runner comparison\n\nBase: `%s`  \nSynthetic merge: `%s`  \nMeasured pairs: %d (one excluded warm-up per revision).\n\n", evidence.BaseRevision, evidence.MergeRevision, result.MeasurementSamples)
	renderSampleReferences(output, evidence.Samples)
	_, _ = fmt.Fprintln(output, "| Lane/scope | Base median | Merge median | Ceiling |\n| --- | ---: | ---: | ---: |")
	for _, name := range laneNames {
		base, baseOK := evidence.BaseMedians[name]
		merge, mergeOK := evidence.MergeMedians[name]
		if !baseOK || !mergeOK {
			continue
		}
		_, _ = fmt.Fprintf(output, "| %s/suite | %.3fs | %.3fs | %.3fs |\n", name, laneSelectedTestSeconds(base), laneSelectedTestSeconds(merge), timingCeiling(laneSelectedTestSeconds(base), evidence.Settings.Suite))
		prior, candidate := timingMap(base.Timings), timingMap(merge.Timings)
		for _, pkg := range sortedUnionKeys(prior, candidate) {
			old, inBase := prior[pkg]
			current, inMerge := candidate[pkg]
			if inBase && inMerge {
				_, _ = fmt.Fprintf(output, "| %s/%s | %.3fs | %.3fs | %.3fs |\n", name, markdownEscape(pkg), old, current, timingCeiling(old, evidence.Settings.Package))
			}
		}
	}
	_, _ = fmt.Fprintln(output)
}

func renderSampleReferences(output io.Writer, samples []sampleReference) {
	_, _ = fmt.Fprint(output, "<details>\n<summary>Collected sample evidence</summary>\n\n")
	_, _ = fmt.Fprintln(output, "| Revision | Excluded warm-up | Report |\n| --- | --- | --- |")
	for _, sample := range samples {
		_, _ = fmt.Fprintf(output, "| `%s` | %t | `%s` |\n", sample.Revision, sample.Warmup, markdownEscape(sample.Path))
	}
	_, _ = fmt.Fprint(output, "\n</details>\n\n")
}
