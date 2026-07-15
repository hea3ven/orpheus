// Package agent defines backend-neutral agent prompt, profile, and command helpers.
package agent

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hea3ven/orpheus/internal/agentexec"
	"github.com/hea3ven/orpheus/internal/state"
	"gopkg.in/yaml.v3"
)

const (
	// ConfigFile is the Orpheus global configuration file containing agent profiles.
	ConfigFile = "config.yaml"

	promptToken      = "{{prompt}}"
	sessionNameToken = "{{session_name}}"

	codexHarness = "codex"
	codexCommand = "codex"
	codexYoloArg = "--dangerously-bypass-approvals-and-sandbox"
	piHarness    = "pi"
	piCommand    = "pi"
)

// Config is Orpheus' global agent profile configuration.
type Config struct {
	Defaults AgentDefaults      `yaml:"-"`
	Agents   map[string]Profile `yaml:"-"`
}

// AgentDefaults names purpose-specific default agent profiles.
type AgentDefaults struct {
	Implementer          string `yaml:"implementer"`
	Reviewer             string `yaml:"reviewer"`
	SyncConflictResolver string `yaml:"sync_conflict_resolver"`
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
	Thinking    string   `yaml:"thinking,omitempty"`
}

// CommandSnapshot is the resolved command line for one dispatch.
type CommandSnapshot struct {
	AgentName string
	Command   string
	Args      []string
	Harness   string
	Model     string
}

