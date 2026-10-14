//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hea3ven/orpheus/internal/testutil"
)

func snapshotGit(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
	cmd.Env = append(gitRoutingEnvironment(os.Environ()), "GIT_OPTIONAL_LOCKS=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func snapshotWrite(t *testing.T, repo, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(repo, path)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, path), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func snapshotRunner(t *testing.T, repo string) *comparisonRunner {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	output := filepath.Join(root, "report.json")
	runner, cleanup, err := newComparisonRunner(output)
	if err != nil {
		t.Fatal(err)
	}
	runner.root = repo
	t.Cleanup(cleanup)
	return runner
}

// Capture the full source tree, Git metadata, and index bytes. Read-only status
// probes must not refresh the operator index before the comparison is checked.
func snapshotOperatorState(t *testing.T, repo string) map[string]string {
	t.Helper()
	state := make(map[string]string)
	if err := filepath.WalkDir(repo, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if entry.Type()&os.ModeSymlink != 0 {
			target, linkErr := os.Readlink(path)
			contents, err = []byte(target), linkErr
		}
		if err != nil {
			return err
		}
		state[path] = info.Mode().String() + ":" + string(contents)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	index := snapshotGit(t, repo, "rev-parse", "--path-format=absolute", "--git-path", "index")
	contents, err := os.ReadFile(index)
	if err != nil {
		t.Fatal(err)
	}
	state["index-bytes"] = string(contents)
	state["refs"] = snapshotGit(t, repo, "show-ref")
	state["worktrees"] = snapshotGit(t, repo, "worktree", "list", "--porcelain")
	state["status"] = snapshotGit(t, repo, "status", "--porcelain", "--untracked-files=all")
	return state
}

func TestIntegrationAdapterContractWorkingTreeSnapshotCapturesDiskPrecedenceWithoutChangingOperatorState(t *testing.T) {
	repo, _, head := syntheticMergeFixture(t)
	snapshotWrite(t, repo, "delete.txt", "delete\n")
	snapshotWrite(t, repo, "index-deleted.txt", "head version\n")
	snapshotWrite(t, repo, "ignored-tracked.txt", "tracked\n")
	snapshotWrite(t, repo, ".gitattributes", "*.txt text eol=crlf\n")
	snapshotGit(t, repo, "add", ".")
	snapshotGit(t, repo, "commit", "-m", "tracked snapshot fixtures")
	head = snapshotGit(t, repo, "rev-parse", "HEAD")
	snapshotWrite(t, repo, ".gitignore", "ignored-*\nindex-deleted.txt\n")
	snapshotWrite(t, repo, "base.txt", "staged version\n")
	snapshotGit(t, repo, "add", "base.txt")
	snapshotWrite(t, repo, "base.txt", "disk version\r\n")
	snapshotGit(t, repo, "rm", "--cached", "index-deleted.txt")
	snapshotWrite(t, repo, "index-deleted.txt", "disk after staged deletion\n")
	if err := os.Remove(filepath.Join(repo, "delete.txt")); err != nil {
		t.Fatal(err)
	}
	snapshotWrite(t, repo, "staged-new.txt", "staged addition\n")
	snapshotGit(t, repo, "add", "staged-new.txt")
	snapshotWrite(t, repo, "staged-new.txt", "disk addition\n")
	snapshotWrite(t, repo, "staged-missing.txt", "only in index\n")
	snapshotGit(t, repo, "add", "staged-missing.txt")
	if err := os.Remove(filepath.Join(repo, "staged-missing.txt")); err != nil {
		t.Fatal(err)
	}
	snapshotWrite(t, repo, "untracked\twith\nname.txt", "untracked bytes\n")
	snapshotWrite(t, repo, "ignored-untracked.txt", "excluded\n")
	snapshotWrite(t, repo, ".git/info/exclude", "local-excluded.txt\n")
	snapshotWrite(t, repo, "local-excluded.txt", "excluded locally\n")
	globalExclude := filepath.Join(testutil.CanonicalTempDir(t), "ignore")
	snapshotWrite(t, filepath.Dir(globalExclude), filepath.Base(globalExclude), "global-excluded.txt\n")
	snapshotGit(t, repo, "config", "core.excludesFile", globalExclude)
	snapshotWrite(t, repo, "global-excluded.txt", "excluded globally\n")
	if err := os.Chmod(filepath.Join(repo, "candidate.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-target", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	before := snapshotOperatorState(t, repo)
	runner := snapshotRunner(t, repo)

	snapshot, base, err := runner.prepareWorkingTree(context.Background(), "HEAD~1")

	if err != nil || base == head || snapshot.HeadRevision != head || !commitPattern.MatchString(snapshot.Revision) {
		t.Fatalf("snapshot = %#v, base %s, %v", snapshot, base, err)
	}
	candidate := filepath.Join(runner.work, "merge")
	for path, want := range map[string]string{
		"base.txt": "disk version\r\n", "staged-new.txt": "disk addition\n",
		"index-deleted.txt": "disk after staged deletion\n", "ignored-tracked.txt": "tracked\n",
		"untracked\twith\nname.txt": "untracked bytes\n",
	} {
		contents, err := os.ReadFile(filepath.Join(candidate, path))
		if err != nil || string(contents) != want {
			t.Fatalf("candidate %q = %q, %v; want %q", path, contents, err, want)
		}
	}
	for _, path := range []string{"delete.txt", "staged-missing.txt", "ignored-untracked.txt", "local-excluded.txt", "global-excluded.txt"} {
		if _, err := os.Lstat(filepath.Join(candidate, path)); !os.IsNotExist(err) {
			t.Fatalf("excluded/deleted %s was captured: %v", path, err)
		}
	}
	if target, err := os.Readlink(filepath.Join(candidate, "link")); err != nil || target != "missing-target" {
		t.Fatalf("snapshot symlink = %q, %v", target, err)
	}
	info, err := os.Stat(filepath.Join(candidate, "candidate.txt"))
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("snapshot executable mode lost: %v, %v", info, err)
	}
	t.Chdir(candidate)
	if _, err := verifyCheckout(snapshot.Revision); err != nil {
		t.Fatal(err)
	}
	if after := snapshotOperatorState(t, repo); !reflect.DeepEqual(before, after) {
		t.Fatal("snapshot changed source contents, refs, worktree registrations, or index")
	}
	second := snapshotRunner(t, repo)
	repeated, _, err := second.prepareWorkingTree(context.Background(), "HEAD")
	if err != nil || repeated.Revision != snapshot.Revision || repeated.Tree != snapshot.Tree {
		t.Fatalf("snapshot identity is not reproducible or depends on base: %#v, %v", repeated, err)
	}
	restored := filepath.Join(testutil.CanonicalTempDir(t), "restored")
	snapshotGit(t, filepath.Dir(restored), "clone", "--branch", "candidate", snapshot.Bundle, restored)
	if got := snapshotGit(t, restored, "rev-parse", "HEAD"); got != snapshot.Revision {
		t.Fatalf("retained bundle restores %s, want %s", got, snapshot.Revision)
	}
}

func TestIntegrationAdapterContractPrivateSnapshotSetupRejectsInheritedHooksAndPreservesSourceIgnoresOnFailure(t *testing.T) {
	for _, configuration := range []string{"counted", "parameters"} {
		t.Run(configuration, func(t *testing.T) {
			t.Setenv("GIT_CONFIG_COUNT", "0")
			t.Setenv("GIT_CONFIG_PARAMETERS", "")
			repo, _, _ := syntheticMergeFixture(t)
			root := testutil.CanonicalTempDir(t)
			hooks := filepath.Join(root, "hooks")
			snapshotWrite(t, hooks, "post-checkout", "#!/bin/sh\nprintf 'operator hook ran\\n' >\"$TIMING_HOOK_MARKER\"\n")
			if err := os.Chmod(filepath.Join(hooks, "post-checkout"), 0o755); err != nil {
				t.Fatal(err)
			}
			marker := filepath.Join(repo, "hook-ran")
			t.Setenv("TIMING_HOOK_MARKER", marker)
			ignore := filepath.Join(root, "ignore")
			snapshotWrite(t, root, "ignore", "ignored-by-environment.txt\n")
			snapshotWrite(t, repo, "ignored-by-environment.txt", "excluded by source environment\n")
			snapshotWrite(t, repo, "base.txt", "staged version\n")
			snapshotGit(t, repo, "add", "base.txt")
			snapshotWrite(t, repo, "base.txt", "disk version\n")
			snapshotWrite(t, repo, "untracked.txt", "existing untracked bytes\n")
			if configuration == "counted" {
				t.Setenv("GIT_CONFIG_COUNT", "2")
				t.Setenv("GIT_CONFIG_KEY_0", "core.hooksPath")
				t.Setenv("GIT_CONFIG_VALUE_0", hooks)
				t.Setenv("GIT_CONFIG_KEY_1", "core.excludesFile")
				t.Setenv("GIT_CONFIG_VALUE_1", ignore)
			} else {
				t.Setenv("GIT_CONFIG_PARAMETERS", "'core.hooksPath="+hooks+"' 'core.excludesFile="+ignore+"'")
			}
			if configured, err := sourceGit(context.Background(), repo, "config", "--get", "core.hooksPath"); err != nil || strings.TrimSpace(configured) != hooks {
				t.Fatalf("source hook configuration = %q, %v; want %q", configured, err, hooks)
			}
			bin := filepath.Join(root, "bin")
			snapshotWrite(t, bin, "go", "#!/bin/sh\nexit 3\n")
			if err := os.Chmod(filepath.Join(bin, "go"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			before := snapshotOperatorState(t, repo)
			t.Cleanup(func() {
				if !reflect.DeepEqual(before, snapshotOperatorState(t, repo)) {
					t.Error("failed setup or cleanup changed source contents, refs, worktree registrations, or index")
				}
			})
			runner := snapshotRunner(t, repo)
			evidence := &comparisonEvidence{}

			err := runner.prepareComparison(context.Background(), options{compareWorktree: true, compareBase: "HEAD"}, evidence)

			if err == nil || !strings.Contains(err.Error(), "build snapshot collector") {
				t.Fatalf("setup error = %v, want controlled collector build failure", err)
			}
			if evidence.Snapshot == nil || !commitPattern.MatchString(evidence.Snapshot.Revision) {
				t.Fatalf("snapshot setup did not complete before collector failure: %#v", evidence)
			}
			if _, err := os.Stat(filepath.Join(runner.work, "merge", "ignored-by-environment.txt")); !os.IsNotExist(err) {
				t.Fatalf("source ignore configuration was lost: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("inherited operator hook executed during private setup: %v", err)
			}
			if !reflect.DeepEqual(before, snapshotOperatorState(t, repo)) {
				t.Fatal("failed private setup changed operator state")
			}
		})
	}
}

func TestIntegrationAdapterContractWorkingTreeSnapshotCapturesDirectoryReplacedByFileWithoutChangingOperatorState(t *testing.T) {
	repo, _, _ := syntheticMergeFixture(t)
	snapshotWrite(t, repo, "old/child.txt", "tracked child\n")
	snapshotWrite(t, repo, "old/nested/child.txt", "tracked nested child\n")
	snapshotGit(t, repo, "add", "old")
	snapshotGit(t, repo, "commit", "-m", "tracked directory")
	snapshotWrite(t, repo, "old/child.txt", "staged child\n")
	snapshotGit(t, repo, "add", "old/child.txt")
	if err := os.RemoveAll(filepath.Join(repo, "old")); err != nil {
		t.Fatal(err)
	}
	snapshotWrite(t, repo, "old", "replacement file\n")
	before := snapshotOperatorState(t, repo)
	runner := snapshotRunner(t, repo)

	snapshot, _, err := runner.prepareWorkingTree(context.Background(), "HEAD")

	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(runner.work, "merge", "old"))
	if err != nil || string(contents) != "replacement file\n" {
		t.Fatalf("replacement file = %q, %v", contents, err)
	}
	if paths := snapshotGit(t, runner.root, "ls-tree", "-r", "--name-only", snapshot.Revision, "--", "old"); paths != "old" {
		t.Fatalf("snapshot paths = %q, want only replacement file old", paths)
	}
	if after := snapshotOperatorState(t, repo); !reflect.DeepEqual(before, after) {
		t.Fatal("directory replacement snapshot changed source contents, refs, worktree registrations, or index")
	}
}

func TestIntegrationAdapterContractWorkingTreeSnapshotSupportsLinkedWorktreesAndRejectsUnsafeInputsWithoutMutation(t *testing.T) {
	repo, base, _ := syntheticMergeFixture(t)
	linked := filepath.Join(testutil.CanonicalTempDir(t), "linked")
	snapshotGit(t, repo, "worktree", "add", "--detach", linked, base)
	snapshotWrite(t, linked, "base.txt", "dirty linked checkout\n")
	before := snapshotOperatorState(t, linked)
	runner := snapshotRunner(t, linked)

	snapshot, resolved, err := runner.prepareWorkingTree(context.Background(), base[:12])

	if err != nil || resolved != base || snapshot.HeadRevision != base {
		t.Fatalf("linked snapshot = %#v, %s, %v", snapshot, resolved, err)
	}
	if !reflect.DeepEqual(before, snapshotOperatorState(t, linked)) {
		t.Fatal("linked snapshot changed operator state")
	}
	for _, ref := range []string{"missing-base", "--all"} {
		failed := snapshotRunner(t, linked)
		if _, _, err := failed.prepareWorkingTree(context.Background(), ref); err == nil {
			t.Fatalf("invalid explicit base %q accepted", ref)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := snapshotRunner(t, linked).prepareWorkingTree(ctx, base); err == nil {
		t.Fatal("canceled snapshot accepted")
	}
	if !reflect.DeepEqual(before, snapshotOperatorState(t, linked)) {
		t.Fatal("failed or canceled snapshot changed operator state")
	}
	t.Chdir(linked)
	unsafe := filepath.Join(repo, ".git", "new-report", "report.json")
	if _, err := comparisonOutput(options{compareWorktree: true, output: unsafe}); err == nil {
		t.Fatal("output in the linked checkout's shared Git metadata accepted")
	}
	if _, err := os.Stat(filepath.Dir(unsafe)); !os.IsNotExist(err) {
		t.Fatalf("output rejection modified shared Git metadata: %v", err)
	}
}

func TestIntegrationAdapterContractLocalTimingUsesSnapshotCollectorAndCandidatePolicyWithSharedDecisions(t *testing.T) {
	for _, test := range []struct {
		name, wantExecution, wantTiming string
		slow, failed, canceled          bool
		pairs                           int
	}{
		{name: "unchanged committed snapshot", wantExecution: statusPass, wantTiming: statusPass, pairs: 1},
		{name: "dirty controlled slowdown", slow: true, wantExecution: statusPass, wantTiming: statusTimingFailed, pairs: 5},
		{name: "invalid evidence", failed: true, wantExecution: statusExecutionFailed, wantTiming: statusUnevaluated},
		{name: "canceled collector", canceled: true, wantExecution: statusExecutionFailed, wantTiming: statusUnevaluated},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, base, _ := syntheticMergeFixture(t)
			policy := testLocalQualityPolicy()
			if err := writeQualityPolicy(filepath.Join(repo, ".quality.yml"), policy); err != nil {
				t.Fatal(err)
			}
			snapshotGit(t, repo, "add", ".quality.yml")
			snapshotGit(t, repo, "commit", "-m", "candidate policy")
			if !test.slow {
				base = snapshotGit(t, repo, "rev-parse", "HEAD")
			}
			if test.slow {
				snapshotWrite(t, repo, "new_test.go", "dirty candidate fixture\n")
			}
			root := testutil.CanonicalTempDir(t)
			runner := snapshotRunner(t, repo)
			snapshot, resolved, err := runner.prepareWorkingTree(context.Background(), base)
			if err != nil {
				t.Fatal(err)
			}
			before := snapshotOperatorState(t, repo)
			collector := filepath.Join(root, "collector-script")
			seed := filepath.Join(root, "seed.json")
			seconds := 1.0
			if test.slow {
				seconds = 2
			}
			candidateSeed := filepath.Join(root, "candidate.json")
			for path, report := range map[string]qualityReport{seed: timingFixture(base, 1), candidateSeed: timingFixture(snapshot.Revision, seconds)} {
				if err := writeJSON(path, report); err != nil {
					t.Fatal(err)
				}
			}
			script := "#!/bin/sh\nset -eu\ntest \"$(git rev-parse HEAD)\" = \"$2\"\ntest -z \"$(git status --porcelain)\"\n" +
				"if test \"$2\" = '" + base + "'; then cat '" + seed + "' >\"$4\"; else cat '" + candidateSeed + "' >\"$4\"; fi\n"
			if test.failed {
				script += "exit 3\n"
			}
			if test.canceled {
				script += "sleep 30 &\nwait\n"
			}
			if err := os.WriteFile(collector, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "bin")
			if err := os.Mkdir(bin, 0o755); err != nil {
				t.Fatal(err)
			}
			build := "#!/bin/sh\nset -eu\ntest \"$1\" = build\ntest \"$2\" = -mod=readonly\ntest \"$3\" = -o\ntest \"$5\" = ./cmd/testcoverage\ntest \"$(git rev-parse HEAD)\" = '" + snapshot.Revision + "'\ncp -f '" + collector + "' \"$4\"\n"
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte(build), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			if err := runner.buildCollector(context.Background()); err != nil {
				t.Fatal(err)
			}
			candidatePolicy, err := loadQualityPolicy(filepath.Join(runner.work, "merge", ".quality.yml"))
			if err != nil {
				t.Fatal(err)
			}
			evidence := &comparisonEvidence{BaseRevision: resolved, MergeRevision: snapshot.Revision, Snapshot: &snapshot}
			ctx := context.Background()
			if test.canceled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
				defer cancel()
			}

			result, compareErr := runner.compare(ctx, candidatePolicy, evidence)

			if (compareErr == nil) != (test.wantTiming == statusPass) || result.Execution.Status != test.wantExecution || result.Timing.Status != test.wantTiming || result.MeasurementSamples != test.pairs {
				t.Fatalf("comparison = execution %s, timing %s, pairs %d, %v", result.Execution.Status, result.Timing.Status, result.MeasurementSamples, compareErr)
			}
			if test.wantTiming == statusPass {
				t.Chdir(repo)
				output := filepath.Join(root, "complete-report", "report.json")
				opts, err := parseOptions([]string{"-compare-worktree", "-compare-base", base, "-output", output})
				if err != nil {
					t.Fatal(err)
				}
				if err := execute(opts); err != nil {
					t.Fatal(err)
				}
				contents, err := os.ReadFile(output)
				if err != nil {
					t.Fatal(err)
				}
				var completed qualityReport
				if err := json.Unmarshal(contents, &completed); err != nil {
					t.Fatal(err)
				}
				if completed.Decision.Status != statusPass || completed.Comparison.Snapshot.Revision != snapshot.Revision {
					t.Fatalf("local command did not use the snapshot: %#v", completed)
				}
			}
			if !reflect.DeepEqual(before, snapshotOperatorState(t, repo)) {
				t.Fatal("comparison or cleanup changed source state")
			}
		})
	}
}

func TestIntegrationAdapterContractLocalTimingSetupFailureRetainsUnevaluatedReportOutsideCheckout(t *testing.T) {
	repo, _, _ := syntheticMergeFixture(t)
	t.Chdir(repo)
	before := snapshotOperatorState(t, repo)
	output := filepath.Join(testutil.CanonicalTempDir(t), "new-reports", "report.json")

	err := executeComparison(options{compareWorktree: true, compareBase: "nonexistent-base", output: output, policy: ".quality.yml"})

	if err == nil {
		t.Fatal("invalid base comparison passed")
	}
	contents, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	var result qualityReport
	if err := json.Unmarshal(contents, &result); err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Execution.Status != statusExecutionFailed || result.Timing.Status != statusUnevaluated || len(result.Timing.Findings) == 0 {
		t.Fatalf("setup failure report = %#v", result)
	}
	if !reflect.DeepEqual(before, snapshotOperatorState(t, repo)) {
		t.Fatal("setup failure changed source state")
	}
	if _, err := comparisonOutput(options{compareWorktree: true, output: filepath.Join(repo, "new-reports", "report.json")}); err == nil {
		t.Fatal("output inside source accepted")
	}
	if _, err := comparisonOutput(options{compareWorktree: true, output: output}); err == nil {
		t.Fatal("existing report directory accepted")
	}
}

func TestIntegrationAdapterContractSnapshotNeverFollowsSymlinkedParents(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	outside := testutil.CanonicalTempDir(t)
	snapshotWrite(t, outside, "file", "outside bytes\n")
	if err := os.Symlink(outside, filepath.Join(root, "parent")); err != nil {
		t.Fatal(err)
	}

	_, _, err := readSnapshotFile(root, "parent/file")

	if err == nil {
		t.Fatal("snapshot followed a directory symlink")
	}
	contents, err := os.ReadFile(filepath.Join(outside, "file"))
	if err != nil || !bytes.Equal(contents, []byte("outside bytes\n")) {
		t.Fatalf("outside contents changed: %q, %v", contents, err)
	}
}
