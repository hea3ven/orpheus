//go:build integration

package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testguard"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestIntegrationAdapterContractToolProbeSeparatesVersionFromCheckoutDiagnostics(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	tool := filepath.Join(root, "version-tool")
	script := "#!/bin/sh\nprintf 'bd version 1.2.2 (fixture)\\n'\nprintf 'Warning: %s/.beads has permissions 0755\\n' \"$PWD\" >&2\n"
	if err := testguard.WriteExecutable(tool, []byte(script)); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"base", "merge"} {
		checkout := filepath.Join(root, name)
		if err := os.Mkdir(checkout, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(checkout)

		output, err := probeToolCommand([]string{tool, "--version"})

		if err != nil || string(output) != "bd version 1.2.2 (fixture)\n" {
			t.Fatalf("%s version metadata = %q, %v; want only the tool version", name, output, err)
		}
	}
}

func TestIntegrationAdapterContractFailedToolProbeRetainsBothStreamsAndExitStatus(t *testing.T) {
	tool := filepath.Join(testutil.CanonicalTempDir(t), "version-tool")
	if err := testguard.WriteExecutable(tool, []byte("#!/bin/sh\nprintf 'partial version\\n'\nprintf 'probe failed\\n' >&2\nexit 7\n")); err != nil {
		t.Fatal(err)
	}

	output, err := probeToolCommand([]string{tool, "--version"})

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatalf("probe error = %v, want exit status 7", err)
	}
	if !strings.Contains(string(output), "partial version") || !strings.Contains(string(output), "probe failed") {
		t.Fatalf("failed probe lost stdout or stderr: %q", output)
	}
}
