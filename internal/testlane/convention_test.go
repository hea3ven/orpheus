package testlane_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testlane"
)

func TestRepositoryIntegrationTestsConformToLaneConvention(t *testing.T) {
	violations, err := testlane.ValidateIntegrationSources(repositoryRoot(t))
	if err != nil {
		t.Fatalf("validate integration test sources: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("integration test lane convention violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestValidateIntegrationSourcesSkipsGitDirectory(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatalf("create .git fixture: %v", err)
	}
	contents := "package ignored\n\nfunc TestIntegrationIgnored(t *testing.T) {}\n"
	if err := os.WriteFile(filepath.Join(gitDir, "ignored_test.go"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write ignored test fixture: %v", err)
	}

	violations, err := testlane.ValidateIntegrationSources(root)
	if err != nil {
		t.Fatalf("validate fixture with .git directory: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("violations = %v, want .git contents ignored", violations)
	}
}

func TestValidateIntegrationSourcesRejectsNonconformingTopLevelTest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "adapter_integration_test.go")
	contents := "//go:build integration\n\npackage adapter\n\nfunc TestAdapterContract(t *testing.T) {}\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write integration fixture: %v", err)
	}

	violations, err := testlane.ValidateIntegrationSources(root)
	if err != nil {
		t.Fatalf("validate integration fixture: %v", err)
	}
	if len(violations) != 1 || !strings.Contains(violations[0], "TestAdapterContract") {
		t.Fatalf("violations = %v, want TestAdapterContract naming violation", violations)
	}
}

func TestValidateIntegrationSourcesRejectsIntegrationNameWithoutTag(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "adapter_test.go")
	contents := "package adapter\n\nfunc TestIntegrationAdapterContract(t *testing.T) {}\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write unit fixture: %v", err)
	}

	violations, err := testlane.ValidateIntegrationSources(root)
	if err != nil {
		t.Fatalf("validate unit fixture: %v", err)
	}
	if len(violations) != 1 || !strings.Contains(violations[0], "TestIntegrationAdapterContract") {
		t.Fatalf("violations = %v, want integration-name placement violation", violations)
	}
}

func TestLaneCommandsUseSharedConvention(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(repositoryRoot(t), "Makefile"))
	if err != nil {
		t.Fatalf("read Makefile: %v", err)
	}

	for _, want := range []string{
		"test-unit:\n\tgo test ./...",
		"test: test-unit",
		"INTEGRATION_TEST_PATTERN := " + testlane.IntegrationTestPattern,
		"INTEGRATION_TEST_ARGS := -tags=" + testlane.IntegrationBuildTag + " -run '$(INTEGRATION_TEST_PATTERN)'",
		"go test $(INTEGRATION_TEST_ARGS) ./...",
		"check: fmt test-unit test-integration lint",
	} {
		if !strings.Contains(string(contents), want) {
			t.Fatalf("Makefile does not use the lane convention %q", want)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()

	_, path, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(path), "..", ".."))
}
