//go:build integration

package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
	"gopkg.in/yaml.v3"
)

func qualityWorkflowScripts(t *testing.T) (string, string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "quality-gate.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct{ Name, ID, Run string }
		}
	}
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatal(err)
	}
	var measure, enforce string
	for _, step := range workflow.Jobs["quality"].Steps {
		if step.ID == "quality-report" {
			measure = step.Run
		}
		if step.Name == "Enforce quality gate" {
			enforce = step.Run
		}
	}
	if measure == "" || enforce == "" {
		t.Fatal("quality workflow is missing measure/enforce scripts")
	}
	return measure, enforce
}

func TestIntegrationAdapterContractQualityWorkflowBlocksUnevaluatedAndInconsistentEvidence(t *testing.T) {
	for _, tool := range []string{"timeout", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("workflow process contract requires %s", tool)
		}
	}
	measure, enforce := qualityWorkflowScripts(t)
	for _, test := range []struct {
		name                                  string
		complete                              bool
		execution, coverage, timing, decision string
		collectorExit                         int
		missing                               bool
		wantPass                              bool
	}{
		{name: "all checks pass", complete: true, execution: statusPass, coverage: statusPass, timing: statusPass, decision: statusPass, wantPass: true},
		{name: "local timing cannot pass CI", complete: true, execution: statusPass, coverage: statusPass, timing: statusNotEvaluated, decision: statusPass},
		{name: "missing evidence", missing: true},
		{name: "incomplete evidence", execution: statusPass, coverage: statusPass, timing: statusPass, decision: statusPass},
		{name: "execution not evaluated", complete: true, execution: statusUnevaluated, coverage: statusPass, timing: statusPass, decision: statusPass},
		{name: "coverage regression", complete: true, execution: statusPass, coverage: statusRegression, timing: statusPass, decision: statusRegression},
		{name: "coverage refresh blocks", complete: true, execution: statusPass, coverage: statusPolicyUpdateRequired, timing: statusPass, decision: statusPolicyUpdateRequired},
		{name: "confirmed timing regression", complete: true, execution: statusPass, coverage: statusPass, timing: statusTimingFailed, decision: statusTimingFailed},
		{name: "pass report with failed process", complete: true, execution: statusPass, coverage: statusPass, timing: statusPass, decision: statusPass, collectorExit: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := testutil.CanonicalTempDir(t)
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			collector := filepath.Join(root, "fixture-collector")
			script := "#!/bin/sh\nset -eu\ntest \"$1\" = -compare-base\ntest \"$2\" = \"$QUALITY_BASE\"\ntest \"$3\" = -compare-merge\ntest \"$4\" = \"$QUALITY_MERGE\"\n" +
				"if test \"$QUALITY_FIXTURE_MISSING\" = false; then cat \"$QUALITY_FIXTURE_REPORT\" >artifacts/test-coverage/report.json; fi\nexit \"$QUALITY_FIXTURE_EXIT\"\n"
			if err := os.WriteFile(collector, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			goScript := "#!/bin/sh\nset -eu\ntest \"$1\" = build\ntest \"$2\" = -o\ntest \"$4\" = ./cmd/testcoverage\ncp -f \"$QUALITY_FIXTURE_COLLECTOR\" \"$3\"\n"
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(goScript), 0o755); err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(root, "fixture.json")
			contents, err := json.Marshal(qualityReport{Complete: test.complete, Execution: decision{Status: test.execution}, CoverageDecision: decision{Status: test.coverage}, Timing: decision{Status: test.timing}, Decision: decision{Status: test.decision}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(report, contents, 0o644); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(root, "outputs")
			summary := filepath.Join(root, "summary.md")
			env := append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "RUNNER_TEMP="+root, "GITHUB_OUTPUT="+output, "GITHUB_STEP_SUMMARY="+summary,
				"QUALITY_BASE="+fixtureBase, "QUALITY_MERGE="+fixtureMerge, "QUALITY_FIXTURE_COLLECTOR="+collector, "QUALITY_FIXTURE_REPORT="+report,
				"QUALITY_FIXTURE_MISSING="+strconv.FormatBool(test.missing), "QUALITY_FIXTURE_EXIT="+strconv.Itoa(test.collectorExit))
			cmd := exec.Command("bash", "-c", measure)
			cmd.Dir, cmd.Env = root, env

			commandOutput, measureErr := cmd.CombinedOutput()
			if (measureErr == nil) != test.wantPass {
				t.Fatalf("measurement pass=%t, want %t: %v\n%s", measureErr == nil, test.wantPass, measureErr, commandOutput)
			}
			values, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			outcome := "success"
			if measureErr != nil {
				outcome = "failure"
			}
			env = append(env, "QUALITY_OUTCOME="+outcome, "LINT_OUTCOME=success", "BUILD_OUTCOME=success")
			for _, line := range strings.Fields(string(values)) {
				key, value, _ := strings.Cut(line, "=")
				if key == "decision" {
					env = append(env, "QUALITY_DECISION="+value)
				}
				if key == "exit_code" {
					env = append(env, "QUALITY_EXIT_CODE="+value)
				}
			}
			gate := exec.Command("bash", "-c", enforce)
			gate.Dir, gate.Env = root, env
			commandOutput, enforceErr := gate.CombinedOutput()
			if (enforceErr == nil) != test.wantPass {
				t.Fatalf("gate pass=%t, want %t: %v\n%s", enforceErr == nil, test.wantPass, enforceErr, commandOutput)
			}
		})
	}
}
