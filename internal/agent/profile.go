// Package agent defines backend-neutral agent prompt, profile, and launch helpers.
package agent

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hea3ven/orpheus/internal/state"
)

const (
	// ConfigFile is the Orpheus global configuration file containing agent profiles.
	ConfigFile = "config.yaml"

	promptToken = "{{prompt}}"
)

// Config is Orpheus' global agent profile configuration.
type Config struct {
	DefaultAgent string             `yaml:"default_agent"`
	Agents       map[string]Profile `yaml:"agents"`
}

// Profile describes one directly executed agent command.
type Profile struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args,omitempty"`
}

// CommandSnapshot is the resolved command line for one dispatch.
type CommandSnapshot struct {
	AgentName string
	Command   string
	Args      []string
}

// LoadConfig reads and validates the global Orpheus agent configuration.
func LoadConfig(paths state.Paths) (Config, error) {
	var config Config
	if err := paths.ReadConfigYAML(ConfigFile, &config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf(
				"load agent profiles from %s: file does not exist; create it with default_agent and agents entries: %w",
				ConfigFile,
				err,
			)
		}
		return Config{}, fmt.Errorf("load agent profiles from %s: %w", ConfigFile, err)
	}

	normalized, err := config.normalized()
	if err != nil {
		return Config{}, fmt.Errorf("load agent profiles from %s: %w", ConfigFile, err)
	}
	return normalized, nil
}

// ResolveCommand resolves selectedAgent, or default_agent when selectedAgent is blank,
// and applies bootstrap prompt interpolation.
func (c Config) ResolveCommand(selectedAgent string, prompt string) (CommandSnapshot, error) {
	normalized, err := c.normalized()
	if err != nil {
		return CommandSnapshot{}, err
	}

	agentName := strings.TrimSpace(selectedAgent)
	if agentName == "" {
		agentName = normalized.DefaultAgent
	}

	profile, ok := normalized.Agents[agentName]
	if !ok {
		return CommandSnapshot{}, fmt.Errorf(
			"agent profile %q is not configured; configured agents: %s",
			agentName,
			strings.Join(normalized.agentNames(), ", "),
		)
	}

	args := make([]string, len(profile.Args))
	for i, arg := range profile.Args {
		args[i] = interpolatePrompt(arg, prompt)
	}

	return CommandSnapshot{
		AgentName: agentName,
		Command:   interpolatePrompt(profile.Command, prompt),
		Args:      args,
	}, nil
}

func (c Config) normalized() (Config, error) {
	defaultAgent := strings.TrimSpace(c.DefaultAgent)
	if defaultAgent == "" {
		return Config{}, errors.New("default_agent is required")
	}

	if len(c.Agents) == 0 {
		return Config{}, errors.New("agents must define at least one profile")
	}

	agents := make(map[string]Profile, len(c.Agents))
	for rawName, rawProfile := range c.Agents {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return Config{}, errors.New("agent profile name is required")
		}
		if _, exists := agents[name]; exists {
			return Config{}, fmt.Errorf("agent profile %q is duplicated after trimming whitespace", name)
		}

		profile, err := normalizeProfile(name, rawProfile)
		if err != nil {
			return Config{}, err
		}
		agents[name] = profile
	}

	if _, ok := agents[defaultAgent]; !ok {
		return Config{}, fmt.Errorf(
			"default_agent %q does not match a configured agent; configured agents: %s",
			defaultAgent,
			strings.Join(agentNames(agents), ", "),
		)
	}

	return Config{DefaultAgent: defaultAgent, Agents: agents}, nil
}

func normalizeProfile(name string, profile Profile) (Profile, error) {
	command := strings.TrimSpace(profile.Command)
	if command == "" {
		return Profile{}, fmt.Errorf("agents.%s.command is required", name)
	}
	if err := validateInterpolationToken(fmt.Sprintf("agents.%s.command", name), command); err != nil {
		return Profile{}, err
	}

	args := make([]string, len(profile.Args))
	for i, arg := range profile.Args {
		if err := validateInterpolationToken(fmt.Sprintf("agents.%s.args[%d]", name, i), arg); err != nil {
			return Profile{}, err
		}
		args[i] = arg
	}

	return Profile{Command: command, Args: args}, nil
}

func validateInterpolationToken(field string, value string) error {
	withoutPromptTokens := strings.ReplaceAll(value, promptToken, "")
	if strings.Contains(withoutPromptTokens, "{{") || strings.Contains(withoutPromptTokens, "}}") {
		return fmt.Errorf(
			"%s contains an unsupported interpolation token; supported interpolation token: %s",
			field,
			promptToken,
		)
	}
	return nil
}

func interpolatePrompt(value string, prompt string) string {
	return strings.ReplaceAll(value, promptToken, prompt)
}

func (c Config) agentNames() []string {
	return agentNames(c.Agents)
}

func agentNames(agents map[string]Profile) []string {
	names := make([]string, 0, len(agents))
	for name := range agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
