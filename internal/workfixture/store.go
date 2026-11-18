// Package workfixture composes phase-owned changes for synthetic test history.
// It is not an application lifecycle API; no command or production service uses it.
package workfixture

import (
	"context"
	"errors"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/implementation"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
	"github.com/hea3ven/orpheus/internal/workflow/review"
	"github.com/hea3ven/orpheus/internal/workstate"
)

// Store pairs the reader under test with explicit synthetic-history composition.
type Store struct {
	taskstate.Store
	paths state.Paths
	now   func() time.Time
}

func NewStore(paths state.Paths) Store { return NewStoreWithClock(paths, time.Now) }

func NewStoreWithClock(paths state.Paths, now func() time.Time) Store {
	return Store{Store: taskstate.NewStoreWithClock(paths, now), paths: paths, now: now}
}

func (s Store) commit(repoID, taskID string, change phaseexec.Change) (taskstate.TaskState, error) {
	current, err := (phaseexec.Runner{Paths: s.paths, Store: workstate.NewStore(s.paths), RepositoryID: repoID, TaskID: taskID}).CommitHeld(context.Background(), change)
	if err != nil {
		return taskstate.TaskState{}, err
	}
	return taskstate.Project(current)
}
func runView(view taskstate.TaskState, err error) (taskstate.RunAttempt, error) {
	if err != nil {
		return taskstate.RunAttempt{}, err
	}
	run, _ := taskstate.LatestRun(view)
	return run, nil
}
func reviewView(view taskstate.TaskState, err error) (taskstate.ReviewAttempt, error) {
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	attempt, _ := taskstate.LatestReview(view)
	return attempt, nil
}

// SeedImplementation supplies an explicit fixture context before reserving the
// execution through Implementation. Production reservations cannot create context.
func (s Store) SeedImplementation(repoID, taskID string, opts implementation.Reservation) (taskstate.RunAttempt, error) {
	at := s.now().UTC()
	return runView(s.commit(repoID, taskID, func(current *work.State) (bool, error) {
		if len(current.Workflows) == 0 {
			current.Workflows = append(current.Workflows, work.Workflow{})
		}
		w := current.ActiveWorkflow()
		if w == nil {
			return false, errors.New("fixture requires an open workflow")
		}
		if w.Context() == nil {
			if opts.Context == nil {
				return false, errors.New("fixture requires explicit context")
			}
			observed := *opts.Context
			w.Phases = append(w.Phases, &work.PreparationPhase{Context: &observed, EstablishedAt: &at})
		}
		return implementation.ReserveChange(opts, at)(current)
	}))
}
func (s Store) SeedReview(repoID, taskID string, opts review.Reservation) (taskstate.ReviewAttempt, error) {
	opts.StartedAt = s.now().UTC()
	return reviewView(s.commit(repoID, taskID, review.ReserveChange(opts)))
}
func (s Store) ObserveStep(repoID, taskID string, attempt int, opts review.StepObservation) (taskstate.ReviewAttempt, error) {
	if opts.ObservedAt.IsZero() {
		opts.ObservedAt = s.now().UTC()
	}
	return reviewView(s.commit(repoID, taskID, review.StepChange(attempt, opts)))
}
func (s Store) SeedManual(ref work.AttemptRef, name string) (taskstate.ReviewAttempt, error) {
	view, err := s.Load(ref.RepositoryID, ref.TaskID)
	if err != nil {
		return taskstate.ReviewAttempt{}, err
	}
	attempt, _ := taskstate.LatestReview(view)
	return s.ObserveStep(ref.RepositoryID, ref.TaskID, attempt.Attempt, review.StepObservation{Address: ref, Name: name, Kind: review.KindManual})
}

// SeedManualDecision supplies already accepted native assessment evidence.
// Submission policy is exercised through Review's real Respond in its owners.
func (s Store) SeedManualDecision(ref work.AttemptRef, status taskstate.ReviewStatus) (taskstate.ReviewAttempt, error) {
	at := s.now().UTC()
	return reviewView(s.commit(ref.RepositoryID, ref.TaskID, func(current *work.State) (bool, error) {
		if ref.Step == nil {
			return false, errors.New("fixture decision requires an assessment")
		}
		p := current.Workflows[ref.Workflow].Phases[ref.Phase].(*work.ReviewPhase)
		step := p.Steps[*ref.Step].(*work.ManualReviewStep)
		switch status {
		case "", taskstate.ReviewStatusPassed:
			step.Decision = &work.ApprovedReview{At: at}
		case taskstate.ReviewStatusAborted:
			step.Decision = &work.AbortedReview{At: at, Reason: "Operator stopped manual review"}
		case taskstate.ReviewStatusBlocked:
			view, err := taskstate.Project(*current)
			if err != nil {
				return false, err
			}
			attempt, _ := taskstate.LatestReview(view)
			decision := &work.RequestedChanges{At: at}
			for _, finding := range attempt.Findings {
				if finding.Address != nil && finding.Address.Step != *ref.Step && taskstate.IsOpenBlockingReviewFinding(finding) {
					decision.Targets = append(decision.Targets, *finding.Address)
				}
			}
			step.Decision = decision
		default:
			return false, errors.New("fixture decision requires an actual assessment")
		}
		return true, nil
	}))
}
func (s Store) CompleteImplementation(ref work.AttemptRef, opts taskstate.CompleteRunOptions) (taskstate.RunAttempt, error) {
	return runView(s.commit(ref.RepositoryID, ref.TaskID, implementation.CompletionChange(ref, opts, s.now().UTC())))
}
func (s Store) ObserveProcess(ref work.AttemptRef, result work.ProcessResult, pid int) (taskstate.RunAttempt, error) {
	return runView(s.commit(ref.RepositoryID, ref.TaskID, implementation.ProcessChange(ref, result, pid)))
}
func (s Store) CheckpointPID(ref work.AttemptRef, pid int) (taskstate.RunAttempt, error) {
	return runView(s.commit(ref.RepositoryID, ref.TaskID, implementation.ChildPIDChange(ref, pid)))
}
func (s Store) SupplementUsage(ref work.AttemptRef, opts taskstate.RecordRunUsageOptions) (taskstate.RunAttempt, error) {
	return runView(s.commit(ref.RepositoryID, ref.TaskID, implementation.UsageChange(ref, opts)))
}
