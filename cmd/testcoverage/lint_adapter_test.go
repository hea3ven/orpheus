//go:build integration

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestIntegrationAdapterContractLintExclusionsFilterOnlyExactPackageFindings(t *testing.T) {
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skip("lint adapter contract requires golangci-lint")
	}
	root := testutil.CanonicalTempDir(t)
	t.Chdir(root)
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOLANGCI_LINT_CACHE", filepath.Join(root, "lint-cache"))
	// Use the cached toolchain directly: automatic toolchain selection requires
	// checksum verification even with GOSUMDB=off and may reach the network.
	cache, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	toolchain := filepath.Join(strings.TrimSpace(string(cache)), "golang.org", "toolchain@v0.0.1-go1.26.3."+runtime.GOOS+"-"+runtime.GOARCH)
	if _, err := os.Stat(filepath.Join(toolchain, "bin", "go")); err != nil {
		t.Skip("lint adapter contract requires cached Go 1.26.3")
	}
	t.Setenv("GOROOT", toolchain)
	t.Setenv("PATH", filepath.Join(toolchain, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	for name, contents := range map[string]string{
		"go.mod":                       "module " + modulePath + "\n\ngo 1.26.1\n",
		".golangci.yml":                lintFixtureConfig,
		"internal/infra/work.go":       "package infra\nimport \"fmt\"\nfunc Work() { fmt.Println(\"excluded\") }\n",
		"internal/infra/work_test.go":  "package infra\nimport (\"fmt\"; \"testing\")\nfunc TestWork(t *testing.T) { fmt.Println(\"excluded test\") }\n",
		"internal/infra-extra/work.go": "package extra\nimport \"fmt\"\nfunc Work() { fmt.Println(\"sibling\") }\n",
		"internal/infra/child/work.go": "package child\nimport \"fmt\"\nfunc Work() { fmt.Println(\"child\") }\n",
		"internal/product/work.go":     "package product\nimport \"fmt\"\nfunc Work() { fmt.Println(\"unlisted\") }\n",
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	policy := testLocalQualityPolicy()
	policy.PackageExclusions = []string{"internal/infra"}
	var output bytes.Buffer
	if err := executeLint(policy, &output, &output); err == nil {
		t.Fatalf("unlisted packages escaped lint: %s", output.String())
	}
	for _, name := range []string{"internal/infra-extra/work.go", "internal/infra/child/work.go", "internal/product/work.go"} {
		if !strings.Contains(output.String(), name+":") {
			t.Fatalf("unlisted package missing lint finding %s:\n%s", name, output.String())
		}
		if err := os.WriteFile(name, []byte("package clean\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Contains(output.String(), "internal/infra/work") {
		t.Fatalf("excluded package retained lint findings: %s", output.String())
	}
	output.Reset()
	if err := executeLint(policy, &output, &output); err != nil {
		t.Fatalf("excluded production/test findings blocked lint: %v\n%s", err, output.String())
	}
	if err := os.WriteFile("internal/infra/work.go", []byte("package infra\nfunc Work() { missingIdentifier() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := executeLint(policy, &output, &output); err == nil || !strings.Contains(output.String(), "undefined: missingIdentifier") {
		t.Fatalf("excluded package type error escaped compilation: %v\n%s", err, output.String())
	}
}
