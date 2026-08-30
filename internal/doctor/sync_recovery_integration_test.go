//go:build integration

package doctor_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/doctor"
	"github.com/hea3ven/orpheus/internal/registry"
	"github.com/hea3ven/orpheus/internal/state"
	"github.com/hea3ven/orpheus/internal/taskstate"
	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestIntegrationDoctorRecoversSyncConflictState(t *testing.T) {
	t.Run("completes pushed state", testDoctorCompletesPushedSyncConflict)
	t.Run("rolls back local conflict state", testDoctorRollsBackSyncConflict)
}

func testDoctorCompletesPushedSyncConflict(t *testing.T) {
	fixture := newDoctorSyncGitFixture(t)
	const branch = "orpheus/op-pushed"
	runDoctorGit(t, fixture.repoPath, "checkout", "-b", branch)
	writeDoctorSyncFile(t, filepath.Join(fixture.repoPath, "completed.txt"), "completed\n")
	runDoctorGit(t, fixture.repoPath, "add", "completed.txt")
	runDoctorGit(t, fixture.repoPath, "commit", "-m", "complete conflict resolution")
	completedHead := runDoctorGit(t, fixture.repoPath, "rev-parse", "HEAD")
	runDoctorGit(t, fixture.repoPath, "push", "-u", "origin", branch)

	paths, store := newDoctorSyncStore(t)
	startFinishedDoctorRun(t, store, "op-pushed", branch, fixture.repoPath)
	operation, err := store.BeginSyncConflictOperation("alpha", "op-pushed", taskstate.SyncConflictOperation{
		ID:            "sync-pushed",
		Branch:        branch,
		Worktree:      fixture.repoPath,
		DefaultBranch: "main",
		Checkpoint: taskstate.SyncConflictCheckpoint{
			LocalHead: fixture.initialHead, RemoteHead: fixture.initialHead, MergeSource: fixture.initialHead,
		},
		Phase:     taskstate.SyncConflictPhasePushIntent,
		LocalHead: completedHead,
	})
	if err != nil {
		t.Fatalf("begin sync conflict operation: %v", err)
	}
	opts := doctor.Options{
		Paths: paths,
		Registry: registry.Registry{Repos: []registry.Repo{{
			ID: "alpha", Path: fixture.repoPath, DefaultBranch: "main",
		}}},
	}

	result, err := doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor dry run: %v", err)
	}
	assertDoctorSyncOutcome(t, result, "pushed")
	loaded, err := store.Load("alpha", "op-pushed")
	if err != nil {
		t.Fatalf("load dry-run state: %v", err)
	}
	if loaded.ActiveSyncConflict == nil || loaded.ActiveSyncConflict.Phase != operation.Phase || loaded.ActiveSyncConflict.ObservedRemoteHead != "" {
		t.Fatalf("dry-run operation = %#v, want unchanged push intent", loaded.ActiveSyncConflict)
	}

	opts.Fix = true
	result, err = doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor fix: %v", err)
	}
	assertDoctorSyncOutcome(t, result, "pushed")
	loaded, err = store.Load("alpha", "op-pushed")
	if err != nil {
		t.Fatalf("load fixed state: %v", err)
	}
	if loaded.ActiveSyncConflict != nil {
		t.Fatalf("active operation = %#v, want nil", loaded.ActiveSyncConflict)
	}
	assertDoctorSyncEvent(t, loaded.Events, taskstate.EventSyncConflictFinished, completedHead)
}

