package main

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const modulePath = "github.com/hea3ven/orpheus"

var relativePackagePattern = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9_.-]*(/[A-Za-z0-9_-][A-Za-z0-9_.-]*)*$`)

func validatePackageExclusions(packages []string) error {
	seen := make(map[string]bool, len(packages))
	for _, name := range packages {
		if !relativePackagePattern.MatchString(name) || strings.HasPrefix(name, modulePath+"/") {
			return fmt.Errorf("package exclusion %q must be an exact module-relative package path", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicate package exclusion %q", name)
		}
		seen[name] = true
	}
	return nil
}

func packageExcluded(packages []string, name string) bool {
	relative, found := strings.CutPrefix(name, modulePath+"/")
	return found && slices.Contains(packages, relative)
}

func guardedCoverage(lane laneReport, exclusions []string) coverageMetric {
	if len(exclusions) == 0 {
		return lane.Coverage
	}
	var result coverageMetric
	for _, pkg := range lane.Packages {
		if !packageExcluded(exclusions, pkg.Name) {
			result.StatementTotal += pkg.StatementTotal
			result.CoveredStatements += pkg.CoveredStatements
		}
	}
	return result
}

func guardedCoverageFloors(floors map[string]float64, exclusions []string) map[string]float64 {
	result := make(map[string]float64, len(floors))
	for name, floor := range floors {
		if !packageExcluded(exclusions, name) {
			result[name] = floor
		}
	}
	return result
}

// withCoverageExclusions annotates policy reports without changing raw sample
// evidence, test selection, or timing inputs.
func withCoverageExclusions(report qualityReport, exclusions []string) qualityReport {
	report.PackageExclusions = slices.Clone(exclusions)
	lanes := make(map[string]laneReport, len(report.Lanes))
	for name, lane := range report.Lanes {
		guarded := guardedCoverage(lane, exclusions)
		lane.GuardedCoverage = &guarded
		lanes[name] = lane
	}
	report.Lanes = lanes
	return report
}
