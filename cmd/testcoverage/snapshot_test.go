package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hea3ven/orpheus/internal/testutil"
)

func TestSnapshotEntriesIncludeHeadAndIndexPathsWithoutInterpretingQuotedNames(t *testing.T) {
	paths := map[string]bool{"existing": true}
	entries := "100644 blob " + fixtureBase + "\ta\tb\n.go\x00" + "100755 " + fixtureMerge + " 0\texecutable\x00"

	err := addSnapshotEntries(paths, entries)

	if err != nil || !reflect.DeepEqual(sortedKeys(paths), []string{"a\tb\n.go", "executable", "existing"}) {
		t.Fatalf("snapshot entries = %v, %v", paths, err)
	}
	for _, invalid := range []string{"no tab\x00", "100644 blob id\t\x00", "160000 commit id\tsubmodule\x00"} {
		if err := addSnapshotEntries(paths, invalid); err == nil {
			t.Fatalf("invalid snapshot entry accepted: %q", invalid)
		}
	}
}

func TestSnapshotReadsRawBytesAndDiskModesAndTreatsAbsentPathsAsDeletions(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	for _, test := range []struct {
		name, contents, mode string
		permissions          os.FileMode
	}{
		{name: "raw", contents: "raw\r\n\x00bytes", mode: "100644", permissions: 0o644},
		{name: "executable", contents: "#!/bin/sh\n", mode: "100755", permissions: 0o755},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(root, test.name), []byte(test.contents), test.permissions); err != nil {
				t.Fatal(err)
			}

			contents, mode, err := readSnapshotFile(root, test.name)

			if err != nil || string(contents) != test.contents || mode != test.mode {
				t.Fatalf("snapshot = %q, %s, %v", contents, mode, err)
			}
		})
	}
	for _, missing := range []string{"deleted", "deleted-parent/file"} {
		contents, mode, err := readSnapshotFile(root, missing)
		if err != nil || contents != nil || mode != "" {
			t.Fatalf("deleted snapshot = %q, %s, %v", contents, mode, err)
		}
	}
	for _, unsafe := range []string{"../outside", "/outside", "nested-repo/"} {
		if _, _, err := readSnapshotFile(root, unsafe); err == nil {
			t.Fatalf("unsafe snapshot path accepted: %s", unsafe)
		}
	}
}

func TestSnapshotTreatsDescendantsOfDirectoriesReplacedByFilesAsDeletions(t *testing.T) {
	root := testutil.CanonicalTempDir(t)
	if err := os.WriteFile(filepath.Join(root, "old"), []byte("replacement\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"old/child.txt", "old/nested/child.txt"} {
		t.Run(path, func(t *testing.T) {
			contents, mode, err := readSnapshotFile(root, path)

			if err != nil || contents != nil || mode != "" {
				t.Fatalf("deleted descendant %q = %q, %s, %v", path, contents, mode, err)
			}
		})
	}
}

func TestLocalTimingReportsRemainOutsideTheSource(t *testing.T) {
	for _, test := range []struct {
		path    string
		wantErr bool
	}{
		{path: "/fixture/repo", wantErr: true},
		{path: "/fixture/repo/reports", wantErr: true},
		{path: "/fixture/reports"},
		{path: "/fixture/repo-other/reports"},
	} {
		if err := outsideSource("/fixture/repo", test.path); (err != nil) != test.wantErr {
			t.Fatalf("outsideSource(%s) = %v, want error %t", test.path, err, test.wantErr)
		}
	}
}

func TestWorkingTreeReportLabelsSnapshotAndKeepsComparisonFailuresDistinct(t *testing.T) {
	for _, test := range []struct {
		status string
		want   string
	}{
		{statusPass, "Quality checks passed"},
		{statusTimingFailed, "Timing ceiling exceeded"},
		{statusUnevaluated, "Timing unevaluated"},
		{statusExecutionFailed, "Execution failed"},
	} {
		report := timingFixture(fixtureMerge, 1)
		report.Decision = decision{Status: test.status}
		report.Comparison = &comparisonEvidence{BaseRevision: fixtureBase, MergeRevision: fixtureMerge, Snapshot: &workingTreeSnapshot{HeadRevision: fixtureBase, Tree: "tree-id", Bundle: "/fixture/candidate.bundle"}}
		var output strings.Builder

		renderReportSummary(&output, report, "/fixture/report.json")

		for _, want := range []string{test.want, "Working-tree snapshot", "Source HEAD:", "Raw tree: `tree-id`", "candidate.bundle"} {
			if !strings.Contains(output.String(), want) {
				t.Fatalf("snapshot report missing %q: %s", want, output.String())
			}
		}
		if strings.Contains(output.String(), "Synthetic merge:") {
			t.Fatalf("local snapshot mislabeled as merge: %s", output.String())
		}
	}
}
