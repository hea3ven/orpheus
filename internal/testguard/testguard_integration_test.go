//go:build integration

package testguard_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntegrationProductionBinaryNamedTestDoesNotEnableTestMode(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "orpheus.test")
	build := exec.Command("go", "build", "-o", binary, "./testdata/testprobe")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build production probe: %v\n%s", err, output)
	}

	run := exec.Command(binary)
	output, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("run production probe: %v\n%s", err, output)
	}
	if got := strings.TrimSpace(string(output)); got != "false" {
		t.Fatalf("production binary IsTestProcess() = %q, want false", got)
	}
}
