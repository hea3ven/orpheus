package main

import (
	"github.com/hea3ven/orpheus/internal/testutil"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTestCommandUsesIntegrationLaneConvention(t *testing.T) {
	t.Parallel()

	want := []string{"go", "test", "-json", "-count=1", "-tags=integration", "-run", "^TestIntegration", "./..."}
	if got := testCommand("integration"); !reflect.DeepEqual(got, want) {
		t.Fatalf("integration command = %#v, want %#v", got, want)
	}
}

func TestMedian(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		values []float64
		want   float64
	}{
		{name: "empty", want: 0},
		{name: "odd unsorted", values: []float64{3, 1, 2}, want: 2},
		{name: "even unsorted", values: []float64{8, 2, 4, 6}, want: 5},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := median(test.values); got != test.want {
				t.Fatalf("median(%v) = %v, want %v", test.values, got, test.want)
			}
		})
	}
}

func TestSummarizeRanksMedianDurations(t *testing.T) {
	t.Parallel()

	got := summarize([]run{
		{WallSeconds: 8, Packages: map[string]float64{"first": 3, "second": 1}, Tests: map[string]float64{"first.A": 2, "first.B": 1}},
		{WallSeconds: 4, Packages: map[string]float64{"first": 5, "second": 2}, Tests: map[string]float64{"first.A": 4, "first.B": 0}},
		{WallSeconds: 6, Packages: map[string]float64{"first": 4, "second": 3}, Tests: map[string]float64{"first.A": 3, "first.B": 2}},
	})
	if got.WallSeconds != 6 {
		t.Fatalf("wall median = %v, want 6", got.WallSeconds)
	}
	if got.TestCount != 2 {
		t.Fatalf("test count = %d, want 2", got.TestCount)
	}
	wantPackages := []duration{{Name: "first", Seconds: 4, Samples: 3}, {Name: "second", Seconds: 2, Samples: 3}}
	if !reflect.DeepEqual(got.Packages, wantPackages) {
		t.Fatalf("package medians = %#v, want %#v", got.Packages, wantPackages)
	}
	wantTests := []duration{{Name: "first.A", Seconds: 3, Samples: 3}, {Name: "first.B", Seconds: 1, Samples: 3}}
	if !reflect.DeepEqual(got.Tests, wantTests) {
		t.Fatalf("test medians = %#v, want %#v", got.Tests, wantTests)
	}
}

func TestBudgetUsesLargestTolerance(t *testing.T) {
	t.Parallel()
	if got := budget(1, .5, .25); got != 1.5 {
		t.Fatalf("relative budget = %v, want 1.5", got)
	}
	if got := budget(.1, .5, .25); got != .35 {
		t.Fatalf("absolute budget = %v, want .35", got)
	}
}

func TestBaselineUpdateOnlyRatchetsDown(t *testing.T) {
	t.Parallel()
	created := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	baseReport := report{Lane: "unit", CreatedAt: created, Environment: environment{GoVersion: "go test"}, Median: summary{
		WallSeconds: 10,
		Packages:    []duration{{Name: "slow", Seconds: 4, Samples: 5}, {Name: "steady", Seconds: 2, Samples: 5}},
		Tests:       []duration{{Name: "slow.Test", Seconds: 4, Samples: 5}},
		TestCount:   1,
	}}
	baseline := initialBaseline(baseReport)

	slower := baseReport
	slower.CreatedAt = created.Add(time.Hour)
	slower.Median.WallSeconds = 12
	slower.Median.Packages = []duration{{Name: "slow", Seconds: 5, Samples: 5}, {Name: "steady", Seconds: 3, Samples: 5}}
	if baseline.update(slower) {
		t.Fatal("slower report unexpectedly updated baseline")
	}
	lane := baseline.Lanes["unit"]
	if lane.Median.WallSeconds != 10 || lane.Median.Packages[0].Seconds != 4 {
		t.Fatalf("slower report changed baseline: %#v", lane)
	}

	faster := baseReport
	faster.CreatedAt = created.Add(2 * time.Hour)
	faster.Median.WallSeconds = 8
	faster.Median.Packages = []duration{{Name: "slow", Seconds: 3, Samples: 5}, {Name: "steady", Seconds: 2.5, Samples: 5}, {Name: "new", Seconds: 1, Samples: 5}}
	if !baseline.update(faster) {
		t.Fatal("faster report did not update baseline")
	}
	lane = baseline.Lanes["unit"]
	if lane.Median.WallSeconds != 8 {
		t.Fatalf("wall baseline = %v, want 8", lane.Median.WallSeconds)
	}
	got := make(map[string]float64)
	for _, value := range lane.Median.Packages {
		got[value.Name] = value.Seconds
	}
	want := map[string]float64{"slow": 3, "steady": 2, "new": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("package baseline = %#v, want %#v", got, want)
	}
}

func TestBaselineCheck(t *testing.T) {
	t.Parallel()
	base := initialBaseline(report{Lane: "unit", Median: summary{WallSeconds: 4, Packages: []duration{{Name: "package", Seconds: 1}}}})

	passing := report{Lane: "unit", Median: summary{WallSeconds: 4.9, Packages: []duration{{Name: "package", Seconds: 1.4}}}}
	if failures := base.check(passing); len(failures) != 0 {
		t.Fatalf("passing report failed budgets: %v", failures)
	}
	failing := report{Lane: "unit", Median: summary{WallSeconds: 5.1, Packages: []duration{{Name: "package", Seconds: 1.6}, {Name: "new-package", Seconds: 0.1}}}}
	if failures := base.check(failing); len(failures) != 3 {
		t.Fatalf("failures = %v, want suite, package, and missing package budget failures", failures)
	}
}

