package cli_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/cli"
)

func TestNewRootCommandHelp(t *testing.T) {
	cmd := cli.NewRootCommand()
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
	if !strings.Contains(output, "--verbose") {
		t.Fatalf("help output does not contain verbose flag: %q", output)
	}
}

func TestRootCommandDoesNotEmitDebugByDefault(t *testing.T) {
	cmd := cli.NewRootCommand()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute root command: %v", err)
	}

	if strings.Contains(stdout.String(), "level=DEBUG") {
		t.Fatalf("stdout contains debug diagnostics: %q", stdout.String())
	}
	if strings.Contains(stderr.String(), "level=DEBUG") {
		t.Fatalf("stderr contains debug diagnostics without verbose: %q", stderr.String())
	}
}

func TestRootCommandVerboseEmitsDebugToStderr(t *testing.T) {
	cmd := cli.NewRootCommand()
	stdout := new(bytes.Buffer)
	stderr := new(bytes.Buffer)
	cmd.SetOut(stdout)
	cmd.SetErr(stderr)
	cmd.SetArgs([]string{"--verbose"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute root command: %v", err)
	}

	if strings.Contains(stdout.String(), "level=DEBUG") {
		t.Fatalf("stdout contains debug diagnostics: %q", stdout.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "level=DEBUG") {
		t.Fatalf("stderr missing debug diagnostics with verbose enabled: %q", got)
	}
	if !strings.Contains(got, "msg=\"rendering root help\"") {
		t.Fatalf("stderr missing root diagnostic message: %q", got)
	}
}
