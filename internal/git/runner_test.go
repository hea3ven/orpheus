//nolint:testpackage // Command runner tests verify unexported Git orchestration decisions.
package git

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/task"
	"github.com/hea3ven/orpheus/internal/testutil"
)

type recordedGitCommand struct {
	directory string
	args      []string
	input     string
	result    CommandResult
	err       error
}

type recordingGitRunner struct {
	t        *testing.T
	commands []recordedGitCommand
	calls    []Command
}

func (r *recordingGitRunner) Run(_ context.Context, command Command) (CommandResult, error) {
	r.t.Helper()
	r.calls = append(r.calls, command)
	index := len(r.calls) - 1
	if index >= len(r.commands) {
		r.t.Fatalf("unexpected Git command %q in %q", command.Args, command.Directory)
	}
	want := r.commands[index]
	if command.Directory != want.directory || !reflect.DeepEqual(command.Args, want.args) || command.Input != want.input {
		r.t.Fatalf("Git command %d = %#v, want arguments %q in %q with input %q", index+1, command, want.args, want.directory, want.input)
	}
	return want.result, want.err
}

func (r *recordingGitRunner) assertComplete() {
	r.t.Helper()
	if len(r.calls) != len(r.commands) {
		r.t.Fatalf("Git command count = %d, want %d; calls = %#v", len(r.calls), len(r.commands), r.calls)
	}
}

type partialWorktreeRemovalRunner struct {
	*recordingGitRunner
	worktree string
}

func (r *partialWorktreeRemovalRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	result, err := r.recordingGitRunner.Run(ctx, command)
	if len(command.Args) >= 2 && command.Args[0] == "worktree" && command.Args[1] == "remove" {
		if removeErr := os.RemoveAll(r.worktree); removeErr != nil {
			r.t.Fatalf("remove worktree during simulated partial cleanup: %v", removeErr)
		}
	}
	return result, err
}

func TestCommandRunnerContextAndExitError(t *testing.T) {
	inner := errors.New("runner failure")
	wrapped := CommandExitError{Code: 17, Err: inner}
	if got := wrapped.Error(); got != inner.Error() {
		t.Fatalf("wrapped error = %q, want %q", got, inner)
	}
	if !errors.Is(wrapped, inner) || wrapped.ExitCode() != 17 {
		t.Fatalf("wrapped command exit error = %#v, want original error and exit code", wrapped)
	}
	plain := CommandExitError{Code: 9}
	if got := plain.Error(); got != "git command exited with status 9" {
		t.Fatalf("plain error = %q, want exit status message", got)
	}

	runner := &recordingGitRunner{t: t}
	ctx := ContextWithRunner(context.Background(), runner)
	if got := runnerFromContext(ctx); got != runner {
		t.Fatalf("runner from context = %#v, want injected runner", got)
	}
	if got := ContextWithRunner(ctx, nil); got != ctx {
		t.Fatal("nil runner did not preserve context")
	}
}

