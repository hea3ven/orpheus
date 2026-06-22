// Package publication defines repository-configured publication title behavior.
package publication

import (
	"errors"
	"fmt"
	"strings"
)

const summaryPlaceholder = "{{summary}}"

// ValidateTitleTemplate checks that a title template uses only literal text and
// the supported summary placeholder.
func ValidateTitleTemplate(template string) error {
	for remaining := template; remaining != ""; {
		open := strings.Index(remaining, "{{")
		closingDelimiter := strings.Index(remaining, "}}")
		switch {
		case closingDelimiter >= 0 && (open < 0 || closingDelimiter < open):
			return errors.New("publication title template has an unexpected closing delimiter \"}}\"")
		case open < 0:
			return nil
		case strings.HasPrefix(remaining[open:], summaryPlaceholder):
			remaining = remaining[open+len(summaryPlaceholder):]
		default:
			return fmt.Errorf("publication title template contains an unsupported placeholder; only %s is supported", summaryPlaceholder)
		}
	}
	return nil
}

// RenderTitle applies a valid title template to a completion summary. An empty
// template preserves the summary unchanged.
func RenderTitle(template string, summary string) (string, error) {
	if err := ValidateTitleTemplate(template); err != nil {
		return "", err
	}
	if strings.TrimSpace(template) == "" {
		return summary, nil
	}
	return strings.ReplaceAll(template, summaryPlaceholder, summary), nil
}
