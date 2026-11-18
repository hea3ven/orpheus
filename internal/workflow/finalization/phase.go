// Package finalization reconciles accepted delivery with source closure and
// independently retryable, non-forced cleanup. It never selects acceptance.
package finalization

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

type Backend interface {
	task.Getter
	task.CloseMutator
}

// Service is scoped to one selected task and invocation. Its scratch values
// carry presentation results, not a durable progress cursor.
type Service struct {
	Paths      state.Paths
	Repository task.Repository
	Task       task.Task
	Backend    Backend
	Git        ClosedTaskWorktreeGit

	finished bool
	observed task.Task
	cleanup  *WorktreeCleanupResult
}

// Outcome separates observed source closure from optional cleanup observations.
// A PR URL identifies PR acceptance; its absence identifies direct acceptance.
type Outcome struct {
	Task    task.Task
	PRURL   string
	Cleanup *WorktreeCleanupResult
}

// Pending is used by batch admission as well as the selected phase. A recorded
// source closure does not make a retained dedicated worktree stop being retryable.
func Pending(snapshot work.State, repoPath string) bool {
	if len(snapshot.Workflows) == 0 {
		return false
	}
	w := &snapshot.Workflows[len(snapshot.Workflows)-1]
	if publication, ok := w.LastPhase().(*work.PublicationPhase); ok {
		_, delivered := publication.Delivery.(*work.DirectDelivery)
		return delivered
	}
	final, ok := w.LastPhase().(*work.FinalizationPhase)
	if !ok {
		return false
	}
	if final.Closure == nil {
		return true
	}
	if w.Context().Directory == repoPath {
		return false
	}
	for _, observation := range final.Cleanup {
		if _, removed := observation.(*work.WorktreeRemoved); removed {
			return false
		}
	}
	return true
}

// Next derives closure or cleanup from the controller-selected phase. No Enter
// method or persisted queue is needed: workflow already supplied acceptance.
func (s *Service) Next(current work.State) (phaseexec.Selection[struct{}, Outcome], error) {
	_, final, err := selected(&current)
	if err != nil {
		return phaseexec.Selection[struct{}, Outcome]{}, err
	}
	if s.finished {
		return phaseexec.Return[struct{}](s.outcome(current)), nil
	}
	if final.Closure == nil {
		return phaseexec.Run[struct{}, Outcome](s.closeOperation()), nil
	}
	return phaseexec.Run[struct{}, Outcome](s.cleanupOperation()), nil
}

// Run drives the phase with a caller-supplied execution capability. Sync supplies
// ExecuteHeld to retain its existing coordinated mutation scope. Cleanup receipt
// failures remain warnings after closure; closure failures stop the invocation.
func (s *Service) Run(ctx context.Context, current work.State, execute func(context.Context, work.State, phaseexec.Operation) (work.State, error)) (work.State, Outcome, error) {
	for {
		selection, err := s.Next(current)
		if err != nil {
			return current, Outcome{}, err
		}
		if outcome, ok := selection.Outcome(); ok {
			return current, outcome, nil
		}
		op, _ := selection.Operation()
		_, final, err := selected(&current)
		if err != nil {
			return current, Outcome{}, err
		}
		closing := final.Closure == nil
		current, err = execute(ctx, current, op)
		if err == nil {
			continue
		}
		if closing {
			return current, Outcome{}, fmt.Errorf("finalize task %s source closure: %w", s.Task.ID, err)
		}
		if s.cleanup == nil {
			return current, Outcome{}, err
		}
		s.cleanup.Reason = joinCleanupReason(s.cleanup.Reason, "could not record local cleanup history: "+err.Error())
		return current, s.outcome(current), nil
	}
}

func selected(current *work.State) (*work.Workflow, *work.FinalizationPhase, error) {
	if len(current.Workflows) == 0 {
		return nil, nil, errors.New("closure handler requires a controller-selected finalization phase")
	}
	w := &current.Workflows[len(current.Workflows)-1]
	final, ok := w.LastPhase().(*work.FinalizationPhase)
	if !ok {
		return nil, nil, errors.New("closure handler requires a controller-selected finalization phase")
	}
	return w, final, nil
}

func (s *Service) outcome(current work.State) Outcome {
	result := Outcome{Task: s.observed, Cleanup: s.cleanup}
	w, final, _ := selected(&current)
	if accepted, ok := final.Acceptance.(*work.PRAcceptance); ok {
		external := w.Phases[accepted.ExternalReview].(*work.ExternalReviewPhase)
		result.PRURL = w.Phases[external.Publication.Phase].(*work.PublicationPhase).Delivery.(*work.PullRequestDelivery).PullRequest.URL
	}
	return result
}

