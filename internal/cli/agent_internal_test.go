package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDetailedDescriptionReadsFileExactly(t *testing.T) {
	body := "## Summary\n\nPreserve trailing newline.\n"
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write detailed description: %v", err)
	}

	got, err := resolveDetailedDescription("", path)
	if err != nil {
		t.Fatalf("resolve detailed description: %v", err)
	}
	if got != body {
		t.Fatalf("detailed description = %q, want exact file content %q", got, body)
	}
}

func TestResolveTechnicalExplanationReadsFileExactly(t *testing.T) {
	body := "## Technical pitch\n\nPreserve trailing newline.\n"
	path := filepath.Join(t.TempDir(), "technical.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write technical explanation: %v", err)
	}

	got, err := resolveTechnicalExplanation("", path)
	if err != nil {
		t.Fatalf("resolve technical explanation: %v", err)
	}
	if got != body {
		t.Fatalf("technical explanation = %q, want exact file content %q", got, body)
	}
}
