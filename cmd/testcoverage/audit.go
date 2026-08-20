package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/testlane"
)

type scenarioResult struct {
	Name                  string  `json:"name"`
	RuntimeSeconds        float64 `json:"runtime_seconds"`
	CoveredStatements     int     `json:"covered_statements"`
	ContainmentPercentage float64 `json:"containment_percentage"`
	SimilarityPercentage  float64 `json:"similarity_percentage"`
	ExclusiveStatements   int     `json:"exclusive_statements"`
}

type scenarioTarget struct {
	Package string
	Test    string
}

func (target scenarioTarget) String() string { return target.Package + "/" + target.Test }

func auditIntegrationScenarios(work string, integration laneCoverage) ([]scenarioResult, error) {
	targets, err := integrationScenarioNames()
	if err != nil {
		return nil, err
	}
	profiles, runtimes, err := scenarioProfiles(work, targets)
	if err != nil {
		return nil, err
	}
	all := blocksForLane(integration)
	result := make([]scenarioResult, 0, len(targets))
	for index, target := range targets {
		others := append(append([]map[string]block(nil), profiles[:index]...), profiles[index+1:]...)
		result = append(result, scenarioMetrics(target.String(), runtimes[index], profiles[index], all, others))
	}
	return result, nil
}

func integrationScenarioNames() ([]scenarioTarget, error) {
	list := exec.Command("go", "test", "-json", "-tags="+testlane.IntegrationBuildTag, "-list", testlane.IntegrationTestPattern, "./...")
	list.Env = coverageEnvironment(os.Environ())
	output, err := list.Output()
	if err != nil {
		return nil, fmt.Errorf("list integration scenarios: %w", err)
	}
	return scenarioTargets(bytes.NewReader(output))
}

func scenarioTargets(input io.Reader) ([]scenarioTarget, error) {
	decoder := json.NewDecoder(input)
	var targets []scenarioTarget
	for {
		var event testEvent
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode integration scenario list: %w", err)
		}
		if event.Action != "output" {
			continue
		}
		for _, line := range strings.Split(event.Output, "\n") {
			name := strings.TrimSpace(line)
			if strings.HasPrefix(name, "TestIntegration") {
				targets = append(targets, scenarioTarget{Package: event.Package, Test: name})
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].String() < targets[j].String() })
	return targets, nil
}

func scenarioProfiles(work string, targets []scenarioTarget) ([]map[string]block, []float64, error) {
	profiles := make([]map[string]block, 0, len(targets))
	runtimes := make([]float64, 0, len(targets))
	for index, target := range targets {
		profile := filepath.Join(work, fmt.Sprintf("scenario-%d.cover", index))
		started := time.Now()
		if _, err := runScenario(coverageCommandForPackages("integration", profile, target.Test, []string{target.Package})); err != nil {
			return nil, nil, fmt.Errorf("profile %s: %w", target, err)
		}
		lane, err := normalizeProfile(profile)
		if err != nil {
			return nil, nil, err
		}
		profiles = append(profiles, blocksForLane(lane))
		runtimes = append(runtimes, time.Since(started).Seconds())
	}
	return profiles, runtimes, nil
}

func runScenario(command []string) (laneReport, error) { return runGoTest("integration", command) }

func blocksForLane(lane laneCoverage) map[string]block {
	result := make(map[string]block)
	for _, pkg := range lane.Packages {
		for _, file := range pkg.Files {
			for _, item := range file.Blocks {
				result[file.Name+":"+blockKey(item)] = item
			}
		}
	}
	return result
}

func scenarioMetrics(name string, runtimeSeconds float64, scenario, all map[string]block, integration []map[string]block) scenarioResult {
	covered, contained := coveredAndContained(scenario, all)
	intersection, union, exclusive := scenarioOverlap(scenario, integration)
	return scenarioResult{Name: name, RuntimeSeconds: runtimeSeconds, CoveredStatements: covered, ContainmentPercentage: ratio(contained, covered), SimilarityPercentage: ratio(intersection, union), ExclusiveStatements: exclusive}
}

func coveredAndContained(scenario, all map[string]block) (int, int) {
	covered, contained := 0, 0
	for key, item := range scenario {
		if !item.Covered {
			continue
		}
		covered += item.Statements
		if all[key].Covered {
			contained += item.Statements
		}
	}
	return covered, contained
}

func scenarioOverlap(scenario map[string]block, scenarios []map[string]block) (int, int, int) {
	intersection, union, exclusive := 0, 0, 0
	all := make(map[string]block, len(scenario))
	for key, item := range scenario {
		all[key] = item
		if !item.Covered {
			continue
		}
		if coveredByOther(key, scenarios) {
			intersection += item.Statements
		} else {
			exclusive += item.Statements
		}
	}
	for _, other := range scenarios {
		for key, item := range other {
			current := all[key]
			item.Covered = item.Covered || current.Covered
			all[key] = item
		}
	}
	for _, item := range all {
		if item.Covered {
			union += item.Statements
		}
	}
	return intersection, union, exclusive
}

func coveredByOther(key string, scenarios []map[string]block) bool {
	for _, other := range scenarios {
		if other[key].Covered {
			return true
		}
	}
	return false
}

func ratio(part, total int) float64 {
	if total == 0 {
		return 100
	}
	return float64(part) * 100 / float64(total)
}
