package tasktarget_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const (
	tasktargetImportPath = "github.com/hea3ven/orpheus/internal/tasktarget"
	workflowImportPath   = "github.com/hea3ven/orpheus/internal/workflow"
)

var targetPolicySurface = map[string]bool{
	"Target":                 true,
	"TargetKind":             true,
	"TargetUnknown":          true,
	"TargetWorktreeTeam":     true,
	"TargetRepoRootTeam":     true,
	"TargetMainSolo":         true,
	"ExpectedTargets":        true,
	"ExpectedTargetsForTask": true,
	"ClassifyMetadataTarget": true,
	"ClassifyRunTarget":      true,
	"ClassifyGitFacts":       true,
}

var targetPolicyTypes = map[string]bool{
	"Target":          true,
	"TargetKind":      true,
	"ExpectedTargets": true,
}

func TestTargetPolicyDeclarationsStayInTaskTarget(t *testing.T) {
	t.Parallel()

	repoRoot := targetPolicyRepoRoot(t)

	var violations []string
	if err := filepath.WalkDir(repoRoot, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skipTargetPolicyBoundaryDir(entry) {
			return filepath.SkipDir
		}
		if skipTargetPolicyBoundaryPath(repoRoot, path, entry) {
			return nil
		}
		fileset := token.NewFileSet()
		file, err := parser.ParseFile(fileset, path, nil, 0)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return err
		}
		imports := importPathsByName(file)
		var fileViolations []string
		ast.Inspect(file, func(node ast.Node) bool {
			switch spec := node.(type) {
			case *ast.TypeSpec:
				if typeAliasToTargetPolicy(relative, imports, spec) {
					fileViolations = append(fileViolations, formatTargetPolicyViolation(repoRoot, fileset, spec.Pos(), "reexports target policy type"))
				}
			case *ast.ValueSpec:
				if valueAliasToTargetPolicy(relative, imports, spec) {
					fileViolations = append(fileViolations, formatTargetPolicyViolation(repoRoot, fileset, spec.Pos(), "reexports target policy operation"))
				}
			}
			return true
		})
		violations = append(violations, fileViolations...)
		return nil
	}); err != nil {
		t.Fatalf("scan target policy declarations: %v", err)
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("execution-target policy declarations must stay in internal/tasktarget:\n%s", strings.Join(violations, "\n"))
	}
}

func TestTargetPolicyConsumersDoNotUseWorkflowSurface(t *testing.T) {
	t.Parallel()

	repoRoot := targetPolicyRepoRoot(t)
	consumerDirs := []string{
		filepath.Join(repoRoot, "internal", "agent"),
		filepath.Join(repoRoot, "internal", "cli"),
		filepath.Join(repoRoot, "internal", "status"),
	}

	var violations []string
	for _, dir := range consumerDirs {
		if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if skipTargetPolicyBoundaryDir(entry) {
				return filepath.SkipDir
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			fileViolations, err := targetPolicyConsumerViolations(repoRoot, path)
			if err != nil {
				return err
			}
			violations = append(violations, fileViolations...)
			return nil
		}); err != nil {
			t.Fatalf("scan target policy consumers: %v", err)
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("target policy consumers must import internal/tasktarget directly instead of workflow target surface:\n%s", strings.Join(violations, "\n"))
	}
}

func targetPolicyRepoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func skipTargetPolicyBoundaryDir(entry fs.DirEntry) bool {
	return entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "vendor")
}

func skipTargetPolicyBoundaryPath(repoRoot string, path string, entry fs.DirEntry) bool {
	if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
		return true
	}
	relative, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return true
	}
	return relative == filepath.Join("internal", "tasktarget") ||
		strings.HasPrefix(relative, filepath.Join("internal", "tasktarget")+string(filepath.Separator))
}

func typeAliasToTargetPolicy(relative string, imports map[string]string, typed *ast.TypeSpec) bool {
	if !typed.Assign.IsValid() {
		return false
	}
	selector, ok := typed.Type.(*ast.SelectorExpr)
	if !ok || !targetPolicyTypes[selector.Sel.Name] {
		return false
	}
	ident, ok := selector.X.(*ast.Ident)
	if !ok {
		return false
	}
	importPath := imports[ident.Name]
	if importPath == tasktargetImportPath {
		return isWorkflowPath(relative) || targetPolicySurface[typed.Name.Name]
	}
	if importPath == workflowImportPath {
		return isTargetPolicyConsumerPath(relative) || targetPolicySurface[typed.Name.Name]
	}
	return false
}

