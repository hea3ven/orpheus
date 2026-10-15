package main

import (
	"fmt"
	"slices"
	"strings"
)

type reportFailure struct {
	subject string
	lane    string
	failure *laneFailure
}

func (failure reportFailure) location() string {
	if failure.subject != "" {
		return failure.subject + "/" + failure.lane
	}
	return failure.lane
}

func reportFailures(result qualityReport) []reportFailure {
	failures := append([]reportFailure(nil), result.sampleFailures...)
	for _, name := range laneNames {
		if failure := result.Lanes[name].Failure; failure != nil {
			failures = append(failures, reportFailure{lane: name, failure: failure})
		}
	}
	return failures
}

// reportFindings builds a human-facing view without changing persisted evidence.
func reportFindings(result qualityReport) []finding {
	var findings []finding
	failures := reportFailures(result)
	for _, item := range result.Decision.Findings {
		if item.Kind == "test" && hasFailureDiagnostics(failures, item.Message) {
			continue
		}
		if item.Kind == "timing_comparison" && result.Timing.Status != statusTimingFailed {
			continue
		}
		if item.Kind == "measurement" && result.Timing.Status == statusUnevaluated && slices.Contains(result.Timing.Findings, item) {
			item.Message = unevaluatedTimingMessage(item.Message)
		}
		findings = append(findings, item)
	}
	for _, evidence := range failures {
		failure := evidence.failure
		named := make(map[string]bool)
		for _, test := range failure.Failures {
			if test.Test != "" {
				named[test.Package] = true
			}
		}
		for _, test := range failure.Failures {
			// Go emits both a named failure and a package failure for it.
			// Keep the package's diagnostic output, not a second conclusion.
			if test.Test == "" && named[test.Package] {
				continue
			}
			identity := test.Package
			if test.Test != "" {
				identity += "/" + test.Test
			}
			findings = append(findings, finding{Kind: "test_failure", Lane: evidence.location(), Name: identity})
		}
		if len(failure.Failures) == 0 {
			findings = append(findings, finding{Kind: "test_failure", Lane: evidence.location(), Name: "lane command"})
		}
	}
	return findings
}

func hasFailureDiagnostics(failures []reportFailure, message string) bool {
	for _, evidence := range failures {
		if message == evidence.lane+" lane: "+evidence.failure.Error {
			return true
		}
	}
	return false
}

func unevaluatedTimingMessage(reason string) string {
	switch reason {
	case "measurement environments are incompatible":
		reason = "measurement environments differ"
	case "measurement configurations are incompatible":
		reason = "measurement configurations differ"
	}
	return "Timing unevaluated: " + strings.TrimSuffix(reason, ".") + "."
}

func failureDiagnostics(failure *laneFailure) string {
	if failure == nil {
		return ""
	}
	var details strings.Builder
	_, _ = fmt.Fprintln(&details, failure.Error)
	hasDecodedOutput := false
	for _, test := range failure.Failures {
		if test.Output == "" {
			continue
		}
		hasDecodedOutput = true
		_, _ = fmt.Fprintln(&details, strings.TrimSpace(test.Output))
	}
	if failure.Stderr != "" {
		_, _ = fmt.Fprintln(&details, strings.TrimSpace(failure.Stderr))
	}
	if !hasDecodedOutput && failure.TestOutput != "" {
		_, _ = fmt.Fprintln(&details, strings.TrimSpace(failure.TestOutput))
	}
	return strings.TrimSpace(details.String())
}