func TestRemoveClosedTaskWorktreeReportsPartialRemovalFailure(t *testing.T) {
	paths, err := state.NewPaths(filepath.Join(testutil.CanonicalTempDir(t), "config"), filepath.Join(testutil.CanonicalTempDir(t), "data"))
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	repoPath := filepath.Join(testutil.CanonicalTempDir(t), "repo")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("create repo path: %v", err)
	}
	opts := ClosedTaskWorktreeOptions{
		RepoID: "alpha", RepoName: "Alpha", RepoPath: repoPath, DefaultBranch: "main", TaskID: "op-partial", Paths: paths,
	}
	worktree, err := paths.DataPath(filepath.Join("repos", "alpha", "worktrees", "op-partial"))
	if err != nil {
		t.Fatalf("resolve worktree path: %v", err)
	}
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatalf("create worktree path: %v", err)
	}
	commonDir := filepath.Join(repoPath, ".git")
	runner := &partialWorktreeRemovalRunner{
		recordingGitRunner: &recordingGitRunner{t: t, commands: []recordedGitCommand{
			gitCommand(repoPath, "rev-parse", "--show-toplevel").withStdout(repoPath + "\n"),
			gitCommand(repoPath, "rev-parse", "--git-common-dir").withStdout(commonDir + "\n"),
			gitCommand(worktree, "rev-parse", "--show-toplevel").withStdout(worktree + "\n"),
			gitCommand(worktree, "symbolic-ref", "--quiet", "--short", "HEAD").withStdout("orpheus/op-partial\n"),
			gitCommand(worktree, "rev-parse", "--git-common-dir").withStdout(commonDir + "\n"),
			gitCommand(repoPath, "worktree", "list", "--porcelain", "-z").withStdout("worktree " + worktree + "\x00\x00"),
			gitCommand(worktree, "status", "--porcelain=v1", "--untracked-files=all", "--ignored=matching"),
			gitCommand(worktree, "worktree", "remove", worktree).withError(CommandExitError{Code: 1, Err: errors.New("administrative cleanup failed")}),
			gitCommand(repoPath, "rev-parse", "--show-toplevel").withStdout(repoPath + "\n"),
			gitCommand(repoPath, "rev-parse", "--git-common-dir").withStdout(commonDir + "\n"),
			gitCommand(repoPath, "worktree", "list", "--porcelain", "-z").withStdout("worktree " + worktree + "\x00prunable stale registration\x00\x00"),
		}},

		worktree: worktree,
	}

	result := RemoveClosedTaskWorktree(ContextWithRunner(context.Background(), runner), opts)
	if result.Outcome != ClosedTaskWorktreeFailed || !strings.Contains(result.Reason, "administrative cleanup failed") {
		t.Fatalf("partial removal = %#v, want failed result with Git error", result)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Fatalf("partial removal worktree stat error = %v, want absent", err)
	}
	inspection := InspectClosedTaskWorktree(ContextWithRunner(context.Background(), runner), opts)
	if inspection.Outcome != ClosedTaskWorktreeFailed || !strings.Contains(inspection.Reason, "still registers") {
		t.Fatalf("inspection after partial removal = %#v, want unresolved registration", inspection)
	}
	runner.assertComplete()
}

func TestValidateRecordedDirectMergeDecisions(t *testing.T) {
	tests := []struct {
		name           string
		remoteContains bool
		localCommit    string
		wantRecovered  bool
		wantError      string
	}{
		{name: "remote already contains merge", remoteContains: true, wantRecovered: true},
		{name: "local destination was reset", localCommit: "parent", wantError: "not recorded merge"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const (
				repoPath = "/recorded/repo"
				merge    = "merge-commit"
			)
			commands := []recordedGitCommand{
				gitCommand(repoPath, "check-ref-format", "refs/heads/main"),
				gitCommand(repoPath, "fetch", "origin", "+refs/heads/main:refs/remotes/origin/main"),
			}
			if tt.remoteContains {
				commands = append(commands, gitCommand(repoPath, "merge-base", "--is-ancestor", merge, "refs/remotes/origin/main"))
			} else {
				commands = append(commands,
					gitCommand(repoPath, "merge-base", "--is-ancestor", merge, "refs/remotes/origin/main").withError(CommandExitError{Code: 1}),
					gitCommand(repoPath, "rev-parse", "refs/heads/main").withStdout(tt.localCommit+"\n"),
				)
			}
			runner := &recordingGitRunner{t: t, commands: commands}

			recovered, err := ValidateRecordedDirectMerge(
				ContextWithRunner(context.Background(), runner),
				task.Repository{ID: "alpha", Path: repoPath, DefaultBranch: "main"},
				merge,
			)
			if tt.wantError != "" {
				if err == nil || !contains(err.Error(), tt.wantError) {
					t.Fatalf("validate recorded merge error = %v, want %q", err, tt.wantError)
				}
			} else if err != nil {
				t.Fatalf("validate recorded merge: %v", err)
			}
			if recovered != tt.wantRecovered {
				t.Fatalf("recovered = %t, want %t", recovered, tt.wantRecovered)
			}
			runner.assertComplete()
		})
	}
}

