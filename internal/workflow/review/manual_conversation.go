package review

import (
	"context"
	"errors"
	"fmt"

	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// ManualStepContext contains the retained comparison and current checkout facts
// needed by the specialized terminal renderer. It is not a persisted draft.
type ManualStepContext struct {
	Source         task.RepositorySource
	Task           task.Task
	Workdir        string
	Review         taskstate.ReviewAttempt
	TaskState      taskstate.TaskState
	Step           Step
	GitStatus      string
	BaselineCommit string
	RevisionCommit string
	GitDiffStat    string
}

// RepoID returns the registered repository ID.
func (c ManualStepContext) RepoID() string { return c.Source.Repository.ID }

// TaskID returns the resolved task ID.
func (c ManualStepContext) TaskID() string { return c.Task.ID }

// ManualStepPrompt gives the frontend a pure draft for this one assessment.
type ManualStepPrompt struct {
	Source    task.RepositorySource
	Task      task.Task
	Review    taskstate.ReviewAttempt
	Step      Step
	HunkNotes []HunkNote
	Draft     *ManualDraft
}

// TaskID returns the resolved task ID.
func (p ManualStepPrompt) TaskID() string { return p.Task.ID }

func manualStepContext(ctx context.Context, current work.State, opts PipelineRunOptions, step Step) (ManualStepContext, error) {
	view, err := taskstate.Project(current)
	if err != nil {
		return ManualStepContext{}, err
	}
	latest, ok := taskstate.LatestRun(view)
	if !ok {
		return ManualStepContext{}, fmt.Errorf("task has no Orpheus run attempts; run `orpheus task run %s` first", opts.TaskID)
	}
	if latest.Completion == nil {
		return ManualStepContext{}, fmt.Errorf("latest run attempt %d has no completion block; run `orpheus agent done` first", latest.Attempt)
	}
	candidate, err := CommittedInput(view, opts.Attempt.Address)
	if err != nil {
		return ManualStepContext{}, fmt.Errorf("resolve review scope: %w", err)
	}
	baseline := current.Workflows[opts.Attempt.Address.Workflow].Context().Baseline.Commit
	stat, err := opts.CandidateGit.DiffStat(ctx, opts.Workdir, baseline, candidate.Revision.Commit)
	if err != nil {
		return ManualStepContext{}, fmt.Errorf("read task diff stat: %w", err)
	}
	status, err := opts.ReadStatus(ctx, opts.Workdir)
	if err != nil {
		return ManualStepContext{}, fmt.Errorf("read git status: %w", err)
	}
	return ManualStepContext{
		Source: opts.Source, Task: opts.Task, Workdir: opts.Workdir,
		Review: opts.Attempt, TaskState: view, Step: step, GitStatus: status,
		BaselineCommit: baseline, RevisionCommit: candidate.Revision.Commit, GitDiffStat: stat,
	}, nil
}

// The token is captured after the command receipt and before editing. Draft
// edits never refresh it; the accepted response boundary revalidates it.
func manualAssessmentPrompt(opts PipelineRunOptions, owner work.AttemptRef, step Step, notes []HunkNote) (ManualStepPrompt, phaseexec.InputID, error) {
	current, err := pipelineReview(opts, owner)
	if err != nil {
		return ManualStepPrompt{}, phaseexec.InputID{}, err
	}
	if len(current.Steps) == 0 || current.Steps[len(current.Steps)-1].Name != step.Name || !current.Steps[len(current.Steps)-1].Address.Equal(owner) {
		return ManualStepPrompt{}, phaseexec.InputID{}, errors.New("manual prompt no longer owns its pipeline step")
	}
	pending, err := pendingAssessment(opts, owner)
	if err != nil {
		return ManualStepPrompt{}, phaseexec.InputID{}, err
	}
	if pending.ID.Kind != ManualInputKind {
		return ManualStepPrompt{}, phaseexec.InputID{}, phaseexec.ErrStaleInput
	}
	draft := NewManualDraft(ManualRequest{ID: pending.ID, Review: current, Step: step.Name})
	return ManualStepPrompt{Source: opts.Source, Task: opts.Task, Review: opts.Attempt, Step: step, HunkNotes: notes, Draft: draft}, pending.ID, nil
}

func pendingAssessment(opts PipelineRunOptions, owner work.AttemptRef) (*DecisionRequest, error) {
	pending, err := PendingDecision(*opts.current)
	if err != nil {
		return nil, err
	}
	if pending == nil || !pending.ID.Owner.Equal(owner) {
		return nil, errors.Join(ErrInvalidExecution, phaseexec.ErrStaleInput)
	}
	return pending, nil
}
