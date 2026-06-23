// Package publication defines repository-configured publication title behavior.
package publication

import (
	"errors"
	"fmt"
	"strings"
)

const (
	summaryPlaceholder     = "{{summary}}"
	externalRefPlaceholder = "{{external_ref}}"
)

// ValidateTitleTemplate checks that a title template uses only literal text and
// the supported summary and external reference placeholders.
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
		case strings.HasPrefix(remaining[open:], externalRefPlaceholder):
			remaining = remaining[open+len(externalRefPlaceholder):]
		default:
			return fmt.Errorf(
				"publication title template contains an unsupported placeholder; only %s and %s are supported",
				summaryPlaceholder,
				externalRefPlaceholder,
			)
		}
	}
	return nil
}

// RequiresExternalRef reports whether a publication title template requires a
// task external reference.
func RequiresExternalRef(template string) bool {
	return strings.Contains(template, externalRefPlaceholder)
}

// RenderTitle applies a valid title template to a completion summary and task
// external reference. An empty template preserves the summary unchanged.
func RenderTitle(template string, summary string, externalRef string) (string, error) {
	if err := ValidateTitleTemplate(template); err != nil {
		return "", err
	}
	if strings.TrimSpace(template) == "" {
		return summary, nil
	}
	if RequiresExternalRef(template) {
		externalRef = normalizeExternalRef(externalRef)
		if externalRef == "" {
			return "", errors.New("publication title template requires a task external reference")
		}
		template = strings.ReplaceAll(template, externalRefPlaceholder, externalRef)
	}
	return strings.ReplaceAll(template, summaryPlaceholder, summary), nil
}

func normalizeExternalRef(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
