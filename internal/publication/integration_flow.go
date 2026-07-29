package publication

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/hea3ven/orpheus/internal/state"
	"gopkg.in/yaml.v3"
)

// IntegrationFlow identifies how approved task work reaches the default branch.
type IntegrationFlow string

const (
	// IntegrationFlowPullRequest publishes a task branch and creates a pull request.
	IntegrationFlowPullRequest IntegrationFlow = "pull-request"
	// IntegrationFlowDirectMerge merges the reviewed task branch directly into the default branch.
	IntegrationFlowDirectMerge IntegrationFlow = "direct-merge"
)

// Config is the publication section of Orpheus' global configuration.
type Config struct {
	IntegrationFlow IntegrationFlow
}

// UnmarshalYAML decodes the publication section from the shared global configuration file.
func (c *Config) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Publication yaml.Node `yaml:"publication"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}
	*c = Config{}
	if raw.Publication.Kind == 0 {
		return nil
	}
	var nested struct {
		IntegrationFlow IntegrationFlow `yaml:"integration_flow"`
	}
	if err := raw.Publication.Decode(&nested); err != nil {
		return err
	}
	c.IntegrationFlow = nested.IntegrationFlow
	return nil
}

// LoadConfig reads the global publication configuration. Missing configuration
// retains the compatible pull-request default.
func LoadConfig(paths state.Paths) (Config, error) {
	var config Config
	if err := paths.ReadConfigYAML("config.yaml", &config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{IntegrationFlow: IntegrationFlowPullRequest}, nil
		}
		return Config{}, fmt.Errorf("load publication configuration from config.yaml: %w", err)
	}
	if err := ValidateIntegrationFlow(config.IntegrationFlow); err != nil {
		return Config{}, fmt.Errorf("load publication configuration from config.yaml: %w", err)
	}
	config.IntegrationFlow = normalizeIntegrationFlow(config.IntegrationFlow)
	return config, nil
}

// ValidateIntegrationFlow accepts an inherited empty value or a supported flow.
func ValidateIntegrationFlow(flow IntegrationFlow) error {
	switch normalizeIntegrationFlow(flow) {
	case "", IntegrationFlowPullRequest, IntegrationFlowDirectMerge:
		return nil
	default:
		return fmt.Errorf("publication integration_flow %q is invalid; expected %q or %q", flow, IntegrationFlowPullRequest, IntegrationFlowDirectMerge)
	}
}

// ResolveIntegrationFlow applies manual, repository, global, and compatibility defaults.
func ResolveIntegrationFlow(manual, repository, global IntegrationFlow) IntegrationFlow {
	for _, candidate := range []IntegrationFlow{manual, repository, global} {
		if candidate = normalizeIntegrationFlow(candidate); candidate != "" {
			return candidate
		}
	}
	return IntegrationFlowPullRequest
}

func normalizeIntegrationFlow(flow IntegrationFlow) IntegrationFlow {
	return IntegrationFlow(strings.TrimSpace(string(flow)))
}
