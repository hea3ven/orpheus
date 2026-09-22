package workflow_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	gitmeta "github.com/hea3ven/orpheus/internal/git"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
	"github.com/hea3ven/orpheus/internal/workflow"
)

type fakeClosedTaskWorktreeGit struct {
	inspection gitmeta.ClosedTaskWorktreeInspection
	removal    gitmeta.ClosedTaskWorktreeRemoval
	inspects   int
	removes    int
}

func (g *fakeClosedTaskWorktreeGit) InspectClosedTaskWorktree(
	context.Context,
	gitmeta.ClosedTaskWorktreeOptions,
) gitmeta.ClosedTaskWorktreeInspection {
	g.inspects++
	return g.inspection
}

func (g *fakeClosedTaskWorktreeGit) RemoveClosedTaskWorktree(
	context.Context,
	gitmeta.ClosedTaskWorktreeOptions,
) gitmeta.ClosedTaskWorktreeRemoval {
	g.removes++
	return g.removal
}

type fakeWorktreeCleanupRecorder struct {
	calls int
	err   error
	opts  taskstate.WorktreeCleanupOptions
}

func (r *fakeWorktreeCleanupRecorder) RecordWorktreeCleanup(
	_ string,
	_ string,
	opts taskstate.WorktreeCleanupOptions,
) (taskstate.Event, error) {
	r.calls++
	r.opts = opts
	if r.err != nil {
		return taskstate.Event{}, r.err
	}
	return taskstate.Event{Type: taskstate.EventWorktreeRemoved}, nil
}

func TestClosedTaskWorktreeCleanupReportsPendingAndRepairsEligibleDedicatedWorktrees(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	repo := task.Repository{ID: "alpha", Name: "Alpha", Path: "/fixture/alpha", DefaultBranch: "main"}
	worktree, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-1"))
	if err != nil {
		t.Fatalf("worktree path: %v", err)
	}
	taskItem := task.Task{
		ID: "op-1", Status: task.StatusClosed,
		Metadata: task.Metadata{task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktree},
	}
	taskState := taskstate.TaskState{
		RepoID: "alpha", TaskID: "op-1",
		WorkDirectory: taskstate.WorkDirectory{Path: worktree},
		GitFacts:      taskstate.GitFacts{Branch: "orpheus/op-1", Worktree: worktree},
	}

	tests := []struct {
		name       string
		fix        bool
		inspection gitmeta.ClosedTaskWorktreeInspection
		removal    gitmeta.ClosedTaskWorktreeRemoval
		recordErr  error
		want       workflow.WorktreeCleanupOutcome
		wantReason string
		wantRemove int
		wantRecord int
	}{
		{
			name:       "dry run reports cleanup pending",
			inspection: gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeEligible, Worktree: worktree},
			want:       workflow.WorktreeCleanupPending,
		},
		{
			name:       "fix removes eligible worktree and records audit",
			fix:        true,
			inspection: gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeEligible, Worktree: worktree},
			removal:    gitmeta.ClosedTaskWorktreeRemoval{Outcome: gitmeta.ClosedTaskWorktreeRemoved, Worktree: worktree},
			want:       workflow.WorktreeCleanupRemoved,
			wantRemove: 1,
			wantRecord: 1,
		},

		{
			name:       "dirty worktree remains for operator",
			fix:        true,
			inspection: gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeEligible, Worktree: worktree},
			removal:    gitmeta.ClosedTaskWorktreeRemoval{Outcome: gitmeta.ClosedTaskWorktreeFailed, Worktree: worktree, Reason: "contains modified or untracked files"},
			want:       workflow.WorktreeCleanupFailed,
			wantReason: "contains modified or untracked files",
			wantRemove: 1,
		},
		{
			name:       "removal failure remains discoverable without an audit event",
			fix:        true,
			inspection: gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeEligible, Worktree: worktree},
			removal:    gitmeta.ClosedTaskWorktreeRemoval{Outcome: gitmeta.ClosedTaskWorktreeFailed, Worktree: worktree, Reason: "worktree is locked"},
			want:       workflow.WorktreeCleanupFailed,
			wantRemove: 1,
		},
		{
			name:       "audit recording failure remains visible after removal",
			fix:        true,
			inspection: gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeEligible, Worktree: worktree},
			removal:    gitmeta.ClosedTaskWorktreeRemoval{Outcome: gitmeta.ClosedTaskWorktreeRemoved, Worktree: worktree},
			recordErr:  errors.New("disk full"),
			want:       workflow.WorktreeCleanupRemoved,
			wantReason: "could not record local cleanup history: disk full",
			wantRemove: 1,
			wantRecord: 1,
		},
		{
			name:       "already absent is idempotent",
			fix:        true,
			inspection: gitmeta.ClosedTaskWorktreeInspection{Outcome: gitmeta.ClosedTaskWorktreeAbsent, Worktree: worktree},
			want:       workflow.WorktreeCleanupAlreadyAbsent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git := &fakeClosedTaskWorktreeGit{inspection: tt.inspection, removal: tt.removal}
			recorder := &fakeWorktreeCleanupRecorder{err: tt.recordErr}
			got := workflow.CleanClosedTaskWorktree(context.Background(), workflow.ClosedTaskWorktreeCleanupOptions{
				Paths: paths, Repository: repo, Task: taskItem, TaskState: taskState, Fix: tt.fix, Git: git, Recorder: recorder,
			})
			if got.Outcome != tt.want {
				t.Fatalf("cleanup = %#v, want outcome %q", got, tt.want)
			}
			if tt.wantReason != "" && !strings.Contains(got.Reason, tt.wantReason) {
				t.Fatalf("cleanup reason = %q, want %q", got.Reason, tt.wantReason)
			}
			if git.removes != tt.wantRemove || recorder.calls != tt.wantRecord {
				t.Fatalf("remove/record calls = %d/%d, want %d/%d", git.removes, recorder.calls, tt.wantRemove, tt.wantRecord)
			}
			if recorder.calls == 1 && recorder.opts.Worktree != worktree {
				t.Fatalf("recorded cleanup = %#v, want worktree %q", recorder.opts, worktree)
			}
		})
	}
}

