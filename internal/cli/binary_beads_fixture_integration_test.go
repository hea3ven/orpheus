//go:build integration

//nolint:testpackage // The binary E2E fixture uses invocation-scoped executable substitutes.
package cli

import (
	"fmt"
	"github.com/hea3ven/orpheus/internal/testutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeBDCommandResponse struct {
	dir      string
	args     string
	stdout   string
	stderr   string
	exitCode int
}

func withFakeBDCommandResponses(t *testing.T, responses []fakeBDCommandResponse) string {
	t.Helper()

	binDir := testutil.CanonicalTempDir(t)
	fixtureDir := filepath.Join(binDir, "fixtures")
	if err := os.MkdirAll(fixtureDir, 0o755); err != nil {
		t.Fatalf("create fake bd fixtures: %v", err)
	}

	logPath := filepath.Join(binDir, "bd.log")
	var script strings.Builder
	script.WriteString(`#!/bin/sh
{
  pwd
  printf '%s\n' "$*"
} >> "$FAKE_BD_LOG"
case "$PWD|$*" in
`)

	for i, response := range responses {
		stdoutPath := filepath.Join(fixtureDir, fmt.Sprintf("stdout-%d.json", i))
		stderrPath := filepath.Join(fixtureDir, fmt.Sprintf("stderr-%d.txt", i))
		if err := os.WriteFile(stdoutPath, []byte(response.stdout), 0o644); err != nil {
			t.Fatalf("write fake bd stdout: %v", err)
		}
		if err := os.WriteFile(stderrPath, []byte(response.stderr), 0o644); err != nil {
			t.Fatalf("write fake bd stderr: %v", err)
		}
		exitCode := response.exitCode
		if exitCode == 0 && response.stderr != "" && response.stdout == "" {
			exitCode = 1
		}
		args := response.args
		if args == "--json --readonly --sandbox list --all --limit 0" {
			args += " --type task"
		}
		fmt.Fprintf(&script, "  %s)\n", shellQuote(canonicalFixturePath(t, response.dir)+"|"+args))
		fmt.Fprintf(&script, "    cat %s\n", shellQuote(stdoutPath))
		fmt.Fprintf(&script, "    cat %s >&2\n", shellQuote(stderrPath))
		fmt.Fprintf(&script, "    exit %d\n", exitCode)
		fmt.Fprintln(&script, "    ;;")
	}
	script.WriteString(`esac
case "$*" in
  "--json --readonly --sandbox list --all --limit 0 --type epic") printf '[]\n'; exit 0 ;;
  --json\ --sandbox\ update\ *--set-metadata\ orpheus.branch=*) exit 0 ;;
  --json\ --sandbox\ update\ *--set-metadata\ orpheus.pr_url=*) exit 0 ;;
esac
echo "unexpected fake bd call: $PWD|$*" >&2
exit 65
`)

	bdPath := filepath.Join(binDir, "bd")
	if err := writeTestExecutable(bdPath, []byte(script.String())); err != nil {
		t.Fatalf("write fake bd: %v", err)
	}
	setTestEnvironment(t, "FAKE_BD_LOG", logPath)
	prependTestPath(t, binDir)
	return logPath
}
