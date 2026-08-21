package testlane_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testlane"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestRepositoryTestSourcesConformToConventions(t *testing.T) {
	violations, err := testlane.ValidateTestSources(repositoryRoot(t))
	if err != nil {
		t.Fatalf("validate repository test sources: %v", err)
	}
	if len(violations) != 0 {
		t.Fatalf("test-source convention violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestValidateTemporaryPathSourcesRejectsDirectTempDir(t *testing.T) {
	violations := validateTemporaryPathFixture(t, `package fixture

import "testing"

func TestFixture(t *testing.T) { _ = t.TempDir() }
`)

	if len(violations) != 1 || !strings.Contains(violations[0], "use testutil.CanonicalTempDir(t)") {
		t.Fatalf("violations = %v, want direct TempDir rejection", violations)
	}
}

func TestValidateTemporaryPathSourcesRejectsAbsoluteTmpPathTokens(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "path", value: temporaryFixturePath()},
		{name: "JSON", value: `{"worktree":"` + temporaryFixturePath() + `"}`},
		{name: "YAML", value: "worktree: " + temporaryFixturePath()},
		{name: "shell", value: "report_dir=$(mktemp -d " + temporaryFixturePath() + ")"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			violations := validateTemporaryPathFixture(t, fixtureStringSource(test.value))
			if len(violations) != 1 || !strings.Contains(violations[0], "absolute /tmp fixture path") {
				t.Fatalf("violations = %v, want absolute /tmp token rejection", violations)
			}
		})
	}
}

func TestValidateTemporaryPathSourcesAllowsCanonicalAndDocumentedPaths(t *testing.T) {
	violations := validateTemporaryPathFixture(t, documentedTemporaryPathFixtureSource())

	if len(violations) != 0 {
		t.Fatalf("violations = %v, want canonical and documented paths allowed", violations)
	}
}

func TestValidateIntegrationSourcesSkipsGitDirectory(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
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
	root := testutil.CanonicalTempDir(t)
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
	root := testutil.CanonicalTempDir(t)
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

func validateTemporaryPathFixture(t *testing.T, contents string) []string {
	t.Helper()

	root := testutil.CanonicalTempDir(t)
	path := filepath.Join(root, "fixture_test.go")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write temporary-path fixture: %v", err)
	}
	violations, err := testlane.ValidateTemporaryPathSources(root)
	if err != nil {
		t.Fatalf("validate temporary-path fixture: %v", err)
	}
	return violations
}

func fixtureStringSource(value string) string {
	return "package fixture\n\nconst fixtureValue = " + strconv.Quote(value) + "\n"
}

func documentedTemporaryPathFixtureSource() string {
	const reason = "Path identity is intentionally irrelevant to this isolated fixture."

	return strings.Join([]string{
		"package fixture",
		"",
		`import "testing"`,
		"",
		"func TestFixture(t *testing.T) {",
		"\t_ = testutil.CanonicalTempDir(t)",
		"\t_ = t.TempDir() //nolint:forbidigo // " + reason,
		"}",
		"",
		"const fixturePath = " + strconv.Quote(temporaryFixturePath()) + " // orpheus:allow-absolute-tmp-path -- " + reason,
		"const shell = " + strconv.Quote("report_dir=$(mktemp -d "+temporaryFixturePath()+")") + " // orpheus:allow-absolute-tmp-path -- " + reason,
		"",
	}, "\n")
}

func temporaryFixturePath() string {
	return "/tm" + "p/fixture"
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
		"quality:\n\tgo run ./cmd/testcoverage",
		"check: fmt quality lint",
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
