// Package agent defines backend-neutral agent prompt, profile, and launch helpers.
package agent

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hea3ven/orpheus/internal/state"
	"gopkg.in/yaml.v3"
)

const (
	// ConfigFile is the Orpheus global configuration file containing agent profiles.
	ConfigFile = "config.yaml"

	promptToken      = "{{prompt}}"
	sessionNameToken = "{{session_name}}"
)

// Config is Orpheus' global agent profile configuration.
type Config struct {
	Defaults AgentDefaults      `yaml:"-"`
	Agents   map[string]Profile `yaml:"-"`
}

// AgentDefaults names purpose-specific default agent profiles.
type AgentDefaults struct {
	Implementer string `yaml:"implementer"`
	Reviewer    string `yaml:"reviewer"`
}

// UnmarshalYAML decodes the agents.defaults/profiles shape.
func (c *Config) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Agents  yaml.Node `yaml:"agents"`
		Reviews yaml.Node `yaml:"reviews"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}

	*c = Config{}
	if raw.Agents.Kind == 0 {
		return nil
	}

	var nested struct {
		Defaults AgentDefaults      `yaml:"defaults"`
		Profiles map[string]Profile `yaml:"profiles"`
	}
	if err := raw.Agents.Decode(&nested); err != nil {
		return err
	}
	c.Defaults = nested.Defaults
	c.Agents = nested.Profiles
	return nil
}

// Profile describes one directly executed agent command.
type Profile struct {
	Command     string   `yaml:"command"`
	Args        []string `yaml:"args,omitempty"`
	Interactive bool     `yaml:"interactive,omitempty"`
	Harness     string   `yaml:"harness,omitempty"`
	Model       string   `yaml:"model,omitempty"`
}

// CommandSnapshot is the resolved command line for one dispatch.
type CommandSnapshot struct {
	AgentName string
	Command   string
	Args      []string
	Harness   string
	Model     string
}

// UnmarshalYAML decodes an agent profile while preserving backwards
// compatibility for profiles that predate the interactive flag.
func (p *Profile) UnmarshalYAML(value *yaml.Node) error {
	type profile Profile
	decoded := profile{Interactive: true}
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	*p = Profile(decoded)
	return nil
}

// InterpolationValues are values available to agent profile command templates.
type InterpolationValues struct {
	Prompt      string
	SessionName string
}

// LoadConfig reads and validates the global Orpheus agent configuration.
func LoadConfig(paths state.Paths) (Config, error) {
	var config Config
	if err := paths.ReadConfigYAML(ConfigFile, &config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf(
				"load agent profiles from %s: file does not exist; create it with agents.defaults and agents.profiles entries: %w",
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

// ResolveCommand resolves selectedAgent, or agents.defaults.implementer when
// selectedAgent is blank, and applies bootstrap prompt interpolation.
func (c Config) ResolveCommand(selectedAgent string, prompt string) (CommandSnapshot, error) {
	return c.ResolveImplementerCommand(selectedAgent, prompt)
}

// ResolveImplementerCommand resolves selectedAgent, or agents.defaults.implementer
// when selectedAgent is blank.
func (c Config) ResolveImplementerCommand(selectedAgent string, prompt string) (CommandSnapshot, error) {
	return c.ResolveImplementerCommandWithValues(selectedAgent, InterpolationValues{Prompt: prompt})
}

// ResolveCommandWithValues resolves selectedAgent, or agents.defaults.implementer
// when selectedAgent is blank, and applies profile interpolation.
func (c Config) ResolveCommandWithValues(selectedAgent string, values InterpolationValues) (CommandSnapshot, error) {
	return c.ResolveImplementerCommandWithValues(selectedAgent, values)
}

// ResolveImplementerCommandWithValues resolves selectedAgent, or
// agents.defaults.implementer when selectedAgent is blank, and applies profile
// interpolation.
func (c Config) ResolveImplementerCommandWithValues(selectedAgent string, values InterpolationValues) (CommandSnapshot, error) {
	normalized, err := c.normalized()
	if err != nil {
		return CommandSnapshot{}, err
	}

	agentName := strings.TrimSpace(selectedAgent)
	if agentName == "" {
		agentName = strings.TrimSpace(normalized.Defaults.Implementer)
	}
	return normalized.resolveAgentProfile(agentName, values)
}

// ResolveImplementerProfile resolves selectedAgent, or agents.defaults.implementer
// when selectedAgent is blank, and returns the normalized profile.
func (c Config) ResolveImplementerProfile(selectedAgent string) (string, Profile, error) {
	normalized, err := c.normalized()
	if err != nil {
		return "", Profile{}, err
	}

	agentName := strings.TrimSpace(selectedAgent)
	if agentName == "" {
		agentName = strings.TrimSpace(normalized.Defaults.Implementer)
	}
	profile, ok := normalized.Agents[agentName]
	if !ok {
		return "", Profile{}, fmt.Errorf(
			"agent profile %q is not configured; configured agents: %s",
			agentName,
			strings.Join(normalized.agentNames(), ", "),
		)
	}
	return agentName, profile, nil
}

// ResolveReviewerCommand resolves selectedAgent, or agents.defaults.reviewer
// when selectedAgent is blank.
func (c Config) ResolveReviewerCommand(selectedAgent string, prompt string) (CommandSnapshot, error) {
	return c.ResolveReviewerCommandWithValues(selectedAgent, InterpolationValues{Prompt: prompt})
}

// ResolveReviewerCommandWithValues resolves selectedAgent, or
// agents.defaults.reviewer when selectedAgent is blank, and applies profile
// interpolation.
func (c Config) ResolveReviewerCommandWithValues(selectedAgent string, values InterpolationValues) (CommandSnapshot, error) {
	normalized, err := c.normalized()
	if err != nil {
		return CommandSnapshot{}, err
	}

	agentName := strings.TrimSpace(selectedAgent)
	if agentName == "" {
		agentName = strings.TrimSpace(normalized.Defaults.Reviewer)
	}
	if agentName == "" {
		return CommandSnapshot{}, errors.New("agents.defaults.reviewer is required for agent_review steps without an agent override")
	}
	return normalized.resolveAgentProfile(agentName, values)
}

// ResolveReviewerProfile resolves selectedAgent, or agents.defaults.reviewer
// when selectedAgent is blank, and returns the normalized profile.
func (c Config) ResolveReviewerProfile(selectedAgent string) (string, Profile, error) {
	normalized, err := c.normalized()
	if err != nil {
		return "", Profile{}, err
	}

	agentName := strings.TrimSpace(selectedAgent)
	if agentName == "" {
		agentName = strings.TrimSpace(normalized.Defaults.Reviewer)
	}
	if agentName == "" {
		return "", Profile{}, errors.New("agents.defaults.reviewer is required for agent_review steps without an agent override")
	}
	profile, ok := normalized.Agents[agentName]
	if !ok {
		return "", Profile{}, fmt.Errorf(
			"agent profile %q is not configured; configured agents: %s",
			agentName,
			strings.Join(normalized.agentNames(), ", "),
		)
	}
	return agentName, profile, nil
}

func (c Config) resolveAgentProfile(agentName string, values InterpolationValues) (CommandSnapshot, error) {
	profile, ok := c.Agents[agentName]
	if !ok {
		return CommandSnapshot{}, fmt.Errorf(
			"agent profile %q is not configured; configured agents: %s",
			agentName,
			strings.Join(c.agentNames(), ", "),
		)
	}

	args := make([]string, len(profile.Args))
	for i, arg := range profile.Args {
		args[i] = interpolateProfileValue(arg, values)
	}

	return CommandSnapshot{
		AgentName: agentName,
		Command:   interpolateProfileValue(profile.Command, values),
		Args:      args,
		Harness:   profile.Harness,
		Model:     profile.Model,
	}, nil
}

func (c Config) normalized() (Config, error) {
	defaults := AgentDefaults{
		Implementer: strings.TrimSpace(c.Defaults.Implementer),
		Reviewer:    strings.TrimSpace(c.Defaults.Reviewer),
	}
	if defaults.Implementer == "" {
		return Config{}, errors.New("agents.defaults.implementer is required")
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

	if _, ok := agents[defaults.Implementer]; !ok {
		return Config{}, fmt.Errorf(
			"agents.defaults.implementer %q does not match a configured agent; configured agents: %s",
			defaults.Implementer,
			strings.Join(agentNames(agents), ", "),
		)
	}
	if defaults.Reviewer != "" {
		if _, ok := agents[defaults.Reviewer]; !ok {
			return Config{}, fmt.Errorf(
				"agents.defaults.reviewer %q does not match a configured agent; configured agents: %s",
				defaults.Reviewer,
				strings.Join(agentNames(agents), ", "),
			)
		}
	}

	return Config{Defaults: defaults, Agents: agents}, nil
}

func normalizeProfile(name string, profile Profile) (Profile, error) {
	command := strings.TrimSpace(profile.Command)
	if command == "" {
		return Profile{}, fmt.Errorf("agents.profiles.%s.command is required", name)
	}
	if err := validateInterpolationToken(fmt.Sprintf("agents.profiles.%s.command", name), command); err != nil {
		return Profile{}, err
	}

	args := make([]string, len(profile.Args))
	for i, arg := range profile.Args {
		if err := validateInterpolationToken(fmt.Sprintf("agents.profiles.%s.args[%d]", name, i), arg); err != nil {
			return Profile{}, err
		}
		args[i] = arg
	}

	harness := strings.TrimSpace(profile.Harness)
	if harness == "" && isCodexCommand(command) {
		harness = "codex"
	}
	model := strings.TrimSpace(profile.Model)
	if model == "" && harness == "codex" {
		model = codexModelFromArgs(args)
	}
	return Profile{Command: command, Args: args, Interactive: profile.Interactive, Harness: harness, Model: model}, nil
}

func isCodexCommand(command string) bool {
	base := command
	if strings.ContainsAny(base, `/\`) {
		parts := strings.FieldsFunc(base, func(r rune) bool {
			return r == '/' || r == '\\'
		})
		if len(parts) > 0 {
			base = parts[len(parts)-1]
		}
	}
	return base == "codex"
}

func codexModelFromArgs(args []string) string {
	for i, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "--model" || arg == "-m" {
			if i+1 < len(args) {
				return strings.TrimSpace(args[i+1])
			}
			return ""
		}
		if value, ok := strings.CutPrefix(arg, "--model="); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func validateInterpolationToken(field string, value string) error {
	withoutSupportedTokens := value
	for _, token := range supportedInterpolationTokens() {
		withoutSupportedTokens = strings.ReplaceAll(withoutSupportedTokens, token, "")
	}
	if strings.Contains(withoutSupportedTokens, "{{") || strings.Contains(withoutSupportedTokens, "}}") {
		return fmt.Errorf(
			"%s contains an unsupported interpolation token; supported interpolation tokens: %s",
			field,
			strings.Join(supportedInterpolationTokens(), ", "),
		)
	}
	return nil
}

func interpolateProfileValue(value string, values InterpolationValues) string {
	replacer := strings.NewReplacer(
		promptToken, values.Prompt,
		sessionNameToken, values.SessionName,
	)
	return replacer.Replace(value)
}

func supportedInterpolationTokens() []string {
	return []string{promptToken, sessionNameToken}
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
