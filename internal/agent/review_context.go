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
	Attempt             int
	Step                string
	EnvStep             string
	Completion          taskstate.Completion
	OriginalCompletion  *taskstate.Completion
	LatestFixCompletion *taskstate.Completion
	PriorFindings       []ContextPriorReviewFinding
}

// ContextPriorReviewFinding is a compact authoritative finding from an earlier review attempt.
type ContextPriorReviewFinding struct {
	Attempt     int
	Number      int
	Step        string
	Type        taskstate.FindingType
	Disposition string
	Title       string
}

// ResolveReview validates the active Orpheus review-agent context.
//
//nolint:funlen // Validation order mirrors the active review lifecycle.
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
	completions, taskTarget, err := r.resolveReviewCompletionRuns(repo.ID, env.TaskID)
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
	state, err := r.RunStore.Load(repo.ID, env.TaskID)
	if err != nil {
		return ReviewContext{}, fmt.Errorf("load review history for task %s/%s: %w", repo.ID, env.TaskID, err)
	}
	activeContext, err := newActiveContext(repo, targets, taskItem, completions.Latest, candidate, cwd)
	if err != nil {
		return ReviewContext{}, err
	}

	completion := reviewCompletionContext(completions)
	return ReviewContext{
		Repository: activeContext.Repository,
		Task:       activeContext.Task,
		Run:        activeContext.Run,
		Target:     activeContext.Target,
		Review: ContextReview{
			Attempt:             review.Attempt,
			Step:                latestReviewStep(review).Name,
			EnvStep:             strings.TrimSpace(r.envValue(envReviewStep)),
			Completion:          completion.latest,
			OriginalCompletion:  completion.original,
			LatestFixCompletion: completion.latestFix,
			PriorFindings:       priorReviewFindings(state, review.Attempt),
		},
	}, nil
}

func priorReviewFindings(state taskstate.TaskState, activeAttempt int) []ContextPriorReviewFinding {
	prior := make([]ContextPriorReviewFinding, 0)
	for _, review := range state.Reviews {
		if review.Attempt >= activeAttempt {
			continue
		}
		authoritativeNumber := 0
		for _, finding := range review.Findings {
			if taskstate.InterruptedPrimaryReviewFinding(review, finding) {
				continue
			}
			authoritativeNumber++
			prior = append(prior, ContextPriorReviewFinding{
				Attempt:     review.Attempt,
				Number:      authoritativeNumber,
				Step:        compactReviewText(finding.Step),
				Type:        finding.Type,
				Disposition: compactReviewText(priorFindingDisposition(state, finding)),
				Title:       compactReviewText(finding.Title),
			})
		}
	}
	return prior
}

func compactReviewText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func priorFindingDisposition(state taskstate.TaskState, finding taskstate.ReviewFinding) string {
	resolution := taskstate.ResolveReviewFindingInState(state, finding)
	switch resolution {
	case taskstate.ReviewFindingResolutionAddressedManually:
		return "addressed manually"
	case taskstate.ReviewFindingResolutionWaived:
		return "waived"
	case taskstate.ReviewFindingResolutionDowngraded:
		return "downgraded to advisory"
	case taskstate.ReviewFindingResolutionCreatedTask:
		return "created task " + strings.TrimSpace(finding.CreatedTaskID)
	case taskstate.ReviewFindingResolutionTargetedByRun:
		return followUpRunDisposition(state, finding.TargetedByRunAttempt)
	case taskstate.ReviewFindingResolutionOpen:
		if finding.TargetedByRunAttempt > 0 {
			return followUpRunDisposition(state, finding.TargetedByRunAttempt)
		}
		return "open"
	case taskstate.ReviewFindingResolutionSeparateTask:
		return "separate-task proposal"
	default:
		return "advisory"
	}
}

func followUpRunDisposition(state taskstate.TaskState, attempt int) string {
	for _, run := range state.Runs {
		if run.Attempt == attempt {
			return fmt.Sprintf("follow-up run %d %s", attempt, run.Status)
		}
	}
	return fmt.Sprintf("follow-up run %d", attempt)
}

type reviewCompletionSelection struct {
	latest    taskstate.Completion
	original  *taskstate.Completion
	latestFix *taskstate.Completion
}

func reviewCompletionContext(history taskstate.CompletionRunHistory) reviewCompletionSelection {
	latest := *history.Latest.Completion
	if history.Latest.ReviewFollowUp == nil {
		return reviewCompletionSelection{latest: latest}
	}
	original := *history.Original.Completion
	latestFix := latest
	return reviewCompletionSelection{
		latest:    latest,
		original:  &original,
		latestFix: &latestFix,
	}
}

func (r ActiveContextResolver) resolveReviewCompletionRuns(
	repoID string,
	taskID string,
) (taskstate.CompletionRunHistory, taskstate.GitFacts, error) {
	state, err := r.RunStore.Load(repoID, taskID)
	if err != nil {
		return taskstate.CompletionRunHistory{}, taskstate.GitFacts{}, fmt.Errorf(
			"load latest Orpheus run for task %s/%s: %w",
			repoID,
			taskID,
			err,
		)
	}
	history, historyErr := taskstate.CompletionRunsForReview(state)
	if historyErr != nil {
		return taskstate.CompletionRunHistory{}, taskstate.GitFacts{}, fmt.Errorf(
			"resolve review completion history for task %s/%s: %w",
			repoID,
			taskID,
			historyErr,
		)
	}
	target, ok := taskstate.GitFactsFor(state)
	if !ok {
		return taskstate.CompletionRunHistory{}, taskstate.GitFacts{}, fmt.Errorf(
			"task %s/%s has no taskstate target",
			repoID,
			taskID,
		)
	}
	return history, target, nil
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
