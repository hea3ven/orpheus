//go:build integration

package main

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/testutil"
	_ "github.com/quasilyte/go-ruleguard/dsl" // Populate the module cache for the offline Ruleguard config.
)

func TestIntegrationAdapterContractLintExclusionsFilterOnlyExactPackageFindings(t *testing.T) {
	root := setupLintFixture(t)
	t.Chdir(root)
	for name, contents := range map[string]string{
		"go.mod":                       "module " + modulePath + "\n\ngo 1.26.1\n",
		".golangci.yml":                lintFixtureConfig,
		"internal/infra/work.go":       "package infra\nimport \"fmt\"\nfunc Work() { fmt.Println(\"excluded\") }\n",
		"internal/infra/work_test.go":  "package infra\nimport (\"fmt\"; \"testing\")\nfunc TestWork(t *testing.T) { fmt.Println(\"excluded test\") }\n",
		"internal/infra-extra/work.go": "package extra\nimport \"fmt\"\nfunc Work() { fmt.Println(\"sibling\") }\n",
		"internal/infra/child/work.go": "package child\nimport \"fmt\"\nfunc Work() { fmt.Println(\"child\") }\n",
		"internal/product/work.go":     "package product\nimport \"fmt\"\nfunc Work() { fmt.Println(\"unlisted\") }\n",
	} {
		writeLintFixture(t, name, contents, 0o644)
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

func TestIntegrationAdapterContractLintEnforcesProductionImportBoundaries(t *testing.T) {
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("lint adapter contract requires make")
	}
	root := setupLintFixture(t)
	for _, name := range []string{".golangci.yml", ".quality.yml", "go.mod", "go.sum", "lint/collaborators.go"} {
		contents, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		writeLintFixture(t, filepath.Join(root, name), string(contents), 0o644)
	}
	// Build the real collector from the checkout, then run it in the fixture
	// module. The wrapper changes only where the collector runs, not the recipe.
	writeLintFixture(t, filepath.Join(root, "bin", "lint-in-fixture"), "#!/bin/sh\ncd \"$LINT_FIXTURE\" || exit 1\nexec \"$@\"\n", 0o755)
	t.Setenv("LINT_FIXTURE", root)
	t.Setenv("PATH", filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOFLAGS", "-exec=lint-in-fixture")
	allowed := map[string]string{
		"agentexec/allow_agent.go":              "agentextra",
		"agentexec/allow_cli.go":                "cliextra",
		"agentexec/allow_workflow.go":           "workflowextra",
		"agentexec/allow_agent_test.go":         "agent",
		"agentexec/sub/allow_agent.go":          "agent",
		"task/allow_registry.go":                "registryextra",
		"task/allow_registry_test.go":           "registry",
		"task/sub/deep/allow_registry_child.go": "registry/child",
		"task/sub/deep/allow_registry_test.go":  "registry",
		"taskextra/allow_registry.go":           "registry",
	}
	writeLintImports(t, root, allowed)
	if output, err := runMakeLint(t); err != nil {
		t.Fatalf("make lint rejected allowed production or test imports: %v\n%s", err, output)
	}
	forbidden := map[string]string{
		"agentexec/deny_agent.go":          "agent",
		"agentexec/deny_agent_child.go":    "agent/child",
		"agentexec/deny_cli.go":            "cli",
		"agentexec/deny_cli_child.go":      "cli/child",
		"agentexec/deny_workflow.go":       "workflow",
		"agentexec/deny_workflow_child.go": "workflow/child",
		"task/deny_registry.go":            "registry",
		"task/sub/deep/deny_registry.go":   "registry",
	}
	writeLintImports(t, root, forbidden)

	output, err := runMakeLint(t)

	if err == nil {
		t.Fatalf("make lint accepted forbidden production imports:\n%s", output)
	}
	findings := make(map[string]string)
	for line := range strings.SplitSeq(output, "\n") {
		if !strings.HasSuffix(line, " (depguard)") {
			continue
		}
		file, _, _ := strings.Cut(line, ":")
		_, message, _ := strings.Cut(line, "import '")
		importPath, _, _ := strings.Cut(message, "'")
		findings[strings.TrimPrefix(filepath.ToSlash(file), "internal/")] = strings.TrimPrefix(importPath, modulePath+"/internal/")
	}
	if !maps.Equal(findings, forbidden) {
		t.Fatalf("depguard findings = %v, want %v:\n%s", findings, forbidden, output)
	}
}

func writeLintImports(t *testing.T, root string, imports map[string]string) {
	t.Helper()
	for file, target := range imports {
		// Use leaf packages to exercise lint without introducing import cycles.
		targetPackage := filepath.Base(target)
		writeLintFixture(t, filepath.Join(root, "internal", target, "marker.go"), "package "+targetPackage+"\n\nconst Marker = 1\n", 0o644)
		pkg := filepath.Base(filepath.Dir(file))
		if strings.HasSuffix(file, "_test.go") {
			pkg += "_test"
		}
		contents := fmt.Sprintf("package %s\n\nimport %q\n\nvar _ = %s.Marker\n", pkg, modulePath+"/internal/"+target, targetPackage)
		writeLintFixture(t, filepath.Join(root, "internal", file), contents, 0o644)
	}
}

func runMakeLint(t *testing.T) (string, error) {
	t.Helper()
	ctx := t.Context()
	// The comparison runner supplies this cache and owns the enclosing process
	// group and deadline. Splitting off a new group would let lint outlive it.
	standalone := os.Getenv(testBinaryCacheEnvironment) == ""
	if standalone {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "make", "lint")
	cmd.Dir = filepath.Join("..", "..")
	if standalone {
		configureProcessCancellation(cmd)
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func setupLintFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("golangci-lint"); err != nil {
		t.Skip("lint adapter contract requires golangci-lint")
	}
	root := testutil.CanonicalTempDir(t)
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOWORK", "off")
	// Resolve the cache used by the enclosing build before disabling GOENV;
	// GOMODCACHE (or its default from GOPATH) may be configured there.
	cache, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	moduleCache := strings.TrimSpace(string(cache))
	t.Setenv("GOMODCACHE", moduleCache)
	t.Setenv("GOENV", "off")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOLANGCI_LINT_CACHE", filepath.Join(root, "lint-cache"))
	// Use a toolchain on PATH or the cached toolchain directly: Go's automatic
	// module-based selection can't verify checksums with GOSUMDB=off.
	t.Setenv("GOTOOLCHAIN", "go1.26.3+path")
	toolchain, err := exec.Command("go", "env", "GOROOT").Output()
	toolchainRoot := strings.TrimSpace(string(toolchain))
	t.Setenv("GOTOOLCHAIN", "local")
	if err != nil {
		toolchainRoot = filepath.Join(moduleCache, "golang.org", "toolchain@v0.0.1-go1.26.3."+runtime.GOOS+"-"+runtime.GOARCH)
		if _, err := os.Stat(filepath.Join(toolchainRoot, "bin", "go")); err != nil {
			t.Skip("lint adapter contract requires Go 1.26.3")
		}
	}
	t.Setenv("GOROOT", toolchainRoot)
	t.Setenv("PATH", filepath.Join(toolchainRoot, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
	return root
}

func writeLintFixture(t *testing.T, name, contents string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
}
