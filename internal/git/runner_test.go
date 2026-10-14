//nolint:testpackage // Command runner tests verify unexported Git orchestration decisions.
package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/state"
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

func TestInjectedCommandRunnerPreservesExitStatusAndErrorCause(t *testing.T) {
	for _, withCause := range []bool{false, true} {
		t.Run(fmt.Sprintf("cause=%t", withCause), func(t *testing.T) {
			var cause error
			wantMessage := "git command exited with status 17"
			if withCause {
				cause = errors.New("runner failure")
				wantMessage = cause.Error()
			}
			commandErr := CommandExitError{Code: 17, Err: cause}
			if commandErr.Error() != wantMessage {
				t.Fatalf("exit message = %q, want %q", commandErr.Error(), wantMessage)
			}
			runner := &recordingGitRunner{t: t, commands: []recordedGitCommand{gitCommand("/fixture/repo", "rev-parse", "HEAD").withError(commandErr)}}

			_, err := HeadCommit(ContextWithRunner(context.Background(), runner), "/fixture/repo")

			var exitError CommandExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 17 || withCause && !errors.Is(err, cause) {
				t.Fatalf("HEAD error = %v, want original exit status and cause", err)
			}
			runner.assertComplete()
		})
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