func TestMergeTaskBranchIntoDestinationRejectsUnsafeOrMatchingBranches(t *testing.T) {
	const repoPath = "/recorded/repo"
	repo := task.Repository{ID: "alpha", Path: repoPath, DefaultBranch: "main"}

	for _, destination := range []string{"@", "HEAD"} {
		t.Run("unsafe "+destination, func(t *testing.T) {
			_, err := MergeTaskBranchIntoDestination(context.Background(), repo, destination, "orpheus/op-1")
			if err == nil || !contains(err.Error(), "safe Git branch name") {
				t.Fatalf("merge error = %v, want unsafe destination rejection", err)
			}
		})
	}

	runner := &recordingGitRunner{t: t, commands: []recordedGitCommand{
		gitCommand(repoPath, "check-ref-format", "refs/heads/orpheus/op-1"),
		gitCommand(repoPath, "ls-remote", "--exit-code", "--heads", "origin", "refs/heads/orpheus/op-1").withStdout("task-head\trefs/heads/orpheus/op-1\n"),
		gitCommand(repoPath, "check-ref-format", "refs/heads/orpheus/op-1"),
		gitCommand(repoPath, "check-ref-format", "refs/heads/orpheus/op-1"),
	}}
	ctx := ContextWithRunner(context.Background(), runner)
	if err := VerifyRemoteBranch(ctx, repoPath, "orpheus/op-1"); err != nil {
		t.Fatalf("verify remote task branch: %v", err)
	}
	if _, err := MergeTaskBranchIntoDestination(ctx, repo, "orpheus/op-1", "orpheus/op-1"); err == nil || !contains(err.Error(), "is the task branch") {
		t.Fatalf("merge error = %v, want matching destination rejection", err)
	}
	runner.assertComplete()
}

func TestMergeTaskBranchIntoDestinationRejectsLocallyAheadDestination(t *testing.T) {
	tests := []struct {
		name    string
		parents string
	}{
		{name: "task branch was fast-forwarded", parents: "remote\n"},
		{name: "unrelated commit follows merge", parents: "merge unrelated\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const repoPath = "/recorded/repo"
			runner := &recordingGitRunner{t: t, commands: []recordedGitCommand{
				gitCommand(repoPath, "check-ref-format", "refs/heads/main"),
				gitCommand(repoPath, "check-ref-format", "refs/heads/orpheus/op-1"),
				gitCommand(repoPath, "status", "--porcelain=v1"),
				gitCommand(repoPath, "fetch", "origin", "+refs/heads/main:refs/remotes/origin/main"),
				gitCommand(repoPath, "show-ref", "--verify", "--quiet", "refs/heads/main"),
				gitCommand(repoPath, "checkout", "--no-guess", "main"),
				gitCommand(repoPath, "rev-parse", "HEAD").withStdout("local\n"),
				gitCommand(repoPath, "rev-parse", "refs/remotes/origin/main").withStdout("remote\n"),
				gitCommand(repoPath, "rev-parse", "refs/heads/orpheus/op-1").withStdout("task\n"),
				gitCommand(repoPath, "show", "--no-patch", "--format=%P", "HEAD").withStdout(tt.parents),
				gitCommand(repoPath, "merge-base", "--is-ancestor", "HEAD", "refs/remotes/origin/main").withError(CommandExitError{Code: 1}),
			}}

			_, err := MergeTaskBranchIntoDestination(
				ContextWithRunner(context.Background(), runner),
				task.Repository{ID: "alpha", Path: repoPath, DefaultBranch: "main"},
				"main",
				"orpheus/op-1",
			)
			if err == nil || !contains(err.Error(), "local branch is ahead of or divergent") {
				t.Fatalf("merge error = %v, want locally-ahead rejection", err)
			}
			runner.assertComplete()
		})
	}
}

