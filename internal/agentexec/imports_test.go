package agentexec_test

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestPackageDoesNotImportHigherLevelPackages(t *testing.T) {
	t.Parallel()

	forbidden := []string{
		"github.com/hea3ven/orpheus/internal/agent",
		"github.com/hea3ven/orpheus/internal/cli",
		"github.com/hea3ven/orpheus/internal/review",
		"github.com/hea3ven/orpheus/internal/workflow",
	}
	for _, path := range goFiles(t, ".") {
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, imported := range file.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatalf("unquote import %s in %s: %v", imported.Path.Value, path, err)
			}
			for _, forbiddenImport := range forbidden {
				if value == forbiddenImport || strings.HasPrefix(value, forbiddenImport+"/") {
					t.Fatalf("internal/agentexec must not import %s; found in %s", value, path)
				}
			}
		}
	}
}

func goFiles(t *testing.T, dir string) []string {
	t.Helper()

	matches, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob go files: %v", err)
	}
	files := make([]string, 0, len(matches))
	for _, match := range matches {
		if strings.HasSuffix(match, "_test.go") {
			continue
		}
		files = append(files, match)
	}
	return files
}
