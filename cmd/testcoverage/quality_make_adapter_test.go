//go:build integration

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
	"gopkg.in/yaml.v3"
)

func TestIntegrationAdapterContractQualityMakeTargetsUseCIToolchainDespiteCallerOverride(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("Make is not installed")
	}
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(repository, ".github", "workflows", "quality-gate.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				ID   string
				With map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		t.Fatal(err)
	}
	var versionFile string
	for _, step := range workflow.Jobs["quality"].Steps {
		if step.ID == "setup-go" {
			versionFile = step.With["go-version-file"]
			if step.With["go-version"] != "" {
				t.Fatal("CI must not override the shared Go version file")
			}
		}
	}
	if versionFile == "" {
		t.Fatal("CI must select Go from a shared version file")
	}
	version, err := os.ReadFile(filepath.Join(repository, versionFile))
	if err != nil {
		t.Fatal(err)
	}

	// A future pin must propagate too, rather than matching a hard-coded recipe.
	for _, pin := range []string{strings.TrimSpace(string(version)), "1.99.42"} {
		t.Run(pin, func(t *testing.T) {
			root := testutil.CanonicalTempDir(t)
			if err := os.WriteFile(filepath.Join(root, versionFile), []byte(pin+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			request := filepath.Join(root, "request")
			script := "#!/bin/sh\nprintf '%s\\n' \"$GOTOOLCHAIN\" \"$@\" >\"$QUALITY_MAKE_REQUEST\"\n"
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("GOTOOLCHAIN", "go1.26.3")
			t.Setenv("QUALITY_MAKE_REQUEST", request)
			for _, target := range []struct{ name, args string }{
				{name: "quality", args: "run\n./cmd/testcoverage\n"},
				{name: "quality-policy-update", args: "run\n./cmd/testcoverage\n-update-policy\n"},
			} {
				cmd := exec.Command("make", "-f", filepath.Join(repository, "Makefile"), target.name)
				cmd.Dir = root

				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("%s: %v\n%s", target.name, err, output)
				}

				got, err := os.ReadFile(request)
				if err != nil {
					t.Fatal(err)
				}
				if want := "go" + pin + "\n" + target.args; string(got) != want {
					t.Fatalf("%s Go request = %q, want %q", target.name, got, want)
				}
			}
		})
	}
}
