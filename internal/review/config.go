// Package review defines local task review pipeline configuration.
package review

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"gopkg.in/yaml.v3"
)

const (
	// ConfigFile is the Orpheus global configuration file containing review pipelines.
	ConfigFile = "config.yaml"

	// DefaultMaxAutonomousReviewAttempts limits one command's automatic review/fix loop.
	DefaultMaxAutonomousReviewAttempts = 4

	// DefaultIncludePRReviewProcess preserves existing PR body publication behavior.
	DefaultIncludePRReviewProcess = true

	KindManual      = taskstate.ReviewStepKindManual
	KindCheck       = taskstate.ReviewStepKindCheck
	KindAgentReview = taskstate.ReviewStepKindAgentReview
)

// Config is the reviews section of Orpheus' global configuration.
type Config struct {
	DefaultPipeline                string
	MaxAutonomousReviewAttempts    int
	IncludePRReviewProcess         bool
	Pipelines                      map[string]Pipeline
	maxAutonomousReviewAttemptsSet bool
	includePRReviewProcessSet      bool
}

// Pipeline is a named ordered list of review steps.
type Pipeline struct {
	Name  string
	Steps []Step
}

// Step is one configured review pipeline step.
type Step struct {
	Kind      string   `yaml:"kind"`
	Name      string   `yaml:"name"`
	Command   string   `yaml:"command,omitempty"`
	Args      []string `yaml:"args,omitempty"`
	Agent     string   `yaml:"agent,omitempty"`
	HunkNotes bool     `yaml:"hunk_notes,omitempty"`
}

// UnmarshalYAML decodes only the top-level reviews section while allowing the
// sibling agents section in the shared config file.
func (c *Config) UnmarshalYAML(value *yaml.Node) error {
	var raw struct {
		Agents  yaml.Node `yaml:"agents"`
		Reviews yaml.Node `yaml:"reviews"`
	}
	if err := value.Decode(&raw); err != nil {
		return err
	}

	*c = Config{}
	if raw.Reviews.Kind == 0 {
		return nil
	}

	var nested struct {
		DefaultPipeline             string              `yaml:"default_pipeline"`
		MaxAutonomousReviewAttempts *int                `yaml:"max_autonomous_review_attempts"`
		IncludePRReviewProcess      *bool               `yaml:"include_pr_review_process"`
		Pipelines                   map[string]Pipeline `yaml:"pipelines"`
	}
	if err := raw.Reviews.Decode(&nested); err != nil {
		return err
	}
	c.DefaultPipeline = nested.DefaultPipeline
	if nested.MaxAutonomousReviewAttempts != nil {
		c.MaxAutonomousReviewAttempts = *nested.MaxAutonomousReviewAttempts
		c.maxAutonomousReviewAttemptsSet = true
	}
	if nested.IncludePRReviewProcess != nil {
		c.IncludePRReviewProcess = *nested.IncludePRReviewProcess
		c.includePRReviewProcessSet = true
	}
	c.Pipelines = nested.Pipelines
	return nil
}

// LoadConfig reads and validates global review pipeline configuration. A
// missing config file means no configured pipelines, so callers can fall back to
// the built-in manual pipeline.
func LoadConfig(paths state.Paths) (Config, error) {
	var config Config
	if err := paths.ReadConfigYAML(ConfigFile, &config); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{
				MaxAutonomousReviewAttempts: DefaultMaxAutonomousReviewAttempts,
				IncludePRReviewProcess:      DefaultIncludePRReviewProcess,
			}, nil
		}
		return Config{}, fmt.Errorf("load review pipelines from %s: %w", ConfigFile, err)
	}

	normalized, err := config.normalized()
	if err != nil {
		return Config{}, fmt.Errorf("load review pipelines from %s: %w", ConfigFile, err)
	}
	return normalized, nil
}

// BuiltinManualPipeline returns the safe zero-config review pipeline.
func BuiltinManualPipeline() Pipeline {
	return Pipeline{
		Name: "default",
		Steps: []Step{{
			Kind: KindManual,
			Name: "local-review",
		}},
	}
}

// ResolvePipeline applies task review pipeline precedence.
func ResolvePipeline(config Config, cliPipeline string, repoPipeline string) (Pipeline, error) {
	normalized, err := config.normalized()
	if err != nil {
		return Pipeline{}, err
	}

	if name := strings.TrimSpace(cliPipeline); name != "" {
		return normalized.namedPipeline(name, "CLI --pipeline")
	}
	if name := strings.TrimSpace(repoPipeline); name != "" {
		return normalized.namedPipeline(name, "repo review_pipeline")
	}
	if name := strings.TrimSpace(normalized.DefaultPipeline); name != "" {
		return normalized.namedPipeline(name, "reviews.default_pipeline")
	}
	return BuiltinManualPipeline(), nil
}

// HasPipeline reports whether name matches a configured global review pipeline.
func (c Config) HasPipeline(name string) bool {
	_, ok := c.Pipelines[strings.TrimSpace(name)]
	return ok
}

