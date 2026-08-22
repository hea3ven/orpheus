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

	// SummaryGuidanceStyleTyped guides agents to write conventional typed summaries.
	SummaryGuidanceStyleTyped = "typed"
	// SummaryGuidanceStyleCapitalized guides agents to write capitalized plain-English summaries.
	SummaryGuidanceStyleCapitalized = "capitalized"
)

// Policy is the resolved publication configuration used by publication consumers.
type Policy struct {
	SummaryGuidance      string
	SummaryGuidanceStyle string
	TitleTemplate        string
}

// Config is the publication section of Orpheus' global configuration.
type Config struct {
	IntegrationFlow      IntegrationFlow
	SummaryGuidance      string
	SummaryGuidanceStyle string
	TitleTemplate        string
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
		IntegrationFlow      IntegrationFlow `yaml:"integration_flow"`
		SummaryGuidance      string          `yaml:"summary_guidance"`
		SummaryGuidanceStyle string          `yaml:"summary_guidance_style"`
		TitleTemplate        string          `yaml:"title_template"`
	}
	if err := raw.Publication.Decode(&nested); err != nil {
		return err
	}
	c.IntegrationFlow = nested.IntegrationFlow
	c.SummaryGuidance = nested.SummaryGuidance
	c.SummaryGuidanceStyle = nested.SummaryGuidanceStyle
	c.TitleTemplate = nested.TitleTemplate
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
	normalized, err := config.normalized()
	if err != nil {
		return Config{}, fmt.Errorf("load publication configuration from config.yaml: %w", err)
	}
	return normalized, nil
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

// ValidateSummaryGuidanceStyle checks whether style is one of the supported named styles.
func ValidateSummaryGuidanceStyle(style string) error {
	switch strings.TrimSpace(style) {
	case "", SummaryGuidanceStyleTyped, SummaryGuidanceStyleCapitalized:
		return nil
	default:
		return fmt.Errorf(
			"summary_guidance_style %q is invalid; expected %q or %q",
			style,
			SummaryGuidanceStyleTyped,
			SummaryGuidanceStyleCapitalized,
		)
	}
}

// Policy returns the global publication settings without applying compatibility defaults.
func (c Config) Policy() Policy {
	return Policy{
		SummaryGuidance:      c.SummaryGuidance,
		SummaryGuidanceStyle: c.SummaryGuidanceStyle,
		TitleTemplate:        c.TitleTemplate,
	}
}

// ResolvePolicy applies repository, global, and compatibility publication defaults.
// A custom summary guidance string remains authoritative over the resolved named
// style when consumers instruct agents how to compose completion summaries.
func ResolvePolicy(repository, global Policy) Policy {
	policy := Policy{
		SummaryGuidance:      firstConfigured(repository.SummaryGuidance, global.SummaryGuidance),
		SummaryGuidanceStyle: firstConfigured(repository.SummaryGuidanceStyle, global.SummaryGuidanceStyle),
		TitleTemplate:        firstConfigured(repository.TitleTemplate, global.TitleTemplate),
	}
	if policy.SummaryGuidanceStyle == "" {
		policy.SummaryGuidanceStyle = SummaryGuidanceStyleTyped
	}
	return policy
}

func (c Config) normalized() (Config, error) {
	c.IntegrationFlow = normalizeIntegrationFlow(c.IntegrationFlow)
	c.SummaryGuidance = strings.TrimSpace(c.SummaryGuidance)
	c.SummaryGuidanceStyle = strings.TrimSpace(c.SummaryGuidanceStyle)
	c.TitleTemplate = strings.TrimSpace(c.TitleTemplate)
	if err := ValidateIntegrationFlow(c.IntegrationFlow); err != nil {
		return Config{}, err
	}
	if err := ValidateSummaryGuidanceStyle(c.SummaryGuidanceStyle); err != nil {
		return Config{}, fmt.Errorf("publication %w", err)
	}
	if err := ValidateTitleTemplate(c.TitleTemplate); err != nil {
		return Config{}, fmt.Errorf("publication title_template is invalid: %w", err)
	}
	return c, nil
}

func firstConfigured(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
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
