// Package taskbranch resolves and renders configured task branch names.
package taskbranch

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	// DefaultTemplate preserves Orpheus' original task branch convention.
	DefaultTemplate = "orpheus/{{task_id}}"

	taskIDPlaceholder      = "{{task_id}}"
	externalRefPlaceholder = "{{external_ref}}"
	taskTitlePlaceholder   = "{{task_title}}"
)

// Values are the task fields available to branch templates.
type Values struct {
	TaskID      string
	ExternalRef string
	TaskTitle   string
}

// ValidateTemplate checks that a branch template contains only literal text and
// supported task placeholders. An empty template is valid because it inherits a
// broader configuration value or the compatibility default.
func ValidateTemplate(template string) error {
	for remaining := template; remaining != ""; {
		open := strings.Index(remaining, "{{")
		closing := strings.Index(remaining, "}}")
		switch {
		case closing >= 0 && (open < 0 || closing < open):
			return errors.New("task branch template has an unexpected closing delimiter \"}}\"")
		case open < 0:
			return nil
		case strings.HasPrefix(remaining[open:], taskIDPlaceholder):
			remaining = remaining[open+len(taskIDPlaceholder):]
		case strings.HasPrefix(remaining[open:], externalRefPlaceholder):
			remaining = remaining[open+len(externalRefPlaceholder):]
		case strings.HasPrefix(remaining[open:], taskTitlePlaceholder):
			remaining = remaining[open+len(taskTitlePlaceholder):]
		default:
			return fmt.Errorf(
				"task branch template contains an unsupported placeholder; only %s, %s, and %s are supported",
				taskIDPlaceholder,
				externalRefPlaceholder,
				taskTitlePlaceholder,
			)
		}
	}
	return nil
}

// ResolveTemplate applies repository, global, and compatibility precedence.
func ResolveTemplate(repository, global string) string {
	for _, candidate := range []string{repository, global} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return DefaultTemplate
}

// RequiresExternalRef reports whether a branch template references the task external reference.
func RequiresExternalRef(template string) bool {
	return strings.Contains(template, externalRefPlaceholder)
}

// Render resolves one template into a deterministic, Git-safe branch name.
// Placeholder values are normalized independently so a value cannot introduce
// additional ref separators. Literal template text remains subject to final ref
// validation, which keeps invalid templates visible to their operators.
func Render(template string, values Values) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		template = DefaultTemplate
	}
	if err := ValidateTemplate(template); err != nil {
		return "", err
	}

	rendered, err := replacePlaceholder(template, taskIDPlaceholder, "task ID", values.TaskID)
	if err != nil {
		return "", err
	}
	rendered, err = replacePlaceholder(rendered, externalRefPlaceholder, "external reference", values.ExternalRef)
	if err != nil {
		return "", err
	}
	rendered, err = replacePlaceholder(rendered, taskTitlePlaceholder, "task title", values.TaskTitle)
	if err != nil {
		return "", err
	}
	if err := ValidateBranch(rendered); err != nil {
		return "", err
	}
	return rendered, nil
}

func replacePlaceholder(template, placeholder, label, value string) (string, error) {
	if !strings.Contains(template, placeholder) {
		return template, nil
	}
	normalized, err := normalizeValue(value)
	if err != nil {
		return "", fmt.Errorf("task branch template requires %s: %w", requiredValueLabel(label), err)
	}
	return strings.ReplaceAll(template, placeholder, normalized), nil
}

func requiredValueLabel(label string) string {
	if label == "external reference" {
		return "a task external reference"
	}
	return "a " + label
}

func normalizeValue(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("value is missing")
	}

	var builder strings.Builder
	separator := false
	for _, r := range value {
		if isBranchValueRune(r) {
			builder.WriteRune(r)
			separator = false
			continue
		}
		if !separator {
			builder.WriteByte('-')
			separator = true
		}
	}
	normalized := strings.Trim(builder.String(), "-")
	if normalized == "" {
		return "", errors.New("value has no Git-safe characters")
	}
	return normalized, nil
}

func isBranchValueRune(r rune) bool {
	return r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_')
}

// ValidateBranch accepts the subset of Git branch refs produced by the renderer
// and rejects all ref syntax Git's check-ref-format --branch rejects. Keeping the
// check pure ensures template errors are reported before Git or metadata mutates.
func ValidateBranch(branch string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return errors.New("rendered task branch is empty")
	}
	if strings.HasPrefix(branch, "-") {
		return fmt.Errorf("rendered task branch %q cannot begin with '-'", branch)
	}
	if strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "//") {
		return fmt.Errorf("rendered task branch %q has an empty path component", branch)
	}
	if strings.Contains(branch, "..") || strings.Contains(branch, "@{") || branch == "@" || branch == "HEAD" {
		return fmt.Errorf("rendered task branch %q has invalid Git ref syntax", branch)
	}
	for _, component := range strings.Split(branch, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("rendered task branch %q has invalid Git ref syntax", branch)
		}
		for _, r := range component {
			if r <= ' ' || r == 0x7f || strings.ContainsRune("~^:?*[\\", r) {
				return fmt.Errorf("rendered task branch %q has invalid Git ref syntax", branch)
			}
		}
	}
	return nil
}