func testDoctorRollsBackSyncConflict(t *testing.T) {
	fixture := newDoctorSyncGitFixture(t)
	const branch = "orpheus/op-rollback"
	runDoctorGit(t, fixture.repoPath, "checkout", "-b", branch)
	writeDoctorSyncFile(t, filepath.Join(fixture.repoPath, "conflict.txt"), "task\n")
	runDoctorGit(t, fixture.repoPath, "commit", "-am", "task change")
	taskHead := runDoctorGit(t, fixture.repoPath, "rev-parse", "HEAD")
	runDoctorGit(t, fixture.repoPath, "push", "-u", "origin", branch)

	runDoctorGit(t, fixture.repoPath, "checkout", "main")
	writeDoctorSyncFile(t, filepath.Join(fixture.repoPath, "conflict.txt"), "main\n")
	runDoctorGit(t, fixture.repoPath, "commit", "-am", "main change")
	mainHead := runDoctorGit(t, fixture.repoPath, "rev-parse", "HEAD")
	runDoctorGit(t, fixture.repoPath, "push", "origin", "main")
	runDoctorGit(t, fixture.repoPath, "checkout", branch)
	command := exec.Command("git", "-C", fixture.repoPath, "merge", "main")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("merge succeeded, want conflict: %s", output)
	}

	paths, store := newDoctorSyncStore(t)
	startFinishedDoctorRun(t, store, "op-rollback", branch, fixture.repoPath)
	_, err := store.BeginSyncConflictOperation("alpha", "op-rollback", taskstate.SyncConflictOperation{
		ID:            "sync-rollback",
		Branch:        branch,
		Worktree:      fixture.repoPath,
		DefaultBranch: "main",
		Checkpoint: taskstate.SyncConflictCheckpoint{
			LocalHead: taskHead, RemoteHead: taskHead, MergeSource: mainHead,
		},
		Phase:         taskstate.SyncConflictPhaseConflicted,
		ConflictFiles: []string{"conflict.txt"},
	})
	if err != nil {
		t.Fatalf("begin sync conflict operation: %v", err)
	}
	opts := doctor.Options{
		Paths: paths,
		Registry: registry.Registry{Repos: []registry.Repo{{
			ID: "alpha", Path: fixture.repoPath, DefaultBranch: "main",
		}}},
	}

	result, err := doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor dry run: %v", err)
	}
	assertDoctorSyncOutcome(t, result, "rollbackable")
	if status := runDoctorGit(t, fixture.repoPath, "status", "--porcelain=v1"); !strings.Contains(status, "UU conflict.txt") {
		t.Fatalf("dry-run status = %q, want unresolved conflict", status)
	}
	loaded, err := store.Load("alpha", "op-rollback")
	if err != nil {
		t.Fatalf("load dry-run state: %v", err)
	}
	if loaded.ActiveSyncConflict == nil {
		t.Fatal("dry run cleared active sync conflict")
	}

	opts.Fix = true
	result, err = doctor.Run(opts)
	if err != nil {
		t.Fatalf("doctor fix: %v", err)
	}
	assertDoctorSyncOutcome(t, result, "rolled_back")
	if head := runDoctorGit(t, fixture.repoPath, "rev-parse", "HEAD"); head != taskHead {
		t.Fatalf("HEAD = %s, want checkpoint %s", head, taskHead)
	}
	if status := runDoctorGit(t, fixture.repoPath, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("status = %q, want clean rollback", status)
	}
	loaded, err = store.Load("alpha", "op-rollback")
	if err != nil {
		t.Fatalf("load fixed state: %v", err)
	}
	if loaded.ActiveSyncConflict != nil {
		t.Fatalf("active operation = %#v, want nil", loaded.ActiveSyncConflict)
	}
	assertDoctorSyncEvent(t, loaded.Events, taskstate.EventSyncConflictRolledBack, "")
}

type doctorSyncGitFixture struct {
	repoPath    string
	initialHead string
}

func newDoctorSyncGitFixture(t *testing.T) doctorSyncGitFixture {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	repoPath := filepath.Join(root, "repo")
	remotePath := filepath.Join(root, "remote.git")
	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		t.Fatalf("create repository: %v", err)
	}
	runDoctorGit(t, repoPath, "init", "-b", "main")
	runDoctorGit(t, repoPath, "config", "user.name", "Orpheus Test")
	runDoctorGit(t, repoPath, "config", "user.email", "orpheus@example.test")
	writeDoctorSyncFile(t, filepath.Join(repoPath, "conflict.txt"), "base\n")
	runDoctorGit(t, repoPath, "add", "conflict.txt")
	runDoctorGit(t, repoPath, "commit", "-m", "initial")
	initialHead := runDoctorGit(t, repoPath, "rev-parse", "HEAD")
	command := exec.Command("git", "init", "--bare", remotePath)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("init bare remote: %v: %s", err, output)
	}
	runDoctorGit(t, repoPath, "remote", "add", "origin", remotePath)
	runDoctorGit(t, repoPath, "push", "-u", "origin", "main")
	return doctorSyncGitFixture{repoPath: repoPath, initialHead: initialHead}
}

func newDoctorSyncStore(t *testing.T) (state.Paths, taskstate.Store) {
	t.Helper()
	paths, err := state.NewPaths(
		filepath.Join(testutil.CanonicalTempDir(t), "config"),
		filepath.Join(testutil.CanonicalTempDir(t), "data"),
	)
	if err != nil {
		t.Fatalf("new paths: %v", err)
	}
	return paths, taskstate.NewStore(paths)
}

func startFinishedDoctorRun(t *testing.T, store taskstate.Store, taskID, branch, worktree string) {
	t.Helper()
	run, err := store.StartRun("alpha", taskID, taskstate.StartRunOptions{
		Agent: "implementer", WorkDirectory: worktree, Branch: branch, Worktree: worktree,
	})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := store.FinishRun("alpha", taskID, run.Attempt, taskstate.RunStatusSucceeded); err != nil {
		t.Fatalf("finish run: %v", err)
	}
}

func assertDoctorSyncOutcome(t *testing.T, result doctor.Result, want string) {
	t.Helper()
	if len(result.SyncConflictRows) != 1 || result.SyncConflictRows[0].Outcome != want {
		t.Fatalf("sync conflict rows = %#v, want %s", result.SyncConflictRows, want)
	}
}

func assertDoctorSyncEvent(t *testing.T, events []taskstate.Event, eventType taskstate.EventType, commit string) {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType && event.Commit == commit {
			return
		}
	}
	t.Fatalf("events = %#v, want %s with commit %q", events, eventType, commit)
}

func writeDoctorSyncFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func runDoctorGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	commandArgs := append([]string{"-C", directory}, args...)
	command := exec.Command("git", commandArgs...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(commandArgs, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
