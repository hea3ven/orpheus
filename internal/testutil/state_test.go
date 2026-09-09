package testutil_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

type fixtureEnvironment struct {
	configHome string
}

func (e fixtureEnvironment) PropagateEnvironment(environment map[string]string) {
	if e.configHome != "" {
		environment["XDG_CONFIG_HOME"] = e.configHome
	}
}

func TestWriteConfigYAMLWritesFixtureUnderOrpheusDirectory(t *testing.T) {
	configHome := testutil.CanonicalTempDir(t)

	err := testutil.WriteConfigYAML(
		fixtureEnvironment{configHome: configHome},
		"nested/config.yaml",
		map[string]string{"name": "example"},
	)

	if err != nil {
		t.Fatalf("write config YAML: %v", err)
	}
	path := filepath.Join(configHome, "orpheus", "nested", "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config fixture: %v", err)
	}
	if got, want := string(data), "name: example\n"; got != want {
		t.Fatalf("config fixture = %q, want %q", got, want)
	}
}

func TestWriteConfigYAMLRequiresPropagatedConfigHome(t *testing.T) {
	err := testutil.WriteConfigYAML(fixtureEnvironment{}, "config.yaml", map[string]string{})

	if err == nil || !strings.Contains(err.Error(), "XDG_CONFIG_HOME is required") {
		t.Fatalf("error = %v, want missing XDG config home", err)
	}
}

func TestWriteConfigYAMLReportsEncodingFailure(t *testing.T) {
	err := testutil.WriteConfigYAML(
		fixtureEnvironment{configHome: testutil.CanonicalTempDir(t)},
		"config.yaml",
		map[string]any{"unsupported": make(chan int)},
	)

	if err == nil || !strings.Contains(err.Error(), "encode config fixture") {
		t.Fatalf("error = %v, want encoding failure", err)
	}
}
