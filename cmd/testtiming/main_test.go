package main

import (
	"reflect"
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
