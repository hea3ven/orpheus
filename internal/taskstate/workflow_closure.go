package taskstate

import (
	"errors"
	"strings"
	"time"

	"github.com/hea3ven/orpheus/internal/work"
)

// RecordTaskClosed retains observed provider acceptance and source closure. It
// cannot turn an arbitrary source PR URL into missing local publication history.
func (s Store) RecordTaskClosed(repoID, taskID string, opts TaskClosedOptions) (Event, error) {
	if opts.Reason != CloseReasonPRMerged || opts.ObservedPRState != "merged" || strings.TrimSpace(opts.PRURL) == "" {
		return Event{}, errors.New("PR closure requires actual merged-provider and source-closure observations")
	}
	var event Event
	_, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		if len(snapshot.Workflows) == 0 {
			return false, errors.New("source closure requires its owning workflow")
		}
		wi := len(snapshot.Workflows) - 1
		w := &snapshot.Workflows[wi]
		if final, ok := w.LastPhase().(*work.FinalizationPhase); ok && final.Closure != nil {
			accepted, ok := final.Acceptance.(*work.PRAcceptance)
			if !ok {
				return false, errors.New("source closure belongs to a different acceptance")
			}
			external := w.Phases[accepted.ExternalReview].(*work.ExternalReviewPhase)
			delivery := w.Phases[external.Publication.Phase].(*work.PublicationPhase).Delivery.(*work.PullRequestDelivery)
			if delivery.PullRequest.URL != opts.PRURL {
				return false, errors.New("closure PR differs from recorded acceptance")
			}
			event = Event{Type: EventTaskClosed, At: final.Closure.At, CloseReason: opts.Reason, PRURL: opts.PRURL, ObservedPRState: opts.ObservedPRState}
			return false, nil
		}
		externalIndex := -1
		for i := len(w.Phases) - 1; i >= 0; i-- {
			external, ok := w.Phases[i].(*work.ExternalReviewPhase)
			if !ok {
				continue
			}
			publication := w.Phases[external.Publication.Phase].(*work.PublicationPhase)
			delivery, ok := publication.Delivery.(*work.PullRequestDelivery)
			if ok && delivery.PullRequest.URL == opts.PRURL {
				externalIndex = i
				break
			}
		}
		if externalIndex < 0 {
			return false, errors.New("merged PR has no recorded external-review owner")
		}
		now := s.nowUTC()
		final := &work.FinalizationPhase{Acceptance: &work.PRAcceptance{ExternalReview: externalIndex, ObservedAt: now, Evidence: "Provider independently reported the recorded PR merged"}, Closure: &work.TaskClosure{At: now, Reason: opts.Reason, Evidence: "Task source independently confirmed closed after merged PR observation"}}
		ref := work.AttemptRef{RepositoryID: repoID, TaskID: taskID, Workflow: wi, Phase: len(w.Phases)}
		w.Phases = append(w.Phases, final)
		event = Event{Address: ref, Type: EventTaskClosed, At: now, CloseReason: opts.Reason, PRURL: opts.PRURL, ObservedPRState: opts.ObservedPRState}
		return true, nil
	})
	return event, err
}

func (s Store) RecordWorktreeCleanup(repoID, taskID string, opts WorktreeCleanupOptions) (Event, error) {
	var event Event
	_, err := s.updateWorkflow(repoID, taskID, func(snapshot *work.State) (bool, error) {
		if len(snapshot.Workflows) == 0 {
			return false, errors.New("cleanup requires observed workflow closure")
		}
		wi := len(snapshot.Workflows) - 1
		w := &snapshot.Workflows[wi]
		final, ok := w.LastPhase().(*work.FinalizationPhase)
		if !ok || final.Closure == nil {
			return false, errors.New("cleanup requires observed source closure")
		}
		if opts.Worktree != w.Context().Directory {
			return false, errors.New("cleanup path differs from the prepared workflow")
		}
		for _, record := range final.Cleanup {
			if removed, ok := record.(*work.WorktreeRemoved); ok && removed.Path == opts.Worktree {
				event = Event{Type: EventWorktreeRemoved, At: removed.At, Worktree: removed.Path}
				return false, nil
			}
		}
		now := s.nowUTC()
		final.Cleanup = append(final.Cleanup, &work.WorktreeRemoved{Path: opts.Worktree, At: now, Evidence: "Native non-forced worktree removal completed"})
		event = Event{Address: work.AttemptRef{RepositoryID: repoID, TaskID: taskID, Workflow: wi, Phase: len(w.Phases) - 1}, Type: EventWorktreeRemoved, At: now, Worktree: opts.Worktree}
		return true, nil
	})
	return event, err
}

// CheckMergedPRClosure validates a prospective closure before mutating the task
// source. The hypothetical observation is never persisted; RecordTaskClosed
// still requires the provider and source's actual independent confirmations.
func CheckMergedPRClosure(view TaskState, url string, at time.Time) error {
	if view.Work == nil || len(view.Work.Workflows) == 0 {
		return errors.New("merged PR has no workflow-owned delivery")
	}
	if err := view.Work.Validate(); err != nil {
		return err
	}
	snapshot := *view.Work
	snapshot.Workflows = append([]work.Workflow(nil), snapshot.Workflows...)
	w := &snapshot.Workflows[len(snapshot.Workflows)-1]
	if final, ok := w.LastPhase().(*work.FinalizationPhase); ok && final.Closure != nil {
		accepted, ok := final.Acceptance.(*work.PRAcceptance)
		if !ok {
			return errors.New("source closure belongs to another acceptance")
		}
		external := w.Phases[accepted.ExternalReview].(*work.ExternalReviewPhase)
		publication := w.Phases[external.Publication.Phase].(*work.PublicationPhase)
		delivery, ok := publication.Delivery.(*work.PullRequestDelivery)
		if !ok || delivery.PullRequest.URL != url {
			return errors.New("source closure PR differs from its delivery")
		}
		return nil
	}
	for index := len(w.Phases) - 1; index >= 0; index-- {
		external, ok := w.Phases[index].(*work.ExternalReviewPhase)
		if !ok {
			continue
		}
		publication := w.Phases[external.Publication.Phase].(*work.PublicationPhase)
		delivery, ok := publication.Delivery.(*work.PullRequestDelivery)
		if !ok || delivery.PullRequest.URL != url {
			continue
		}
		w.Phases = append(append([]work.Phase(nil), w.Phases...), &work.FinalizationPhase{Acceptance: &work.PRAcceptance{ExternalReview: index, ObservedAt: at, Evidence: "Prospective independently observed PR merge"}, Closure: &work.TaskClosure{At: at, Reason: CloseReasonPRMerged, Evidence: "Prospective task-source closure"}})
		return snapshot.Validate()
	}
	return errors.New("merged PR has no recorded external-review owner")
}
