// Package testlane defines structural test-lane conventions shared by test tooling.
package testlane

import (
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// IntegrationBuildTag selects integration test sources.
	IntegrationBuildTag = "integration"
	// IntegrationTestPattern selects integration test scenarios by name.
	IntegrationTestPattern = "^TestIntegration"
)

// ValidateIntegrationSources reports test bodies that would be omitted from, or
// selected by both, the unit and integration lanes. Untagged test bodies belong
// to the unit lane; integration-tagged bodies belong only to the integration
// lane. The name convention makes the integration target's Go name filter agree
// with that structural membership.
func ValidateIntegrationSources(root string) ([]string, error) {
	var violations []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}

		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		integrationTagged, err := hasIntegrationBuildConstraint(path, contents)
		if err != nil {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, contents, 0)
		if err != nil {
			return err
		}
		for _, name := range topLevelTestNames(file) {
			isIntegrationName := strings.HasPrefix(name, "TestIntegration")
			switch {
			case integrationTagged && !isIntegrationName:
				violations = append(violations, fmt.Sprintf("%s: integration-tagged top-level test %s must begin TestIntegration", path, name))
			case !integrationTagged && isIntegrationName:
				violations = append(violations, fmt.Sprintf("%s: %s must be declared in a //go:build integration test source", path, name))
			}
		}
		return nil
	})
	return violations, err
}

func hasIntegrationBuildConstraint(path string, contents []byte) (bool, error) {
	for _, line := range strings.Split(string(contents), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "//go:build ") {
			continue
		}
		expr, err := constraint.Parse(line)
		if err != nil {
			return false, fmt.Errorf("parse build constraint in %s: %w", path, err)
		}
		if !mentionsPositiveIntegrationTag(expr, false) {
			return false, nil
		}
		if expr.String() != IntegrationBuildTag {
			return false, fmt.Errorf("%s: integration test sources must use //go:build %s", path, IntegrationBuildTag)
		}
		return true, nil
	}
	return false, nil
}

func mentionsPositiveIntegrationTag(expr constraint.Expr, negated bool) bool {
	switch expr := expr.(type) {
	case *constraint.TagExpr:
		return !negated && expr.Tag == IntegrationBuildTag
	case *constraint.NotExpr:
		return mentionsPositiveIntegrationTag(expr.X, !negated)
	case *constraint.AndExpr:
		return mentionsPositiveIntegrationTag(expr.X, negated) || mentionsPositiveIntegrationTag(expr.Y, negated)
	case *constraint.OrExpr:
		return mentionsPositiveIntegrationTag(expr.X, negated) || mentionsPositiveIntegrationTag(expr.Y, negated)
	default:
		return false
	}
}

func topLevelTestNames(file *ast.File) []string {
	var names []string
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Recv != nil || function.Name.Name == "TestMain" || !isGoTestName(function.Name.Name) {
			continue
		}
		names = append(names, function.Name.Name)
	}
	return names
}

func isGoTestName(name string) bool {
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	if len(name) == len("Test") {
		return true
	}
	runeAfterPrefix, _ := utf8.DecodeRuneInString(name[len("Test"):])
	return !unicode.IsLower(runeAfterPrefix)
}
