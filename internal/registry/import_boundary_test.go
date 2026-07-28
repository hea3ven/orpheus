package registry_test

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

func TestTaskPackageDoesNotImportRegistry(t *testing.T) {
	t.Parallel()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	taskDir := filepath.Join(filepath.Dir(file), "..", "task")
	var violations []string
	err := filepath.WalkDir(taskDir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imported := range parsed.Imports {
			value, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if value == "github.com/hea3ven/orpheus/internal/registry" {
				violations = append(violations, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan task imports: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("internal/task must not import internal/registry:\n%s", strings.Join(violations, "\n"))
	}
}
