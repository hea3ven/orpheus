//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

func syntheticMergeFixture(t *testing.T) (string, string, string) {
	t.Helper()
	root := testutil.CanonicalTempDir(t)
	repo := filepath.Join(root, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_AUTHOR_NAME", "Quality fixture")
	t.Setenv("GIT_AUTHOR_EMAIL", "quality@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "Quality fixture")
	t.Setenv("GIT_COMMITTER_EMAIL", "quality@example.invalid")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	git("init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(repo, "candidate.txt"), []byte("candidate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "candidate")
	head := git("rev-parse", "HEAD")
	merge := git("commit-tree", "HEAD^{tree}", "-p", base, "-p", head, "-m", "tested synthetic merge")
	git("reset", "--hard", merge)
	return repo, base, merge
}

func TestIntegrationAdapterContractComparisonDoltVersionRemainsStableWithCachedUpdate(t *testing.T) {
	if _, err := exec.LookPath("dolt"); err != nil {
		t.Skip("Dolt is not installed")
	}
	t.Setenv("DOLT_ROOT_PATH", "")
	output := filepath.Join(testutil.CanonicalTempDir(t), "report.json")
	runner, cleanup, err := newComparisonRunner(output)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	doltConfig := filepath.Join(runner.work, "home", ".dolt")
	if err := os.MkdirAll(doltConfig, 0o755); err != nil {
		t.Fatal(err)
	}
	// A fresh cached newer release reproduces the once-only update warning
	// without allowing either version probe to query the network.
	if err := os.WriteFile(filepath.Join(doltConfig, "version_check.txt"), []byte("999.0.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	var versions [][]byte

	for _, name := range []string{"base", "base", "merge"} {
		cmd := exec.Command("dolt", "version")
		cmd.Dir = runner.work
		cmd.Env = comparisonEnvironment(runner.work, name)
		version, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s Dolt version probe: %v\n%s", name, err, version)
		}
		versions = append(versions, version)
	}

	for _, version := range versions {
		if !bytes.HasPrefix(version, []byte("dolt version ")) || bytes.Count(version, []byte("\n")) != 1 {
			t.Fatalf("version metadata contains more than the installed version: %q", version)
		}
		if !bytes.Equal(version, versions[0]) {
			t.Fatalf("Dolt version metadata changed: %q != %q", version, versions[0])
		}
	}
}

func TestIntegrationAdapterContractComparisonPreparesExactDetachedCheckouts(t *testing.T) {
	repo, base, merge := syntheticMergeFixture(t)
	work := filepath.Join(testutil.CanonicalTempDir(t), "work")
	var diagnostics bytes.Buffer
	runner := comparisonRunner{root: repo, work: work, log: &diagnostics}
	t.Cleanup(func() {
		for _, name := range []string{"base", "merge"} {
			if err := runner.gitCommand(context.Background(), repo, "worktree", "remove", "--force", filepath.Join(work, name)); err != nil {
				t.Error(err)
			}
		}
	})

	if err := runner.prepare(context.Background(), base, merge); err != nil {
		t.Fatal(err)
	}

	for _, item := range []struct{ name, revision string }{{"base", base}, {"merge", merge}} {
		t.Chdir(filepath.Join(work, item.name))
		identity, err := verifyCheckout(item.revision)
		if err != nil || identity.Revision != item.revision || !identity.Clean {
			t.Fatalf("%s identity = %#v, %v", item.name, identity, err)
		}
		if output, err := exec.Command("git", "symbolic-ref", "-q", "HEAD").CombinedOutput(); err == nil {
			t.Fatalf("measurement checkout is not detached: %s", output)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "base", "candidate.txt")); !os.IsNotExist(err) {
		t.Fatalf("candidate contents leaked into base: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "merge", "candidate.txt")); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationAdapterContractComparisonRejectsMismatchedAndDirtyIdentities(t *testing.T) {
	repo, base, merge := syntheticMergeFixture(t)
	runner := comparisonRunner{root: repo, work: testutil.CanonicalTempDir(t), log: &bytes.Buffer{}}

	if err := runner.prepare(context.Background(), merge, merge); err == nil {
		t.Fatal("identical identities accepted")
	}
	if err := runner.prepare(context.Background(), merge, base); err == nil {
		t.Fatal("non-merge or mismatched parent accepted")
	}
	if err := runner.prepare(context.Background(), base[:12], merge); err == nil {
		t.Fatal("abbreviated identity accepted")
	}
	t.Chdir(repo)
	if _, err := verifyCheckout(base); err == nil {
		t.Fatal("wrong checkout HEAD accepted")
	}
	if _, err := verifyCheckout(merge); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("base.txt", []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyCheckout(merge); err == nil {
		t.Fatal("dirty checkout accepted")
	}
}

func TestIntegrationAdapterContractComparisonCollectorUsesSameBinaryAndIsolatedCaches(t *testing.T) {
	repo, base, merge := syntheticMergeFixture(t)
	root := testutil.CanonicalTempDir(t)
	collector := filepath.Join(root, "collector")
	seed := filepath.Join(root, "seed.json")
	contents, err := json.Marshal(timingFixture(merge, 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(seed, contents, 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nset -eu\n" +
		"test \"$1\" = -measure-revision\ntest \"$3\" = -output\n" +
		"printf '%s\\n' \"$2\" \"$PWD\" \"$GOCACHE\" \"$GOMODCACHE\" \"$GOENV\" \"$GOWORK\" >\"$4.request\"\n" +
		"cat '" + seed + "' >\"$4\"\n"
	if err := os.WriteFile(collector, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := comparisonRunner{root: repo, work: filepath.Join(root, "work"), output: filepath.Join(root, "reports", "report.json"), collector: collector, log: &bytes.Buffer{}}
	if err := runner.prepare(context.Background(), base, merge); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range []string{"base", "merge"} {
			if err := runner.gitCommand(context.Background(), repo, "worktree", "remove", "--force", filepath.Join(runner.work, name)); err != nil {
				t.Error(err)
			}
		}
	})

	for _, revision := range []string{base, merge} {
		result, path, err := runner.collect(context.Background(), revision, false, 1)
		if err != nil || !result.Complete {
			t.Fatalf("collector report = %#v, %v", result, err)
		}
		request, err := os.ReadFile(path + ".request")
		if err != nil {
			t.Fatal(err)
		}
		name := "merge"
		if revision == base {
			name = "base"
		}
		want := strings.Join([]string{revision, filepath.Join(runner.work, name), filepath.Join(runner.work, "cache-"+name), filepath.Join(runner.work, "modules"), "off", "off", ""}, "\n")
		if string(request) != want {
			t.Fatalf("collector request = %q, want %q", request, want)
		}
	}
	if err := os.WriteFile(collector, []byte(script+"exit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	partial, path, err := runner.collect(context.Background(), merge, false, 2)
	if err == nil || !partial.Complete {
		t.Fatalf("failed child lost available partial report: %#v, %s, %v", partial, path, err)
	}
}
