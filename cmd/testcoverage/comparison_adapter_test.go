//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

func TestIntegrationAdapterContractComparisonDoltVersionOmitsCachedUpdateWarning(t *testing.T) {
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
	// without allowing the version probe to query the network.
	if err := os.WriteFile(filepath.Join(doltConfig, "version_check.txt"), []byte("999.0.0"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("dolt", "version")
	cmd.Dir = runner.work
	cmd.Env = comparisonEnvironment(runner.work, "base")
	version, err := cmd.CombinedOutput()

	if err != nil {
		t.Fatalf("Dolt version probe: %v\n%s", err, version)
	}
	if !bytes.HasPrefix(version, []byte("dolt version ")) || bytes.Count(version, []byte("\n")) != 1 {
		t.Fatalf("version metadata contains more than the installed version: %q", version)
	}
}

func TestIntegrationAdapterContractComparisonPinsTestedMergeParentsDespiteStaleMetadataAndMovingMain(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(fmt.Sprintf("stale base metadata=%t", stale), func(t *testing.T) {
			repo, eventBase, merge := syntheticMergeFixture(t)
			head := snapshotGit(t, repo, "rev-parse", merge+"^2")
			base := eventBase
			if stale {
				base = snapshotGit(t, repo, "commit-tree", eventBase+"^{tree}", "-p", eventBase, "-m", "base advanced before tested merge")
				merge = snapshotGit(t, repo, "commit-tree", head+"^{tree}", "-p", base, "-p", head, "-m", "tested merge with newer base")
			}
			newMain := snapshotGit(t, repo, "commit-tree", base+"^{tree}", "-p", base, "-m", "main advanced after tested merge")
			snapshotGit(t, repo, "update-ref", "refs/heads/main", newMain)
			t.Chdir(repo)
			// Event base metadata is intentionally irrelevant to selection.
			t.Setenv("QUALITY_BASE", eventBase)
			runner := snapshotRunner(t, repo)
			evidence := &comparisonEvidence{MergeRevision: merge}

			if err := runner.prepareComparison(context.Background(), options{compareHead: head, compareMerge: merge}, evidence); err != nil {
				t.Fatal(err)
			}
			if evidence.BaseRevision != base || evidence.MergeRevision != merge {
				t.Fatalf("comparison identities = %#v, want base %s and merge %s", evidence, base, merge)
			}
			for _, item := range []struct{ name, revision string }{{"base", base}, {"merge", merge}} {
				t.Chdir(filepath.Join(runner.work, item.name))
				identity, err := verifyCheckout(item.revision)
				if err != nil || identity.Revision != item.revision || !identity.Clean {
					t.Fatalf("%s identity = %#v, %v", item.name, identity, err)
				}
				if output, err := exec.Command("git", "symbolic-ref", "-q", "HEAD").CombinedOutput(); err == nil {
					t.Fatalf("measurement checkout is not detached: %s", output)
				}
			}
			if _, err := os.Stat(filepath.Join(runner.work, "base", "candidate.txt")); !os.IsNotExist(err) {
				t.Fatalf("candidate contents leaked into base: %v", err)
			}
			if _, err := os.Stat(filepath.Join(runner.work, "merge", "candidate.txt")); err != nil {
				t.Fatal(err)
			}
			laterMain := snapshotGit(t, repo, "commit-tree", newMain+"^{tree}", "-p", newMain, "-m", "main advanced after selection")
			snapshotGit(t, repo, "update-ref", "refs/heads/main", laterMain)
			seedComparisonCollector(t, runner, base, merge)

			result, err := runner.compare(context.Background(), testLocalQualityPolicy(), evidence)
			if err := finishReport(runner.output, result, err); err != nil {
				t.Fatal(err)
			}
			if result.Decision.Status != statusPass || len(result.Comparison.Samples) != 4 {
				t.Fatalf("comparison did not run one measured pair: %#v", result)
			}
			for _, sample := range result.Comparison.Samples {
				want := merge
				if sample.Subject == "base" {
					want = base
				}
				if sample.Revision != want {
					t.Fatalf("sample identity = %s, want %s", sample.Revision, want)
				}
			}
			for _, path := range []string{runner.output, reportSummaryPath(runner.output)} {
				contents, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(contents, []byte(base)) || !bytes.Contains(contents, []byte(merge)) || bytes.Contains(contents, []byte(laterMain)) || (stale && bytes.Contains(contents, []byte(eventBase))) {
					t.Fatalf("report does not identify actual comparison commits: %s", contents)
				}
			}
		})
	}
}

func seedComparisonCollector(t *testing.T, runner *comparisonRunner, base, merge string) {
	t.Helper()
	script := "#!/bin/sh\nset -eu\ntest \"$1\" = -measure-revision\ntest \"$3\" = -output\ncase \"$2\" in\n"
	for _, revision := range []string{base, merge} {
		seed := filepath.Join(runner.work, revision+".json")
		if err := writeJSON(seed, timingFixture(revision, 1)); err != nil {
			t.Fatal(err)
		}
		script += fmt.Sprintf("%s) cat '%s' >\"$4\";;\n", revision, seed)
	}
	script += "*) exit 1;;\nesac\n"
	runner.collector = filepath.Join(runner.work, "collector")
	if err := os.WriteFile(runner.collector, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationAdapterContractComparisonInvalidMergeIdentitiesBlockBeforeMeasurement(t *testing.T) {
	for _, test := range []struct {
		name, head, merge, remove, want string
	}{
		{name: "root commit", merge: "base", want: "two-parent synthetic merge"},
		{name: "one parent", head: "base", merge: "head", want: "two-parent synthetic merge"},
		{name: "octopus merge", merge: "octopus", want: "two-parent synthetic merge"},
		{name: "unexpected PR head", head: "base", want: "second parent"},
		{name: "reversed parents", merge: "reversed", want: "second parent"},
		{name: "identical identities", head: "merge", want: "distinct exact full commit IDs"},
		{name: "abbreviated head", head: "short", want: "exact full commit IDs"},
		{name: "abbreviated merge", merge: "short", want: "exact full commit IDs"},
		{name: "missing merge", merge: "missing", want: "verify tested merge"},
		{name: "missing base object", remove: "base", want: "verify base (first parent) commit"},
		{name: "missing head object", remove: "head", want: "verify PR head (second parent) commit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, base, merge := syntheticMergeFixture(t)
			head := snapshotGit(t, repo, "rev-parse", merge+"^2")
			identities := map[string]string{
				"base": base, "head": head, "merge": merge, "short": merge[:12], "missing": strings.Repeat("f", 40),
				"octopus":  snapshotGit(t, repo, "commit-tree", merge+"^{tree}", "-p", base, "-p", head, "-p", merge, "-m", "three parents"),
				"reversed": snapshotGit(t, repo, "commit-tree", merge+"^{tree}", "-p", head, "-p", base, "-m", "reversed parents"),
			}
			if test.head != "" {
				head = identities[test.head]
			}
			if test.merge != "" {
				merge = identities[test.merge]
			}
			if test.remove != "" {
				id := identities[test.remove]
				if err := os.Remove(filepath.Join(repo, ".git", "objects", id[:2], id[2:])); err != nil {
					t.Fatal(err)
				}
			}
			t.Chdir(repo)
			before := snapshotGit(t, repo, "worktree", "list", "--porcelain")
			output := filepath.Join(testutil.CanonicalTempDir(t), "report.json")

			err := execute(options{compareHead: head, compareMerge: merge, output: output, policy: ".quality.yml"})

			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("comparison error = %v, want %q", err, test.want)
			}
			contents, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			var result qualityReport
			if err := json.Unmarshal(contents, &result); err != nil {
				t.Fatal(err)
			}
			if result.Complete || result.Execution.Status != statusExecutionFailed || result.Timing.Status != statusUnevaluated || len(result.Timing.Findings) == 0 || len(result.Comparison.Samples) != 0 {
				t.Fatalf("invalid identity did not block measurement: %#v", result)
			}
			if after := snapshotGit(t, repo, "worktree", "list", "--porcelain"); after != before {
				t.Fatalf("invalid comparison changed worktrees: %s", after)
			}
		})
	}
}

func TestIntegrationAdapterContractComparisonCheckoutRejectsWrongAndDirtyIdentities(t *testing.T) {
	repo, base, merge := syntheticMergeFixture(t)
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
	base, merge := fixtureBase, fixtureMerge
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
		"printf '%s\\n' \"$2\" \"$PWD\" \"$GOCACHE\" \"$GOMODCACHE\" \"$GOENV\" \"$GOWORK\" \"$ORPHEUS_TEST_BINARY_CACHE\" >\"$4.request\"\n" +
		"cat '" + seed + "' >\"$4\"\n"
	if err := os.WriteFile(collector, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := comparisonRunner{work: filepath.Join(root, "work"), output: filepath.Join(root, "reports", "report.json"), collector: collector, log: &bytes.Buffer{}}
	// This contract owns collector invocation, not Git checkout preparation.
	for _, name := range []string{"base", "merge"} {
		if err := os.MkdirAll(filepath.Join(runner.work, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, revision := range []string{base, merge} {
		name := "merge"
		if revision == base {
			name = "base"
		}
		result, path, err := runner.collect(context.Background(), name, revision, false, 1)
		if err != nil || !result.Complete {
			t.Fatalf("collector report = %#v, %v", result, err)
		}
		request, err := os.ReadFile(path + ".request")
		if err != nil {
			t.Fatal(err)
		}
		want := strings.Join([]string{revision, filepath.Join(runner.work, name), filepath.Join(runner.work, "cache-"+name), filepath.Join(runner.work, "modules"), "off", "off", filepath.Join(runner.work, "test-binaries-"+name), ""}, "\n")
		if string(request) != want {
			t.Fatalf("collector request = %q, want %q", request, want)
		}
	}
	if err := os.WriteFile(collector, []byte(script+"exit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	partial, path, err := runner.collect(context.Background(), "merge", merge, false, 2)
	if err == nil || !partial.Complete {
		t.Fatalf("failed child lost available partial report: %#v, %s, %v", partial, path, err)
	}
}
