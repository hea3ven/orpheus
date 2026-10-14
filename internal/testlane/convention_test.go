package testlane_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testlane"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/stretchr/testify/require"
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

func TestValidateIntegrationSourcesRejectsLaneMembershipMismatch(t *testing.T) {
	for _, tt := range []struct{ name, contents, testName string }{
		{name: "tag without category", contents: "//go:build integration\n\npackage adapter\n\nfunc TestAdapterContract(t *testing.T) {}\n", testName: "TestAdapterContract"},
		{name: "category without tag", contents: "package adapter\n\nfunc TestIntegrationAdapterContract(t *testing.T) {}\n", testName: "TestIntegrationAdapterContract"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := testutil.CanonicalTempDir(t)
			if err := os.WriteFile(filepath.Join(root, "adapter_test.go"), []byte(tt.contents), 0o644); err != nil {
				t.Fatal(err)
			}
			violations, err := testlane.ValidateIntegrationSources(root)
			if err != nil {
				t.Fatal(err)
			}
			if len(violations) != 1 || !strings.Contains(violations[0], tt.testName) {
				t.Fatalf("violations = %v, want one violation naming %s", violations, tt.testName)
			}
		})
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
	conventions := parseMakefileConventions(string(contents))
	for _, tt := range []struct {
		target string
		want   []string
	}{
		{target: "test-unit", want: []string{"go", "test", "./..."}},
		{target: "test-integration", want: []string{"go", "test", "-tags=" + testlane.IntegrationBuildTag, "-run", "'" + testlane.IntegrationTestPattern + "'", "./..."}},
		{target: "quality", want: []string{"go", "run", "./cmd/testcoverage"}},
	} {
		t.Run(tt.target, func(t *testing.T) {
			commands := conventions.goCommands(tt.target)
			require.Len(t, commands, 1, "effective Go commands for %s", tt.target)
			require.GreaterOrEqual(t, len(commands[0]), 2)
			require.Equal(t, tt.want[:2], commands[0][:2])
			arguments, expected := slices.Clone(commands[0][2:]), slices.Clone(tt.want[2:])
			slices.Sort(arguments)
			slices.Sort(expected)
			if !slices.Equal(arguments, expected) {
				t.Fatalf("effective Go commands for %s = %v, want %v", tt.target, commands, tt.want)
			}
		})
	}
	if strings.Join(conventions.dependencies["test"], " ") != "test-unit" {
		t.Fatalf("test dependencies = %v", conventions.dependencies["test"])
	}
	for _, target := range []string{"fmt", "quality", "lint", "build"} {
		require.Contains(t, conventions.dependencies["check"], target)
	}
}

type makefileConventions struct {
	variables    map[string]string
	dependencies map[string][]string
	recipes      map[string][]string
}

func parseMakefileConventions(contents string) makefileConventions {
	conventions := makefileConventions{variables: map[string]string{}, dependencies: map[string][]string{}, recipes: map[string][]string{}}
	var targets []string
	for _, raw := range strings.Split(contents, "\n") {
		if strings.HasPrefix(raw, "\t") {
			command := strings.TrimSpace(strings.TrimLeft(raw, "\t@"))
			for _, target := range targets {
				conventions.recipes[target] = append(conventions.recipes[target], command)
			}
			continue
		}
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if line == "" {
			continue
		}
		targets = nil
		if name, value, ok := strings.Cut(line, ":="); ok {
			conventions.variables[strings.TrimSpace(name)] = strings.TrimSpace(value)
		} else if names, deps, ok := strings.Cut(line, ":"); ok {
			targets = strings.Fields(names)
			for _, target := range targets {
				conventions.dependencies[target] = strings.Fields(deps)
			}
		}
	}
	return conventions
}

func (m makefileConventions) goCommands(target string) [][]string {
	var commands [][]string
	for _, recipe := range m.recipes[target] {
		for range len(m.variables) {
			for name, value := range m.variables {
				recipe = strings.ReplaceAll(recipe, "$("+name+")", value)
			}
		}
		fields := strings.Fields(recipe)
		if len(fields) >= 2 && fields[0] == "go" {
			commands = append(commands, fields)
		}
	}
	return commands
}

func repositoryRoot(t *testing.T) string {
	t.Helper()

	_, path, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(path), "..", ".."))
}
