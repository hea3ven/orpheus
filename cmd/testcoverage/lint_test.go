package main

import (
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const lintFixtureConfig = `version: "2"
run:
  tests: true
linters:
  default: none
  enable:
    - forbidigo
  settings:
    forbidigo:
      forbid:
        - pattern: '^fmt\.Println$'
  exclusions:
    rules:
      - path: '.*_test\.go$'
        linters: [funlen]
`

func TestLintConfigurationPreservesGuardsAndMatchesOnlyExcludedPackageFiles(t *testing.T) {
	contents, err := lintConfiguration([]byte(lintFixtureConfig), []string{"internal/infra", "cmd/dev.tool"})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Run struct {
			Tests            bool
			RelativePathMode string `yaml:"relative-path-mode"`
		}
		Linters struct {
			Default    string
			Enable     []string
			Settings   map[string]any
			Exclusions struct {
				Paths []string
				Rules []struct {
					Path    string
					Linters []string
				}
			}
		}
	}
	if err := yaml.Unmarshal(contents, &config); err != nil {
		t.Fatal(err)
	}
	if !config.Run.Tests || config.Run.RelativePathMode != "gomod" || config.Linters.Default != "none" || !reflect.DeepEqual(config.Linters.Enable, []string{"forbidigo"}) || config.Linters.Settings["forbidigo"] == nil || len(config.Linters.Exclusions.Rules) != 1 || config.Linters.Exclusions.Rules[0].Path != `.*_test\.go$` || !reflect.DeepEqual(config.Linters.Exclusions.Rules[0].Linters, []string{"funlen"}) {
		t.Fatalf("base lint settings changed: %s", contents)
	}
	if len(config.Linters.Exclusions.Paths) != 2 {
		t.Fatalf("exclusion paths = %v", config.Linters.Exclusions.Paths)
	}
	for _, test := range []struct {
		path     string
		excluded bool
	}{
		{path: "internal/infra/work.go", excluded: true},
		{path: "internal/infra/work_test.go", excluded: true},
		{path: "cmd/dev.tool/main.go", excluded: true},
		{path: "internal/infra-extra/work.go"},
		{path: "internal/infra/child/work.go"},
		{path: "other/internal/infra/work.go"},
		{path: "cmd/devXtool/main.go"},
		{path: "internal/product/work.go"},
	} {
		excluded := false
		for _, pattern := range config.Linters.Exclusions.Paths {
			excluded = excluded || regexp.MustCompile(pattern).MatchString(test.path)
		}
		if excluded != test.excluded {
			t.Errorf("path %s excluded = %t, want %t", test.path, excluded, test.excluded)
		}
	}
}

func TestLintConfigurationRejectsMalformedSectionsAndSeparatePackageLists(t *testing.T) {
	for _, contents := range []string{"[", "null", "run: []", "linters: []", "linters: {exclusions: []}", "linters: {exclusions: {paths: [internal/infra]}}"} {
		if _, err := lintConfiguration([]byte(contents), nil); err == nil {
			t.Fatalf("invalid lint config %q accepted", contents)
		}
	}
	if _, err := lintConfiguration([]byte(lintFixtureConfig), []string{"internal/*"}); err == nil || !strings.Contains(err.Error(), "exact module-relative") {
		t.Fatalf("invalid lint package exclusion error = %v", err)
	}
}
