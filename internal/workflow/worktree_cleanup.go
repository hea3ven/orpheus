package workflow

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/tasktarget"
)

// WorktreeCleanupOutcome is the operator-facing result of closed-task
// worktree cleanup.
type WorktreeCleanupOutcome string

const (
	WorktreeCleanupNotApplicable WorktreeCleanupOutcome = "not_applicable"
	WorktreeCleanupWouldRemove   WorktreeCleanupOutcome = "would_remove"
	WorktreeCleanupRemoved       WorktreeCleanupOutcome = "removed"
	WorktreeCleanupAlreadyAbsent WorktreeCleanupOutcome = "already_absent"
	WorktreeCleanupDirty         WorktreeCleanupOutcome = "dirty"
	WorktreeCleanupUnsafe        WorktreeCleanupOutcome = "unsafe"
	WorktreeCleanupFailed        WorktreeCleanupOutcome = "failed"
)

// WorktreeCleanupResult reports the shared cleanup policy decision.
type WorktreeCleanupResult struct {
	Outcome  WorktreeCleanupOutcome
	Worktree string
	Reason   string
}

// ClosedTaskWorktreeGit is the Git boundary used by the shared cleanup policy.
type ClosedTaskWorktreeGit interface {
	InspectClosedTaskWorktree(context.Context, gitmeta.ClosedTaskWorktreeOptions) gitmeta.ClosedTaskWorktreeInspection
	RemoveClosedTaskWorktree(context.Context, gitmeta.ClosedTaskWorktreeOptions) gitmeta.ClosedTaskWorktreeRemoval
}

// WorktreeCleanupRecorder records successful cleanup in local task history.
type WorktreeCleanupRecorder interface {
	RecordWorktreeCleanup(repoID, taskID string, opts taskstate.WorktreeCleanupOptions) (taskstate.Event, error)
}

// ClosedTaskWorktreeCleanupOptions gives the shared policy all of the durable
// facts required to identify an Orpheus-owned dedicated worktree.
type ClosedTaskWorktreeCleanupOptions struct {
	Paths      state.Paths
	Repository task.Repository
	Task       task.Task
	TaskState  taskstate.TaskState
	Fix        bool
	Git        ClosedTaskWorktreeGit
	Recorder   WorktreeCleanupRecorder
}

