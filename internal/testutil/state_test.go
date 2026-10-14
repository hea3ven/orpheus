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

func TestWriteConfigYAMLUsesOnlyPropagatedFixtureRoot(t *testing.T) {
	for _, tt := range []struct {
		name        string
		missingRoot bool
		value       any
		wantError   string
	}{
		{name: "writes nested YAML", value: map[string]string{"name": "example"}},
		{name: "requires propagated root", missingRoot: true, value: map[string]string{}, wantError: "XDG_CONFIG_HOME is required"},
		{name: "reports encoding failure", value: map[string]any{"unsupported": make(chan int)}, wantError: "encode config fixture"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := testutil.CanonicalTempDir(t)
			environment := fixtureEnvironment{configHome: root}
			if tt.missingRoot {
				environment.configHome = ""
			}

			err := testutil.WriteConfigYAML(environment, "nested/config.yaml", tt.value)

			path := filepath.Join(root, "orpheus", "nested", "config.yaml")
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("write error = %v, want %q", err, tt.wantError)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("failed write created fixture: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("write config YAML: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read config fixture: %v", err)
			}
			if string(data) != "name: example\n" {
				t.Fatalf("config fixture = %q", data)
			}
		})
	}
}