// PipelineNames returns configured global review pipeline names in display order.
func (c Config) PipelineNames() []string {
	return c.pipelineNames()
}

func (c Config) namedPipeline(name string, source string) (Pipeline, error) {
	pipeline, ok := c.Pipelines[name]
	if ok {
		return pipeline, nil
	}
	return Pipeline{}, fmt.Errorf(
		"%s %q does not match a configured review pipeline; configured pipelines: %s",
		source,
		name,
		strings.Join(c.pipelineNames(), ", "),
	)
}

func (c Config) normalized() (Config, error) {
	defaultPipeline := strings.TrimSpace(c.DefaultPipeline)
	maxAutonomousReviewAttempts := c.MaxAutonomousReviewAttempts
	if !c.maxAutonomousReviewAttemptsSet && maxAutonomousReviewAttempts == 0 {
		maxAutonomousReviewAttempts = DefaultMaxAutonomousReviewAttempts
	}
	includePRReviewProcess := c.IncludePRReviewProcess
	if !c.includePRReviewProcessSet {
		includePRReviewProcess = DefaultIncludePRReviewProcess
	}
	if maxAutonomousReviewAttempts <= 0 {
		return Config{}, fmt.Errorf(
			"reviews.max_autonomous_review_attempts must be positive, got %d",
			c.MaxAutonomousReviewAttempts,
		)
	}
	pipelines := map[string]Pipeline{}
	for rawName, rawPipeline := range c.Pipelines {
		name := strings.TrimSpace(rawName)
		if name == "" {
			return Config{}, errors.New("reviews.pipelines name is required")
		}
		if _, exists := pipelines[name]; exists {
			return Config{}, fmt.Errorf("reviews.pipelines.%s is duplicated after trimming whitespace", name)
		}
		pipeline, err := normalizePipeline(name, rawPipeline)
		if err != nil {
			return Config{}, err
		}
		pipelines[name] = pipeline
	}

	if defaultPipeline != "" {
		if _, ok := pipelines[defaultPipeline]; !ok {
			return Config{}, fmt.Errorf(
				"reviews.default_pipeline %q does not match a configured review pipeline; configured pipelines: %s",
				defaultPipeline,
				strings.Join(pipelineNames(pipelines), ", "),
			)
		}
	}

	return Config{
		DefaultPipeline:                defaultPipeline,
		MaxAutonomousReviewAttempts:    maxAutonomousReviewAttempts,
		IncludePRReviewProcess:         includePRReviewProcess,
		Pipelines:                      pipelines,
		maxAutonomousReviewAttemptsSet: true,
		includePRReviewProcessSet:      true,
	}, nil
}

func normalizePipeline(name string, pipeline Pipeline) (Pipeline, error) {
	if len(pipeline.Steps) == 0 {
		return Pipeline{}, fmt.Errorf("reviews.pipelines.%s.steps must contain at least one step", name)
	}

	steps := make([]Step, 0, len(pipeline.Steps))
	stepNames := make(map[string]struct{}, len(pipeline.Steps))
	for index, rawStep := range pipeline.Steps {
		step, err := normalizeStep(name, index, rawStep)
		if err != nil {
			return Pipeline{}, err
		}
		if _, exists := stepNames[step.Name]; exists {
			return Pipeline{}, fmt.Errorf(
				"reviews.pipelines.%s.steps contains duplicate step name %q after trimming whitespace",
				name,
				step.Name,
			)
		}
		stepNames[step.Name] = struct{}{}
		steps = append(steps, step)
	}
	return Pipeline{Name: name, Steps: steps}, nil
}

func normalizeStep(pipelineName string, index int, step Step) (Step, error) {
	field := fmt.Sprintf("reviews.pipelines.%s.steps[%d]", pipelineName, index)
	step.Kind = strings.TrimSpace(step.Kind)
	step.Name = strings.TrimSpace(step.Name)
	step.Command = strings.TrimSpace(step.Command)
	step.Agent = strings.TrimSpace(step.Agent)
	args := make([]string, len(step.Args))
	copy(args, step.Args)
	step.Args = args

	if step.Name == "" {
		return Step{}, fmt.Errorf("%s.name is required", field)
	}
	switch step.Kind {
	case KindManual:
		if step.Command == "" && len(step.Args) > 0 {
			return Step{}, fmt.Errorf("%s.args requires command", field)
		}
	case KindCheck:
		if step.Command == "" {
			return Step{}, fmt.Errorf("%s.command is required for check steps", field)
		}
	case KindAgentReview:
	default:
		return Step{}, fmt.Errorf(
			"%s.kind %q is invalid; expected %q, %q, or %q",
			field,
			step.Kind,
			KindManual,
			KindCheck,
			KindAgentReview,
		)
	}
	return step, nil
}

func (c Config) pipelineNames() []string {
	return pipelineNames(c.Pipelines)
}

func pipelineNames(pipelines map[string]Pipeline) []string {
	names := make([]string, 0, len(pipelines))
	for name := range pipelines {
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return []string{"(none)"}
	}
	return names
}
