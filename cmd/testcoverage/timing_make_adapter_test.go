//go:build integration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestIntegrationAdapterContractTimingMakeTargetRequiresExplicitBaseAndUsesReadOnlyBootstrap(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("Make is not installed")
	}
	makefile, err := filepath.Abs(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	root := testutil.CanonicalTempDir(t)
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	request := filepath.Join(root, "request")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >'" + request + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	missing := exec.Command("make", "-f", makefile, "timing-check", "BASE=")
	missing.Dir = root

	output, missingErr := missing.CombinedOutput()

	if missingErr == nil || !strings.Contains(string(output), "Supply an explicit base") {
		t.Fatalf("missing base = %v, %s", missingErr, output)
	}
	if _, err := os.Stat(request); !os.IsNotExist(err) {
		t.Fatalf("missing base started the collector: %v", err)
	}
	explicit := exec.Command("make", "-f", makefile, "timing-check", "BASE=HEAD~1")
	explicit.Dir = root
	if output, err := explicit.CombinedOutput(); err != nil {
		t.Fatalf("explicit target: %v\n%s", err, output)
	}
	contents, err := os.ReadFile(request)
	if err != nil {
		t.Fatal(err)
	}
	want := "run\n-mod=readonly\n./cmd/testcoverage\n-compare-worktree\n-compare-base\nHEAD~1\n"
	if string(contents) != want {
		t.Fatalf("timing bootstrap = %q, want %q", contents, want)
	}
}
