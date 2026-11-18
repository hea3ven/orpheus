package review

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hea3ven/orpheus/internal/agent"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
)

func ResolveReviewAgentConfig(paths state.Paths, pipeline Pipeline) (agent.Config, error) {
	var agentConfig agent.Config
	loaded := false
	for _, step := range pipeline.Steps {
		if step.Kind != KindAgentReview {
			continue
		}
		if !loaded {
			config, err := agent.LoadConfig(paths)
			if err != nil {
				return agent.Config{}, err
			}
			agentConfig = config
			loaded = true
		}
		if _, err := agentConfig.ResolveReviewerCommandWithValues(step.Agent, agent.InterpolationValues{}); err != nil {
			return agent.Config{}, fmt.Errorf("resolve agent_review step %q: %w", step.Name, err)
		}
	}
	return agentConfig, nil
}

func UsesAgentReview(pipeline Pipeline) bool {
	for _, step := range pipeline.Steps {
		if step.Kind == KindAgentReview {
			return true
		}
	}
	return false
}

func MaxAutonomousReviewAttempts(paths state.Paths) (int, error) {
	config, err := LoadConfig(paths)
	if err != nil {
		return 0, err
	}
	return config.MaxAutonomousReviewAttempts, nil
}

// ResolveTaskReviewPipeline resolves the effective review pipeline for a repository.
func ResolveTaskReviewPipeline(paths state.Paths, repo task.Repository, pipelineName string) (Pipeline, error) {
	config, err := LoadConfig(paths)
	if err != nil {
		return Pipeline{}, err
	}
	if name := strings.TrimSpace(pipelineName); name != "" {
		if target, ok := repo.ReviewPipelineAliases[name]; ok {
			pipeline, err := ResolvePipeline(config, target, "")
			if err != nil {
				return Pipeline{}, fmt.Errorf("CLI --pipeline alias %q targets %q: %w", name, target, err)
			}
			return pipeline, nil
		}
		pipeline, err := ResolvePipeline(config, name, "")
		if err != nil {
			return Pipeline{}, appendRepoReviewPipelineAliases(err, repo)
		}
		return pipeline, nil
	}
	return ResolvePipeline(config, pipelineName, repo.ReviewPipeline)
}

// ResolvePausedTaskReviewPipeline resolves the recorded configuration, not current defaults or aliases.
func ResolvePausedTaskReviewPipeline(paths state.Paths, repo task.Repository, paused taskstate.ReviewAttempt, pipelineName string) (Pipeline, error) {
	pipeline, err := resolveRecordedTaskReviewPipeline(paths, paused)
	if err != nil {
		return Pipeline{}, err
	}
	if strings.TrimSpace(pipelineName) == "" {
		return pipeline, nil
	}
	requested, err := ResolveTaskReviewPipeline(paths, repo, pipelineName)
	if err != nil {
		return Pipeline{}, err
	}
	if requested.Name != pipeline.Name {
		return Pipeline{}, fmt.Errorf("review attempt %d is waiting at step %q in pipeline %q; --pipeline %q resolves to %q and cannot replace a paused review", paused.Attempt, paused.Step, pipeline.Name, strings.TrimSpace(pipelineName), requested.Name)
	}
	return pipeline, nil
}

func resolveRecordedTaskReviewPipeline(paths state.Paths, attempt taskstate.ReviewAttempt) (Pipeline, error) {
	name := strings.TrimSpace(attempt.Pipeline)
	if name == "" {
		return Pipeline{}, fmt.Errorf("review attempt %d has no recorded pipeline", attempt.Attempt)
	}
	config, err := LoadConfig(paths)
	if err == nil {
		pipeline, lookupErr := config.namedPipeline(name, "recorded pipeline")
		if lookupErr == nil {
			return pipeline, nil
		}
		if builtin := BuiltinManualPipeline(); name == builtin.Name {
			return builtin, nil
		}
		err = lookupErr
	}
	return Pipeline{}, fmt.Errorf("%w: review attempt %d at step %q cannot resolve recorded pipeline %q: %w; restore reviews.pipelines.%s in config.yaml before continuing", ErrInvalidExecution, attempt.Attempt, attempt.Step, name, err, name)
}

func appendRepoReviewPipelineAliases(err error, repo task.Repository) error {
	aliases := make([]string, 0, len(repo.ReviewPipelineAliases))
	for alias, target := range repo.ReviewPipelineAliases {
		aliases = append(aliases, fmt.Sprintf("%s=%s", alias, target))
	}
	sort.Strings(aliases)
	if len(aliases) == 0 {
		return err
	}
	return fmt.Errorf("%w; configured repo aliases: %s", err, strings.Join(aliases, ", "))
}

// ContinuationIndex resolves the current configured name and kind, not a
// position saved from an earlier configuration or an actual-attempt address.
func ContinuationIndex(pipeline Pipeline, paused taskstate.ReviewAttempt) (int, error) {
	kind := ""
	if len(paused.Steps) > 0 {
		last := paused.Steps[len(paused.Steps)-1]
		if last.Name != paused.Step {
			return 0, fmt.Errorf("%w: current review step disagrees with its owning attempt", ErrInvalidExecution)
		}
		kind = last.Kind
	}
	for index, step := range pipeline.Steps {
		if step.Name == paused.Step && (kind == "" || step.Kind == kind) {
			return index, nil
		}
	}
	return 0, fmt.Errorf("%w: review attempt %d in phase review, pipeline %q, cannot locate current step %q with type %q; restore that name and type in reviews.pipelines.%s in config.yaml", ErrInvalidExecution, paused.Attempt, pipeline.Name, paused.Step, kind, pipeline.Name)
}