func (s *Service) closeOperation() phaseexec.Operation {
	return phaseexec.Operation{Name: "close accepted task source", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		_, final, err := selected(current)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		changed := final.ClosureIntentAt == nil
		if changed {
			at := time.Now().UTC()
			final.ClosureIntentAt = &at
		}
		reason := taskstate.CloseReasonPRMerged
		if _, direct := final.Acceptance.(*work.DirectAcceptance); direct {
			reason = "destination_published"
		}
		return phaseexec.Prepared{Changed: changed, Run: func(ctx context.Context) phaseexec.Result {
			// Reconcile authoritative status after an ambiguous Close response or
			// failed receipt. Never blindly repeat the source mutation.
			current, err := s.Backend.Get(ctx, s.Task.ID)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			if current.Status != task.StatusClosed {
				if err := s.Backend.Close(ctx, s.Task.ID); err != nil {
					return phaseexec.Result{Err: err}
				}
			}
			observed, err := s.Backend.Get(ctx, s.Task.ID)
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			if observed.Status != task.StatusClosed {
				return phaseexec.Result{Err: errors.New("task-source closure was not independently observed")}
			}
			closure := work.TaskClosure{At: time.Now().UTC(), Reason: reason, Evidence: "Task source independently reports closed after recorded delivery acceptance"}
			return phaseexec.Result{Update: func(saved *work.State) (bool, error) {
				_, final, err := selected(saved)
				if err != nil {
					return false, err
				}
				final.Closure = &closure
				return true, nil
			}}
		}}, nil
	}}
}

func (s *Service) cleanupOperation() phaseexec.Operation {
	return phaseexec.Operation{Name: "clean finalized worktree", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		w, final, err := selected(current)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		if final.Closure == nil {
			return phaseexec.Prepared{}, errors.New("cleanup requires observed source closure")
		}
		view, err := taskstate.Project(*current)
		if err != nil {
			return phaseexec.Prepared{}, err
		}
		directory := w.Context().Directory
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			return s.cleanWorktree(ctx, view, directory)
		}}, nil
	}}
}

func (s *Service) cleanWorktree(ctx context.Context, view taskstate.TaskState, directory string) phaseexec.Result {
	observed, err := s.Backend.Get(ctx, s.Task.ID)
	if err != nil {
		return phaseexec.Result{Err: err}
	}
	s.observed, s.finished = observed, true
	if directory == s.Repository.Path {
		return phaseexec.Result{}
	}
	confirmed, err := s.Backend.Get(ctx, s.Task.ID)
	if err != nil {
		s.cleanup = &WorktreeCleanupResult{Outcome: WorktreeCleanupUnsafe, Worktree: directory, Reason: "confirm source closure before cleanup: " + err.Error()}
		return phaseexec.Result{}
	}
	if confirmed.Status != task.StatusClosed {
		s.cleanup = &WorktreeCleanupResult{Outcome: WorktreeCleanupUnsafe, Worktree: directory, Reason: "task source did not confirm closure before cleanup"}
		return phaseexec.Result{}
	}
	result := CleanClosedTaskWorktree(ctx, ClosedTaskWorktreeCleanupOptions{Paths: s.Paths, Repository: s.Repository, Task: confirmed, TaskState: view, Fix: true, Git: s.Git})
	s.cleanup = &result
	var observation work.CleanupObservation
	switch result.Outcome {
	case WorktreeCleanupRemoved:
		observation = &work.WorktreeRemoved{Path: result.Worktree, At: time.Now().UTC(), Evidence: "Native non-forced worktree removal completed"}
	case WorktreeCleanupAlreadyAbsent:
		observation = &work.WorktreeRemoved{Path: directory, At: time.Now().UTC(), Evidence: "Native inspection independently confirmed worktree absent"}
	case WorktreeCleanupUnsafe, WorktreeCleanupFailed:
		observation = &work.WorktreeRetained{Path: directory, At: time.Now().UTC(), Reason: result.Reason, Evidence: "Closed-task cleanup observed refusal or failure"}
	}
	if observation == nil {
		return phaseexec.Result{}
	}
	return phaseexec.Result{Update: func(saved *work.State) (bool, error) {
		_, final, err := selected(saved)
		if err != nil {
			return false, err
		}
		if final.Closure == nil {
			return false, errors.New("cleanup requires observed source closure")
		}
		final.Cleanup = append(final.Cleanup, observation)
		return true, nil
	}}
}