func TestFailuresFromEventsPreservesFailingTestOutput(t *testing.T) {
	t.Parallel()

	measurement := run{Packages: make(map[string]float64), Tests: make(map[string]float64)}
	outputs := make(map[testID]string)
	failed := make(map[testID]bool)
	for _, event := range []testEvent{
		{Action: "output", Package: "example/cli", Test: "TestReview", Output: "    review_test.go:42: assertion failed\n"},
		{Action: "fail", Package: "example/cli", Test: "TestReview"},
		{Action: "output", Package: "example/cli", Output: "FAIL\texample/cli\t0.123s\n"},
		{Action: "fail", Package: "example/cli"},
	} {
		recordTestEvent(measurement, outputs, failed, event)
	}

	got := failuresFromEvents(outputs, failed)
	want := []testFailure{
		{Package: "example/cli", Output: "FAIL\texample/cli\t0.123s\n"},
		{Package: "example/cli", Test: "TestReview", Output: "    review_test.go:42: assertion failed\n"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("failures = %#v, want %#v", got, want)
	}
}

func TestHandleBaselineRejectsIncompleteSampleSetWithoutWriting(t *testing.T) {
	t.Parallel()

	path := filepath.Join(testutil.CanonicalTempDir(t), "baseline.json")
	original := initialBaseline(report{Lane: "unit", Complete: true, Samples: 1, Runs: []run{{}}, Median: summary{TestCount: 1}})
	if err := writeJSON(path, original); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read baseline before update: %v", err)
	}

	err = handleBaseline(options{baseline: path, updateBaseline: true}, report{
		Lane:     "unit",
		Samples:  5,
		Runs:     []run{{}},
		Complete: false,
		Failure:  &sampleFailure{Sample: 2, Error: "exit status 1"},
	})
	if err == nil || !strings.Contains(err.Error(), "incomplete or failed sample set") {
		t.Fatalf("handle incomplete report error = %v, want rejected sample set", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read baseline after update: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("incomplete sample set modified baseline")
	}
}

func TestBaselineCheckRejectsChangedTestCount(t *testing.T) {
	t.Parallel()

	base := initialBaseline(report{Lane: "unit", Median: summary{TestCount: 4}})
	failures := base.check(report{Lane: "unit", Median: summary{TestCount: 3}})
	if len(failures) != 1 || failures[0] != "test event count 3 differs from baseline 4" {
		t.Fatalf("failures = %#v, want test count mismatch", failures)
	}
}

func TestParseOptionsRejectsMultipleBaselineActions(t *testing.T) {
	t.Parallel()

	_, err := parseOptions([]string{"--init-baseline", "--replace-baseline"})
	if err == nil || !strings.Contains(err.Error(), "only one of init-baseline, replace-baseline, and update-baseline") {
		t.Fatalf("parse options error = %v, want mutually exclusive baseline actions", err)
	}
}

func TestHandleBaselineReplacesCompleteLane(t *testing.T) {
	t.Parallel()

	path := filepath.Join(testutil.CanonicalTempDir(t), "baseline.json")
	original := initialBaseline(report{Lane: "unit", Complete: true, Samples: 1, Runs: []run{{}}, Median: summary{WallSeconds: 10, TestCount: 1}})
	if err := writeJSON(path, original); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	updated := report{Lane: "unit", CreatedAt: time.Date(2026, time.August, 16, 12, 0, 0, 0, time.UTC), Complete: true, Samples: 2, Runs: []run{{}, {}}, Median: summary{WallSeconds: 20, TestCount: 2}}
	if err := handleBaseline(options{lane: "unit", baseline: path, replaceBaseline: true}, updated); err != nil {
		t.Fatalf("replace baseline: %v", err)
	}
	loaded, err := loadBaseline(path)
	if err != nil {
		t.Fatalf("load replaced baseline: %v", err)
	}
	lane := loaded.Lanes["unit"]
	if lane.Median.WallSeconds != 20 || lane.Median.TestCount != 2 || !lane.RecordedAt.Equal(updated.CreatedAt) {
		t.Fatalf("replaced lane = %#v, want complete report baseline", lane)
	}
}

func TestHandleBaselineUpdateRejectsChangedTestCount(t *testing.T) {
	t.Parallel()

	path := filepath.Join(testutil.CanonicalTempDir(t), "baseline.json")
	original := initialBaseline(report{Lane: "unit", Complete: true, Samples: 1, Runs: []run{{}}, Median: summary{TestCount: 4}})
	if err := writeJSON(path, original); err != nil {
		t.Fatalf("write baseline: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read baseline before update: %v", err)
	}

	err = handleBaseline(options{baseline: path, updateBaseline: true}, report{
		Lane:     "unit",
		Samples:  1,
		Runs:     []run{{}},
		Complete: true,
		Median:   summary{TestCount: 5},
	})
	if err == nil || !strings.Contains(err.Error(), "regenerate it from the complete current suite") {
		t.Fatalf("handle changed test count error = %v, want regeneration guidance", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read baseline after update: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatal("changed test count modified baseline")
	}
}
