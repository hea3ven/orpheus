package git_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestProductionGitExecBoundary(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))

	var violations []string
	err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if insideInternalGit(repoRoot, path) {
			return nil
		}

		fileViolations, err := gitExecViolations(repoRoot, path)
		if err != nil {
			return err
		}
		violations = append(violations, fileViolations...)
		return nil
	})
	if err != nil {
		t.Fatalf("scan production Go files: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("production git exec calls outside internal/git:\n%s", strings.Join(violations, "\n"))
	}
}

func insideInternalGit(repoRoot string, path string) bool {
	relative, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return false
	}
	return relative == filepath.Join("internal", "git") ||
		strings.HasPrefix(relative, filepath.Join("internal", "git")+string(filepath.Separator))
}

func gitExecViolations(repoRoot string, path string) ([]string, error) {
	fileset := token.NewFileSet()
	file, err := parser.ParseFile(fileset, path, nil, 0)
	if err != nil {
		return nil, err
	}

	execNames := execImportNames(file)
	if len(execNames) == 0 {
		return nil, nil
	}

	var violations []string
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || !execNames[receiver.Name] {
			return true
		}
		if selector.Sel.Name != "Command" && selector.Sel.Name != "CommandContext" {
			return true
		}
		if len(call.Args) == 0 || selector.Sel.Name == "CommandContext" && len(call.Args) < 2 {
			return true
		}
		gitArgIndex := 0
		if selector.Sel.Name == "CommandContext" {
			gitArgIndex = 1
		}
		if !isStringLiteral(call.Args[gitArgIndex], "git") {
			return true
		}

		position := fileset.Position(call.Pos())
		relative, err := filepath.Rel(repoRoot, position.Filename)
		if err != nil {
			relative = position.Filename
		}
		violations = append(violations, relative+":"+strconv.Itoa(position.Line))
		return true
	})
	return violations, nil
}

func execImportNames(file *ast.File) map[string]bool {
	names := map[string]bool{}
	for _, spec := range file.Imports {
		pathValue, err := strconv.Unquote(spec.Path.Value)
		if err != nil || pathValue != "os/exec" {
			continue
		}
		name := "exec"
		if spec.Name != nil {
			name = spec.Name.Name
		}
		names[name] = true
	}
	return names
}

func isStringLiteral(expr ast.Expr, want string) bool {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	return err == nil && value == want
}