func TestSyncFetchedTaskBranchWithDefaultReportsCurrentOrPush(t *testing.T) {
	tests := []struct {
		name       string
		remoteHead string
		wantStatus TaskBranchSyncStatus
	}{
		{name: "already current", remoteHead: "head", wantStatus: TaskBranchSyncAlreadyCurrent},
		{name: "remote is behind", remoteHead: "remote", wantStatus: TaskBranchSyncPushed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const worktree = "/recorded/worktree"
			commands := []recordedGitCommand{
				gitCommand(worktree, "rev-parse", "HEAD").withStdout("head\n"),
				gitCommand(worktree, "merge-base", "--is-ancestor", "refs/remotes/origin/main", "HEAD"),
				gitCommand(worktree, "rev-parse", "--verify", "refs/remotes/origin/orpheus/op-1").withStdout(tt.remoteHead + "\n"),
			}
			if tt.remoteHead != "head" {
				commands = append(commands,
					gitCommand(worktree, "check-ref-format", "refs/heads/orpheus/op-1"),
					gitCommand(worktree, "push", "-u", "origin", "refs/heads/orpheus/op-1:refs/heads/orpheus/op-1"),
				)
			}
			runner := &recordingGitRunner{t: t, commands: commands}
			plan := taskBranchSyncPlan{DefaultBranch: "main", Branch: "orpheus/op-1", Worktree: worktree}

			result, err := syncFetchedTaskBranchWithDefault(ContextWithRunner(context.Background(), runner), plan, newTaskBranchSyncResult(plan))
			if err != nil {
				t.Fatalf("sync fetched task branch: %v", err)
			}
			if result.Status != tt.wantStatus || result.PreviousHead != "head" || result.Head != "head" {
				t.Fatalf("sync result = %#v, want %s at head", result, tt.wantStatus)
			}
			runner.assertComplete()
		})
	}
}

func TestFastForwardTaskBranchFromOrigin(t *testing.T) {
	const (
		worktree = "/recorded/worktree"
		branch   = "orpheus/op-1"
	)
	runner := &recordingGitRunner{t: t, commands: []recordedGitCommand{
		gitCommand(worktree, "merge-base", "--is-ancestor", "refs/remotes/origin/"+branch, "HEAD").withError(CommandExitError{Code: 1}),
		gitCommand(worktree, "merge-base", "--is-ancestor", "HEAD", "refs/remotes/origin/"+branch),
		gitCommand(worktree, "merge", "--ff-only", "refs/remotes/origin/"+branch),
	}}

	if err := fastForwardTaskBranchFromOrigin(ContextWithRunner(context.Background(), runner), worktree, branch); err != nil {
		t.Fatalf("fast-forward task branch: %v", err)
	}
	runner.assertComplete()
}

func TestCompleteTaskBranchConflictResolutionRejectsUnexpectedChanges(t *testing.T) {
	tests := []struct {
		name     string
		status   string
		wantPath string
	}{
		{name: "staged file", status: "A  unexpected.txt\x00", wantPath: "unexpected.txt"},
		{name: "untracked file", status: "?? unexpected.txt\x00", wantPath: "unexpected.txt"},
		{name: "unstaged file", status: " M unexpected.txt\x00", wantPath: "unexpected.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoPath, worktreePath := newRecordingSyncPaths(t)
			conflictPath := filepath.Join(worktreePath, "conflict.txt")
			if err := os.WriteFile(conflictPath, []byte("resolved\n"), 0o644); err != nil {
				t.Fatalf("write resolved conflict: %v", err)
			}
			runner := &recordingGitRunner{t: t, commands: conflictResolutionCommands(repoPath, worktreePath, "orpheus/op-unexpected", "conflict.txt\x00", "", tt.status)}

			_, err := CompleteTaskBranchConflictResolution(
				ContextWithRunner(context.Background(), runner),
				TaskBranchSyncOptions{RepoPath: repoPath, DefaultBranch: "main", Branch: "orpheus/op-unexpected", Worktree: worktreePath},
				[]string{"conflict.txt"},
			)
			if err == nil || !contains(err.Error(), "unexpected changes outside merge conflict files") || !contains(err.Error(), tt.wantPath) {
				t.Fatalf("complete conflict resolution error = %v, want unexpected change for %s", err, tt.wantPath)
			}
			runner.assertComplete()
		})
	}
}

func TestCompleteTaskBranchConflictResolutionKeepsUnresolvedFiles(t *testing.T) {
	repoPath, worktreePath := newRecordingSyncPaths(t)
	runner := &recordingGitRunner{t: t, commands: conflictResolutionCommands(repoPath, worktreePath, "orpheus/op-unresolved", "deleted.txt\x00", "deleted.txt\n", "")}

	result, err := CompleteTaskBranchConflictResolution(
		ContextWithRunner(context.Background(), runner),
		TaskBranchSyncOptions{RepoPath: repoPath, DefaultBranch: "main", Branch: "orpheus/op-unresolved", Worktree: worktreePath},
		[]string{"deleted.txt"},
	)
	if err == nil || !contains(err.Error(), "unresolved merge conflicts remain") || !contains(err.Error(), "deleted.txt") {
		t.Fatalf("complete conflict resolution error = %v, want unresolved deleted.txt conflict", err)
	}
	if result.Status != TaskBranchSyncConflicted || !reflect.DeepEqual(result.ConflictFiles, []string{"deleted.txt"}) {
		t.Fatalf("complete conflict resolution result = %#v, want conflicted deleted.txt", result)
	}
	runner.assertComplete()
}

