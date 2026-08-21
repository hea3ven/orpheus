package taskbranch

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hea3ven/orpheus/internal/state"
	"gopkg.in/yaml.v3"
)

const configFile = "config.yaml"

// Config is the task branch section of Orpheus' shared global configuration.
type Config struct {
	Template string
}

// UnmarshalYAML decodes the tasks.branch_template shape from config.yaml.
func (c *Config) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Tasks yaml.Node `yaml:"tasks"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*c = Config{}
	if raw.Tasks.Kind == 0 {
		return nil
	}
	var nested struct {
		Template string `yaml:"branch_template"`
	}
	if err := raw.Tasks.Decode(&nested); err != nil {
		return err
	}
	c.Template = nested.Template
	return nil
}

// LoadConfig reads and validates task branch configuration. A missing shared
// configuration file leaves the global template unset so callers use the
// compatibility default.
func LoadConfig(paths state.Paths) (Config, error) {
	var config Config
	if err := paths.ReadConfigYAML(configFile, &config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("load task branch configuration from %s: %w", configFile, err)
	}
	config.Template = strings.TrimSpace(config.Template)
	if err := ValidateTemplate(config.Template); err != nil {
		return Config{}, fmt.Errorf("load task branch configuration from %s: %w", configFile, err)
	}
	return config, nil
}
