package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"gopkg.in/yaml.v3"
)

func executeLint(policy localQualityPolicy, stdout, stderr io.Writer) error {
	contents, err := os.ReadFile(".golangci.yml")
	if err != nil {
		return err
	}
	configuration, err := lintConfiguration(contents, policy.PackageExclusions)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "orpheus-lint-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	config := filepath.Join(work, ".golangci.yml")
	if err := os.WriteFile(config, configuration, 0o600); err != nil {
		return err
	}
	command := exec.Command("golangci-lint", "run", "--config", config, "./...")
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, stdout, stderr
	return command.Run()
}

// lintConfiguration filters findings, not package loading or type checking.
// Anchoring at the filename boundary excludes only the named package, not its
// descendants or similarly named siblings.
func lintConfiguration(contents []byte, exclusions []string) ([]byte, error) {
	if err := validatePackageExclusions(exclusions); err != nil {
		return nil, err
	}
	var config map[string]any
	if err := yaml.Unmarshal(contents, &config); err != nil {
		return nil, err
	}
	run, err := lintConfigSection(config, "run")
	if err != nil {
		return nil, err
	}
	// A temporary config must not make regex paths relative to its directory.
	run["relative-path-mode"] = "gomod"
	paths, err := lintConfigSection(config, "linters", "exclusions")
	if err != nil {
		return nil, err
	}
	var patterns []string
	for _, name := range exclusions {
		patterns = append(patterns, "^"+regexp.QuoteMeta(name)+`/[^/]+\.go$`)
	}
	// Keep other exclusion rules and linter settings from the base config.
	if _, found := paths["paths"]; found {
		return nil, fmt.Errorf("lint package exclusions must be configured only in .quality.yml, not linters.exclusions.paths")
	}
	if len(patterns) > 0 {
		paths["paths"] = patterns
	}
	return yaml.Marshal(config)
}

func lintConfigSection(config map[string]any, keys ...string) (map[string]any, error) {
	if config == nil {
		return nil, fmt.Errorf("lint configuration must be a mapping")
	}
	for _, key := range keys {
		value, found := config[key]
		if !found {
			value = make(map[string]any)
			config[key] = value
		}
		section, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("lint configuration %s must be a mapping", key)
		}
		config = section
	}
	return config, nil
}