func TestCompleteTaskBranchConflictResolutionRejectsConflictMarkers(t *testing.T) {
	repoPath, worktreePath := newRecordingSyncPaths(t)
	markerContent := "<<<<<<< HEAD\nstill task\n=======\nstill default\n>>>>>>> origin/main\n"
	if err := os.WriteFile(filepath.Join(worktreePath, "conflict.txt"), []byte(markerContent), 0o644); err != nil {
		t.Fatalf("write marker conflict file: %v", err)
	}
	runner := &recordingGitRunner{t: t, commands: conflictResolutionCommands(repoPath, worktreePath, "orpheus/op-markers", "conflict.txt\x00", "", "")}

	_, err := CompleteTaskBranchConflictResolution(
		ContextWithRunner(context.Background(), runner),
		TaskBranchSyncOptions{RepoPath: repoPath, DefaultBranch: "main", Branch: "orpheus/op-markers", Worktree: worktreePath},
		[]string{"conflict.txt"},
	)
	if err == nil || !contains(err.Error(), "still contains conflict markers") {
		t.Fatalf("complete conflict resolution error = %v, want conflict marker failure", err)
	}
	runner.assertComplete()
}

func newRecordingSyncPaths(t *testing.T) (string, string) {
	t.Helper()

	root := testutil.CanonicalTempDir(t)
	repoPath := filepath.Join(root, "repo")
	worktreePath := filepath.Join(root, "worktree")
	for _, path := range []string{repoPath, worktreePath, filepath.Join(repoPath, ".git")} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("create recording path %q: %v", path, err)
		}
	}
	return repoPath, worktreePath
}

func conflictResolutionCommands(repoPath, worktreePath, branch, mergeFiles, unresolved, status string) []recordedGitCommand {
	commands := []recordedGitCommand{
		gitCommand(repoPath, "rev-parse", "--show-toplevel").withStdout(repoPath + "\n"),
		gitCommand(repoPath, "check-ref-format", "refs/heads/"+branch),
		gitCommand(repoPath, "check-ref-format", "refs/heads/main"),
		gitCommand(repoPath, "remote", "get-url", "origin").withStdout("origin\n"),
		gitCommand(repoPath, "rev-parse", "--git-common-dir").withStdout(filepath.Join(repoPath, ".git") + "\n"),
		gitCommand(worktreePath, "rev-parse", "--show-toplevel").withStdout(worktreePath + "\n"),
		gitCommand(worktreePath, "rev-parse", "--git-common-dir").withStdout(filepath.Join(repoPath, ".git") + "\n"),
		gitCommand(worktreePath, "symbolic-ref", "--quiet", "--short", "HEAD").withStdout(branch + "\n"),
		gitCommand(worktreePath, "rev-parse", "--verify", "--quiet", "MERGE_HEAD").withStdout("merge-head\n"),
		gitCommand(worktreePath, "diff", "--name-only", "-z", "HEAD", "MERGE_HEAD").withStdout(mergeFiles),
		gitCommand(worktreePath, "diff", "--name-only", "--diff-filter=U").withStdout(unresolved),
	}
	if unresolved != "" {
		return commands
	}
	commands = append(commands,
		gitCommand(worktreePath, "add", "--", "conflict.txt"),
		gitCommand(worktreePath, "status", "--porcelain=v1", "-z", "--untracked-files=normal").withStdout(status),
	)
	return commands
}

func gitCommand(directory string, args ...string) recordedGitCommand {
	return recordedGitCommand{directory: directory, args: args}
}

func (command recordedGitCommand) withStdout(stdout string) recordedGitCommand {
	command.result.Stdout = stdout
	return command
}

func (command recordedGitCommand) withError(err error) recordedGitCommand {
	command.err = err
	return command
}

func contains(value string, want string) bool {
	return strings.Contains(value, want)
}
