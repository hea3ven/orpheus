package preparation

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/pathutil"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/tasktarget"
	"github.com/hea3ven/orpheus/internal/work"
	"github.com/hea3ven/orpheus/internal/workflow/phaseexec"
)

// Service is scoped to one dispatch. Setup observations are transient; a fresh
// invocation reconciles Git and task-source metadata rather than resuming a cursor.
type Service struct {
	Paths      state.Paths
	Repository task.Repository
	Task       task.Task
	Git        Git
	Backend    task.DispatchBackend
	setup      *gitmeta.TaskWorktreeSetupResult
	activated  bool
}

// Intent carries the admitted checkout choice, not a queued execution plan.
type Intent struct {
	Expected     gitmeta.TaskWorktreeSetupResult
	TargetKind   tasktarget.TargetKind
	AllowDirty   bool
	OwnedContext bool
}

type Request struct{}
type ContextEstablished struct {
	Setup gitmeta.TaskWorktreeSetupResult
}

// Next establishes or reconciles the selected checkout without choosing another phase.
func (s *Service) Next(current work.State, intent Intent) (phaseexec.Selection[Request, ContextEstablished], error) {
	if current.RepositoryID != s.Repository.ID || current.TaskID != s.Task.ID {
		return phaseexec.Selection[Request, ContextEstablished]{}, errors.New("preparation state belongs to a different task")
	}
	if s.setup == nil {
		if w := current.ActiveWorkflow(); w != nil && w.ResetRequestedAt != nil {
			return s.nextReset(current)
		}
		return phaseexec.Run[Request, ContextEstablished](s.setupOperation(intent)), nil
	}
	if !s.activated {
		return phaseexec.Run[Request, ContextEstablished](s.activationOperation()), nil
	}
	observed := setupContext(*s.setup)
	if w := current.ActiveWorkflow(); w != nil && w.Context() != nil && *w.Context() == observed {
		return phaseexec.Return[Request](ContextEstablished{Setup: *s.setup}), nil
	}
	return phaseexec.Run[Request, ContextEstablished](phaseexec.Operation{Name: "retain prepared work context", Prepare: func(snapshot *work.State) (phaseexec.Prepared, error) {
		changed, err := establish(snapshot, &observed, time.Now().UTC())
		return phaseexec.Prepared{Changed: changed}, err
	}}), nil
}

func (s *Service) setupOperation(intent Intent) phaseexec.Operation {
	return phaseexec.Operation{Name: "prepare task checkout", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		workflowIndex := len(current.Workflows) - 1
		if workflowIndex < 0 {
			workflowIndex = 0
		}
		ref := baselineRef(s.Repository.ID, s.Task.ID, workflowIndex)
		var recorded *gitmeta.Revision
		if w := current.ActiveWorkflow(); w != nil && w.Context() != nil {
			c := w.Context()
			recorded = &gitmeta.Revision{Commit: c.Baseline.Commit, Tree: c.Baseline.Tree, RetainedRef: c.CheckpointRef}
			ref = c.CheckpointRef
		}
		opts := gitmeta.TaskWorktreeOptions{RepoID: s.Repository.ID, RepoName: s.Repository.Name, RepoPath: s.Repository.Path, DefaultBranch: s.Repository.DefaultBranch, TaskID: s.Task.ID, Branch: intent.Expected.Branch, Paths: s.Paths, AllowDirty: intent.AllowDirty, OwnedContext: intent.OwnedContext}
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			git := s.Git
			var setup gitmeta.TaskWorktreeSetupResult
			var err error
			switch intent.TargetKind {
			case tasktarget.TargetRepoRootTeam:
				setup, err = git.SetupRepoRootTaskBranch(ctx, opts)
			case tasktarget.TargetWorktreeTeam:
				setup, err = git.SetupTaskWorktree(ctx, opts)
			default:
				err = fmt.Errorf("task %s has unsupported target %q", s.Task.ID, intent.TargetKind)
			}
			if err != nil {
				return phaseexec.Result{Err: err}
			}
			if setup.Branch != intent.Expected.Branch || cleanPath(setup.WorktreePath) != cleanPath(intent.Expected.WorktreePath) {
				return phaseexec.Result{Err: errors.New("prepared Git target does not match the selected task context")}
			}
			baseline, err := git.PrepareBaseline(ctx, setup.WorktreePath, setup.Branch, ref, recorded)
			if err != nil {
				return phaseexec.Result{Err: fmt.Errorf("retain prepared task baseline: %w", err)}
			}
			setup.StartingCommit, setup.StartingTree, setup.CheckpointRef = baseline.Commit, baseline.Tree, baseline.RetainedRef
			s.setup = &setup
			return phaseexec.Result{}
		}}, nil
	}}
}

func (s *Service) activationOperation() phaseexec.Operation {
	return phaseexec.Operation{Name: "activate task source", Prepare: func(current *work.State) (phaseexec.Prepared, error) {
		established := current.ActiveWorkflow() != nil && current.ActiveWorkflow().Context() != nil
		setup := *s.setup
		return phaseexec.Prepared{Run: func(ctx context.Context) phaseexec.Result {
			observed, err := s.Backend.Get(ctx, s.Task.ID)
			if err != nil {
				return phaseexec.Result{Err: fmt.Errorf("verify task before implementation: %w", err)}
			}
			if observed.ID != s.Task.ID {
				return phaseexec.Result{Err: errors.New("task source returned a different task identity")}
			}
			metadata := observed.OrpheusMetadata()
			reusing := established && observed.Status == task.StatusInProgress && metadata.HasBranch && strings.TrimSpace(metadata.Branch) == setup.Branch && metadata.HasWorktree && cleanPath(metadata.Worktree) == cleanPath(setup.WorktreePath)
			if !reusing {
				if err := s.Backend.MarkInProgress(ctx, s.Task.ID, setup.Branch, setup.WorktreePath); err != nil {
					return phaseexec.Result{Err: fmt.Errorf("mark task in progress: %w", err)}
				}
			}
			s.activated = true
			return phaseexec.Result{}
		}}, nil
	}}
}

func setupContext(setup gitmeta.TaskWorktreeSetupResult) work.WorkContext {
	return work.WorkContext{Directory: setup.WorktreePath, TaskBranch: setup.Branch, CheckpointRef: setup.CheckpointRef, Baseline: work.CodeIdentity{Commit: setup.StartingCommit, Tree: setup.StartingTree}}
}

func baselineRef(repoID, taskID string, workflow int) string {
	return fmt.Sprintf("refs/orpheus/baselines/%s/%s/%d", hex.EncodeToString([]byte(repoID)), hex.EncodeToString([]byte(taskID)), workflow)
}

func cleanPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		if canonical, err := pathutil.CanonicalAbs(path); err == nil {
			return canonical
		}
	}
	return filepath.Clean(path)
}
