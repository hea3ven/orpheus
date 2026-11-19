package main

import (
	"fmt"
	"go/ast"
	"go/types"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

// Ruleguard handles qualified selectors but does not visit bare identifiers.
// Check unqualified function references with the type checker's use map.
func lintCollaboratorFunctions(exclusions []string, output io.Writer) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	loaded, err := packages.Load(&packages.Config{Mode: packages.LoadSyntax},
		"./internal/agent", "./internal/cli", "./internal/doctor", "./internal/workflow/...")
	if err != nil {
		return fmt.Errorf("load collaborator lint packages: %w", err)
	}
	var diagnostics []string
	for _, pkg := range loaded {
		if !strings.HasPrefix(pkg.PkgPath, modulePath+"/") || slices.Contains(exclusions, strings.TrimPrefix(pkg.PkgPath, modulePath+"/")) {
			continue
		}
		if len(pkg.Errors) > 0 {
			return fmt.Errorf("load collaborator lint package %s: %w", pkg.PkgPath, pkg.Errors[0])
		}
		diagnostics = append(diagnostics, collaboratorFunctionUses(pkg, root)...)
	}
	if len(diagnostics) == 0 {
		return nil
	}
	slices.Sort(diagnostics)
	for _, diagnostic := range diagnostics {
		if _, err := fmt.Fprintln(output, diagnostic); err != nil {
			return err
		}
	}
	return fmt.Errorf("%d production function references bypass collaborator wiring", len(diagnostics))
}

func collaboratorFunctionUses(pkg *packages.Package, root string) []string {
	qualified := make(map[*ast.Ident]bool)
	for _, file := range pkg.Syntax {
		for node := range ast.Preorder(file) {
			if selector, ok := node.(*ast.SelectorExpr); ok {
				qualified[selector.Sel] = true
			}
		}
	}

	var diagnostics []string
	for ident, object := range pkg.TypesInfo.Uses {
		fn, ok := object.(*types.Func)
		if !ok || qualified[ident] || fn.Pkg() == nil || fn.Parent() != fn.Pkg().Scope() || !isProductionCollaboratorFunction(fn) {
			continue
		}
		location := pkg.Fset.PositionFor(ident.Pos(), false)
		if collaboratorAssemblyFile(pkg.PkgPath, filepath.Base(location.Filename)) {
			continue
		}
		if filename, err := filepath.Rel(root, location.Filename); err == nil {
			location.Filename = filename
		}
		diagnostics = append(diagnostics, fmt.Sprintf("%s: use the wired collaborator instead of selecting %s in execution code", location, fn.FullName()))
	}
	return diagnostics
}

func isProductionCollaboratorFunction(fn *types.Func) bool {
	switch fn.Pkg().Path() {
	case modulePath + "/internal/agent":
		return fn.Name() == "CaptureUsage"
	case modulePath + "/internal/agentexec":
		return fn.Name() == "ProbePID"
	case modulePath + "/internal/git":
		return fn.Name() == "ShortStatus"
	case modulePath + "/internal/workflow/review":
		return fn.Name() == "RunPipeline" || fn.Name() == "captureLocalCandidate"
	default:
		return false
	}
}

// Keep these assembly exceptions in sync with lint/collaborators.go.
func collaboratorAssemblyFile(pkgPath, filename string) bool {
	switch pkgPath {
	case modulePath + "/internal/cli":
		return filename == "invocation.go" || filename == "command_options.go"
	case modulePath + "/internal/doctor", modulePath + "/internal/workflow/review":
		return filename == "effects.go"
	default:
		return false
	}
}
