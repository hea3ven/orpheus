package agent

import (
	"fmt"
	"strings"
)

// RenderBootstrapPrompt renders the minimal prompt injected into agent profiles.
func RenderBootstrapPrompt() string {
	var builder strings.Builder

	builder.WriteString("You are an attached implementation agent dispatched by Orpheus.\n\n")
	builder.WriteString("Run `orpheus agent context` now to obtain the task instructions ")
	builder.WriteString("and execution contract before starting implementation.\n")

	return builder.String()
}

func appendPromptLine(builder *strings.Builder, label string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "-"
	}
	_, _ = fmt.Fprintf(builder, "%s: %s\n", label, value)
}

func appendPromptBlock(builder *strings.Builder, label string, value string) {
	value = strings.TrimRight(strings.ReplaceAll(value, "\r\n", "\n"), "\r\n")
	if strings.TrimSpace(value) == "" {
		_, _ = fmt.Fprintf(builder, "%s: -\n", label)
		return
	}
	if !strings.Contains(value, "\n") {
		_, _ = fmt.Fprintf(builder, "%s: %s\n", label, value)
		return
	}

	_, _ = fmt.Fprintf(builder, "%s:\n", label)
	for _, line := range strings.Split(value, "\n") {
		_, _ = fmt.Fprintf(builder, "  %s\n", line)
	}
}