// CleanClosedTaskWorktree classifies or removes one closed task's dedicated
// worktree. It trusts neither recorded paths nor Git alone: backend task
// status, task metadata, task state, and the deterministic target must agree.
// Dirty or uncertain worktrees are left untouched.
//
//nolint:funlen // The policy intentionally lists each safety proof before Git can remove a worktree.
func CleanClosedTaskWorktree(ctx context.Context, opts ClosedTaskWorktreeCleanupOptions) WorktreeCleanupResult {
	if ctx == nil {
		ctx = context.Background()
	}
	if opts.Task.Status != task.StatusClosed {
		return WorktreeCleanupResult{Outcome: WorktreeCleanupNotApplicable, Reason: "task source does not confirm closure"}
	}
	facts, ok := taskstate.GitFactsFor(opts.TaskState)
	if !ok {
		return WorktreeCleanupResult{Outcome: WorktreeCleanupUnsafe, Reason: "task state has no recorded Git target"}
	}
	targets, err := tasktarget.ExpectedTargetsForTaskOrRecordedBranch(opts.Repository, opts.Task, facts.Branch, opts.Paths)
	if err != nil {
		return WorktreeCleanupResult{Outcome: WorktreeCleanupUnsafe, Reason: "resolve deterministic worktree identity: " + err.Error()}
	}
	metadataTarget, err := tasktarget.ClassifyMetadataTarget(opts.Task.OrpheusMetadata(), targets)
	if err != nil {
		return unsafeClosedTaskWorktreeCleanup(targets.WorktreeTeam.Worktree, "task metadata does not identify an Orpheus target", err)
	}
	stateTarget, err := tasktarget.ClassifyGitFacts(facts, targets)
	if err != nil {
		return unsafeClosedTaskWorktreeCleanup(targets.WorktreeTeam.Worktree, "task state does not identify an Orpheus target", err)
	}
	if metadataTarget.Kind != stateTarget.Kind || metadataTarget.Branch != stateTarget.Branch || metadataTarget.Worktree != stateTarget.Worktree {
		return WorktreeCleanupResult{
			Outcome:  WorktreeCleanupUnsafe,
			Worktree: targets.WorktreeTeam.Worktree,
			Reason:   "task metadata and task state do not both identify the deterministic dedicated worktree",
		}
	}
	if metadataTarget.Kind != tasktarget.TargetWorktreeTeam {
		return WorktreeCleanupResult{Outcome: WorktreeCleanupNotApplicable}
	}
	if directory, recorded := taskstate.WorkDirectoryFor(opts.TaskState); recorded && filepath.Clean(directory.Path) != stateTarget.Worktree {
		return WorktreeCleanupResult{
			Outcome:  WorktreeCleanupUnsafe,
			Worktree: stateTarget.Worktree,
			Reason:   "recorded work directory does not match the deterministic dedicated worktree",
		}
	}

	gitState := opts.Git
	if gitState == nil {
		gitState = gitmeta.LocalClosedTaskWorktreeGit{}
	}
	gitOpts := gitmeta.ClosedTaskWorktreeOptions{
		RepoID: opts.Repository.ID, RepoName: opts.Repository.Name, RepoPath: opts.Repository.Path,
		DefaultBranch: opts.Repository.DefaultBranch, TaskID: opts.Task.ID, Branch: stateTarget.Branch, Paths: opts.Paths,
	}
	inspection := gitState.InspectClosedTaskWorktree(ctx, gitOpts)
	if !opts.Fix || inspection.Outcome != gitmeta.ClosedTaskWorktreeClean {
		return worktreeCleanupInspectionResult(inspection)
	}

	removal := gitState.RemoveClosedTaskWorktree(ctx, gitOpts)
	result := worktreeCleanupRemovalResult(removal)
	if result.Outcome != WorktreeCleanupRemoved || opts.Recorder == nil {
		return result
	}
	if _, err := opts.Recorder.RecordWorktreeCleanup(opts.Repository.ID, opts.Task.ID, taskstate.WorktreeCleanupOptions{Worktree: result.Worktree}); err != nil {
		result.Reason = joinCleanupReason(result.Reason, "removed, but could not record local cleanup history: "+err.Error())
	}
	return result
}

type closedTaskWorktreeCleanupStore interface {
	WorktreeCleanupRecorder
	Load(repoID, taskID string) (taskstate.TaskState, error)
}

func cleanClosedTaskWorktreeAfterClosure(
	ctx context.Context,
	paths state.Paths,
	repo task.Repository,
	taskItem task.Task,
	backend task.Getter,
	store closedTaskWorktreeCleanupStore,
	gitState ClosedTaskWorktreeGit,
) *WorktreeCleanupResult {
	worktree := deterministicDedicatedWorktreePath(paths, repo, taskItem)
	if worktree == "" {
		return nil
	}
	current, err := store.Load(repo.ID, taskItem.ID)
	if err != nil {
		return &WorktreeCleanupResult{Outcome: WorktreeCleanupUnsafe, Worktree: worktree, Reason: "reload local task state after closure: " + err.Error()}
	}
	facts, hasFacts := taskstate.GitFactsFor(current)
	if !hasFacts || strings.TrimSpace(facts.Worktree) == "" || filepath.Clean(facts.Worktree) == filepath.Clean(repo.Path) {
		return nil
	}
	closed, err := backend.Get(ctx, taskItem.ID)
	if err != nil {
		return &WorktreeCleanupResult{Outcome: WorktreeCleanupUnsafe, Worktree: worktree, Reason: "confirm task source closure before cleanup: " + err.Error()}
	}
	if closed.Status != task.StatusClosed {
		return &WorktreeCleanupResult{Outcome: WorktreeCleanupUnsafe, Worktree: worktree, Reason: "task source did not confirm closure before cleanup"}
	}
	result := CleanClosedTaskWorktree(ctx, ClosedTaskWorktreeCleanupOptions{
		Paths: paths, Repository: repo, Task: closed, TaskState: current, Fix: true, Git: gitState, Recorder: store,
	})
	return &result
}

