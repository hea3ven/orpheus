package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestNewRootCommandHelp(t *testing.T) {
	cmd := NewRootCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.SetArgs([]string{"--help"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute help: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "Orpheus") {
		t.Fatalf("help output does not contain root description: %q", output)
	}
	if !strings.Contains(output, "Usage:") {
		t.Fatalf("help output does not contain usage: %q", output)
	}
}
