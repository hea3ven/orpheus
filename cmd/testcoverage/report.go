package main

import "fmt"

func printReport(result qualityReport, output string) {
	for _, name := range laneNames {
		lane := result.Lanes[name]
		fmt.Printf("%s: %d/%d statements (%.2f%%), %d test events, %.2fs\n", name, lane.Coverage.CoveredStatements, lane.Coverage.StatementTotal, percentage(lane.Coverage), lane.TestCount, lane.WallSeconds)
		if lane.Failure != nil {
			fmt.Printf("  failed: %s (%d decoded failures)\n", lane.Failure.Error, len(lane.Failure.Failures))
		}
	}
	fmt.Printf("quality decision: %s\n", result.Decision.Status)
	for _, item := range result.Decision.Findings {
		location := item.Lane
		if item.Name != "" {
			location += "/" + item.Name
		}
		if location != "" {
			location += ": "
		}
		fmt.Printf("  - %s%s\n", location, findingMessage(item))
	}
	for _, scenario := range result.Scenarios {
		fmt.Printf("scenario %s: %.2fs, containment %.2f%%, similarity %.2f%%, %d exclusive statements\n", scenario.Name, scenario.RuntimeSeconds, scenario.ContainmentPercentage, scenario.SimilarityPercentage, scenario.ExclusiveStatements)
	}
	fmt.Printf("Quality report: %s\n", output)
}

func findingMessage(item finding) string {
	if item.Kind != "timing" || item.Prior <= 0 {
		return item.Message
	}
	baseline := item.Baseline
	if baseline <= 0 {
		baseline = item.Prior
	}
	difference := item.Current - baseline
	percentageDifference := difference * 100 / baseline
	overBudget := item.Current - item.Prior
	return fmt.Sprintf("%s (current %.3fs, baseline %.3fs, difference %+.3fs / %+.1f%%, budget %.3fs, over budget %+.3fs)",
		item.Message, item.Current, baseline, difference, percentageDifference, item.Prior, overBudget)
}