func valueAliasToTargetPolicy(relative string, imports map[string]string, typed *ast.ValueSpec) bool {
	for _, value := range typed.Values {
		selector, ok := value.(*ast.SelectorExpr)
		if !ok || !targetPolicySurface[selector.Sel.Name] {
			continue
		}
		ident, ok := selector.X.(*ast.Ident)
		if !ok {
			continue
		}
		importPath := imports[ident.Name]
		switch importPath {
		case tasktargetImportPath:
			if isWorkflowPath(relative) {
				return true
			}
		case workflowImportPath:
			if isTargetPolicyConsumerPath(relative) {
				return true
			}
		}
	}
	return false
}

func targetPolicyConsumerViolations(repoRoot string, path string) ([]string, error) {
	fileset := token.NewFileSet()
	file, err := parser.ParseFile(fileset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	imports := importPathsByName(file)

	violations := []string{}
	if isAgentPath(repoRoot, path) {
		violations = append(violations, agentShapeTargetClassificationViolations(repoRoot, fileset, file, imports)...)
	}
	for name, importPath := range imports {
		if importPath == workflowImportPath && isAgentPath(repoRoot, path) {
			violations = append(violations, formatTargetPolicyViolation(repoRoot, fileset, file.Name.Pos(), "imports internal/workflow"))
		}
		if importPath != workflowImportPath {
			continue
		}
		violations = append(violations, workflowTargetPolicySelectorViolations(repoRoot, fileset, file, name)...)
	}
	return violations, nil
}

func agentShapeTargetClassificationViolations(
	repoRoot string,
	fileset *token.FileSet,
	file *ast.File,
	imports map[string]string,
) []string {
	violations := []string{}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "ClassifyRunTarget" {
			return true
		}
		ident, ok := selector.X.(*ast.Ident)
		if !ok || imports[ident.Name] != tasktargetImportPath {
			return true
		}
		violations = append(violations, formatTargetPolicyViolation(
			repoRoot,
			fileset,
			selector.Sel.Pos(),
			"uses shape-only tasktarget.ClassifyRunTarget",
		))
		return true
	})
	return violations
}

func workflowTargetPolicySelectorViolations(
	repoRoot string,
	fileset *token.FileSet,
	file *ast.File,
	workflowName string,
) []string {
	violations := []string{}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok || !targetPolicySurface[selector.Sel.Name] {
			return true
		}
		ident, ok := selector.X.(*ast.Ident)
		if !ok || ident.Name != workflowName {
			return true
		}
		violations = append(violations, formatTargetPolicyViolation(
			repoRoot,
			fileset,
			selector.Sel.Pos(),
			"uses workflow."+selector.Sel.Name,
		))
		return true
	})
	return violations
}

func importPathsByName(file *ast.File) map[string]string {
	imports := map[string]string{}
	for _, imported := range file.Imports {
		pathValue, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(pathValue)
		if imported.Name != nil && imported.Name.Name != "." && imported.Name.Name != "_" {
			name = imported.Name.Name
		}
		imports[name] = pathValue
	}
	return imports
}

func isWorkflowPath(relative string) bool {
	return relative == filepath.Join("internal", "workflow") ||
		strings.HasPrefix(relative, filepath.Join("internal", "workflow")+string(filepath.Separator))
}

func isAgentPath(repoRoot string, path string) bool {
	relative, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return false
	}
	return relative == filepath.Join("internal", "agent") ||
		strings.HasPrefix(relative, filepath.Join("internal", "agent")+string(filepath.Separator))
}

func isTargetPolicyConsumerPath(relative string) bool {
	for _, dir := range []string{"agent", "cli", "status", "workflow"} {
		prefix := filepath.Join("internal", dir)
		if relative == prefix || strings.HasPrefix(relative, prefix+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func formatTargetPolicyViolation(repoRoot string, fileset *token.FileSet, pos token.Pos, detail string) string {
	position := fileset.Position(pos)
	relative, err := filepath.Rel(repoRoot, position.Filename)
	if err != nil {
		relative = position.Filename
	}
	return relative + ":" + strconv.Itoa(position.Line) + " " + detail
}
