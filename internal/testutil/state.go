package testutil

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

const orpheusStateDirectory = "orpheus"

type environmentPropagator interface {
	PropagateEnvironment(map[string]string)
}

// WriteConfigYAML writes an OS-backed Orpheus config fixture.
func WriteConfigYAML(paths environmentPropagator, rel string, value any) error {
	environment := make(map[string]string, 2)
	paths.PropagateEnvironment(environment)
	configHome := environment["XDG_CONFIG_HOME"]
	if configHome == "" {
		return fmt.Errorf("XDG_CONFIG_HOME is required")
	}

	data, err := marshalFixtureYAML(value)
	if err != nil {
		return fmt.Errorf("encode config fixture: %w", err)
	}
	path := filepath.Join(configHome, orpheusStateDirectory, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config fixture parent: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config fixture: %w", err)
	}
	return nil
}

func marshalFixtureYAML(value any) (data []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("%v", recovered)
		}
	}()
	return yaml.Marshal(value)
}
