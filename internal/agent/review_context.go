package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/hea3ven/orpheus/internal/taskstate"
)

// ReviewContext is the backend-neutral execution contract rendered for review agents.
type ReviewContext struct {
	Repository ContextRepository
	Task       ContextTask
	Run        ContextRun
	Target     ContextTarget
	Review     ContextReview
}

// ContextReview describes the active review attempt and step.
type ContextReview struct {
	Attempt    int
	Step       string
	EnvStep    string
	Completion taskstate.Completion
}

// ResolveReview validates the active Orpheus review-agent context.
func (r ActiveContextResolver) ResolveReview(ctx context.Context) (ReviewContext, error) {
	if err := r.validateDependencies(); err != nil {
		return ReviewContext{}, err
	}
	env, err := r.resolveEnvironment()
	if err != nil {
		return ReviewContext{}, err
	}
	if purpose := strings.TrimSpace(r.envValue(envAgentPurpose)); purpose != "review" {
		return ReviewContext{}, fmt.Errorf("%s must be %q for review agent context", envAgentPurpose, "review")
	}
	reviewAttempt, err := r.requiredReviewAttempt()
	if err != nil {
		return ReviewContext{}, err
	}

	repo, source, taskItem, err := r.resolveTask(ctx, env)
	if err != nil {
		return ReviewContext{}, err
	}
	run, taskTarget, err := r.resolveLatestCompletedRun(repo.ID, env.TaskID)
	if err != nil {
		return ReviewContext{}, err
	}
	targets, candidate, err := r.resolveContextTarget(source, taskItem, env.TaskID, taskTarget)
	if err != nil {
		return ReviewContext{}, err
	}
	if err := validateEnvironmentMatchesTarget(env, candidate); err != nil {
		return ReviewContext{}, err
	}
	cwd, err := r.resolveTargetCWD(candidate)
	if err != nil {
		return ReviewContext{}, err
	}

	review, err := r.resolveRunningAgentReview(repo.ID, env.TaskID, reviewAttempt)
	if err != nil {
		return ReviewContext{}, err
	}
	activeContext, err := newActiveContext(repo, targets, taskItem, run, candidate, cwd)
	if err != nil {
		return ReviewContext{}, err
	}

	return ReviewContext{
		Repository: activeContext.Repository,
		Task:       activeContext.Task,
		Run:        activeContext.Run,
		Target:     activeContext.Target,
		Review: ContextReview{
			Attempt:    review.Attempt,
			Step:       latestReviewStep(review).Name,
			EnvStep:    strings.TrimSpace(r.envValue(envReviewStep)),
			Completion: *run.Completion,
		},
	}, nil
}

func (r ActiveContextResolver) resolveLatestCompletedRun(
	repoID string,
	taskID string,
) (taskstate.RunAttempt, taskstate.TaskTarget, error) {
	state, err := r.RunStore.Load(repoID, taskID)
	if err != nil {
		return taskstate.RunAttempt{}, taskstate.TaskTarget{}, fmt.Errorf(
			"load latest Orpheus run for task %s/%s: %w",
			repoID,
			taskID,
			err,
		)
	}
	run, ok := taskstate.LatestRun(state)
	if !ok {
		return taskstate.RunAttempt{}, taskstate.TaskTarget{}, fmt.Errorf(
			"task %s/%s has no Orpheus run attempts",
			repoID,
			taskID,
		)
	}
	if run.Completion == nil {
		return taskstate.RunAttempt{}, taskstate.TaskTarget{}, fmt.Errorf(
			"latest Orpheus run attempt %d has no completion block",
			run.Attempt,
		)
	}
	target, ok := taskstate.Target(state)
	if !ok {
		return taskstate.RunAttempt{}, taskstate.TaskTarget{}, fmt.Errorf(
			"task %s/%s has no taskstate target",
			repoID,
			taskID,
		)
	}
	return run, target, nil
}

func (r ActiveContextResolver) requiredReviewAttempt() (int, error) {
	raw, err := r.requiredEnv(envReviewAttempt)
	if err != nil {
		return 0, err
	}
	attempt, parseErr := strconv.Atoi(raw)
	if parseErr != nil || attempt <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer, got %q", envReviewAttempt, raw)
	}
	return attempt, nil
}

func (r ActiveContextResolver) resolveRunningAgentReview(repoID string, taskID string, attempt int) (taskstate.ReviewAttempt, error) {
	state, err := r.RunStore.Load(repoID, taskID)
	if err != nil {
		return taskstate.ReviewAttempt{}, fmt.Errorf("load latest review attempt for task %s/%s: %w", repoID, taskID, err)
	}
	review, ok := taskstate.LatestReview(state)
	if !ok {
		return taskstate.ReviewAttempt{}, fmt.Errorf("task %s/%s has no review attempts", repoID, taskID)
	}
	if review.Attempt != attempt {
		return taskstate.ReviewAttempt{}, fmt.Errorf(
			"latest review attempt for task %s/%s is %d, expected %d from %s",
			repoID,
			taskID,
			review.Attempt,
			attempt,
			envReviewAttempt,
		)
	}
	if review.Status != taskstate.ReviewStatusRunning {
		return taskstate.ReviewAttempt{}, fmt.Errorf(
			"review attempt %d for task %s/%s is %q, expected %q",
			attempt,
			repoID,
			taskID,
			review.Status,
			taskstate.ReviewStatusRunning,
		)
	}
	step := latestReviewStep(review)
	if step.Kind != "agent_review" {
		return taskstate.ReviewAttempt{}, fmt.Errorf(
			"current_step.kind for review step %q on task %s/%s is %q, expected %q",
			step.Name,
			repoID,
			taskID,
			step.Kind,
			"agent_review",
		)
	}
	return review, nil
}

func latestReviewStep(review taskstate.ReviewAttempt) taskstate.ReviewStep {
	if len(review.Steps) == 0 {
		return taskstate.ReviewStep{}
	}
	return review.Steps[len(review.Steps)-1]
}
