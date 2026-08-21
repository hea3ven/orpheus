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
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// IntegrationBuildTag selects integration test sources.
	IntegrationBuildTag = "integration"
	// IntegrationTestPattern selects integration test scenarios by name.
	IntegrationTestPattern = "^TestIntegration"

	directTempDirSuppression    = "nolint:forbidigo"
	absoluteTmpPathSuppression  = "orpheus:allow-absolute-tmp-path"
	canonicalTempDirReplacement = "testutil.CanonicalTempDir(t)"
)

// ValidateIntegrationSources reports test bodies that would be omitted from, or
// selected by both, the unit and integration lanes. Untagged test bodies belong
// to the unit lane; integration-tagged bodies belong only to the integration
// lane. The name convention makes the integration target's Go name filter agree
// with that structural membership.
func ValidateIntegrationSources(root string) ([]string, error) {
	return validateTestSources(root, true, false)
}

// ValidateTestSources reports all repository test-source convention violations
// in one pass. It combines test-lane and temporary-path validation so routine
// validation only parses each test source once.
func ValidateTestSources(root string) ([]string, error) {
	return validateTestSources(root, true, true)
}

// ValidateTemporaryPathSources reports test-source paths that can retain a
// platform-specific temporary-directory alias. Tests must use
// testutil.CanonicalTempDir instead of testing.TB.TempDir. Absolute /tmp paths
// are fixture values only when a same-line documented suppression explains why
// their identity is intentionally irrelevant.
func ValidateTemporaryPathSources(root string) ([]string, error) {
	return validateTestSources(root, false, true)
}

func validateTestSources(root string, validateIntegration bool, validateTemporaryPaths bool) ([]string, error) {
	var violations []string
	fileSet := token.NewFileSet()
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

		sourceViolations, err := validateTestSource(path, fileSet, validateIntegration, validateTemporaryPaths)
		if err != nil {
			return err
		}
		violations = append(violations, sourceViolations...)
		return nil
	})
	return violations, err
}

func validateTestSource(path string, fileSet *token.FileSet, validateIntegration bool, validateTemporaryPaths bool) ([]string, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	file, err := parser.ParseFile(fileSet, path, contents, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var violations []string
	if validateIntegration {
		integrationTagged, err := hasIntegrationBuildConstraint(path, contents)
		if err != nil {
			return nil, err
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
	}
	if validateTemporaryPaths {
		violations = append(violations, temporaryPathViolations(file, fileSet)...)
	}
	return violations, nil
}

func temporaryPathViolations(file *ast.File, fileSet *token.FileSet) []string {
	var violations []string
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			if isDirectTestingTempDirCall(node) && !hasSameLineSuppression(file.Comments, fileSet, node.Pos(), directTempDirSuppression, "//") {
				violations = append(violations, temporaryPathViolation(fileSet, node.Pos(), "direct TempDir call; use "+canonicalTempDirReplacement))
			}
		case *ast.BasicLit:
			if hasAbsoluteTmpPathToken(node) && !hasSameLineSuppression(file.Comments, fileSet, node.Pos(), absoluteTmpPathSuppression, "--") {
				violations = append(violations, temporaryPathViolation(fileSet, node.Pos(), "absolute /tmp fixture path requires "+absoluteTmpPathSuppression+" -- reason"))
			}
		}
		return true
	})
	return violations
}

func isDirectTestingTempDirCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "TempDir" || len(call.Args) != 0 {
		return false
	}
	receiver, ok := selector.X.(*ast.Ident)
	return ok && receiver.Name != "os"
}

func hasAbsoluteTmpPathToken(literal *ast.BasicLit) bool {
	if literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	return err == nil && containsAbsoluteTmpPathToken(value)
}

func containsAbsoluteTmpPathToken(value string) bool {
	const tmpPathPrefix = "/tmp/"

	for offset := 0; ; {
		index := strings.Index(value[offset:], tmpPathPrefix)
		if index == -1 {
			return false
		}
		index += offset
		if index == 0 || isAbsoluteTmpPathBoundary(lastRune(value[:index])) {
			return true
		}
		offset = index + len(tmpPathPrefix)
	}
}

func isAbsoluteTmpPathBoundary(previous rune) bool {
	return !unicode.IsLetter(previous) && !unicode.IsNumber(previous) && previous != '_' && previous != '.'
}

func lastRune(value string) rune {
	previous, _ := utf8.DecodeLastRuneInString(value)
	return previous
}

func hasSameLineSuppression(comments []*ast.CommentGroup, fileSet *token.FileSet, position token.Pos, directive string, separator string) bool {
	line := fileSet.Position(position).Line
	for _, group := range comments {
		for _, comment := range group.List {
			if fileSet.Position(comment.Pos()).Line != line {
				continue
			}
			text := strings.TrimSpace(strings.TrimPrefix(comment.Text, "//"))
			if !strings.HasPrefix(text, directive) {
				continue
			}
			explanation := strings.TrimSpace(strings.TrimPrefix(text, directive))
			if strings.HasPrefix(explanation, separator) && strings.TrimSpace(strings.TrimPrefix(explanation, separator)) != "" {
				return true
			}
		}
	}
	return false
}

func temporaryPathViolation(fileSet *token.FileSet, position token.Pos, message string) string {
	location := fileSet.Position(position)
	return fmt.Sprintf("%s:%d: %s", location.Filename, location.Line, message)
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
