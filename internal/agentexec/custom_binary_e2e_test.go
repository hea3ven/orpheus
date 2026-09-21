//go:build integration

package agentexec_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestIntegrationBinaryE2ECustomNamedTestBinaryRetainsSafetyGate(t *testing.T) {
	if os.Getenv("ORPHEUS_TESTGUARD_CUSTOM_BINARY") == "1" {
		marker := os.Getenv("ORPHEUS_TESTGUARD_CUSTOM_BINARY_MARKER")
		if marker == "" {
			t.Fatal("custom binary execution marker is missing")
		}
		if err := os.WriteFile(marker, []byte("executed"), 0o644); err != nil {
			t.Fatalf("write custom binary execution marker: %v", err)
		}
		assertSupportedAgentBlockedBeforePATHLookup(t)
		return
	}

	root := testutil.CanonicalTempDir(t)
	binary := filepath.Join(root, "op-ejt-agentexec-testbin")
	marker := filepath.Join(root, "executed")
	build := exec.Command("go", "test", "-c", "-tags=integration", "-o", binary, ".")
	build.Dir = "."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("compile custom-named test binary: %v\n%s", err, output)
	}

	run := exec.Command(binary, "-test.run", "^TestIntegrationBinaryE2ECustomNamedTestBinaryRetainsSafetyGate$")
	run.Env = append(
		os.Environ(),
		"ORPHEUS_TESTGUARD_CUSTOM_BINARY=1",
		"ORPHEUS_TESTGUARD_CUSTOM_BINARY_MARKER="+marker,
	)
	if output, err := run.CombinedOutput(); err != nil {
		t.Fatalf("run custom-named test binary: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("custom-named test binary did not run selected test: %v", err)
	}
	if string(contents) != "executed" {
		t.Fatalf("custom binary execution marker = %q, want executed", contents)
	}
}
