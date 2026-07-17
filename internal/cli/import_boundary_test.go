package cli_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestApplicationPackagesDoNotImportCLI(t *testing.T) {
	t.Parallel()
	repoRoot := testRepoRoot(t)
	var violations []string
	if err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skipImportBoundaryDir(entry) {
			return filepath.SkipDir
		}
		if skipImportBoundaryPath(repoRoot, path, entry) {
			return nil
		}
		importsCLI, err := fileImportsCLI(path)
		if err != nil {
			return err
		}
		if importsCLI {
			relative, _ := filepath.Rel(repoRoot, path)
			violations = append(violations, relative)
		}
		return nil
	}); err != nil {
		t.Fatalf("scan imports: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("application packages must not import internal/cli:\n%s", strings.Join(violations, "\n"))
	}
}

func testRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func skipImportBoundaryDir(entry fs.DirEntry) bool {
	return entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "vendor")
}

func skipImportBoundaryPath(repoRoot string, path string, entry fs.DirEntry) bool {
	if entry.IsDir() {
		return true
	}
	if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
		return true
	}
	relative, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return true
	}
	return strings.HasPrefix(relative, filepath.Join("internal", "cli")+string(filepath.Separator)) ||
		strings.HasPrefix(relative, filepath.Join("cmd", "orpheus")+string(filepath.Separator))
}

func fileImportsCLI(path string) (bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		return false, err
	}
	for _, imported := range file.Imports {
		value, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			return false, err
		}
		if value == "github.com/hea3ven/orpheus/internal/cli" ||
			strings.HasPrefix(value, "github.com/hea3ven/orpheus/internal/cli/") {
			return true, nil
		}
	}
	return false, nil
}