func deterministicDedicatedWorktreePath(paths state.Paths, repo task.Repository, taskItem task.Task) string {
	metadata := taskItem.OrpheusMetadata()
	targets, err := tasktarget.ExpectedTargetsForTaskOrRecordedBranch(repo, taskItem, metadata.Branch, paths)
	if err != nil {
		return ""
	}
	target, err := tasktarget.ClassifyMetadataTarget(metadata, targets)
	if err != nil || target.Kind != tasktarget.TargetWorktreeTeam {
		return ""
	}
	return target.Worktree
}

func unsafeClosedTaskWorktreeCleanup(worktree string, operation string, err error) WorktreeCleanupResult {
	return WorktreeCleanupResult{Outcome: WorktreeCleanupUnsafe, Worktree: worktree, Reason: operation + ": " + err.Error()}
}

func worktreeCleanupInspectionResult(inspection gitmeta.ClosedTaskWorktreeInspection) WorktreeCleanupResult {
	switch inspection.Outcome {
	case gitmeta.ClosedTaskWorktreeClean:
		return WorktreeCleanupResult{Outcome: WorktreeCleanupWouldRemove, Worktree: inspection.Worktree, Reason: "clean deterministic worktree can be removed"}
	case gitmeta.ClosedTaskWorktreeAbsent:
		return WorktreeCleanupResult{Outcome: WorktreeCleanupAlreadyAbsent, Worktree: inspection.Worktree, Reason: inspection.Reason}
	case gitmeta.ClosedTaskWorktreeDirty:
		return WorktreeCleanupResult{Outcome: WorktreeCleanupDirty, Worktree: inspection.Worktree, Reason: inspection.Reason}
	case gitmeta.ClosedTaskWorktreeUnsafe:
		return WorktreeCleanupResult{Outcome: WorktreeCleanupUnsafe, Worktree: inspection.Worktree, Reason: inspection.Reason}
	default:
		return WorktreeCleanupResult{Outcome: WorktreeCleanupFailed, Worktree: inspection.Worktree, Reason: joinCleanupReason(inspection.Reason, "unexpected Git cleanup inspection result")}
	}
}

func worktreeCleanupRemovalResult(removal gitmeta.ClosedTaskWorktreeRemoval) WorktreeCleanupResult {
	switch removal.Outcome {
	case gitmeta.ClosedTaskWorktreeRemoved:
		return WorktreeCleanupResult{Outcome: WorktreeCleanupRemoved, Worktree: removal.Worktree, Reason: removal.Reason}
	case gitmeta.ClosedTaskWorktreeAbsent:
		return WorktreeCleanupResult{Outcome: WorktreeCleanupAlreadyAbsent, Worktree: removal.Worktree, Reason: removal.Reason}
	case gitmeta.ClosedTaskWorktreeDirty:
		return WorktreeCleanupResult{Outcome: WorktreeCleanupDirty, Worktree: removal.Worktree, Reason: removal.Reason}
	case gitmeta.ClosedTaskWorktreeUnsafe:
		return WorktreeCleanupResult{Outcome: WorktreeCleanupUnsafe, Worktree: removal.Worktree, Reason: removal.Reason}
	default:
		return WorktreeCleanupResult{Outcome: WorktreeCleanupFailed, Worktree: removal.Worktree, Reason: removal.Reason}
	}
}

func joinCleanupReason(reason string, suffix string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return suffix
	}
	return fmt.Sprintf("%s; %s", reason, suffix)
}