func TestClosedTaskWorktreeCleanupRejectsUnownedOrActiveTargetsBeforeGit(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatal(err)
	}
	worktree, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"active", "repo-root", "metadata", "state", "work-directory"} {
		t.Run(kind, func(t *testing.T) {
			git := &fakeClosedTaskWorktreeGit{}
			opts := workflow.ClosedTaskWorktreeCleanupOptions{
				Paths:      paths,
				Repository: task.Repository{ID: "alpha", Path: "/fixture/alpha", DefaultBranch: "main"},
				Task: task.Task{ID: "op-1", Status: task.StatusClosed, Metadata: task.Metadata{
					task.MetadataBranch: "orpheus/op-1", task.MetadataWorktree: worktree,
				}},
				TaskState: taskstate.TaskState{RepoID: "alpha", TaskID: "op-1", GitFacts: taskstate.GitFacts{
					Branch: "orpheus/op-1", Worktree: worktree,
				}},
				Fix: true, Git: git,
			}
			want := workflow.WorktreeCleanupUnsafe
			switch kind {
			case "active":
				opts.Task.Status = task.StatusInProgress
				want = workflow.WorktreeCleanupNotApplicable
			case "repo-root":
				opts.Task.Metadata[task.MetadataWorktree] = opts.Repository.Path
				opts.TaskState.GitFacts.Worktree = opts.Repository.Path
				want = workflow.WorktreeCleanupNotApplicable
			case "metadata":
				opts.Task.Metadata[task.MetadataBranch] = "foreign"
			case "state":
				opts.TaskState.GitFacts.Worktree = "/foreign/worktree"
			case "work-directory":
				opts.TaskState.WorkDirectory = taskstate.WorkDirectory{Path: "/foreign/worktree"}
			}

			got := workflow.CleanClosedTaskWorktree(context.Background(), opts)

			if got.Outcome != want || git.inspects != 0 || git.removes != 0 {
				t.Fatalf("cleanup = %#v, Git calls = %d/%d, want %s without Git", got, git.inspects, git.removes, want)
			}
		})
	}
}