// ExecCommand returns the harness-neutral process invocation for this command.
func (s CommandSnapshot) ExecCommand() agentexec.Command {
	return agentexec.Command{
		Name:    s.AgentName,
		Command: s.Command,
		Args:    append([]string{}, s.Args...),
	}
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
// selectedAgent is blank, and applies standard profile interpolation.
func (c Config) ResolveCommand(selectedAgent string) (CommandSnapshot, error) {
	return c.ResolveImplementerCommand(selectedAgent)
}

// ResolveImplementerCommand resolves selectedAgent, or agents.defaults.implementer
// when selectedAgent is blank.
func (c Config) ResolveImplementerCommand(selectedAgent string) (CommandSnapshot, error) {
	return c.ResolveImplementerCommandWithValues(selectedAgent, InterpolationValues{})
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
func (c Config) ResolveReviewerCommand(selectedAgent string) (CommandSnapshot, error) {
	return c.ResolveReviewerCommandWithValues(selectedAgent, InterpolationValues{})
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

// ResolveSyncConflictResolverCommand resolves the conflict-specific default
// agent profile, falling back to agents.defaults.implementer when unset.
func (c Config) ResolveSyncConflictResolverCommand(values InterpolationValues) (CommandSnapshot, error) {
	normalized, err := c.normalized()
	if err != nil {
		return CommandSnapshot{}, err
	}

	agentName := strings.TrimSpace(normalized.Defaults.SyncConflictResolver)
	if agentName == "" {
		agentName = strings.TrimSpace(normalized.Defaults.Implementer)
	}
	return normalized.resolveAgentProfile(agentName, values)
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

	if profile.isStructuredCodex() {
		return resolveCodexProfile(agentName, profile, values), nil
	}
	if profile.isStructuredPi() {
		return resolvePiProfile(agentName, profile, values), nil
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
		Implementer:          strings.TrimSpace(c.Defaults.Implementer),
		Reviewer:             strings.TrimSpace(c.Defaults.Reviewer),
		SyncConflictResolver: strings.TrimSpace(c.Defaults.SyncConflictResolver),
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
	if defaults.SyncConflictResolver != "" {
		if _, ok := agents[defaults.SyncConflictResolver]; !ok {
			return Config{}, fmt.Errorf(
				"agents.defaults.sync_conflict_resolver %q does not match a configured agent; configured agents: %s",
				defaults.SyncConflictResolver,
				strings.Join(agentNames(agents), ", "),
			)
		}
	}

	return Config{Defaults: defaults, Agents: agents}, nil
}

func normalizeProfile(name string, profile Profile) (Profile, error) {
	command := strings.TrimSpace(profile.Command)
	args := make([]string, len(profile.Args))
	for i, arg := range profile.Args {
		if err := validateInterpolationToken(fmt.Sprintf("agents.profiles.%s.args[%d]", name, i), arg); err != nil {
			return Profile{}, err
		}
		args[i] = arg
	}
	harness := strings.ToLower(strings.TrimSpace(profile.Harness))
	model := strings.TrimSpace(profile.Model)
	thinking := strings.TrimSpace(profile.Thinking)

	switch harness {
	case codexHarness:
		return normalizeStructuredProfile(structuredProfileOptions{
			name:           name,
			profile:        profile,
			command:        command,
			args:           args,
			harness:        harness,
			model:          model,
			thinking:       thinking,
			displayHarness: "Codex",
		})
	case piHarness:
		return normalizeStructuredProfile(structuredProfileOptions{
			name:           name,
			profile:        profile,
			command:        command,
			args:           args,
			harness:        harness,
			model:          model,
			thinking:       thinking,
			displayHarness: "Pi",
		})
	case "":
	default:
		return Profile{}, fmt.Errorf("agents.profiles.%s.harness %q is not supported; supported harnesses: codex, pi", name, harness)
	}
	if command == "" {
		return Profile{}, fmt.Errorf("agents.profiles.%s.command is required", name)
	}
	if err := validateInterpolationToken(fmt.Sprintf("agents.profiles.%s.command", name), command); err != nil {
		return Profile{}, err
	}
	if model != "" {
		return Profile{}, fmt.Errorf(
			"agents.profiles.%s.model requires structured harness: codex or pi; remove model for a generic raw command profile",
			name,
		)
	}
	if thinking != "" {
		return Profile{}, fmt.Errorf(
			"agents.profiles.%s.thinking requires structured harness: codex or pi; remove thinking for a generic raw command profile",
			name,
		)
	}
	return Profile{Command: command, Args: args, Interactive: profile.Interactive, Harness: harness, Model: model}, nil
}

type structuredProfileOptions struct {
	name           string
	profile        Profile
	command        string
	args           []string
	harness        string
	model          string
	thinking       string
	displayHarness string
}

func normalizeStructuredProfile(opts structuredProfileOptions) (Profile, error) {
	if opts.command != "" || len(opts.args) > 0 {
		return Profile{}, fmt.Errorf(
			"agents.profiles.%s mixes structured %s configuration with raw command/args; "+
				"use harness: %s with model and no command/args, or remove harness/model for a generic raw command profile",
			opts.name,
			opts.displayHarness,
			opts.harness,
		)
	}
	if opts.model == "" {
		return Profile{}, fmt.Errorf("agents.profiles.%s.model is required for harness: %s", opts.name, opts.harness)
	}
	return Profile{
		Interactive: opts.profile.Interactive,
		Harness:     opts.harness,
		Model:       opts.model,
		Thinking:    opts.thinking,
	}, nil
}

func resolveCodexProfile(agentName string, profile Profile, values InterpolationValues) CommandSnapshot {
	args := []string{}
	if !profile.Interactive {
		args = append(args, "exec")
	}
	args = append(args,
		"--model",
		profile.Model,
		codexYoloArg,
	)
	if profile.Thinking != "" {
		args = append(args, "-c", "model_reasoning_effort="+profile.Thinking)
	}
	args = append(args, interpolateCodexPrompt(values))
	return CommandSnapshot{
		AgentName: agentName,
		Command:   codexCommand,
		Args:      args,
		Harness:   profile.Harness,
		Model:     profile.Model,
	}
}

func resolvePiProfile(agentName string, profile Profile, values InterpolationValues) CommandSnapshot {
	args := []string{}
	if !profile.Interactive {
		args = append(args, "--print")
	}
	args = append(args, "--model", profile.Model)
	if profile.Thinking != "" {
		args = append(args, "--thinking", profile.Thinking)
	}
	if strings.TrimSpace(values.SessionName) != "" {
		args = append(args, "--name", values.SessionName)
	}
	args = append(args, RenderBootstrapPrompt())
	return CommandSnapshot{
		AgentName: agentName,
		Command:   piCommand,
		Args:      args,
		Harness:   profile.Harness,
		Model:     profile.Model,
	}
}

func (p Profile) isStructuredCodex() bool {
	return p.Harness == codexHarness
}

func (p Profile) isStructuredPi() bool {
	return p.Harness == piHarness
}

func interpolateCodexPrompt(values InterpolationValues) string {
	return values.SessionName + " - " + RenderBootstrapPrompt()
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
		promptToken, RenderBootstrapPrompt(),
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
